package tools

import (
	"context"
	"testing"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/llm"
	"github.com/torrischen/goat/streaming"
)

// MockAgent implements common.Agent for testing
type MockAgent struct {
	doFunc    func(context.Context, *common.AgentDoArgs) (common.RunSignature, streaming.Stream[common.AgentEvent], error)
	forkFunc  func(context.Context, *common.AgentForkArgs) (common.ContextUID, error)
	steerFunc func(context.Context, *common.AgentSteerArgs) error
}

func (m *MockAgent) Do(ctx context.Context, args *common.AgentDoArgs, _ ...llm.CallOption) (common.RunSignature, streaming.Stream[common.AgentEvent], error) {
	if m.doFunc != nil {
		return m.doFunc(ctx, args)
	}
	return common.RunSignature{}, nil, nil
}

func (m *MockAgent) Fork(ctx context.Context, args *common.AgentForkArgs) (common.ContextUID, error) {
	if m.forkFunc != nil {
		return m.forkFunc(ctx, args)
	}
	return "", nil
}

func (m *MockAgent) Steer(ctx context.Context, args *common.AgentSteerArgs) error {
	if m.steerFunc != nil {
		return m.steerFunc(ctx, args)
	}
	return nil
}

func TestSpawnSubAgent(t *testing.T) {
	// Clean up registry before test
	globalRegistry = &SubAgentRegistry{
		agents: make(map[string]*SubAgentInfo),
	}

	mockAgent := &MockAgent{
		doFunc: func(ctx context.Context, args *common.AgentDoArgs) (common.RunSignature, streaming.Stream[common.AgentEvent], error) {
			signature := common.RunSignature{
				ContextUID: "test-context",
				RunUID:     "test-run",
			}
			eventStream := streaming.NewStream[common.AgentEvent](10)

			// Send events in background
			go func() {
				defer eventStream.Close()
				_ = eventStream.WriteWithContext(ctx, common.RunStartedEvent{
					Signature: signature,
					MaxStep:   8,
				})
				_ = eventStream.WriteWithContext(ctx, common.FinalAnswerCompletedEvent{
					Answer: "Task completed successfully",
				})
				_ = eventStream.WriteWithContext(ctx, common.RunCompletedEvent{
					Usage: &common.AgentUsage{
						PromptTokens:     100,
						CompletionTokens: 50,
					},
					IterationsUsed: 2,
					ToolCalls:      3,
				})
			}()

			return signature, eventStream, nil
		},
	}

	tool := SpawnSubAgent(mockAgent)

	// Test tool execution
	actx := common.NewAgentContext(context.Background())
	inputs := map[string]any{
		"task":     "Test task",
		"max_step": float64(10),
		"compress": true,
	}

	result := tool.Execute(actx, inputs)
	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	resultStr := result.String()
	if resultStr == "" {
		t.Fatal("Expected non-empty result string")
	}

	// Wait for background goroutine to process events
	time.Sleep(100 * time.Millisecond)

	// Verify subagent was registered
	infos := globalRegistry.List()
	if len(infos) == 0 {
		t.Fatal("Expected at least one subagent to be registered")
	}

	info := infos[0]
	if info.Task != "Test task" {
		t.Errorf("Expected task 'Test task', got '%s'", info.Task)
	}

	if info.Status != SubAgentStatusCompleted {
		t.Errorf("Expected status completed, got %s", info.Status)
	}

	if info.Result != "Task completed successfully" {
		t.Errorf("Expected result 'Task completed successfully', got '%s'", info.Result)
	}
}

func TestSpawnSubAgent_MissingTask(t *testing.T) {
	tool := SpawnSubAgent(&MockAgent{})
	actx := common.NewAgentContext(context.Background())
	inputs := map[string]any{}

	result := tool.Execute(actx, inputs)
	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	resultStr := result.String()
	if resultStr != "task parameter is missing or invalid." {
		t.Errorf("Expected error message about missing task, got '%s'", resultStr)
	}
}

func TestGetSubAgentStatus(t *testing.T) {
	// Clean up and setup registry
	globalRegistry = &SubAgentRegistry{
		agents: make(map[string]*SubAgentInfo),
	}

	testInfo := &SubAgentInfo{
		SubID:          "test-123",
		Task:           "Test task",
		Status:         SubAgentStatusCompleted,
		Result:         "Success",
		StartTime:      time.Now().Add(-5 * time.Minute),
		IterationsUsed: 3,
		ToolCalls:      5,
		Usage: &common.AgentUsage{
			PromptTokens:     200,
			CompletionTokens: 100,
			CachedTokens:     50,
		},
		Signature: common.RunSignature{
			ContextUID: "ctx-123",
			RunUID:     "run-456",
		},
	}
	endTime := time.Now()
	testInfo.EndTime = &endTime

	globalRegistry.Register("test-123", testInfo)

	tool := GetSubAgentStatus()

	// Test successful retrieval
	actx := common.NewAgentContext(context.Background())
	inputs := map[string]any{
		"sub_id": "test-123",
	}

	result := tool.Execute(actx, inputs)
	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	resultStr := result.String()
	if resultStr == "" {
		t.Fatal("Expected non-empty result string")
	}

	// Verify result contains expected information
	expectedStrings := []string{
		"test-123",
		"Test task",
		"completed",
		"Success",
		"Iterations: 3",
		"Tool calls: 5",
		"200 prompt + 100 completion",
		"ctx-123",
		"run-456",
	}

	for _, expected := range expectedStrings {
		if !contains(resultStr, expected) {
			t.Errorf("Expected result to contain '%s', but it didn't. Got: %s", expected, resultStr)
		}
	}
}

func TestGetSubAgentStatus_NotFound(t *testing.T) {
	// Clean up registry
	globalRegistry = &SubAgentRegistry{
		agents: make(map[string]*SubAgentInfo),
	}

	tool := GetSubAgentStatus()
	actx := common.NewAgentContext(context.Background())
	inputs := map[string]any{
		"sub_id": "nonexistent",
	}

	result := tool.Execute(actx, inputs)
	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	resultStr := result.String()
	expected := "Subagent with ID 'nonexistent' not found."
	if resultStr != expected {
		t.Errorf("Expected '%s', got '%s'", expected, resultStr)
	}
}

func TestGetSubAgentStatus_MissingSubID(t *testing.T) {
	tool := GetSubAgentStatus()
	actx := common.NewAgentContext(context.Background())
	inputs := map[string]any{}

	result := tool.Execute(actx, inputs)
	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	resultStr := result.String()
	if resultStr != "sub_id parameter is missing or invalid." {
		t.Errorf("Expected error message about missing sub_id, got '%s'", resultStr)
	}
}

func TestSubAgentRegistry(t *testing.T) {
	registry := &SubAgentRegistry{
		agents: make(map[string]*SubAgentInfo),
	}

	// Test Register and Get
	info := &SubAgentInfo{
		SubID:  "test-1",
		Task:   "Task 1",
		Status: SubAgentStatusRunning,
	}
	registry.Register("test-1", info)

	retrieved, exists := registry.Get("test-1")
	if !exists {
		t.Fatal("Expected to find registered subagent")
	}
	if retrieved.SubID != "test-1" {
		t.Errorf("Expected SubID 'test-1', got '%s'", retrieved.SubID)
	}

	// Test Update
	registry.Update("test-1", func(i *SubAgentInfo) {
		i.Status = SubAgentStatusCompleted
		i.Result = "Done"
	})

	updated, _ := registry.Get("test-1")
	if updated.Status != SubAgentStatusCompleted {
		t.Errorf("Expected status completed, got %s", updated.Status)
	}
	if updated.Result != "Done" {
		t.Errorf("Expected result 'Done', got '%s'", updated.Result)
	}

	// Test List
	info2 := &SubAgentInfo{
		SubID:  "test-2",
		Task:   "Task 2",
		Status: SubAgentStatusRunning,
	}
	registry.Register("test-2", info2)

	list := registry.List()
	if len(list) != 2 {
		t.Errorf("Expected 2 subagents, got %d", len(list))
	}

	// Test Delete
	registry.Delete("test-1")
	_, exists = registry.Get("test-1")
	if exists {
		t.Error("Expected subagent to be deleted")
	}

	list = registry.List()
	if len(list) != 1 {
		t.Errorf("Expected 1 subagent after delete, got %d", len(list))
	}
}

func TestSpawnSubAgent_FailedStart(t *testing.T) {
	// Clean up registry
	globalRegistry = &SubAgentRegistry{
		agents: make(map[string]*SubAgentInfo),
	}

	mockAgent := &MockAgent{
		doFunc: func(ctx context.Context, args *common.AgentDoArgs) (common.RunSignature, streaming.Stream[common.AgentEvent], error) {
			signature := common.RunSignature{
				ContextUID: "test-context",
				RunUID:     "test-run",
			}
			eventStream := streaming.NewStream[common.AgentEvent](10)

			// Send failure events
			go func() {
				defer eventStream.Close()
				_ = eventStream.WriteWithContext(ctx, common.RunStartedEvent{
					Signature: signature,
					MaxStep:   8,
				})
				_ = eventStream.WriteWithContext(ctx, common.RunFailedEvent{
					Usage: &common.AgentUsage{
						PromptTokens:     50,
						CompletionTokens: 20,
					},
					IterationsUsed: 1,
					Operation:      "test operation",
					Error:          "test error",
				})
			}()

			return signature, eventStream, nil
		},
	}

	tool := SpawnSubAgent(mockAgent)
	actx := common.NewAgentContext(context.Background())
	inputs := map[string]any{
		"task": "Failed task",
	}

	result := tool.Execute(actx, inputs)
	if result == nil {
		t.Fatal("Expected result, got nil")
	}

	// Wait for background processing
	time.Sleep(100 * time.Millisecond)

	// Verify subagent status is failed
	infos := globalRegistry.List()
	if len(infos) == 0 {
		t.Fatal("Expected subagent to be registered")
	}

	info := infos[0]
	if info.Status != SubAgentStatusFailed {
		t.Errorf("Expected status failed, got %s", info.Status)
	}
	if info.Error == "" {
		t.Error("Expected error message to be set")
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
