// Package contextmgr coordinates conversation state and persistent storage.
package contextmgr

import (
	"context"
	"errors"
	"fmt"

	"github.com/torrischen/goat/agent/common"

	"github.com/cloudwego/eino/schema"
)

var (
	ErrContextNotFound       = errors.New("context not found")
	ErrRevisionConflict      = errors.New("context revision conflict")
	ErrRevisionNotFound      = errors.New("context revision not found")
	ErrInvalidEvent          = errors.New("invalid context event")
	ErrStoreUnavailable      = errors.New("context store is unavailable")
	ErrInvalidMessage        = errors.New("message must not be nil")
	ErrInvalidPendingMessage = errors.New("pending message must be a non-nil user message")
	ErrConversationFinalized = errors.New("conversation already has a final answer")
	ErrInvalidFinalMessage   = errors.New("final message must be a non-nil assistant answer")
	ErrInvalidRunSignature   = errors.New("run signature must include context UID and run UID")
	ErrInvalidRunOutcome     = errors.New("invalid run outcome")
	ErrRunNotFound           = errors.New("run not found")
	ErrRunNotSettled         = errors.New("run has not settled")
	ErrRunNotCurrent         = errors.New("run is not current")
)

// Store is the persistence boundary for conversation state. Implementations
// own ID generation, isolate values at the boundary, and atomically append
// events under an expected stream revision.
type Store interface {
	Create(context.Context, *State) (common.ContextUID, error)
	Load(context.Context, common.ContextUID) (*State, error)
	LoadAt(context.Context, common.ContextUID, uint64) (*State, error)
	Append(context.Context, common.ContextUID, uint64, []Event) error
	Delete(context.Context, common.ContextUID) error
}

// RunOutcome records why a run reached its immutable terminal snapshot.
type RunOutcome string

const (
	RunOutcomeCompleted   RunOutcome = "completed"
	RunOutcomeInterrupted RunOutcome = "interrupted"
	RunOutcomeCanceled    RunOutcome = "canceled"
	RunOutcomeFailed      RunOutcome = "failed"
)

// RunSnapshot is the retained context and terminal outcome for one run.
type RunSnapshot struct {
	Outcome  RunOutcome `json:"outcome"`
	Revision uint64     `json:"revision,omitempty"`

	// Messages is populated only while reading stores created before event
	// revisions existed. New snapshots retain only Revision.
	Messages []*schema.AgenticMessage `json:"messages,omitempty"`
}

// State is the materialized versioned state reconstructed by a Store.
type State struct {
	Revision        uint64                        `json:"revision"`
	Messages        []*schema.AgenticMessage      `json:"messages"`
	PendingMessages []*schema.AgenticMessage      `json:"pending_messages,omitempty"`
	RunSnapshots    map[common.RunUID]RunSnapshot `json:"run_snapshots,omitempty"`
}

// NewState returns normalized conversation state with a cloned message slice.
func NewState(messages []*schema.AgenticMessage) *State {
	return &State{
		Messages:        common.CloneAgenticMessages(messages),
		PendingMessages: []*schema.AgenticMessage{},
		RunSnapshots:    make(map[common.RunUID]RunSnapshot),
	}
}

// Clone copies all State containers. Stores must additionally isolate nested
// message values at their persistence boundary.
func (s *State) Clone() *State {
	if s == nil {
		return NewState(nil)
	}
	clone := &State{
		Revision:        s.Revision,
		Messages:        common.CloneAgenticMessages(s.Messages),
		PendingMessages: common.CloneAgenticMessages(s.PendingMessages),
		RunSnapshots:    make(map[common.RunUID]RunSnapshot, len(s.RunSnapshots)),
	}
	for runUID, snapshot := range s.RunSnapshots {
		clone.RunSnapshots[runUID] = RunSnapshot{
			Outcome:  snapshot.Outcome,
			Revision: snapshot.Revision,
			Messages: common.CloneAgenticMessages(snapshot.Messages),
		}
	}
	return clone
}

// SettleRunArgs atomically commits a completed final answer when present and
// saves the run's immutable terminal snapshot. Non-completed outcomes preserve
// pending steering messages for the next run.
type SettleRunArgs struct {
	Signature    common.RunSignature
	Outcome      RunOutcome
	FinalMessage *schema.AgenticMessage
}

// TurnCommitResult describes pending messages atomically applied after a
// committed assistant turn.
type TurnCommitResult struct {
	AppliedPendingMessages []*schema.AgenticMessage
}

// Manager owns all conversation and run state transitions. Storage backends
// implement only Store; callers should not mutate Store state directly.
type Manager struct {
	store Store
}

// NewManager builds a context manager over a versioned Store.
func NewManager(store Store) *Manager {
	return &Manager{store: store}
}

// Create atomically creates a conversation containing initialMessages.
func (m *Manager) Create(
	ctx context.Context,
	initialMessages []*schema.AgenticMessage,
) (common.ContextUID, error) {
	if err := validateMessages(initialMessages); err != nil {
		return "", err
	}
	if m == nil || m.store == nil {
		return "", ErrStoreUnavailable
	}
	return m.store.Create(ctx, NewState(initialMessages))
}

// Load retrieves a cloned committed conversation history.
func (m *Manager) Load(
	ctx context.Context,
	contextUID common.ContextUID,
) ([]*schema.AgenticMessage, error) {
	state, err := m.loadState(ctx, contextUID)
	if err != nil {
		return nil, err
	}
	return common.CloneAgenticMessages(state.Messages), nil
}

// Append atomically appends committed messages.
func (m *Manager) Append(
	ctx context.Context,
	contextUID common.ContextUID,
	messages ...*schema.AgenticMessage,
) error {
	if err := validateMessages(messages); err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}
	cloned := common.CloneAgenticMessages(messages)
	_, err := m.update(ctx, contextUID, func(*State) ([]Event, any, error) {
		return []Event{{Type: EventMessagesAppended, Messages: cloned}}, nil, nil
	})
	return err
}

// Replace atomically replaces committed history while preserving pending
// messages and immutable run snapshots.
func (m *Manager) Replace(
	ctx context.Context,
	contextUID common.ContextUID,
	messages []*schema.AgenticMessage,
) error {
	if err := validateMessages(messages); err != nil {
		return err
	}
	cloned := common.CloneAgenticMessages(messages)
	_, err := m.update(ctx, contextUID, func(*State) ([]Event, any, error) {
		return []Event{{Type: EventMessagesReplaced, Messages: cloned}}, nil, nil
	})
	return err
}

// Enqueue adds user messages to the pending inbox. They remain outside the
// committed model context until CommitTurn applies them.
func (m *Manager) Enqueue(
	ctx context.Context,
	contextUID common.ContextUID,
	messages []*schema.AgenticMessage,
) error {
	if err := validatePendingMessages(messages); err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}
	cloned := common.CloneAgenticMessages(messages)
	_, err := m.update(ctx, contextUID, func(state *State) ([]Event, any, error) {
		if len(state.Messages) > 0 && isFinalAnswerMessage(state.Messages[len(state.Messages)-1]) {
			return nil, nil, ErrConversationFinalized
		}
		return []Event{{Type: EventPendingEnqueued, Messages: cloned}}, nil, nil
	})
	return err
}

// CommitTurn appends a protocol-complete non-final turn and then atomically
// moves every pending message behind it.
func (m *Manager) CommitTurn(
	ctx context.Context,
	contextUID common.ContextUID,
	turnMessages []*schema.AgenticMessage,
) (*TurnCommitResult, error) {
	if err := validateMessages(turnMessages); err != nil {
		return nil, err
	}
	turn := common.CloneAgenticMessages(turnMessages)
	result, err := m.update(ctx, contextUID, func(state *State) ([]Event, any, error) {
		applied := common.CloneAgenticMessages(state.PendingMessages)
		if len(turn) == 0 && len(applied) == 0 {
			return nil, &TurnCommitResult{AppliedPendingMessages: applied}, nil
		}
		return []Event{{Type: EventTurnCommitted, Messages: turn}}, &TurnCommitResult{
			AppliedPendingMessages: common.CloneAgenticMessages(applied),
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*TurnCommitResult), nil
}

// SettleRun atomically commits a completed final answer, applies the terminal
// pending-message policy, and saves an immutable snapshot. Repeated calls for
// an already settled signature are no-ops.
func (m *Manager) SettleRun(ctx context.Context, args *SettleRunArgs) error {
	if err := validateSettlement(args); err != nil {
		return err
	}

	_, err := m.update(ctx, args.Signature.ContextUID, func(state *State) ([]Event, any, error) {
		if _, exists := state.RunSnapshots[args.Signature.RunUID]; exists {
			return nil, nil, nil
		}
		if err := validateCurrentRun(args.Signature, state.Messages); err != nil {
			return nil, nil, err
		}
		return []Event{{
			Type:         EventRunSettled,
			RunUID:       args.Signature.RunUID,
			Outcome:      args.Outcome,
			FinalMessage: args.FinalMessage,
		}}, nil, nil
	})
	return err
}

// Fork creates a conversation from a settled run snapshot. The selected run
// and every inherited fork point through it are retained; pending messages are
// excluded.
func (m *Manager) Fork(
	ctx context.Context,
	from common.RunSignature,
) (common.ContextUID, error) {
	if err := validateRunSignature(from); err != nil {
		return "", err
	}
	state, err := m.loadState(ctx, from.ContextUID)
	if err != nil {
		return "", err
	}
	snapshot, settled := state.RunSnapshots[from.RunUID]
	if !settled {
		if runExists(state.Messages, from.RunUID) {
			return "", ErrRunNotSettled
		}
		return "", ErrRunNotFound
	}

	snapshotState, err := m.snapshotState(ctx, from.ContextUID, snapshot)
	if err != nil {
		return "", err
	}
	forked := NewState(snapshotState.Messages)
	for _, runUID := range runUIDs(snapshotState.Messages) {
		if inherited, exists := state.RunSnapshots[runUID]; exists {
			inheritedState, err := m.snapshotState(ctx, from.ContextUID, inherited)
			if err != nil {
				return "", err
			}
			forked.RunSnapshots[runUID] = RunSnapshot{
				Outcome:  inherited.Outcome,
				Messages: common.CloneAgenticMessages(inheritedState.Messages),
			}
		}
	}
	return m.store.Create(ctx, forked)
}

// Delete removes a conversation and all state owned by it.
func (m *Manager) Delete(ctx context.Context, contextUID common.ContextUID) error {
	if m == nil || m.store == nil {
		return ErrStoreUnavailable
	}
	return m.store.Delete(ctx, contextUID)
}

const maxUpdateAttempts = 32

type transition func(*State) (events []Event, result any, err error)

func (m *Manager) update(
	ctx context.Context,
	contextUID common.ContextUID,
	apply transition,
) (any, error) {
	if m == nil || m.store == nil {
		return nil, ErrStoreUnavailable
	}
	for attempt := 0; attempt < maxUpdateAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current, err := m.store.Load(ctx, contextUID)
		if err != nil {
			return nil, err
		}
		events, result, err := apply(current)
		if err != nil || len(events) == 0 {
			return result, err
		}
		if err := m.store.Append(ctx, contextUID, current.Revision, events); err != nil {
			if errors.Is(err, ErrRevisionConflict) {
				continue
			}
			return nil, err
		}
		return result, nil
	}
	return nil, fmt.Errorf("%w after %d attempts", ErrRevisionConflict, maxUpdateAttempts)
}

func (m *Manager) loadState(ctx context.Context, contextUID common.ContextUID) (*State, error) {
	if m == nil || m.store == nil {
		return nil, ErrStoreUnavailable
	}
	return m.store.Load(ctx, contextUID)
}

func (m *Manager) snapshotState(
	ctx context.Context,
	contextUID common.ContextUID,
	snapshot RunSnapshot,
) (*State, error) {
	if snapshot.Revision != 0 {
		return m.store.LoadAt(ctx, contextUID, snapshot.Revision)
	}
	state := NewState(snapshot.Messages)
	state.Revision = 1
	return state, nil
}

func validateMessages(messages []*schema.AgenticMessage) error {
	for i, message := range messages {
		if message == nil {
			return fmt.Errorf("%w at index %d", ErrInvalidMessage, i)
		}
	}
	return nil
}

func validatePendingMessages(messages []*schema.AgenticMessage) error {
	for i, message := range messages {
		if message == nil || message.Role != schema.AgenticRoleTypeUser {
			return fmt.Errorf("%w at index %d", ErrInvalidPendingMessage, i)
		}
	}
	return nil
}

func isFinalAnswerMessage(message *schema.AgenticMessage) bool {
	if message == nil || message.Role != schema.AgenticRoleTypeAssistant {
		return false
	}
	for _, block := range message.ContentBlocks {
		if block != nil && block.FunctionToolCall != nil {
			return false
		}
	}
	return true
}

func validateFinalMessage(message *schema.AgenticMessage) error {
	if !isFinalAnswerMessage(message) {
		return ErrInvalidFinalMessage
	}
	return nil
}

func validateRunSignature(signature common.RunSignature) error {
	if signature.IsZero() {
		return ErrInvalidRunSignature
	}
	return nil
}

func validateCurrentRun(signature common.RunSignature, messages []*schema.AgenticMessage) error {
	if err := validateRunSignature(signature); err != nil {
		return err
	}
	found := false
	var latest common.RunUID
	for _, message := range messages {
		runUID, ok := common.RunUIDFromMessage(message)
		if !ok {
			continue
		}
		if runUID == signature.RunUID {
			found = true
		}
		latest = runUID
	}
	if !found {
		return ErrRunNotFound
	}
	if latest != signature.RunUID {
		return ErrRunNotCurrent
	}
	return nil
}

func runExists(messages []*schema.AgenticMessage, runUID common.RunUID) bool {
	for _, message := range messages {
		storedRunUID, ok := common.RunUIDFromMessage(message)
		if ok && storedRunUID == runUID {
			return true
		}
	}
	return false
}

func runUIDs(messages []*schema.AgenticMessage) []common.RunUID {
	runUIDs := make([]common.RunUID, 0)
	for _, message := range messages {
		if runUID, ok := common.RunUIDFromMessage(message); ok {
			runUIDs = append(runUIDs, runUID)
		}
	}
	return runUIDs
}

func validateSettlement(args *SettleRunArgs) error {
	if args == nil {
		return errors.New("settle run args are nil")
	}
	if err := validateRunSignature(args.Signature); err != nil {
		return err
	}
	switch args.Outcome {
	case RunOutcomeCompleted:
		return validateFinalMessage(args.FinalMessage)
	case RunOutcomeInterrupted, RunOutcomeCanceled, RunOutcomeFailed:
		if args.FinalMessage != nil {
			return ErrInvalidFinalMessage
		}
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidRunOutcome, args.Outcome)
	}
}
