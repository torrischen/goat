package ram

import (
	"context"
	"errors"
	"testing"

	"github.com/torrischen/goat/agent/contextmgr"

	"github.com/cloudwego/eino/schema"
)

func TestRAMStoreHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewRAMStore()
	if _, err := store.Create(ctx, contextmgr.NewState(nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Create() error = %v", err)
	}
}

func TestNewRAMContextManager(t *testing.T) {
	manager := NewRAMContextManager()
	contextUID, err := manager.Create(context.Background(), []*schema.AgenticMessage{
		schema.UserAgenticMessage("hello"),
	})
	if err != nil || contextUID == "" {
		t.Fatalf("Create() = %q, %v", contextUID, err)
	}
}

func TestRAMStoreLoadAtZeroReturnsLatest(t *testing.T) {
	ctx := context.Background()
	store := NewRAMStore()
	contextUID, err := store.Create(ctx, contextmgr.NewState(nil))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.Append(ctx, contextUID, 1, []contextmgr.Event{{
		Type:     contextmgr.EventMessagesAppended,
		Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("latest")},
	}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	state, err := store.LoadAt(ctx, contextUID, 0)
	if err != nil {
		t.Fatalf("LoadAt(0) error = %v", err)
	}
	if state.Revision != 2 || len(state.Messages) != 1 {
		t.Fatalf("LoadAt(0) = revision %d, messages %#v", state.Revision, state.Messages)
	}
}
