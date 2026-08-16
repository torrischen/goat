package ram

import (
	"context"
	"fmt"
	"sync"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type stream struct {
	head        contextmgr.ContextHead
	initial     *contextmgr.State
	current     *contextmgr.State
	checkpoints map[uint64]*contextmgr.State
	revisions   [][]contextmgr.Event
}

// RAMStore is the in-process reference implementation of ContextStore.
type RAMStore struct {
	mu      sync.RWMutex
	streams map[common.ContextUID]*stream
}

var _ contextmgr.ContextStore = (*RAMStore)(nil)

func NewRAMStore() *RAMStore { return &RAMStore{streams: make(map[common.ContextUID]*stream)} }

func NewRAMContextManager() *contextmgr.Manager { return contextmgr.NewManager(NewRAMStore()) }

func (s *RAMStore) Create(ctx context.Context, request contextmgr.CreateRequest) (contextmgr.CreateResult, error) {
	if err := ctx.Err(); err != nil {
		return contextmgr.CreateResult{}, err
	}
	initialState := contextmgr.NewState(request.InitialMessages)
	for runUID, snapshot := range request.RunSnapshots {
		initialState.RunSnapshots[runUID] = snapshot
	}
	state, err := cloneState(initialState)
	if err != nil {
		return contextmgr.CreateResult{}, err
	}
	state.Revision = 1

	s.mu.Lock()
	defer s.mu.Unlock()
	var contextUID common.ContextUID
	for {
		contextUID = common.ContextUID(uuid.NewString())
		if _, exists := s.streams[contextUID]; !exists {
			break
		}
	}
	state.ContextUID = contextUID
	head := headFromState(contextUID, state)
	initial, err := cloneState(state)
	if err != nil {
		return contextmgr.CreateResult{}, err
	}
	s.streams[contextUID] = &stream{
		head: head, initial: initial, current: state,
		checkpoints: map[uint64]*contextmgr.State{1: initial},
	}
	return contextmgr.CreateResult{ContextUID: contextUID, Revision: 1}, nil
}

func (s *RAMStore) ReadHead(ctx context.Context, contextUID common.ContextUID) (contextmgr.ContextHead, error) {
	if err := ctx.Err(); err != nil {
		return contextmgr.ContextHead{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	stored, exists := s.streams[contextUID]
	if !exists {
		return contextmgr.ContextHead{}, contextmgr.ErrContextNotFound
	}
	return stored.head, nil
}

func (s *RAMStore) Append(ctx context.Context, request contextmgr.AppendRequest) (contextmgr.AppendResult, error) {
	if err := ctx.Err(); err != nil {
		return contextmgr.AppendResult{}, err
	}
	if err := contextmgr.ValidateEvents(request.Events); err != nil {
		return contextmgr.AppendResult{}, err
	}
	cloned, err := contextmgr.CloneEvents(request.Events)
	if err != nil {
		return contextmgr.AppendResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	stored, exists := s.streams[request.ContextUID]
	if !exists {
		return contextmgr.AppendResult{}, contextmgr.ErrContextNotFound
	}
	if stored.head.Revision != request.ExpectedRevision {
		return contextmgr.AppendResult{}, contextmgr.ErrRevisionConflict
	}
	appliedPending, noOp, err := contextmgr.ValidateTransition(stored.current, cloned)
	if err != nil {
		return contextmgr.AppendResult{}, err
	}
	if noOp {
		return contextmgr.AppendResult{Revision: stored.head.Revision}, nil
	}

	next, err := cloneState(stored.current)
	if err != nil {
		return contextmgr.AppendResult{}, err
	}
	nextRevision := request.ExpectedRevision + 1
	if err := contextmgr.ApplyEvents(next, nextRevision, cloned); err != nil {
		return contextmgr.AppendResult{}, err
	}
	stored.current = next
	stored.head = headFromState(request.ContextUID, next)
	stored.revisions = append(stored.revisions, cloned)
	if nextRevision%contextmgr.CheckpointInterval == 0 {
		checkpoint, err := cloneState(next)
		if err != nil {
			return contextmgr.AppendResult{}, err
		}
		stored.checkpoints[nextRevision] = checkpoint
	}
	return contextmgr.AppendResult{
		Revision: nextRevision, AppliedPendingMessages: common.CloneAgenticMessages(appliedPending),
	}, nil
}

func (s *RAMStore) ReadEvents(ctx context.Context, contextUID common.ContextUID, fromRevision uint64) ([]contextmgr.RevisionedEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	stored, exists := s.streams[contextUID]
	if !exists {
		return nil, contextmgr.ErrContextNotFound
	}
	if fromRevision == 0 {
		fromRevision = 1
	}
	if fromRevision > stored.head.Revision+1 {
		return nil, contextmgr.ErrRevisionNotFound
	}
	result := make([]contextmgr.RevisionedEvent, 0)
	for revision := max(fromRevision, 2); revision <= stored.head.Revision; revision++ {
		events, err := contextmgr.CloneEvents(stored.revisions[revision-2])
		if err != nil {
			return nil, err
		}
		result = append(result, contextmgr.RevisionedEvent{Revision: revision, Events: events})
	}
	return result, nil
}

func (s *RAMStore) ReadView(ctx context.Context, request contextmgr.ReadViewRequest) (contextmgr.ContextView, error) {
	if err := ctx.Err(); err != nil {
		return contextmgr.ContextView{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	stored, exists := s.streams[request.ContextUID]
	if !exists {
		return contextmgr.ContextView{}, contextmgr.ErrContextNotFound
	}
	revision := request.Revision
	if revision == 0 {
		revision = stored.head.Revision
	}
	state, err := loadStreamAt(stored, revision)
	if err != nil {
		return contextmgr.ContextView{}, err
	}
	state.ContextUID = request.ContextUID
	applyViewOptions(state, request)
	return *state, nil
}

func (s *RAMStore) Delete(ctx context.Context, contextUID common.ContextUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.streams, contextUID)
	return nil
}

func headFromState(contextUID common.ContextUID, state *contextmgr.State) contextmgr.ContextHead {
	head := contextmgr.ContextHead{
		ContextUID: contextUID, Revision: state.Revision, PendingCount: len(state.PendingMessages),
	}
	if len(state.Messages) > 0 {
		head.Finalized = isFinalAnswer(state.Messages[len(state.Messages)-1])
	}
	for _, message := range state.Messages {
		if runUID, ok := common.RunUIDFromMessage(message); ok {
			head.CurrentRunUID = runUID
		}
	}
	return head
}

func isFinalAnswer(message *schema.AgenticMessage) bool {
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

func applyViewOptions(state *contextmgr.State, request contextmgr.ReadViewRequest) {
	if request.MessageLimit > 0 && len(state.Messages) > request.MessageLimit {
		state.Messages = state.Messages[len(state.Messages)-request.MessageLimit:]
	}
	if !request.IncludePending {
		state.PendingMessages = nil
	}
	if !request.IncludeRuns {
		state.RunSnapshots = nil
	}
}

func loadStreamAt(stored *stream, revision uint64) (*contextmgr.State, error) {
	if revision < 1 || revision > stored.head.Revision {
		return nil, contextmgr.ErrRevisionNotFound
	}
	selected := stored.initial
	for checkpointRevision, checkpoint := range stored.checkpoints {
		if checkpointRevision <= revision && checkpointRevision > selected.Revision {
			selected = checkpoint
		}
	}
	state, err := cloneState(selected)
	if err != nil {
		return nil, err
	}
	for next := selected.Revision + 1; next <= revision; next++ {
		if err := contextmgr.ApplyEvents(state, next, stored.revisions[next-2]); err != nil {
			return nil, err
		}
	}
	return cloneState(state)
}

func cloneState(state *contextmgr.State) (*contextmgr.State, error) {
	payload, err := sonic.Marshal(state.Clone())
	if err != nil {
		return nil, fmt.Errorf("encode context state: %w", err)
	}
	var clone contextmgr.State
	if err := sonic.Unmarshal(payload, &clone); err != nil {
		return nil, fmt.Errorf("decode context state: %w", err)
	}
	return clone.Clone(), nil
}
