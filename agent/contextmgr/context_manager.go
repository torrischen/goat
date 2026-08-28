// Package contextmgr coordinates conversation state and persistent storage.
package contextmgr

import (
	"context"
	"errors"
	"fmt"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/message"

	"github.com/google/uuid"
)

var (
	ErrContextNotFound       = errors.New("context not found")
	ErrRevisionConflict      = errors.New("context revision conflict")
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

	ErrNotFound    = errors.New("key not found")
	ErrCASConflict = errors.New("compare-and-swap conflict")
)

const (
	maxUpdateAttempts = 32
)

// RunOutcome records why a run reached its immutable terminal snapshot.
type RunOutcome string

const (
	RunOutcomeCompleted   RunOutcome = "completed"
	RunOutcomeInterrupted RunOutcome = "interrupted"
	RunOutcomeCanceled    RunOutcome = "canceled"
	RunOutcomeFailed      RunOutcome = "failed"
)

// RunSnapshot identifies the immutable seq watermark captured when a run settled.
type RunSnapshot struct {
	Outcome      RunOutcome `json:"outcome"`
	CommittedSeq uint64     `json:"committed_seq"`
}

// SettleRunArgs atomically commits a terminal run outcome.
type SettleRunArgs struct {
	Signature    common.RunSignature
	Outcome      RunOutcome
	FinalMessage *message.Message
}

// TurnCommitResult describes pending messages atomically applied after a turn.
type TurnCommitResult struct {
	AppliedPendingMessages []*message.Message
}

// Manager owns all context workflow and projection logic.
type Manager struct {
	store Store
}

func NewManager(store Store) *Manager {
	return &Manager{store: store}
}

// Create creates a conversation with its initial committed messages.
func (m *Manager) Create(ctx context.Context, initialMessages []*message.Message) (common.ContextUID, error) {
	if err := validateMessages(initialMessages); err != nil {
		return "", err
	}
	if err := m.checkStore(); err != nil {
		return "", err
	}

	uid := common.ContextUID(uuid.NewString())
	if err := m.createContext(ctx, uid, initialMessages); err != nil {
		return "", err
	}
	return uid, nil
}

// CreateWithUID creates a context with a specific UID.
func (m *Manager) CreateWithUID(ctx context.Context, uid common.ContextUID, initialMessages []*message.Message) error {
	if uid == "" {
		return fmt.Errorf("context UID must not be empty")
	}
	if err := validateMessages(initialMessages); err != nil {
		return err
	}
	if err := m.checkStore(); err != nil {
		return err
	}
	return m.createContext(ctx, uid, initialMessages)
}

func (m *Manager) createContext(ctx context.Context, uid common.ContextUID, initialMessages []*message.Message) error {
	rows := make([]MessageRow, len(initialMessages))
	for i, msg := range initialMessages {
		rows[i] = MessageRow{
			UID:     uid,
			Lane:    LaneCommitted,
			Seq:     uint64(i + 1),
			Message: common.CloneAgenticMessages([]*message.Message{msg})[0],
		}
	}
	if err := m.store.AppendMessages(ctx, rows); err != nil {
		return err
	}

	head := &Head{
		UID:          uid,
		Generation:   uuid.NewString(),
		Version:      1,
		CommittedSeq: uint64(len(initialMessages)),
		Runs:         make(map[common.RunUID]RunSnapshot),
	}
	err := m.store.CreateHead(ctx, head)
	if errors.Is(err, ErrCASConflict) {
		return fmt.Errorf("context %s already exists", uid)
	}
	return err
}

// Load returns committed history.
func (m *Manager) Load(ctx context.Context, uid common.ContextUID) ([]*message.Message, error) {
	if err := m.checkStore(); err != nil {
		return nil, err
	}
	head, err := m.store.LoadHead(ctx, uid)
	if err != nil {
		return nil, err
	}
	return m.loadMessages(ctx, uid, LaneCommitted, 1, head.CommittedSeq)
}

// Append adds committed messages and reopens a previously completed context.
func (m *Manager) Append(ctx context.Context, uid common.ContextUID, messages ...*message.Message) error {
	if err := validateMessages(messages); err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}
	if err := m.checkStore(); err != nil {
		return err
	}

	for attempt := 0; attempt < maxUpdateAttempts; attempt++ {
		head, err := m.store.LoadHead(ctx, uid)
		if err != nil {
			return err
		}

		rows := make([]MessageRow, len(messages))
		for i, msg := range messages {
			rows[i] = MessageRow{
				UID:     uid,
				Lane:    LaneCommitted,
				Seq:     head.CommittedSeq + uint64(i+1),
				Message: common.CloneAgenticMessages([]*message.Message{msg})[0],
			}
		}
		if err := m.store.AppendMessages(ctx, rows); err != nil {
			return err
		}

		next := *head
		next.Version = head.Version + 1
		next.CommittedSeq = head.CommittedSeq + uint64(len(messages))
		next.Finalized = false

		err = m.store.CommitHead(ctx, &next, head.Version)
		if errors.Is(err, ErrCASConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("%w after %d attempts", ErrRevisionConflict, maxUpdateAttempts)
}

// Replace atomically replaces committed history while preserving pending messages
// and invalidating all previous fork points.
func (m *Manager) Replace(ctx context.Context, uid common.ContextUID, messages []*message.Message) error {
	if err := validateMessages(messages); err != nil {
		return err
	}
	if err := m.checkStore(); err != nil {
		return err
	}

	for attempt := 0; attempt < maxUpdateAttempts; attempt++ {
		head, err := m.store.LoadHead(ctx, uid)
		if err != nil {
			return err
		}

		rows := make([]MessageRow, len(messages))
		for i, msg := range messages {
			rows[i] = MessageRow{
				UID:     uid,
				Lane:    LaneCommitted,
				Seq:     uint64(i + 1),
				Message: common.CloneAgenticMessages([]*message.Message{msg})[0],
			}
		}

		next := *head
		next.Version = head.Version + 1
		next.CommittedSeq = uint64(len(messages))
		// Replacing the conversation invalidates all previous fork points.
		next.Runs = make(map[common.RunUID]RunSnapshot)

		err = m.store.ReplaceCommitted(ctx, uid, rows, &next, head.Version)
		if errors.Is(err, ErrCASConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("%w after %d attempts", ErrRevisionConflict, maxUpdateAttempts)
}

// Enqueue adds user messages to the pending inbox.
func (m *Manager) Enqueue(ctx context.Context, uid common.ContextUID, messages []*message.Message) error {
	if err := validatePendingMessages(messages); err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}
	if err := m.checkStore(); err != nil {
		return err
	}

	for attempt := 0; attempt < maxUpdateAttempts; attempt++ {
		head, err := m.store.LoadHead(ctx, uid)
		if err != nil {
			return err
		}
		if head.Finalized {
			return ErrConversationFinalized
		}

		pendingStart := head.PendingStart
		if pendingStart == 0 {
			pendingStart = head.PendingSeq + 1
		}

		rows := make([]MessageRow, len(messages))
		for i, msg := range messages {
			rows[i] = MessageRow{
				UID:     uid,
				Lane:    LanePending,
				Seq:     head.PendingSeq + uint64(i+1),
				Message: common.CloneAgenticMessages([]*message.Message{msg})[0],
			}
		}
		if err := m.store.AppendMessages(ctx, rows); err != nil {
			return err
		}

		next := *head
		next.Version = head.Version + 1
		next.PendingStart = pendingStart
		next.PendingSeq = head.PendingSeq + uint64(len(messages))

		err = m.store.CommitHead(ctx, &next, head.Version)
		if errors.Is(err, ErrCASConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("%w after %d attempts", ErrRevisionConflict, maxUpdateAttempts)
}

// CommitTurn appends a complete non-final turn, applies pending messages after it,
// and clears the pending inbox in one head CAS.
func (m *Manager) CommitTurn(ctx context.Context, uid common.ContextUID, turnMessages []*message.Message) (*TurnCommitResult, error) {
	if err := validateMessages(turnMessages); err != nil {
		return nil, err
	}
	if err := m.checkStore(); err != nil {
		return nil, err
	}

	for attempt := 0; attempt < maxUpdateAttempts; attempt++ {
		head, err := m.store.LoadHead(ctx, uid)
		if err != nil {
			return nil, err
		}

		pendingMessages, err := m.loadMessages(ctx, uid, LanePending, head.PendingStart, head.PendingSeq)
		if err != nil {
			return nil, err
		}
		if len(turnMessages) == 0 && len(pendingMessages) == 0 {
			return &TurnCommitResult{AppliedPendingMessages: []*message.Message{}}, nil
		}

		allMessages := append(turnMessages, pendingMessages...)
		rows := make([]MessageRow, len(allMessages))
		for i, msg := range allMessages {
			rows[i] = MessageRow{
				UID:     uid,
				Lane:    LaneCommitted,
				Seq:     head.CommittedSeq + uint64(i+1),
				Message: common.CloneAgenticMessages([]*message.Message{msg})[0],
			}
		}
		if err := m.store.AppendMessages(ctx, rows); err != nil {
			return nil, err
		}

		next := *head
		next.Version = head.Version + 1
		next.CommittedSeq = head.CommittedSeq + uint64(len(allMessages))
		next.PendingStart = 0

		err = m.store.CommitHead(ctx, &next, head.Version)
		if errors.Is(err, ErrCASConflict) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &TurnCommitResult{AppliedPendingMessages: pendingMessages}, nil
	}
	return nil, fmt.Errorf("%w after %d attempts", ErrRevisionConflict, maxUpdateAttempts)
}

// SettleRun records a terminal run outcome. State, final answer, pending clear,
// and the run snapshot become visible together when the head CAS succeeds.
func (m *Manager) SettleRun(ctx context.Context, args *SettleRunArgs) error {
	if err := validateSettlement(args); err != nil {
		return err
	}
	if err := m.checkStore(); err != nil {
		return err
	}

	uid := args.Signature.ContextUID
	runUID := args.Signature.RunUID

	for attempt := 0; attempt < maxUpdateAttempts; attempt++ {
		head, err := m.store.LoadHead(ctx, uid)
		if err != nil {
			return err
		}

		if _, settled := head.Runs[runUID]; settled {
			return nil
		}

		messages, err := m.loadMessages(ctx, uid, LaneCommitted, 1, head.CommittedSeq)
		if err != nil {
			return err
		}
		if !runExists(messages, runUID) {
			return ErrRunNotFound
		}

		var current common.RunUID
		for _, msg := range messages {
			if candidate, ok := common.RunUIDFromMessage(msg); ok {
				current = candidate
			}
		}
		if current != runUID {
			return ErrRunNotCurrent
		}

		next := *head
		next.Version = head.Version + 1
		if next.Runs == nil {
			next.Runs = make(map[common.RunUID]RunSnapshot)
		}

		if args.Outcome == RunOutcomeCompleted {
			rows := []MessageRow{{
				UID:     uid,
				Lane:    LaneCommitted,
				Seq:     head.CommittedSeq + 1,
				Message: common.CloneAgenticMessages([]*message.Message{args.FinalMessage})[0],
			}}
			if err := m.store.AppendMessages(ctx, rows); err != nil {
				return err
			}
			next.CommittedSeq = head.CommittedSeq + 1
			next.PendingStart = 0
			next.Finalized = true
		}

		// Scheme A retains only the latest valid fork point.
		next.Runs = map[common.RunUID]RunSnapshot{
			runUID: {
				Outcome:      args.Outcome,
				CommittedSeq: next.CommittedSeq,
			},
		}

		err = m.store.CommitHead(ctx, &next, head.Version)
		if errors.Is(err, ErrCASConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("%w after %d attempts", ErrRevisionConflict, maxUpdateAttempts)
}

// Fork creates a context at the latest valid seq watermark captured by a settled run.
func (m *Manager) Fork(ctx context.Context, from common.RunSignature) (common.ContextUID, error) {
	if err := validateRunSignature(from); err != nil {
		return "", err
	}
	if err := m.checkStore(); err != nil {
		return "", err
	}

	source, err := m.store.LoadHead(ctx, from.ContextUID)
	if err != nil {
		return "", err
	}

	snapshot, settled := source.Runs[from.RunUID]
	if !settled {
		return "", ErrRunNotSettled
	}

	messages, err := m.loadMessages(ctx, from.ContextUID, LaneCommitted, 1, snapshot.CommittedSeq)
	if err != nil {
		return "", err
	}

	newUID := common.ContextUID(uuid.NewString())
	rows := make([]MessageRow, len(messages))
	for i, msg := range messages {
		rows[i] = MessageRow{
			UID:     newUID,
			Lane:    LaneCommitted,
			Seq:     uint64(i + 1),
			Message: msg,
		}
	}
	if err := m.store.AppendMessages(ctx, rows); err != nil {
		return "", err
	}

	head := &Head{
		UID:          newUID,
		Generation:   uuid.NewString(),
		Version:      1,
		CommittedSeq: uint64(len(messages)),
		Runs:         make(map[common.RunUID]RunSnapshot),
	}
	if err := m.store.CreateHead(ctx, head); err != nil {
		return "", err
	}
	return newUID, nil
}

// Delete removes the context head.
func (m *Manager) Delete(ctx context.Context, uid common.ContextUID) error {
	if err := m.checkStore(); err != nil {
		return err
	}
	return m.store.DeleteContext(ctx, uid)
}

func (m *Manager) loadMessages(ctx context.Context, uid common.ContextUID, lane Lane, fromSeq, toSeq uint64) ([]*message.Message, error) {
	if fromSeq == 0 || toSeq < fromSeq || toSeq == 0 {
		return []*message.Message{}, nil
	}
	rows, err := m.store.ReadMessages(ctx, uid, lane, fromSeq, toSeq)
	if err != nil {
		return nil, err
	}
	messages := make([]*message.Message, len(rows))
	for i, row := range rows {
		messages[i] = common.CloneAgenticMessages([]*message.Message{row.Message})[0]
	}
	return messages, nil
}

func (m *Manager) checkStore() error {
	if m.store == nil {
		return ErrStoreUnavailable
	}
	return nil
}

func validateMessages(messages []*message.Message) error {
	for i, msg := range messages {
		if msg == nil {
			return fmt.Errorf("%w at index %d", ErrInvalidMessage, i)
		}
	}
	return nil
}

func validatePendingMessages(messages []*message.Message) error {
	for i, msg := range messages {
		if msg == nil || msg.Role != message.RoleUser {
			return fmt.Errorf("%w at index %d", ErrInvalidPendingMessage, i)
		}
	}
	return nil
}

func isFinalAnswerMessage(msg *message.Message) bool {
	if msg == nil || msg.Role != message.RoleAssistant {
		return false
	}
	for _, block := range msg.Blocks {
		if block != nil && block.Kind == message.BlockToolCall {
			return false
		}
	}
	return true
}

func validateFinalMessage(msg *message.Message) error {
	if !isFinalAnswerMessage(msg) {
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

func runExists(messages []*message.Message, runUID common.RunUID) bool {
	for _, msg := range messages {
		if storedRunUID, ok := common.RunUIDFromMessage(msg); ok && storedRunUID == runUID {
			return true
		}
	}
	return false
}
