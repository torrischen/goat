package ram

import (
	"context"
	"testing"

	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/agent/message"
)

// TestManagerBasicOperations tests the Manager with RAM storage
func TestManagerBasicOperations(t *testing.T) {
	manager := NewRAMContextManager()
	ctx := context.Background()

	messages := []*message.Message{
		message.UserMessage("Hello"),
	}

	contextUID, err := manager.Create(ctx, messages)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if contextUID == "" {
		t.Error("expected non-empty context UID")
	}

	loaded, err := manager.Load(ctx, contextUID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if len(loaded) != 1 {
		t.Errorf("expected 1 message, got %d", len(loaded))
	}

	newMessages := []*message.Message{
		message.UserMessage("Hi there!"),
	}

	err = manager.Append(ctx, contextUID, newMessages...)
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	loaded, err = manager.Load(ctx, contextUID)
	if err != nil {
		t.Fatalf("Load after append failed: %v", err)
	}

	if len(loaded) != 2 {
		t.Errorf("expected 2 messages, got %d", len(loaded))
	}

	pendingMsg := []*message.Message{
		message.UserMessage("Pending message"),
	}

	err = manager.Enqueue(ctx, contextUID, pendingMsg)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	turnMsg := []*message.Message{
		message.UserMessage("Turn response"),
	}

	result, err := manager.CommitTurn(ctx, contextUID, turnMsg)
	if err != nil {
		t.Fatalf("CommitTurn failed: %v", err)
	}

	if len(result.AppliedPendingMessages) != 1 {
		t.Errorf("expected 1 applied pending message, got %d", len(result.AppliedPendingMessages))
	}

	loaded, err = manager.Load(ctx, contextUID)
	if err != nil {
		t.Fatalf("Load after commit turn failed: %v", err)
	}

	if len(loaded) != 4 {
		t.Errorf("expected 4 messages after commit turn, got %d", len(loaded))
	}
}

func TestReplace(t *testing.T) {
	manager := NewRAMContextManager()
	ctx := context.Background()

	initialMessages := []*message.Message{
		message.UserMessage("Message 1"),
		message.UserMessage("Message 2"),
	}

	contextUID, err := manager.Create(ctx, initialMessages)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	newMessages := []*message.Message{
		message.UserMessage("Replaced message"),
	}

	err = manager.Replace(ctx, contextUID, newMessages)
	if err != nil {
		t.Fatalf("Replace failed: %v", err)
	}

	loaded, err := manager.Load(ctx, contextUID)
	if err != nil {
		t.Fatalf("Load after replace failed: %v", err)
	}

	if len(loaded) != 1 {
		t.Errorf("expected 1 message after replace, got %d", len(loaded))
	}
}

func TestDelete(t *testing.T) {
	manager := NewRAMContextManager()
	ctx := context.Background()

	contextUID, err := manager.Create(ctx, []*message.Message{})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	_, err = manager.Load(ctx, contextUID)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	err = manager.Delete(ctx, contextUID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = manager.Load(ctx, contextUID)
	if err != contextmgr.ErrContextNotFound {
		t.Errorf("expected ErrContextNotFound after delete, got %v", err)
	}
}
