// Package contextmgr coordinates conversation state and persistent storage.
package contextmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/torrischen/goat/agent/common"

	"github.com/cloudwego/eino/schema"
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

// Storage is the context manager's persistence boundary. Implementations only
// provide byte-oriented key-value operations and do not need to understand
// conversations, revisions, events, pending messages, runs, or forks.
type Storage interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte) error
	CreateIfAbsent(ctx context.Context, key string, value []byte) error
	CompareAndSwap(ctx context.Context, key string, oldValue, newValue []byte) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

const (
	maxUpdateAttempts = 32
	maxSequenceDepth  = 64
)

const (
	sequenceNodeLeaf   = "leaf"
	sequenceNodeConcat = "concat"
)

func keyHead(uid common.ContextUID) string {
	return fmt.Sprintf("contexts:%s:head", uid)
}

func newObjectKey(kind string) string {
	return fmt.Sprintf("objects:%s:%020d:%s", kind, time.Now().UnixNano(), uuid.NewString())
}

type contextHead struct {
	ContextUID  common.ContextUID `json:"context_uid"`
	Revision    uint64            `json:"revision"`
	RevisionKey string            `json:"revision_key"`
}

type sequenceRef struct {
	Root  string `json:"root,omitempty"`
	Count int    `json:"count,omitempty"`
	Depth int    `json:"depth,omitempty"`
}

type sequenceNode struct {
	Kind     string                   `json:"kind"`
	Left     string                   `json:"left,omitempty"`
	Right    string                   `json:"right,omitempty"`
	Messages []*schema.AgenticMessage `json:"messages,omitempty"`
}

type revisionObject struct {
	Revision  uint64      `json:"revision"`
	Parent    string      `json:"parent,omitempty"`
	Messages  sequenceRef `json:"messages"`
	Pending   sequenceRef `json:"pending"`
	RunsRoot  string      `json:"runs_root,omitempty"`
	Finalized bool        `json:"finalized,omitempty"`
	Event     *Event      `json:"event,omitempty"`
}

type runIndexNode struct {
	ContextUID common.ContextUID `json:"context_uid"`
	RunUID     common.RunUID     `json:"run_uid"`
	Parent     string            `json:"parent,omitempty"`
	Snapshot   RunSnapshot       `json:"snapshot"`
}

type objectWrite struct {
	key   string
	value []byte
}

type loadedContext struct {
	head        contextHead
	headPayload []byte
	revision    revisionObject
}

// RunOutcome records why a run reached its immutable terminal snapshot.
type RunOutcome string

const (
	RunOutcomeCompleted   RunOutcome = "completed"
	RunOutcomeInterrupted RunOutcome = "interrupted"
	RunOutcomeCanceled    RunOutcome = "canceled"
	RunOutcomeFailed      RunOutcome = "failed"
)

// RunSnapshot identifies the immutable revision captured when a run settled.
type RunSnapshot struct {
	Outcome     RunOutcome `json:"outcome"`
	Revision    uint64     `json:"revision"`
	RevisionKey string     `json:"revision_key"`
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

// Manager owns all context workflow and projection logic.
type Manager struct {
	storage Storage
}

func NewManager(storage Storage) *Manager {
	return &Manager{storage: storage}
}

// Create creates a conversation with its initial committed messages.
func (m *Manager) Create(ctx context.Context, initialMessages []*schema.AgenticMessage) (common.ContextUID, error) {
	if err := validateMessages(initialMessages); err != nil {
		return "", err
	}
	if err := m.checkStorage(); err != nil {
		return "", err
	}

	contextUID := common.ContextUID(uuid.NewString())
	if err := m.createContext(ctx, contextUID, initialMessages, "", ""); err != nil {
		return "", err
	}
	return contextUID, nil
}

// CreateWithUID creates a context with a specific UID.
func (m *Manager) CreateWithUID(ctx context.Context, contextUID common.ContextUID, initialMessages []*schema.AgenticMessage) error {
	if contextUID == "" {
		return fmt.Errorf("context UID must not be empty")
	}
	if err := validateMessages(initialMessages); err != nil {
		return err
	}
	if err := m.checkStorage(); err != nil {
		return err
	}

	return m.createContext(ctx, contextUID, initialMessages, "", "")
}

func (m *Manager) createContext(
	ctx context.Context,
	contextUID common.ContextUID,
	initialMessages []*schema.AgenticMessage,
	parentRevision string,
	runsRoot string,
) error {
	writes := make([]objectWrite, 0, 2)
	messages, err := m.newSequenceLeaf(contextUID, initialMessages, &writes)
	if err != nil {
		return err
	}

	revisionKey := newObjectKey("revisions")
	revision := revisionObject{
		Revision: 1,
		Parent:   parentRevision,
		Messages: messages,
		RunsRoot: runsRoot,
	}
	if err := appendObjectWrite(&writes, revisionKey, revision); err != nil {
		return err
	}
	if err := m.writeObjects(ctx, writes); err != nil {
		return err
	}

	head := contextHead{ContextUID: contextUID, Revision: 1, RevisionKey: revisionKey}
	headPayload, err := json.Marshal(head)
	if err != nil {
		return fmt.Errorf("encode context head: %w", err)
	}
	if err := m.storage.CreateIfAbsent(ctx, keyHead(contextUID), headPayload); errors.Is(err, ErrCASConflict) {
		return fmt.Errorf("context %s already exists", contextUID)
	} else {
		return err
	}
}

// Load returns committed history.
func (m *Manager) Load(ctx context.Context, contextUID common.ContextUID) ([]*schema.AgenticMessage, error) {
	if err := m.checkStorage(); err != nil {
		return nil, err
	}
	loaded, err := m.loadContext(ctx, contextUID)
	if err != nil {
		return nil, err
	}
	return m.loadSequence(ctx, loaded.revision.Messages)
}

// Append adds committed messages and reopens a previously completed context.
func (m *Manager) Append(ctx context.Context, contextUID common.ContextUID, messages ...*schema.AgenticMessage) error {
	if err := validateMessages(messages); err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}
	if err := m.checkStorage(); err != nil {
		return err
	}

	for attempt := 0; attempt < maxUpdateAttempts; attempt++ {
		loaded, err := m.loadContext(ctx, contextUID)
		if err != nil {
			return err
		}
		writes := make([]objectWrite, 0, 3)
		messageRoot, err := m.appendMessages(ctx, contextUID, loaded.revision.Messages, messages, &writes)
		if err != nil {
			return err
		}
		revision := revisionObject{
			Revision: loaded.head.Revision + 1,
			Parent:   loaded.head.RevisionKey,
			Messages: messageRoot,
			Pending:  loaded.revision.Pending,
			RunsRoot: loaded.revision.RunsRoot,
			Event: &Event{
				Type:     EventMessagesAppended,
				Messages: common.CloneAgenticMessages(messages),
			},
		}
		err = m.publish(ctx, contextUID, loaded, revision, writes)
		if errors.Is(err, ErrRevisionConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("%w after %d attempts", ErrRevisionConflict, maxUpdateAttempts)
}

// Replace replaces committed history while preserving pending messages and run snapshots.
func (m *Manager) Replace(ctx context.Context, contextUID common.ContextUID, messages []*schema.AgenticMessage) error {
	if err := validateMessages(messages); err != nil {
		return err
	}
	if err := m.checkStorage(); err != nil {
		return err
	}

	for attempt := 0; attempt < maxUpdateAttempts; attempt++ {
		loaded, err := m.loadContext(ctx, contextUID)
		if err != nil {
			return err
		}
		writes := make([]objectWrite, 0, 2)
		messageRoot, err := m.newSequenceLeaf(contextUID, messages, &writes)
		if err != nil {
			return err
		}
		revision := revisionObject{
			Revision:  loaded.head.Revision + 1,
			Parent:    loaded.head.RevisionKey,
			Messages:  messageRoot,
			Pending:   loaded.revision.Pending,
			RunsRoot:  loaded.revision.RunsRoot,
			Finalized: loaded.revision.Finalized,
			Event: &Event{
				Type:     EventMessagesReplaced,
				Messages: common.CloneAgenticMessages(messages),
			},
		}
		err = m.publish(ctx, contextUID, loaded, revision, writes)
		if errors.Is(err, ErrRevisionConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("%w after %d attempts", ErrRevisionConflict, maxUpdateAttempts)
}

// Enqueue adds user messages to the pending inbox.
func (m *Manager) Enqueue(ctx context.Context, contextUID common.ContextUID, messages []*schema.AgenticMessage) error {
	if err := validatePendingMessages(messages); err != nil {
		return err
	}
	if len(messages) == 0 {
		return nil
	}
	if err := m.checkStorage(); err != nil {
		return err
	}

	for attempt := 0; attempt < maxUpdateAttempts; attempt++ {
		loaded, err := m.loadContext(ctx, contextUID)
		if err != nil {
			return err
		}
		if loaded.revision.Finalized {
			return ErrConversationFinalized
		}
		writes := make([]objectWrite, 0, 3)
		pendingRoot, err := m.appendMessages(ctx, contextUID, loaded.revision.Pending, messages, &writes)
		if err != nil {
			return err
		}
		revision := revisionObject{
			Revision: loaded.head.Revision + 1,
			Parent:   loaded.head.RevisionKey,
			Messages: loaded.revision.Messages,
			Pending:  pendingRoot,
			RunsRoot: loaded.revision.RunsRoot,
			Event: &Event{
				Type:     EventPendingEnqueued,
				Messages: common.CloneAgenticMessages(messages),
			},
		}
		err = m.publish(ctx, contextUID, loaded, revision, writes)
		if errors.Is(err, ErrRevisionConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("%w after %d attempts", ErrRevisionConflict, maxUpdateAttempts)
}

// CommitTurn appends a complete non-final turn, applies pending messages after
// it, and clears the pending inbox in one head CAS.
func (m *Manager) CommitTurn(ctx context.Context, contextUID common.ContextUID, turnMessages []*schema.AgenticMessage) (*TurnCommitResult, error) {
	if err := validateMessages(turnMessages); err != nil {
		return nil, err
	}
	if err := m.checkStorage(); err != nil {
		return nil, err
	}

	for attempt := 0; attempt < maxUpdateAttempts; attempt++ {
		loaded, err := m.loadContext(ctx, contextUID)
		if err != nil {
			return nil, err
		}
		appliedPending, err := m.loadSequence(ctx, loaded.revision.Pending)
		if err != nil {
			return nil, err
		}
		if len(turnMessages) == 0 && len(appliedPending) == 0 {
			return &TurnCommitResult{AppliedPendingMessages: []*schema.AgenticMessage{}}, nil
		}

		writes := make([]objectWrite, 0, 5)
		messageRoot, err := m.commitMessages(
			ctx,
			contextUID,
			loaded.revision.Messages,
			turnMessages,
			loaded.revision.Pending,
			appliedPending,
			&writes,
		)
		if err != nil {
			return nil, err
		}
		revision := revisionObject{
			Revision:  loaded.head.Revision + 1,
			Parent:    loaded.head.RevisionKey,
			Messages:  messageRoot,
			RunsRoot:  loaded.revision.RunsRoot,
			Finalized: loaded.revision.Finalized,
			Event: &Event{
				Type:     EventTurnCommitted,
				Messages: common.CloneAgenticMessages(turnMessages),
			},
		}
		err = m.publish(ctx, contextUID, loaded, revision, writes)
		if errors.Is(err, ErrRevisionConflict) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &TurnCommitResult{AppliedPendingMessages: appliedPending}, nil
	}
	return nil, fmt.Errorf("%w after %d attempts", ErrRevisionConflict, maxUpdateAttempts)
}

// SettleRun records a terminal run outcome. State, final answer, pending clear,
// and the run snapshot become visible together when the head CAS succeeds.
func (m *Manager) SettleRun(ctx context.Context, args *SettleRunArgs) error {
	if err := validateSettlement(args); err != nil {
		return err
	}
	if err := m.checkStorage(); err != nil {
		return err
	}

	contextUID := args.Signature.ContextUID
	runUID := args.Signature.RunUID
	for attempt := 0; attempt < maxUpdateAttempts; attempt++ {
		loaded, err := m.loadContext(ctx, contextUID)
		if err != nil {
			return err
		}
		if _, settled, err := m.findRunSnapshot(ctx, loaded.revision.RunsRoot, runUID); err != nil {
			return err
		} else if settled {
			return nil
		}

		messages, err := m.loadSequence(ctx, loaded.revision.Messages)
		if err != nil {
			return err
		}
		if !runExists(messages, runUID) {
			return ErrRunNotFound
		}
		var current common.RunUID
		for _, message := range messages {
			if candidate, ok := common.RunUIDFromMessage(message); ok {
				current = candidate
			}
		}
		if current != runUID {
			return ErrRunNotCurrent
		}

		writes := make([]objectWrite, 0, 5)
		messageRoot := loaded.revision.Messages
		pendingRoot := loaded.revision.Pending
		finalized := loaded.revision.Finalized
		if args.Outcome == RunOutcomeCompleted {
			messageRoot, err = m.appendMessages(
				ctx,
				contextUID,
				messageRoot,
				[]*schema.AgenticMessage{args.FinalMessage},
				&writes,
			)
			if err != nil {
				return err
			}
			pendingRoot = sequenceRef{}
			finalized = true
		}

		revisionKey := newObjectKey("revisions")
		runNodeKey := newObjectKey("runs")
		revisionNumber := loaded.head.Revision + 1
		snapshot := RunSnapshot{
			Outcome:     args.Outcome,
			Revision:    revisionNumber,
			RevisionKey: revisionKey,
		}
		runNode := runIndexNode{
			ContextUID: contextUID,
			RunUID:     runUID,
			Parent:     loaded.revision.RunsRoot,
			Snapshot:   snapshot,
		}
		if err := appendObjectWrite(&writes, runNodeKey, runNode); err != nil {
			return err
		}

		revision := revisionObject{
			Revision:  revisionNumber,
			Parent:    loaded.head.RevisionKey,
			Messages:  messageRoot,
			Pending:   pendingRoot,
			RunsRoot:  runNodeKey,
			Finalized: finalized,
			Event: &Event{
				Type:         EventRunSettled,
				RunUID:       runUID,
				Outcome:      args.Outcome,
				FinalMessage: args.FinalMessage,
			},
		}
		err = m.publishAt(ctx, contextUID, loaded, revisionKey, revision, writes)
		if errors.Is(err, ErrRevisionConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("%w after %d attempts", ErrRevisionConflict, maxUpdateAttempts)
}

// Fork creates a context at the immutable revision captured by a settled run.
// Message and run objects are shared; pending messages and finalized state are not.
func (m *Manager) Fork(ctx context.Context, from common.RunSignature) (common.ContextUID, error) {
	if err := validateRunSignature(from); err != nil {
		return "", err
	}
	if err := m.checkStorage(); err != nil {
		return "", err
	}

	source, err := m.loadContext(ctx, from.ContextUID)
	if err != nil {
		return "", err
	}
	snapshot, settled, err := m.findRunSnapshot(ctx, source.revision.RunsRoot, from.RunUID)
	if err != nil {
		return "", err
	}
	if !settled {
		messages, err := m.loadSequence(ctx, source.revision.Messages)
		if err != nil {
			return "", err
		}
		if runExists(messages, from.RunUID) {
			return "", ErrRunNotSettled
		}
		return "", ErrRunNotFound
	}

	settledRevision, err := m.loadRevision(ctx, snapshot.RevisionKey)
	if err != nil {
		return "", err
	}
	forkedUID := common.ContextUID(uuid.NewString())
	revisionKey := newObjectKey("revisions")
	revision := revisionObject{
		Revision: 1,
		Parent:   snapshot.RevisionKey,
		Messages: settledRevision.Messages,
		RunsRoot: settledRevision.RunsRoot,
	}
	writes := make([]objectWrite, 0, 1)
	if err := appendObjectWrite(&writes, revisionKey, revision); err != nil {
		return "", err
	}
	if err := m.writeObjects(ctx, writes); err != nil {
		return "", err
	}
	head := contextHead{ContextUID: forkedUID, Revision: 1, RevisionKey: revisionKey}
	headPayload, err := json.Marshal(head)
	if err != nil {
		return "", fmt.Errorf("encode fork head: %w", err)
	}
	if err := m.storage.CreateIfAbsent(ctx, keyHead(forkedUID), headPayload); err != nil {
		if errors.Is(err, ErrCASConflict) {
			return "", fmt.Errorf("context %s already exists", forkedUID)
		}
		return "", err
	}
	return forkedUID, nil
}

// Delete removes the context head. Immutable objects may remain reachable from
// forks and are reclaimed separately by CollectGarbage.
func (m *Manager) Delete(ctx context.Context, contextUID common.ContextUID) error {
	if err := m.checkStorage(); err != nil {
		return err
	}
	return m.storage.Delete(ctx, keyHead(contextUID))
}

// CollectGarbage deletes immutable objects that are unreachable from every
// context head and older than minObjectAge. The age guard protects objects that
// a concurrent mutation has written but has not published through head CAS yet.
func (m *Manager) CollectGarbage(ctx context.Context, minObjectAge time.Duration) (int, error) {
	if err := m.checkStorage(); err != nil {
		return 0, err
	}
	if minObjectAge <= 0 {
		return 0, fmt.Errorf("garbage collection minimum object age must be positive")
	}

	headKeys, err := m.storage.List(ctx, "contexts:")
	if err != nil {
		return 0, err
	}
	roots := make([]string, 0, len(headKeys))
	for _, key := range headKeys {
		if !strings.HasSuffix(key, ":head") {
			continue
		}
		payload, err := m.storage.Get(ctx, key)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return 0, err
		}
		var head contextHead
		if err := json.Unmarshal(payload, &head); err != nil {
			return 0, fmt.Errorf("decode context head %q during garbage collection: %w", key, err)
		}
		if head.RevisionKey != "" {
			roots = append(roots, head.RevisionKey)
		}
	}

	reachable, err := m.markReachableObjects(ctx, roots)
	if err != nil {
		return 0, err
	}
	objectKeys, err := m.storage.List(ctx, "objects:")
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-minObjectAge).UnixNano()
	deleted := 0
	for _, key := range objectKeys {
		if _, ok := reachable[key]; ok {
			continue
		}
		createdAt, ok := objectCreationTime(key)
		if !ok || createdAt > cutoff {
			continue
		}
		if err := m.storage.Delete(ctx, key); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func (m *Manager) markReachableObjects(ctx context.Context, roots []string) (map[string]struct{}, error) {
	marked := make(map[string]struct{})
	queue := append([]string(nil), roots...)
	for len(queue) > 0 {
		last := len(queue) - 1
		key := queue[last]
		queue = queue[:last]
		if key == "" {
			continue
		}
		if _, exists := marked[key]; exists {
			continue
		}
		payload, err := m.storage.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("read reachable object %q: %w", key, err)
		}
		marked[key] = struct{}{}

		switch {
		case strings.HasPrefix(key, "objects:revisions:"):
			var revision revisionObject
			if err := json.Unmarshal(payload, &revision); err != nil {
				return nil, fmt.Errorf("decode revision object %q: %w", key, err)
			}
			queue = append(queue, revision.Parent, revision.Messages.Root, revision.Pending.Root, revision.RunsRoot)
		case strings.HasPrefix(key, "objects:sequences:"):
			var node sequenceNode
			if err := json.Unmarshal(payload, &node); err != nil {
				return nil, fmt.Errorf("decode sequence object %q: %w", key, err)
			}
			queue = append(queue, node.Left, node.Right)
		case strings.HasPrefix(key, "objects:runs:"):
			var node runIndexNode
			if err := json.Unmarshal(payload, &node); err != nil {
				return nil, fmt.Errorf("decode run index object %q: %w", key, err)
			}
			queue = append(queue, node.Parent, node.Snapshot.RevisionKey)
		default:
			return nil, fmt.Errorf("unknown immutable object key %q", key)
		}
	}
	return marked, nil
}

func objectCreationTime(key string) (int64, bool) {
	parts := strings.Split(key, ":")
	if len(parts) != 4 || parts[0] != "objects" {
		return 0, false
	}
	createdAt, err := strconv.ParseInt(parts[2], 10, 64)
	return createdAt, err == nil
}

func (m *Manager) checkStorage() error {
	if m == nil || m.storage == nil {
		return ErrStoreUnavailable
	}
	return nil
}

func (m *Manager) loadContext(ctx context.Context, contextUID common.ContextUID) (loadedContext, error) {
	headPayload, err := m.storage.Get(ctx, keyHead(contextUID))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return loadedContext{}, ErrContextNotFound
		}
		return loadedContext{}, err
	}
	var head contextHead
	if err := json.Unmarshal(headPayload, &head); err != nil {
		return loadedContext{}, fmt.Errorf("decode context head: %w", err)
	}
	if head.ContextUID != contextUID || head.RevisionKey == "" {
		return loadedContext{}, fmt.Errorf("invalid context head for %q", contextUID)
	}
	revision, err := m.loadRevision(ctx, head.RevisionKey)
	if err != nil {
		return loadedContext{}, err
	}
	if revision.Revision != head.Revision {
		return loadedContext{}, fmt.Errorf(
			"context %s head revision %d points to revision %d",
			contextUID,
			head.Revision,
			revision.Revision,
		)
	}
	return loadedContext{head: head, headPayload: headPayload, revision: revision}, nil
}

func (m *Manager) loadRevision(ctx context.Context, key string) (revisionObject, error) {
	payload, err := m.storage.Get(ctx, key)
	if err != nil {
		return revisionObject{}, fmt.Errorf("read revision object %q: %w", key, err)
	}
	var revision revisionObject
	if err := json.Unmarshal(payload, &revision); err != nil {
		return revisionObject{}, fmt.Errorf("decode revision object %q: %w", key, err)
	}
	return revision, nil
}

func (m *Manager) publish(
	ctx context.Context,
	contextUID common.ContextUID,
	loaded loadedContext,
	revision revisionObject,
	writes []objectWrite,
) error {
	return m.publishAt(ctx, contextUID, loaded, newObjectKey("revisions"), revision, writes)
}

func (m *Manager) publishAt(
	ctx context.Context,
	contextUID common.ContextUID,
	loaded loadedContext,
	revisionKey string,
	revision revisionObject,
	writes []objectWrite,
) error {
	if err := appendObjectWrite(&writes, revisionKey, revision); err != nil {
		return err
	}
	if err := m.writeObjects(ctx, writes); err != nil {
		return err
	}

	newHead := contextHead{
		ContextUID:  contextUID,
		Revision:    revision.Revision,
		RevisionKey: revisionKey,
	}
	newHeadPayload, err := json.Marshal(newHead)
	if err != nil {
		return fmt.Errorf("encode context head: %w", err)
	}
	err = m.storage.CompareAndSwap(ctx, keyHead(contextUID), loaded.headPayload, newHeadPayload)
	if errors.Is(err, ErrNotFound) {
		return ErrContextNotFound
	}
	if errors.Is(err, ErrCASConflict) {
		return ErrRevisionConflict
	}
	return err
}

func appendObjectWrite(writes *[]objectWrite, key string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode object %q: %w", key, err)
	}
	*writes = append(*writes, objectWrite{key: key, value: payload})
	return nil
}

func (m *Manager) writeObjects(ctx context.Context, writes []objectWrite) error {
	for _, write := range writes {
		if err := m.storage.Set(ctx, write.key, write.value); err != nil {
			return fmt.Errorf("write immutable object %q: %w", write.key, err)
		}
	}
	return nil
}

func (m *Manager) newSequenceLeaf(
	_ common.ContextUID,
	messages []*schema.AgenticMessage,
	writes *[]objectWrite,
) (sequenceRef, error) {
	if len(messages) == 0 {
		return sequenceRef{}, nil
	}
	key := newObjectKey("sequences")
	node := sequenceNode{
		Kind:     sequenceNodeLeaf,
		Messages: common.CloneAgenticMessages(messages),
	}
	if err := appendObjectWrite(writes, key, node); err != nil {
		return sequenceRef{}, err
	}
	return sequenceRef{Root: key, Count: len(messages), Depth: 1}, nil
}

func (m *Manager) concatSequences(left, right sequenceRef, writes *[]objectWrite) (sequenceRef, error) {
	if left.Root == "" {
		return right, nil
	}
	if right.Root == "" {
		return left, nil
	}
	key := newObjectKey("sequences")
	node := sequenceNode{Kind: sequenceNodeConcat, Left: left.Root, Right: right.Root}
	if err := appendObjectWrite(writes, key, node); err != nil {
		return sequenceRef{}, err
	}
	depth := left.Depth
	if right.Depth > depth {
		depth = right.Depth
	}
	return sequenceRef{Root: key, Count: left.Count + right.Count, Depth: depth + 1}, nil
}

func (m *Manager) appendMessages(
	ctx context.Context,
	contextUID common.ContextUID,
	base sequenceRef,
	messages []*schema.AgenticMessage,
	writes *[]objectWrite,
) (sequenceRef, error) {
	if len(messages) == 0 {
		return base, nil
	}
	if base.Root != "" && base.Depth+1 > maxSequenceDepth {
		existing, err := m.loadSequence(ctx, base)
		if err != nil {
			return sequenceRef{}, err
		}
		compacted := append(existing, common.CloneAgenticMessages(messages)...)
		return m.newSequenceLeaf(contextUID, compacted, writes)
	}
	leaf, err := m.newSequenceLeaf(contextUID, messages, writes)
	if err != nil {
		return sequenceRef{}, err
	}
	return m.concatSequences(base, leaf, writes)
}

func (m *Manager) commitMessages(
	ctx context.Context,
	contextUID common.ContextUID,
	committed sequenceRef,
	turnMessages []*schema.AgenticMessage,
	pending sequenceRef,
	pendingMessages []*schema.AgenticMessage,
	writes *[]objectWrite,
) (sequenceRef, error) {
	predicted := committed
	if len(turnMessages) > 0 {
		if predicted.Root == "" {
			predicted = sequenceRef{Count: len(turnMessages), Depth: 1}
		} else {
			predicted.Count += len(turnMessages)
			predicted.Depth++
		}
	}
	if pending.Root != "" {
		depth := predicted.Depth
		if pending.Depth > depth {
			depth = pending.Depth
		}
		predicted.Count += pending.Count
		predicted.Depth = depth + 1
	}

	if predicted.Depth > maxSequenceDepth {
		messages, err := m.loadSequence(ctx, committed)
		if err != nil {
			return sequenceRef{}, err
		}
		messages = append(messages, common.CloneAgenticMessages(turnMessages)...)
		messages = append(messages, common.CloneAgenticMessages(pendingMessages)...)
		return m.newSequenceLeaf(contextUID, messages, writes)
	}

	result := committed
	if len(turnMessages) > 0 {
		turn, err := m.newSequenceLeaf(contextUID, turnMessages, writes)
		if err != nil {
			return sequenceRef{}, err
		}
		result, err = m.concatSequences(result, turn, writes)
		if err != nil {
			return sequenceRef{}, err
		}
	}
	return m.concatSequences(result, pending, writes)
}

func (m *Manager) loadSequence(ctx context.Context, ref sequenceRef) ([]*schema.AgenticMessage, error) {
	if ref.Root == "" {
		return []*schema.AgenticMessage{}, nil
	}
	result := make([]*schema.AgenticMessage, 0, ref.Count)
	stack := []string{ref.Root}
	for len(stack) > 0 {
		last := len(stack) - 1
		key := stack[last]
		stack = stack[:last]
		payload, err := m.storage.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("read sequence object %q: %w", key, err)
		}
		var node sequenceNode
		if err := json.Unmarshal(payload, &node); err != nil {
			return nil, fmt.Errorf("decode sequence object %q: %w", key, err)
		}
		switch node.Kind {
		case sequenceNodeLeaf:
			result = append(result, node.Messages...)
		case sequenceNodeConcat:
			if node.Left == "" || node.Right == "" {
				return nil, fmt.Errorf("invalid concat sequence object %q", key)
			}
			stack = append(stack, node.Right, node.Left)
		default:
			return nil, fmt.Errorf("unknown sequence node kind %q", node.Kind)
		}
	}
	if len(result) != ref.Count {
		return nil, fmt.Errorf("sequence count mismatch: loaded %d, expected %d", len(result), ref.Count)
	}
	return result, nil
}

func (m *Manager) findRunSnapshot(
	ctx context.Context,
	root string,
	runUID common.RunUID,
) (RunSnapshot, bool, error) {
	for key := root; key != ""; {
		payload, err := m.storage.Get(ctx, key)
		if err != nil {
			return RunSnapshot{}, false, fmt.Errorf("read run index object %q: %w", key, err)
		}
		var node runIndexNode
		if err := json.Unmarshal(payload, &node); err != nil {
			return RunSnapshot{}, false, fmt.Errorf("decode run index object %q: %w", key, err)
		}
		if node.RunUID == runUID {
			return node.Snapshot, true, nil
		}
		key = node.Parent
	}
	return RunSnapshot{}, false, nil
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

func runExists(messages []*schema.AgenticMessage, runUID common.RunUID) bool {
	for _, message := range messages {
		if storedRunUID, ok := common.RunUIDFromMessage(message); ok && storedRunUID == runUID {
			return true
		}
	}
	return false
}
