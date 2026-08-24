package react

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/streaming"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type blockingSteerModel struct {
	firstStarted chan struct{}
	releaseFirst chan struct{}
	responses    []*schema.AgenticMessage

	mu     sync.Mutex
	inputs [][]*schema.AgenticMessage
}

func newBlockingSteerModel(responses ...*schema.AgenticMessage) *blockingSteerModel {
	return &blockingSteerModel{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		responses:    responses,
	}
}

func (m *blockingSteerModel) Generate(
	_ context.Context,
	_ []*schema.AgenticMessage,
	_ ...model.Option,
) (*schema.AgenticMessage, error) {
	return nil, errors.New("unexpected Generate call")
}

func (m *blockingSteerModel) Stream(
	ctx context.Context,
	input []*schema.AgenticMessage,
	_ ...model.Option,
) (*schema.StreamReader[*schema.AgenticMessage], error) {
	m.mu.Lock()
	m.inputs = append(m.inputs, common.CloneAgenticMessages(input))
	call := len(m.inputs)
	m.mu.Unlock()

	if call == 1 {
		close(m.firstStarted)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-m.releaseFirst:
		}
	}
	if call > len(m.responses) {
		return nil, fmt.Errorf("unexpected Generate call %d", call)
	}
	return schema.StreamReaderFromArray([]*schema.AgenticMessage{m.responses[call-1]}), nil
}

func (m *blockingSteerModel) recordedInputs() [][]*schema.AgenticMessage {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([][]*schema.AgenticMessage, len(m.inputs))
	for i, input := range m.inputs {
		result[i] = common.CloneAgenticMessages(input)
	}
	return result
}

func TestFinalAnswerDiscardsPendingSteeringAndClosesInbox(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	llm := newBlockingSteerModel(common.AssistantTextMessage("final answer"))
	manager := ram.NewRAMContextManager()
	agent := NewAgent(llm, 128, manager)

	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "original request"},
		MaxStep:   4,
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-llm.firstStarted:
	case <-ctx.Done():
		t.Fatalf("first Think did not start: %v", ctx.Err())
	}

	// The call succeeds because no final answer has been committed yet. The
	// pending messages are discarded when the final answer wins the boundary.
	if err := agent.Steer(ctx, &common.AgentSteerArgs{
		ContextUID: signature.ContextUID,
		UserInputs: []common.AgentUserInput{
			{Text: "ignored steer one"},
			{Text: "ignored steer two"},
		},
	}); err != nil {
		t.Fatalf("Steer before final: %v", err)
	}
	close(llm.releaseFirst)

	events := readAllEvents(t, ctx, eventStream)
	finalAnswers := eventsByType[common.FinalAnswerCompletedEvent](events)
	if len(finalAnswers) != 1 || finalAnswers[0].Answer != "final answer" {
		t.Fatalf("final answer events = %+v", finalAnswers)
	}
	terminals := terminalEvents(events)
	if len(terminals) != 1 || terminals[0].Type() != common.AgentEventTypeRunCompleted {
		t.Fatalf("terminal events = %+v", terminals)
	}
	if got := len(llm.recordedInputs()); got != 1 {
		t.Fatalf("model call count = %d, want 1", got)
	}

	history := mustLoadHistory(t, ctx, manager, signature.ContextUID)
	if len(history) != 3 {
		t.Fatalf("history count = %d, want system, user, final", len(history))
	}
	if got := messagePlainText(history[2]); got != "final answer" {
		t.Fatalf("last history message = %q", got)
	}

	if err := agent.Steer(ctx, &common.AgentSteerArgs{
		ContextUID: signature.ContextUID,
		UserInputs: []common.AgentUserInput{{Text: "too late"}},
	}); !errors.Is(err, contextmgr.ErrConversationFinalized) {
		t.Fatalf("Steer after final error = %v, want ErrConversationFinalized", err)
	}
}

func TestSteerIsAppliedAfterCompleteToolTurn(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	toolCall := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.FunctionToolCall{
				CallID:    "call-1",
				Name:      "echo",
				Arguments: `{}`,
			}),
		},
	}
	llm := newBlockingSteerModel(toolCall, common.AssistantTextMessage("final after steer"))
	manager := ram.NewRAMContextManager()
	agent := NewAgent(llm, 128, manager)
	agent.AddTool(ctx, common.NewDefaultTool(
		"echo",
		"Return a test observation.",
		common.NewToolParameters(),
		func(*common.AgentContext, map[string]any) common.ToolResult {
			return common.NewDefaultToolResult("tool observation")
		},
	))

	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "use the tool"},
		MaxStep:   4,
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-llm.firstStarted:
	case <-ctx.Done():
		t.Fatalf("first Think did not start: %v", ctx.Err())
	}
	if err := agent.Steer(ctx, &common.AgentSteerArgs{
		ContextUID: signature.ContextUID,
		UserInputs: []common.AgentUserInput{
			{Text: "steer one"},
			{Text: "steer two"},
		},
	}); err != nil {
		t.Fatalf("Steer: %v", err)
	}
	close(llm.releaseFirst)

	events := readAllEvents(t, ctx, eventStream)
	completedTools := eventsByType[common.ToolCallCompletedEvent](events)
	if len(completedTools) != 1 || completedTools[0].Result != "tool observation" {
		t.Fatalf("tool completed events = %+v", completedTools)
	}
	finalAnswers := eventsByType[common.FinalAnswerCompletedEvent](events)
	if len(finalAnswers) != 1 || finalAnswers[0].Answer != "final after steer" {
		t.Fatalf("final answer events = %+v", finalAnswers)
	}

	inputs := llm.recordedInputs()
	if len(inputs) != 2 {
		t.Fatalf("model input count = %d, want 2", len(inputs))
	}
	secondInput := inputs[1]
	if got := messagePlainText(secondInput[len(secondInput)-2]); got != "steer one" {
		t.Fatalf("penultimate model message = %q, want steer one", got)
	}
	if got := messagePlainText(secondInput[len(secondInput)-1]); got != "steer two" {
		t.Fatalf("last model message = %q, want steer two", got)
	}
}

func readAllEvents(
	t *testing.T,
	ctx context.Context,
	eventStream streaming.Stream[common.AgentEvent],
) []common.AgentEvent {
	t.Helper()

	events := make([]common.AgentEvent, 0)
	for {
		event, err := eventStream.ReadWithContext(ctx)
		if errors.Is(err, streaming.ErrStreamClosed) {
			return events
		}
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		events = append(events, event)
	}
}

func eventsByType[T common.AgentEvent](events []common.AgentEvent) []T {
	result := make([]T, 0)
	for _, event := range events {
		if typed, ok := event.(T); ok {
			result = append(result, typed)
		}
	}
	return result
}

func terminalEvents(events []common.AgentEvent) []common.AgentEvent {
	result := make([]common.AgentEvent, 0, 1)
	for _, event := range events {
		if common.IsTerminalAgentEvent(event) {
			result = append(result, event)
		}
	}
	return result
}
