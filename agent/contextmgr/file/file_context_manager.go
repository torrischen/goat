package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/util/logging"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

const (
	conversationStateVersion = 1
	eventRecordVersion       = 1
)

// FileStore persists an initial checkpoint and one atomic file per revision.
type FileStore struct {
	mu  *sync.Mutex
	dir string
}

var directoryLocks sync.Map

type conversationState struct {
	Version         int                                      `json:"version"`
	Revision        uint64                                   `json:"revision"`
	Messages        []*schema.AgenticMessage                 `json:"messages"`
	PendingMessages []*schema.AgenticMessage                 `json:"pending_messages,omitempty"`
	RunSnapshots    map[common.RunUID]contextmgr.RunSnapshot `json:"run_snapshots,omitempty"`
}

type eventRecord struct {
	Version  int                `json:"version"`
	Revision uint64             `json:"revision"`
	Events   []contextmgr.Event `json:"events"`
}

var _ contextmgr.Store = (*FileStore)(nil)

// NewFileStore creates a file-backed Store. An empty path uses
// data/conversations.
func NewFileStore(dir string) *FileStore {
	if dir == "" {
		dir = "data/conversations"
	}
	if absolute, err := filepath.Abs(dir); err == nil {
		dir = absolute
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logging.Errorf("Failed to create conversation directory: %v", err)
	}
	lock, _ := directoryLocks.LoadOrStore(dir, &sync.Mutex{})
	return &FileStore{dir: dir, mu: lock.(*sync.Mutex)}
}

// NewFileContextManager creates a Manager backed by atomic local files.
func NewFileContextManager(dir string) *contextmgr.Manager {
	return contextmgr.NewManager(NewFileStore(dir))
}

func (s *FileStore) Create(ctx context.Context, state *contextmgr.State) (common.ContextUID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	contextUID := common.ContextUID(uuid.NewString())
	next := state.Clone()
	next.Revision = 1
	if err := s.persistState(contextUID, next); err != nil {
		return "", err
	}
	return contextUID, nil
}

func (s *FileStore) Load(ctx context.Context, contextUID common.ContextUID) (*contextmgr.State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadAt(contextUID, 0)
}

func (s *FileStore) LoadAt(
	ctx context.Context,
	contextUID common.ContextUID,
	revision uint64,
) (*contextmgr.State, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadAt(contextUID, revision)
}

func (s *FileStore) Append(
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

	current, err := s.loadAt(contextUID, 0)
	if err != nil {
		return err
	}
	if current.Revision != expectedRevision {
		return contextmgr.ErrRevisionConflict
	}
	nextRevision := expectedRevision + 1
	var checkpoint *contextmgr.State
	if nextRevision%contextmgr.CheckpointInterval == 0 {
		checkpoint = current.Clone()
		if err := contextmgr.ApplyEvents(checkpoint, nextRevision, events); err != nil {
			return err
		}
	}
	if err := s.persistEvents(contextUID, nextRevision, events); err != nil {
		return err
	}
	if checkpoint != nil {
		if err := s.persistCheckpoint(contextUID, checkpoint); err != nil {
			logging.Errorf("Failed to persist context checkpoint: %v", err)
		}
	}
	return nil
}

func (s *FileStore) Delete(ctx context.Context, contextUID common.ContextUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := s.getFilePath(contextUID)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(filePath + ".tmp")
	return os.RemoveAll(eventDir(filePath))
}

func stateToFile(state *contextmgr.State) *conversationState {
	clone := state.Clone()
	return &conversationState{
		Version:         conversationStateVersion,
		Revision:        clone.Revision,
		Messages:        clone.Messages,
		PendingMessages: clone.PendingMessages,
		RunSnapshots:    clone.RunSnapshots,
	}
}

func (s *conversationState) toState() *contextmgr.State {
	if s == nil {
		return contextmgr.NewState(nil)
	}
	state := (&contextmgr.State{
		Revision:        s.Revision,
		Messages:        s.Messages,
		PendingMessages: s.PendingMessages,
		RunSnapshots:    s.RunSnapshots,
	}).Clone()
	if state.Revision == 0 {
		state.Revision = 1
	}
	return state
}

func (s *FileStore) getFilePath(contextUID common.ContextUID) string {
	dateDir := filepath.Join(s.dir, time.Now().Format("2006-01-02"))
	fileName := string(contextUID) + ".jsonl"
	currentPath := filepath.Join(dateDir, fileName)
	if _, err := os.Stat(currentPath); err == nil {
		return currentPath
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		logging.Errorf("Failed to read conversation directory: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == filepath.Base(dateDir) {
			continue
		}
		candidate := filepath.Join(s.dir, entry.Name(), fileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	if err := os.MkdirAll(dateDir, 0o755); err != nil {
		logging.Errorf("Failed to create date directory: %v", err)
	}
	return currentPath
}

func eventDir(statePath string) string {
	return statePath + ".events"
}

func eventPath(statePath string, revision uint64) string {
	return filepath.Join(eventDir(statePath), fmt.Sprintf("%020d.json", revision))
}

func checkpointPath(statePath string, revision uint64) string {
	return filepath.Join(eventDir(statePath), fmt.Sprintf("checkpoint-%020d.json", revision))
}

func (s *FileStore) persistState(contextUID common.ContextUID, state *contextmgr.State) error {
	filePath := s.getFilePath(contextUID)
	payload, err := sonic.Marshal(stateToFile(state))
	if err != nil {
		return err
	}
	return atomicWrite(filePath, append(payload, '\n'))
}

func (s *FileStore) persistCheckpoint(
	contextUID common.ContextUID,
	state *contextmgr.State,
) error {
	payload, err := sonic.Marshal(stateToFile(state))
	if err != nil {
		return err
	}
	return atomicWrite(checkpointPath(s.getFilePath(contextUID), state.Revision), append(payload, '\n'))
}

func (s *FileStore) persistEvents(
	contextUID common.ContextUID,
	revision uint64,
	events []contextmgr.Event,
) error {
	statePath := s.getFilePath(contextUID)
	if err := os.MkdirAll(eventDir(statePath), 0o755); err != nil {
		return err
	}
	payload, err := sonic.Marshal(&eventRecord{
		Version:  eventRecordVersion,
		Revision: revision,
		Events:   events,
	})
	if err != nil {
		return err
	}
	path := eventPath(statePath, revision)
	if _, err := os.Stat(path); err == nil {
		return contextmgr.ErrRevisionConflict
	}
	return atomicWrite(path, append(payload, '\n'))
}

func atomicWrite(path string, payload []byte) error {
	tmpPath := path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func (s *FileStore) loadAt(contextUID common.ContextUID, targetRevision uint64) (*contextmgr.State, error) {
	state, exists, err := s.loadState(contextUID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, contextmgr.ErrContextNotFound
	}

	revisions, err := eventRevisions(s.getFilePath(contextUID))
	if err != nil {
		return nil, err
	}
	latest := state.Revision
	if len(revisions) > 0 {
		latest = revisions[len(revisions)-1]
	}
	if targetRevision == 0 {
		targetRevision = latest
	}
	if targetRevision < state.Revision || targetRevision > latest {
		return nil, contextmgr.ErrRevisionNotFound
	}
	checkpoints, err := checkpointRevisions(s.getFilePath(contextUID))
	if err != nil {
		return nil, err
	}
	for i := len(checkpoints) - 1; i >= 0; i-- {
		if checkpoints[i] <= targetRevision && checkpoints[i] > state.Revision {
			checkpoint, err := loadConversationState(checkpointPath(s.getFilePath(contextUID), checkpoints[i]))
			if err != nil {
				return nil, err
			}
			state = checkpoint
			break
		}
	}
	for _, revision := range revisions {
		if revision <= state.Revision {
			continue
		}
		if revision > targetRevision {
			break
		}
		if revision != state.Revision+1 {
			return nil, fmt.Errorf("context event revision gap after %d", state.Revision)
		}
		record, err := loadEventRecord(eventPath(s.getFilePath(contextUID), revision))
		if err != nil {
			return nil, err
		}
		if err := contextmgr.ApplyEvents(state, revision, record.Events); err != nil {
			return nil, err
		}
	}
	return state.Clone(), nil
}

func eventRevisions(statePath string) ([]uint64, error) {
	return revisionFiles(statePath, "", ".json")
}

func checkpointRevisions(statePath string) ([]uint64, error) {
	return revisionFiles(statePath, "checkpoint-", ".json")
}

func revisionFiles(statePath, prefix, suffix string) ([]uint64, error) {
	entries, err := os.ReadDir(eventDir(statePath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	revisions := make([]uint64, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		number := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), prefix), suffix)
		revision, err := strconv.ParseUint(number, 10, 64)
		if err != nil {
			continue
		}
		revisions = append(revisions, revision)
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i] < revisions[j] })
	return revisions, nil
}

func loadEventRecord(path string) (*eventRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var record eventRecord
	if err := sonic.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("decode context event %s: %w", path, err)
	}
	if record.Version != eventRecordVersion {
		return nil, fmt.Errorf("unsupported context event version %d", record.Version)
	}
	return &record, nil
}

func loadConversationState(path string) (*contextmgr.State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		state := contextmgr.NewState(nil)
		state.Revision = 1
		return state, nil
	}
	var persisted conversationState
	if err := sonic.UnmarshalString(trimmed, &persisted); err != nil {
		return nil, fmt.Errorf("decode conversation state: %w", err)
	}
	if persisted.Version != conversationStateVersion {
		return nil, fmt.Errorf(
			"unsupported conversation state version %d (expected %d)",
			persisted.Version,
			conversationStateVersion,
		)
	}
	return persisted.toState(), nil
}

func (s *FileStore) loadState(contextUID common.ContextUID) (*contextmgr.State, bool, error) {
	filePath := s.getFilePath(contextUID)
	if _, err := os.Stat(filePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	state, err := loadConversationState(filePath)
	if err != nil {
		return nil, false, err
	}
	return state, true, nil
}
