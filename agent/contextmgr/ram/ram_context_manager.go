package ram

import (
	"context"
	"fmt"
	"sync"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

type stream struct {
	checkpoint  *contextmgr.State
	checkpoints map[uint64]*contextmgr.State
	revisions   [][]contextmgr.Event
}

// RAMStore persists an initial checkpoint followed by incremental revisions.
type RAMStore struct {
	mu      sync.RWMutex
	streams map[common.ContextUID]*stream
}

var _ contextmgr.Store = (*RAMStore)(nil)

// NewRAMStore creates an empty in-process Store.
func NewRAMStore() *RAMStore {
	return &RAMStore{streams: make(map[common.ContextUID]*stream)}
}

// NewRAMContextManager creates a Manager backed by in-process storage.
func NewRAMContextManager() *contextmgr.Manager {
	return contextmgr.NewManager(NewRAMStore())
}

func (s *RAMStore) Create(ctx context.Context, state *contextmgr.State) (common.ContextUID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	var contextUID common.ContextUID
	for {
		contextUID = common.ContextUID(uuid.NewString())
		if _, exists := s.streams[contextUID]; !exists {
			break
		}
	}
	checkpoint, err := cloneState(state)
	if err != nil {
		return "", err
	}
	checkpoint.Revision = 1
	s.streams[contextUID] = &stream{
		checkpoint: checkpoint,
		checkpoints: map[uint64]*contextmgr.State{
			checkpoint.Revision: checkpoint,
		},
	}
	return contextUID, nil
}

func (s *RAMStore) Load(ctx context.Context, contextUID common.ContextUID) (*contextmgr.State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	stored, exists := s.streams[contextUID]
	if !exists {
		return nil, contextmgr.ErrContextNotFound
	}
	return loadStreamAt(stored, stored.checkpoint.Revision+uint64(len(stored.revisions)))
}

func (s *RAMStore) LoadAt(
	ctx context.Context,
	contextUID common.ContextUID,
	revision uint64,
) (*contextmgr.State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	stored, exists := s.streams[contextUID]
	if !exists {
		return nil, contextmgr.ErrContextNotFound
	}
	if revision == 0 {
		revision = stored.checkpoint.Revision + uint64(len(stored.revisions))
	}
	return loadStreamAt(stored, revision)
}

func (s *RAMStore) Append(
	ctx context.Context,
	contextUID common.ContextUID,
	expectedRevision uint64,
	events []contextmgr.Event,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := contextmgr.ValidateEvents(events); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	stored, exists := s.streams[contextUID]
	if !exists {
		return contextmgr.ErrContextNotFound
	}
	currentRevision := stored.checkpoint.Revision + uint64(len(stored.revisions))
	if currentRevision != expectedRevision {
		return contextmgr.ErrRevisionConflict
	}
	cloned, err := contextmgr.CloneEvents(events)
	if err != nil {
		return err
	}
	nextRevision := expectedRevision + 1
	var checkpoint *contextmgr.State
	if nextRevision%contextmgr.CheckpointInterval == 0 {
		checkpoint, err = loadStreamAt(stored, expectedRevision)
		if err != nil {
			return err
		}
		if err := contextmgr.ApplyEvents(checkpoint, nextRevision, cloned); err != nil {
			return err
		}
	}
	stored.revisions = append(stored.revisions, cloned)
	if checkpoint != nil {
		stored.checkpoints[nextRevision] = checkpoint
	}
	return nil
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

func loadStreamAt(stored *stream, revision uint64) (*contextmgr.State, error) {
	first := stored.checkpoint.Revision
	last := first + uint64(len(stored.revisions))
	if revision < first || revision > last {
		return nil, contextmgr.ErrRevisionNotFound
	}
	selected := stored.checkpoint
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
		if err := contextmgr.ApplyEvents(state, next, stored.revisions[next-first-1]); err != nil {
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
