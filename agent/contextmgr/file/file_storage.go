package file

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/torrischen/goat/agent/contextmgr"
)

// FileStorage stores each key as one file below a base directory.
type FileStorage struct {
	mu      *sync.RWMutex
	baseDir string
}

var directoryLocks sync.Map

// NewFileStorage creates file storage. An empty baseDir defaults to
// data/conversations.
func NewFileStorage(baseDir string) (*FileStorage, error) {
	if baseDir == "" {
		baseDir = "data/conversations"
	}
	absolute, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absolute, 0o755); err != nil {
		return nil, err
	}
	lock, _ := directoryLocks.LoadOrStore(absolute, &sync.RWMutex{})
	return &FileStorage{baseDir: absolute, mu: lock.(*sync.RWMutex)}, nil
}

// NewFileContextManager creates a Manager backed by file storage.
func NewFileContextManager(baseDir string) (*contextmgr.Manager, error) {
	storage, err := NewFileStorage(baseDir)
	if err != nil {
		return nil, err
	}
	return contextmgr.NewManager(storage), nil
}

func (s *FileStorage) keyToPath(key string) string {
	return filepath.Join(s.baseDir, strings.ReplaceAll(key, ":", string(os.PathSeparator)))
}

func (s *FileStorage) writeLocked(key string, value []byte) error {
	path := s.keyToPath(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, value, 0o644); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func (s *FileStorage) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	value, err := os.ReadFile(s.keyToPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, contextmgr.ErrNotFound
	}
	return value, err
}

func (s *FileStorage) Set(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(key, value)
}

func (s *FileStorage) CreateIfAbsent(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.keyToPath(key)
	if _, err := os.Stat(path); err == nil {
		return contextmgr.ErrCASConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.writeLocked(key, value)
}

func (s *FileStorage) CompareAndSwap(ctx context.Context, key string, oldValue, newValue []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := os.ReadFile(s.keyToPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return contextmgr.ErrNotFound
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(current, oldValue) {
		return contextmgr.ErrCASConflict
	}
	return s.writeLocked(key, newValue)
}

func (s *FileStorage) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	err := os.Remove(s.keyToPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *FileStorage) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	basePrefix := filepath.Join(s.baseDir, strings.ReplaceAll(prefix, ":", string(os.PathSeparator)))
	keys := make([]string, 0)
	err := filepath.WalkDir(s.baseDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.HasSuffix(path, ".tmp") || !strings.HasPrefix(path, basePrefix) {
			return nil
		}
		relative, err := filepath.Rel(s.baseDir, path)
		if err != nil {
			return err
		}
		keys = append(keys, strings.ReplaceAll(relative, string(os.PathSeparator), ":"))
		return nil
	})
	sort.Strings(keys)
	return keys, err
}
