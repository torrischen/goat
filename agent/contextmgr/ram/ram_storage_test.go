package ram

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/torrischen/goat/agent/contextmgr"
)

// TestRAMStorage tests the basic storage operations
func TestRAMStorage(t *testing.T) {
	storage := NewRAMStorage()
	ctx := context.Background()

	// Test basic Get/Set
	key := "test-key"
	value := []byte("test-value")

	if err := storage.Set(ctx, key, value); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	got, err := storage.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if string(got) != string(value) {
		t.Errorf("expected %q, got %q", value, got)
	}

	// Test CompareAndSwap
	oldVal := []byte("old")
	newVal := []byte("new")
	storage.Set(ctx, "cas-key", oldVal)

	err = storage.CompareAndSwap(ctx, "cas-key", oldVal, newVal)
	if err != nil {
		t.Fatalf("CompareAndSwap failed: %v", err)
	}

	// Test CAS conflict
	err = storage.CompareAndSwap(ctx, "cas-key", oldVal, []byte("another"))
	if err != contextmgr.ErrCASConflict {
		t.Errorf("expected ErrCASConflict, got %v", err)
	}
}

// TestManagerBasicOperations tests the Manager with RAM storage
func TestManagerBasicOperations(t *testing.T) {
	manager := NewRAMContextManager()
	ctx := context.Background()

	// Test Create with UserAgenticMessage
	messages := []*schema.AgenticMessage{
		schema.UserAgenticMessage("Hello"),
	}

	contextUID, err := manager.Create(ctx, messages)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if contextUID == "" {
		t.Error("expected non-empty context UID")
	}

	// Test Load
	loaded, err := manager.Load(ctx, contextUID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded) != 1 {
		t.Errorf("expected 1 message, got %d", len(loaded))
	}

	// Test Append - use UserAgenticMessage helper
	newMessages := []*schema.AgenticMessage{
		schema.UserAgenticMessage("Hi there!"),
	}

	err = manager.Append(ctx, contextUID, newMessages...)
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Verify appended
	loaded, err = manager.Load(ctx, contextUID)
	if err != nil {
		t.Fatalf("Load after append failed: %v", err)
	}

	if len(loaded) != 2 {
		t.Errorf("expected 2 messages, got %d", len(loaded))
	}

	// Test Enqueue
	pendingMsg := []*schema.AgenticMessage{
		schema.UserAgenticMessage("Pending message"),
	}

	err = manager.Enqueue(ctx, contextUID, pendingMsg)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Test CommitTurn
	turnMsg := []*schema.AgenticMessage{
		schema.UserAgenticMessage("Turn response"),
	}

	result, err := manager.CommitTurn(ctx, contextUID, turnMsg)
	if err != nil {
		t.Fatalf("CommitTurn failed: %v", err)
	}

	if len(result.AppliedPendingMessages) != 1 {
		t.Errorf("expected 1 applied pending message, got %d", len(result.AppliedPendingMessages))
	}

	// Verify final state
	loaded, err = manager.Load(ctx, contextUID)
	if err != nil {
		t.Fatalf("Load after commit turn failed: %v", err)
	}

	// Should have: 2 initial + 1 turn + 1 pending = 4 messages
	if len(loaded) != 4 {
		t.Errorf("expected 4 messages after commit turn, got %d", len(loaded))
	}
}

// TestReplace tests the Replace operation
func TestReplace(t *testing.T) {
	manager := NewRAMContextManager()
	ctx := context.Background()

	// Create with some messages
	initialMessages := []*schema.AgenticMessage{
		schema.UserAgenticMessage("Message 1"),
		schema.UserAgenticMessage("Message 2"),
	}

	contextUID, err := manager.Create(ctx, initialMessages)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Replace with new messages
	newMessages := []*schema.AgenticMessage{
		schema.UserAgenticMessage("Replaced message"),
	}

	err = manager.Replace(ctx, contextUID, newMessages)
	if err != nil {
		t.Fatalf("Replace failed: %v", err)
	}

	// Verify replaced
	loaded, err := manager.Load(ctx, contextUID)
	if err != nil {
		t.Fatalf("Load after replace failed: %v", err)
	}

	if len(loaded) != 1 {
		t.Errorf("expected 1 message after replace, got %d", len(loaded))
	}
}

// TestDelete tests the Delete operation
func TestDelete(t *testing.T) {
	manager := NewRAMContextManager()
	ctx := context.Background()

	// Create a context
	contextUID, err := manager.Create(ctx, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify it exists
	_, err = manager.Load(ctx, contextUID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Delete it
	err = manager.Delete(ctx, contextUID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify it's gone
	_, err = manager.Load(ctx, contextUID)
	if err != contextmgr.ErrContextNotFound {
		t.Errorf("expected ErrContextNotFound after delete, got %v", err)
	}
}

func stringPtr(s string) *string {
	return &s
}
