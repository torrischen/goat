package ram

import (
	"context"
	"sort"
	"sync"

	"github.com/bytedance/sonic"
	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/agent/message"
)

// RAMStore is an in-memory Store implementation.
type RAMStore struct {
	mu       sync.RWMutex
	heads    map[common.ContextUID]*contextmgr.Head
	messages map[common.ContextUID]map[contextmgr.Lane][]contextmgr.MessageRow
}

func NewRAMStore() *RAMStore {
	return &RAMStore{
		heads:    make(map[common.ContextUID]*contextmgr.Head),
		messages: make(map[common.ContextUID]map[contextmgr.Lane][]contextmgr.MessageRow),
	}
}

func NewRAMContextManager() *contextmgr.Manager {
	return contextmgr.NewManager(NewRAMStore())
}

func (s *RAMStore) CreateHead(ctx context.Context, head *contextmgr.Head) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.heads[head.UID]; exists {
		return contextmgr.ErrCASConflict
	}
	s.heads[head.UID] = cloneHead(head)
	return nil
}

func (s *RAMStore) LoadHead(ctx context.Context, uid common.ContextUID) (*contextmgr.Head, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	head, exists := s.heads[uid]
	if !exists {
		return nil, contextmgr.ErrContextNotFound
	}
	return cloneHead(head), nil
}

func (s *RAMStore) AppendMessages(ctx context.Context, rows []contextmgr.MessageRow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, row := range rows {
		if s.messages[row.UID] == nil {
			s.messages[row.UID] = make(map[contextmgr.Lane][]contextmgr.MessageRow)
		}

		// Check if this seq already exists (idempotency for concurrent retries)
		exists := false
		for _, existing := range s.messages[row.UID][row.Lane] {
			if existing.Seq == row.Seq {
				exists = true
				break
			}
		}
		if exists {
			continue
		}

		// Deep clone via JSON round-trip to ensure complete isolation
		cloned, err := deepCloneMessage(row.Message)
		if err != nil {
			return err
		}
		s.messages[row.UID][row.Lane] = append(s.messages[row.UID][row.Lane], contextmgr.MessageRow{
			UID:     row.UID,
			Lane:    row.Lane,
			Seq:     row.Seq,
			Message: cloned,
		})
	}
	return nil
}

func (s *RAMStore) ReadMessages(ctx context.Context, uid common.ContextUID, lane contextmgr.Lane, fromSeq, toSeq uint64) ([]contextmgr.MessageRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	laneMessages, exists := s.messages[uid][lane]
	if !exists {
		return []contextmgr.MessageRow{}, nil
	}

	result := make([]contextmgr.MessageRow, 0)
	for _, row := range laneMessages {
		if row.Seq >= fromSeq && row.Seq <= toSeq {
			// Deep clone via JSON round-trip to ensure complete isolation
			cloned, err := deepCloneMessage(row.Message)
			if err != nil {
				return nil, err
			}
			result = append(result, contextmgr.MessageRow{
				UID:     row.UID,
				Lane:    row.Lane,
				Seq:     row.Seq,
				Message: cloned,
			})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Seq < result[j].Seq
	})
	return result, nil
}

func (s *RAMStore) ClearLane(ctx context.Context, uid common.ContextUID, lane contextmgr.Lane) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.messages[uid] != nil {
		delete(s.messages[uid], lane)
	}
	return nil
}

func (s *RAMStore) ReplaceCommitted(ctx context.Context, uid common.ContextUID, rows []contextmgr.MessageRow, next *contextmgr.Head, expectVersion uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	current, exists := s.heads[uid]
	if !exists {
		return contextmgr.ErrContextNotFound
	}
	if current.Version != expectVersion {
		return contextmgr.ErrCASConflict
	}

	clonedRows := make([]contextmgr.MessageRow, 0, len(rows))
	for _, row := range rows {
		cloned, err := deepCloneMessage(row.Message)
		if err != nil {
			return err
		}
		clonedRows = append(clonedRows, contextmgr.MessageRow{
			UID: row.UID, Lane: row.Lane, Seq: row.Seq, Message: cloned,
		})
	}
	if s.messages[uid] == nil {
		s.messages[uid] = make(map[contextmgr.Lane][]contextmgr.MessageRow)
	}
	s.messages[uid][contextmgr.LaneCommitted] = clonedRows
	s.heads[uid] = cloneHead(next)
	return nil
}

func (s *RAMStore) CommitHead(ctx context.Context, next *contextmgr.Head, expectVersion uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	current, exists := s.heads[next.UID]
	if !exists {
		return contextmgr.ErrContextNotFound
	}
	if current.Version != expectVersion {
		return contextmgr.ErrCASConflict
	}
	s.heads[next.UID] = cloneHead(next)
	return nil
}

func (s *RAMStore) DeleteContext(ctx context.Context, uid common.ContextUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.heads, uid)
	delete(s.messages, uid)
	return nil
}

func (s *RAMStore) ListContexts(ctx context.Context) ([]common.ContextUID, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	uids := make([]common.ContextUID, 0, len(s.heads))
	for uid := range s.heads {
		uids = append(uids, uid)
	}
	sort.Slice(uids, func(i, j int) bool {
		return uids[i] < uids[j]
	})
	return uids, nil
}

func cloneHead(head *contextmgr.Head) *contextmgr.Head {
	if head == nil {
		return nil
	}
	clone := *head
	if head.Runs != nil {
		clone.Runs = make(map[common.RunUID]contextmgr.RunSnapshot, len(head.Runs))
		for k, v := range head.Runs {
			clone.Runs[k] = v
		}
	}
	return &clone
}

func deepCloneMessage(msg *message.Message) (*message.Message, error) {
	if msg == nil {
		return nil, nil
	}
	// Use JSON round-trip for deep cloning to ensure complete isolation
	data, err := sonic.Marshal(msg)
	if err != nil {
		return nil, err
	}
	var cloned message.Message
	if err := sonic.Unmarshal(data, &cloned); err != nil {
		return nil, err
	}
	return &cloned, nil
}
