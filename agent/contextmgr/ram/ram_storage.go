package ram

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/torrischen/goat/agent/contextmgr"
)

// RAMStorage is an in-memory Storage implementation.
type RAMStorage struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func NewRAMStorage() *RAMStorage {
	return &RAMStorage{data: make(map[string][]byte)}
}

func NewRAMContextManager() *contextmgr.Manager {
	return contextmgr.NewManager(NewRAMStorage())
}

func (s *RAMStorage) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	value, exists := s.data[key]
	if !exists {
		return nil, contextmgr.ErrNotFound
	}
	return bytes.Clone(value), nil
}

func (s *RAMStorage) Set(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = bytes.Clone(value)
	return nil
}

func (s *RAMStorage) CreateIfAbsent(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[key]; exists {
		return contextmgr.ErrCASConflict
	}
	s.data[key] = bytes.Clone(value)
	return nil
}

func (s *RAMStorage) CompareAndSwap(ctx context.Context, key string, oldValue, newValue []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	current, exists := s.data[key]
	if !exists {
		return contextmgr.ErrNotFound
	}
	if !bytes.Equal(current, oldValue) {
		return contextmgr.ErrCASConflict
	}
	s.data[key] = bytes.Clone(newValue)
	return nil
}

func (s *RAMStorage) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
	return nil
}

func (s *RAMStorage) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := make([]string, 0)
	for key := range s.data {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}
