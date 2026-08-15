package file

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"

	"github.com/cloudwego/eino/schema"
)

func TestFileStoreDetectsCASAcrossInstances(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	first := NewFileStore(dir)
	second := NewFileStore(dir)
	contextUID, err := first.Create(ctx, contextmgr.NewState([]*schema.AgenticMessage{
		schema.UserAgenticMessage("initial"),
	}))
	if err != nil {
		t.Fatal(err)
	}

	stale, err := second.Load(ctx, contextUID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := first.Load(ctx, contextUID)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Append(ctx, contextUID, current.Revision, []contextmgr.Event{{
		Type:     contextmgr.EventMessagesAppended,
		Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("first update")},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := second.Append(ctx, contextUID, stale.Revision, []contextmgr.Event{{
		Type:     contextmgr.EventMessagesAppended,
		Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("stale update")},
	}}); !errors.Is(err, contextmgr.ErrRevisionConflict) {
		t.Fatalf("Append(stale) error = %v", err)
	}
}

func TestFileStoreAppendDoesNotRewriteBaseline(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	contextUID, err := store.Create(ctx, contextmgr.NewState([]*schema.AgenticMessage{
		schema.UserAgenticMessage("baseline-only-content"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	statePath := store.getFilePath(contextUID)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, contextUID, 1, []contextmgr.Event{{
		Type:     contextmgr.EventMessagesAppended,
		Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("delta-only-content")},
	}}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("Append rewrote the baseline state file")
	}
	revisions, err := eventRevisions(statePath)
	if err != nil || len(revisions) != 1 || revisions[0] != 2 {
		t.Fatalf("event revisions = %v, %v", revisions, err)
	}
}

func TestFileStoreCheckpointPreservesHistoricalReads(t *testing.T) {
	ctx := context.Background()
	store := NewFileStore(t.TempDir())
	contextUID, err := store.Create(ctx, contextmgr.NewState(nil))
	if err != nil {
		t.Fatal(err)
	}
	for revision := uint64(1); revision < contextmgr.CheckpointInterval; revision++ {
		if err := store.Append(ctx, contextUID, revision, []contextmgr.Event{{
			Type:     contextmgr.EventMessagesAppended,
			Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("message")},
		}}); err != nil {
			t.Fatalf("Append revision %d: %v", revision+1, err)
		}
	}
	if _, err := os.Stat(checkpointPath(store.getFilePath(contextUID), contextmgr.CheckpointInterval)); err != nil {
		t.Fatal(err)
	}
	current, err := store.Load(ctx, contextUID)
	if err != nil || current.Revision != contextmgr.CheckpointInterval || len(current.Messages) != 63 {
		t.Fatalf("Load() = %+v, %v", current, err)
	}
	historical, err := store.LoadAt(ctx, contextUID, contextmgr.CheckpointInterval-1)
	if err != nil || historical.Revision != contextmgr.CheckpointInterval-1 || len(historical.Messages) != 62 {
		t.Fatalf("LoadAt(63) = %+v, %v", historical, err)
	}
}

func TestFileStoreLoadVariantsAndOldDateLookup(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	id := common.ContextUID("stored")
	oldDir := filepath.Join(dir, "2000-01-01")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(oldDir, "stored.jsonl")
	if err := os.WriteFile(path, []byte(`{"version":1,"messages":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := store.getFilePath(id); got != path {
		t.Fatalf("old date path = %q, want %q", got, path)
	}
	state, exists, err := store.loadState(id)
	if err != nil || !exists || state.Revision != 1 || state.Messages == nil || state.PendingMessages == nil || state.RunSnapshots == nil {
		t.Fatalf("load state = %+v, %v, %v", state, exists, err)
	}

	emptyID := common.ContextUID("empty")
	emptyPath := filepath.Join(dir, time.Now().Format("2006-01-02"), "empty.jsonl")
	if err := os.MkdirAll(filepath.Dir(emptyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if state, exists, err := store.loadState(emptyID); err != nil || !exists || state.Revision != 1 {
		t.Fatalf("empty state = %+v, %v, %v", state, exists, err)
	}

	for name, payload := range map[string]string{
		"bad-json":    "not json",
		"bad-version": `{"version":2,"messages":[]}`,
	} {
		id := common.ContextUID(name)
		path := filepath.Join(dir, time.Now().Format("2006-01-02"), name+".jsonl")
		if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.loadState(id); err == nil {
			t.Fatalf("%s unexpectedly loaded", name)
		}
	}

	if state, exists, err := store.loadState("absent"); err != nil || exists || state != nil {
		t.Fatalf("missing state = %+v, %v, %v", state, exists, err)
	}
}
