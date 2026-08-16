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

// ContextHead is the bounded, frequently-read state used for mutations.
// It deliberately excludes conversation history.
type ContextHead struct {
	ContextUID    common.ContextUID
	Revision      uint64
	Finalized     bool
	CurrentRunUID common.RunUID
	PendingCount  int
}

// CreateRequest describes a new context. Initial messages become revision 1.
type CreateRequest struct {
	InitialMessages []*schema.AgenticMessage
	RunSnapshots    map[common.RunUID]RunSnapshot
}

// CreateResult identifies a newly-created context and its initial revision.
type CreateResult struct {
	ContextUID common.ContextUID
	Revision   uint64
}

// AppendRequest atomically appends one event batch when ExpectedRevision still
// matches the context head.
type AppendRequest struct {
	ContextUID       common.ContextUID
	ExpectedRevision uint64
	Events           []Event
}

// AppendResult reports the committed revision and any pending messages consumed
// by EventTurnCommitted.
type AppendResult struct {
	Revision               uint64
	AppliedPendingMessages []*schema.AgenticMessage
}

// RevisionedEvent is one atomically-persisted event batch.
type RevisionedEvent struct {
	Revision uint64
	Events   []Event
}

// ReadViewRequest selects a materialized context revision. Revision 0 means
// latest. MessageLimit 0 returns all messages; a positive limit returns the tail.
type ReadViewRequest struct {
	ContextUID     common.ContextUID
	Revision       uint64
	MessageLimit   int
	IncludePending bool
	IncludeRuns    bool
}

// ContextView is an on-demand materialized read model, not mutation state.
type ContextView struct {
	ContextUID      common.ContextUID             `json:"-"`
	Revision        uint64                        `json:"revision"`
	Messages        []*schema.AgenticMessage      `json:"messages"`
	PendingMessages []*schema.AgenticMessage      `json:"pending_messages,omitempty"`
	RunSnapshots    map[common.RunUID]RunSnapshot `json:"run_snapshots,omitempty"`
}

// ContextStore persists an append-only context log, a lightweight head, and
// on-demand read views. Append performs CAS and all event-specific checks and
// projection changes atomically without materializing full message history.
type ContextStore interface {
	Create(context.Context, CreateRequest) (CreateResult, error)
	ReadHead(context.Context, common.ContextUID) (ContextHead, error)
	Append(context.Context, AppendRequest) (AppendResult, error)
	ReadEvents(context.Context, common.ContextUID, uint64) ([]RevisionedEvent, error)
	ReadView(context.Context, ReadViewRequest) (ContextView, error)
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
	Outcome  RunOutcome               `json:"outcome"`
	Revision uint64                   `json:"revision,omitempty"`
	Messages []*schema.AgenticMessage `json:"messages,omitempty"`
}

// State is kept as a source-compatible alias for event reducers and callers
// that explicitly request a complete read view. Mutations never load it.
type State = ContextView

// NewState returns a detached materialized view for event replay.
func NewState(messages []*schema.AgenticMessage) *State {
	return &State{
		Messages:        common.CloneAgenticMessages(messages),
		PendingMessages: []*schema.AgenticMessage{},
		RunSnapshots:    make(map[common.RunUID]RunSnapshot),
	}
}

// Clone copies all ContextView containers and nested messages.
func (s *ContextView) Clone() *ContextView {
	if s == nil {
		return NewState(nil)
	}
	clone := &ContextView{
		ContextUID:      s.ContextUID,
		Revision:        s.Revision,
		Messages:        common.CloneAgenticMessages(s.Messages),
		PendingMessages: common.CloneAgenticMessages(s.PendingMessages),
		RunSnapshots:    make(map[common.RunUID]RunSnapshot, len(s.RunSnapshots)),
	}
	for runUID, snapshot := range s.RunSnapshots {
		clone.RunSnapshots[runUID] = RunSnapshot{
			Outcome: snapshot.Outcome, Revision: snapshot.Revision,
			Messages: common.CloneAgenticMessages(snapshot.Messages),
		}
	}
	return clone
}

// SettleRunArgs atomically commits a terminal run outcome.
type SettleRunArgs struct {
	Signature    common.RunSignature
	Outcome      RunOutcome
	FinalMessage *schema.AgenticMessage
}

// TurnCommitResult describes pending messages atomically applied after a turn.
type TurnCommitResult struct {
	AppliedPendingMessages []*schema.AgenticMessage
}

// Manager owns context workflows; ContextStore owns atomic persistence rules.
type Manager struct{ store ContextStore }

func NewManager(store ContextStore) *Manager { return &Manager{store: store} }

func (m *Manager) Create(ctx context.Context, initialMessages []*schema.AgenticMessage) (common.ContextUID, error) {
	if err := validateMessages(initialMessages); err != nil {
		return "", err
	}
	if m == nil || m.store == nil {
		return "", ErrStoreUnavailable
	}
	result, err := m.store.Create(ctx, CreateRequest{InitialMessages: common.CloneAgenticMessages(initialMessages)})
	return result.ContextUID, err
}

func (m *Manager) Load(ctx context.Context, contextUID common.ContextUID) ([]*schema.AgenticMessage, error) {
	view, err := m.readFullView(ctx, contextUID, 0)
	if err != nil {
		return nil, err
	}
	return common.CloneAgenticMessages(view.Messages), nil
}

func (m *Manager) Append(ctx context.Context, contextUID common.ContextUID, messages ...*schema.AgenticMessage) error {
	if err := validateMessages(messages); err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}
	_, err := m.appendEvents(ctx, contextUID, []Event{{
		Type: EventMessagesAppended, Messages: common.CloneAgenticMessages(messages),
	}})
	return err
}

func (m *Manager) Replace(ctx context.Context, contextUID common.ContextUID, messages []*schema.AgenticMessage) error {
	if err := validateMessages(messages); err != nil {
		return err
	}
	_, err := m.appendEvents(ctx, contextUID, []Event{{
		Type: EventMessagesReplaced, Messages: common.CloneAgenticMessages(messages),
	}})
	return err
}

func (m *Manager) Enqueue(ctx context.Context, contextUID common.ContextUID, messages []*schema.AgenticMessage) error {
	if err := validatePendingMessages(messages); err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}
	_, err := m.appendEvents(ctx, contextUID, []Event{{
		Type: EventPendingEnqueued, Messages: common.CloneAgenticMessages(messages),
	}})
	return err
}

func (m *Manager) CommitTurn(ctx context.Context, contextUID common.ContextUID, turnMessages []*schema.AgenticMessage) (*TurnCommitResult, error) {
	if err := validateMessages(turnMessages); err != nil {
		return nil, err
	}
	result, err := m.appendEvents(ctx, contextUID, []Event{{
		Type: EventTurnCommitted, Messages: common.CloneAgenticMessages(turnMessages),
	}})
	if err != nil {
		return nil, err
	}
	return &TurnCommitResult{AppliedPendingMessages: result.AppliedPendingMessages}, nil
}

func (m *Manager) SettleRun(ctx context.Context, args *SettleRunArgs) error {
	if err := validateSettlement(args); err != nil {
		return err
	}
	_, err := m.appendEvents(ctx, args.Signature.ContextUID, []Event{{
		Type: EventRunSettled, RunUID: args.Signature.RunUID,
		Outcome: args.Outcome, FinalMessage: args.FinalMessage,
	}})
	return err
}

func (m *Manager) Fork(ctx context.Context, from common.RunSignature) (common.ContextUID, error) {
	if err := validateRunSignature(from); err != nil {
		return "", err
	}
	view, err := m.readFullView(ctx, from.ContextUID, 0)
	if err != nil {
		return "", err
	}
	snapshot, settled := view.RunSnapshots[from.RunUID]
	if !settled {
		if runExists(view.Messages, from.RunUID) {
			return "", ErrRunNotSettled
		}
		return "", ErrRunNotFound
	}
	snapshotView, err := m.readSnapshot(ctx, from.ContextUID, snapshot)
	if err != nil {
		return "", err
	}
	forked := NewState(snapshotView.Messages)
	for _, runUID := range runUIDs(snapshotView.Messages) {
		if inherited, exists := view.RunSnapshots[runUID]; exists {
			inheritedView, err := m.readSnapshot(ctx, from.ContextUID, inherited)
			if err != nil {
				return "", err
			}
			forked.RunSnapshots[runUID] = RunSnapshot{
				Outcome:  inherited.Outcome,
				Messages: common.CloneAgenticMessages(inheritedView.Messages),
			}
		}
	}
	result, err := m.store.Create(ctx, CreateRequest{
		InitialMessages: forked.Messages,
		RunSnapshots:    forked.RunSnapshots,
	})
	return result.ContextUID, err
}

func (m *Manager) Delete(ctx context.Context, contextUID common.ContextUID) error {
	if m == nil || m.store == nil {
		return ErrStoreUnavailable
	}
	return m.store.Delete(ctx, contextUID)
}

const maxUpdateAttempts = 32

func (m *Manager) appendEvents(ctx context.Context, contextUID common.ContextUID, events []Event) (AppendResult, error) {
	if m == nil || m.store == nil {
		return AppendResult{}, ErrStoreUnavailable
	}
	for attempt := 0; attempt < maxUpdateAttempts; attempt++ {
		head, err := m.store.ReadHead(ctx, contextUID)
		if err != nil {
			return AppendResult{}, err
		}
		result, err := m.store.Append(ctx, AppendRequest{
			ContextUID: contextUID, ExpectedRevision: head.Revision, Events: events,
		})
		if errors.Is(err, ErrRevisionConflict) {
			continue
		}
		return result, err
	}
	return AppendResult{}, fmt.Errorf("%w after %d attempts", ErrRevisionConflict, maxUpdateAttempts)
}

func (m *Manager) readHead(ctx context.Context, contextUID common.ContextUID) (ContextHead, error) {
	if m == nil || m.store == nil {
		return ContextHead{}, ErrStoreUnavailable
	}
	return m.store.ReadHead(ctx, contextUID)
}

func (m *Manager) readFullView(ctx context.Context, contextUID common.ContextUID, revision uint64) (ContextView, error) {
	if m == nil || m.store == nil {
		return ContextView{}, ErrStoreUnavailable
	}
	return m.store.ReadView(ctx, ReadViewRequest{
		ContextUID: contextUID, Revision: revision, IncludePending: true, IncludeRuns: true,
	})
}

func (m *Manager) readSnapshot(ctx context.Context, contextUID common.ContextUID, snapshot RunSnapshot) (ContextView, error) {
	if snapshot.Revision != 0 {
		return m.readFullView(ctx, contextUID, snapshot.Revision)
	}
	return ContextView{Revision: 1, Messages: common.CloneAgenticMessages(snapshot.Messages)}, nil
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

func runExists(messages []*schema.AgenticMessage, runUID common.RunUID) bool {
	for _, message := range messages {
		if storedRunUID, ok := common.RunUIDFromMessage(message); ok && storedRunUID == runUID {
			return true
		}
	}
	return false
}

func runUIDs(messages []*schema.AgenticMessage) []common.RunUID {
	result := make([]common.RunUID, 0)
	for _, message := range messages {
		if runUID, ok := common.RunUIDFromMessage(message); ok {
			result = append(result, runUID)
		}
	}
	return result
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
