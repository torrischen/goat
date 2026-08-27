package planexecute

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/agent/react"
	"github.com/torrischen/goat/llm"
	"github.com/torrischen/goat/llm/llmtest"
	"github.com/torrischen/goat/streaming"
)

type scriptedModel struct {
	mu        sync.Mutex
	generated []*message.Message
	streamed  [][]*message.Message
	inputs    [][]*message.Message
}

func (m *scriptedModel) ModelID() string { return "test-model" }

func (m *scriptedModel) Generate(
	_ context.Context,
	input []*message.Message,
	_ ...llm.CallOption,
) (*message.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inputs = append(m.inputs, common.CloneAgenticMessages(input))
	if len(m.generated) == 0 {
		return nil, errors.New("no scripted generated response")
	}
	response := m.generated[0]
	m.generated = m.generated[1:]
	return response, nil
}

func (m *scriptedModel) Stream(
	_ context.Context,
	input []*message.Message,
	_ ...llm.CallOption,
) (llm.StreamReader, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inputs = append(m.inputs, common.CloneAgenticMessages(input))
	if len(m.streamed) == 0 {
		return nil, errors.New("no scripted streamed response")
	}
	response := m.streamed[0]
	m.streamed = m.streamed[1:]
	return llmtest.NewStreamReader(response), nil
}

func TestAgentExecutesDependencyReadyStepsAndSettlesParentRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	planner := &scriptedModel{
		generated: []*message.Message{withUsage(common.AssistantTextMessage(
			`{"goal":"ship safely","steps":[`+
				`{"id":"verify","description":"verify the result","dependencies":["change"]},`+
				`{"id":"change","description":"make the change"}]}`,
		), 2, 0, 1)},
		streamed: [][]*message.Message{{withUsage(common.AssistantTextMessage("finished"), 3, 1, 2)}},
	}
	executorModel := &scriptedModel{streamed: [][]*message.Message{
		{withUsage(common.AssistantTextMessage("changed"), 5, 0, 2)},
		{withUsage(common.AssistantTextMessage("verified"), 7, 1, 3)},
	}}
	executor := react.NewAgent(executorModel, 128, ram.NewRAMContextManager())
	manager := ram.NewRAMContextManager()
	agent := NewAgent(planner, executor, manager, &Config{MaxPlanSteps: 4, ExecutorMaxSteps: 3})

	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "make and verify a change"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := readEvents(t, ctx, eventStream)

	var started []string
	terminals := 0
	for _, event := range events {
		switch typed := event.(type) {
		case StepStartedEvent:
			started = append(started, typed.Step.ID)
		case common.RunCompletedEvent:
			terminals++
			if typed.Usage == nil || typed.Usage.PromptTokens != 17 || typed.Usage.CachedTokens != 2 || typed.Usage.CompletionTokens != 8 {
				t.Fatalf("usage = %+v", typed.Usage)
			}
		case common.RunFailedEvent, common.RunCanceledEvent, common.RunInterruptedEvent:
			terminals++
		}
	}
	if !reflect.DeepEqual(started, []string{"change", "verify"}) {
		t.Fatalf("step order = %v", started)
	}
	if terminals != 1 {
		t.Fatalf("terminal event count = %d", terminals)
	}

	history, err := manager.Load(ctx, signature.ContextUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 || messageText(history[2]) != "finished" {
		t.Fatalf("parent history = %+v", history)
	}
	if runUID, ok := common.RunUIDFromMessage(history[1]); !ok || runUID != signature.RunUID {
		t.Fatalf("run boundary = %q, %v", runUID, ok)
	}
	if _, err := agent.Fork(ctx, &common.AgentForkArgs{From: signature}); err != nil {
		t.Fatalf("fork settled run: %v", err)
	}
}

func TestAgentRejectsInvalidPlanAndEmitsFailedTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	planner := &scriptedModel{generated: []*message.Message{common.AssistantTextMessage(
		`{"goal":"bad","steps":[{"id":"one","description":"loop","dependencies":["one"]}]}`,
	)}}
	executor := react.NewAgent(&scriptedModel{}, 128, ram.NewRAMContextManager())
	agent := NewAgent(planner, executor, ram.NewRAMContextManager(), nil)
	_, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{UserInput: common.AgentUserInput{Text: "bad plan"}})
	if err != nil {
		t.Fatal(err)
	}
	events := readEvents(t, ctx, eventStream)
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	failed, ok := events[1].(common.RunFailedEvent)
	if !ok || failed.Operation != "plan and execute" {
		t.Fatalf("terminal = %+v", events[1])
	}
}

func TestValidatePlanRejectsUnknownDependencyAndCycle(t *testing.T) {
	tests := []Plan{
		{Goal: "unknown", Steps: []Step{{ID: "one", Description: "one", Dependencies: []string{"missing"}}}},
		{Goal: "cycle", Steps: []Step{
			{ID: "one", Description: "one", Dependencies: []string{"two"}},
			{ID: "two", Description: "two", Dependencies: []string{"one"}},
		}},
	}
	for _, plan := range tests {
		plan := plan
		if err := validatePlan(&plan, 8, nil); err == nil {
			t.Fatalf("plan %+v was accepted", plan)
		}
	}
}

func readEvents(
	t *testing.T,
	ctx context.Context,
	events streaming.Stream[common.AgentEvent],
) []common.AgentEvent {
	t.Helper()
	var result []common.AgentEvent
	for {
		event, err := events.ReadWithContext(ctx)
		if errors.Is(err, streaming.ErrStreamClosed) {
			return result
		}
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, event)
	}
}

func withUsage(msg *message.Message, prompt, cached, completion int) *message.Message {
	msg.Meta = &message.ResponseMeta{Usage: &message.Usage{
		PromptTokens: prompt, CachedTokens: cached, CompletionTokens: completion,
	}}
	return msg
}
