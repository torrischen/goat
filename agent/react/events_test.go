package react

import (
	"context"
	"errors"
	"reflect"
	"strings"
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

type scriptedEventModel struct {
	mu           sync.Mutex
	responses    [][]*schema.AgenticMessage
	inputs       [][]*schema.AgenticMessage
	optionCounts []int
	streamErr    error
}

type failingSettleStore struct {
	delegate contextmgr.Store
	err      error
}

func (s *failingSettleStore) Create(
	ctx context.Context,
	state *contextmgr.State,
) (common.ContextUID, error) {
	return s.delegate.Create(ctx, state)
}

func (s *failingSettleStore) Load(
	ctx context.Context,
	contextUID common.ContextUID,
) (*contextmgr.State, error) {
	return s.delegate.Load(ctx, contextUID)
}

func (s *failingSettleStore) CompareAndSwap(
	ctx context.Context,
	contextUID common.ContextUID,
	expectedRevision uint64,
	state *contextmgr.State,
) error {
	if len(state.RunSnapshots) > 0 {
		return s.err
	}
	return s.delegate.CompareAndSwap(ctx, contextUID, expectedRevision, state)
}

func (s *failingSettleStore) Delete(ctx context.Context, contextUID common.ContextUID) error {
	return s.delegate.Delete(ctx, contextUID)
}

func (m *scriptedEventModel) Generate(
	context.Context,
	[]*schema.AgenticMessage,
	...model.Option,
) (*schema.AgenticMessage, error) {
	return nil, errors.New("unexpected Generate call")
}

func (m *scriptedEventModel) Stream(
	_ context.Context,
	input []*schema.AgenticMessage,
	opts ...model.Option,
) (*schema.StreamReader[*schema.AgenticMessage], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inputs = append(m.inputs, common.CloneAgenticMessages(input))
	m.optionCounts = append(m.optionCounts, len(opts))
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	if len(m.responses) == 0 {
		return nil, errors.New("no scripted model response")
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	return schema.StreamReaderFromArray(response), nil
}

func TestDoEmitsDirectAnswerLifecycleAndUsage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	answer := messageWithUsage(common.AssistantTextMessage("done"), 7, 2, 3)
	store := ram.NewRAMStore()
	manager := contextmgr.NewManager(store)
	agent := NewAgent(&scriptedEventModel{responses: [][]*schema.AgenticMessage{{answer}}}, 128, manager)
	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "answer directly"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := readAllEvents(t, ctx, eventStream)

	wantTypes := []common.AgentEventType{
		common.AgentEventTypeRunStarted,
		common.AgentEventTypeModelCallStarted,
		common.AgentEventTypeAssistantTextDelta,
		common.AgentEventTypeModelCallCompleted,
		common.AgentEventTypeFinalAnswerCompleted,
		common.AgentEventTypeRunCompleted,
	}
	if got := eventTypes(events); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	if signature.IsZero() {
		t.Fatal("Do() returned an empty run signature")
	}
	started := events[0].(common.RunStartedEvent)
	if started.Signature != signature {
		t.Fatalf("run started signature = %+v, want %+v", started.Signature, signature)
	}
	history := mustLoadHistory(t, ctx, manager, signature.ContextUID)
	if len(history) != 3 {
		t.Fatalf("history count = %d, want system, user, final", len(history))
	}
	if got, ok := common.RunUIDFromMessage(history[1]); !ok || got != signature.RunUID {
		t.Fatalf("stored run boundary = %q, %v, want %q", got, ok, signature.RunUID)
	}
	completed := events[len(events)-1].(common.RunCompletedEvent)
	if !reflect.DeepEqual(completed.Usage, &common.AgentUsage{
		PromptTokens: 7, CachedTokens: 2, CompletionTokens: 3,
	}) {
		t.Fatalf("run usage = %+v", completed.Usage)
	}
	if completed.IterationsUsed != 1 || completed.ToolCalls != 0 {
		t.Fatalf("run completed = %+v", completed)
	}
	assertRunOutcome(t, ctx, store, signature, contextmgr.RunOutcomeCompleted)
}

func TestDoUsesContextUIDAsPromptCacheKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	llm := &scriptedEventModel{responses: [][]*schema.AgenticMessage{
		{common.AssistantTextMessage("first answer")},
		{common.AssistantTextMessage("second answer")},
	}}
	agent := NewAgent(llm, 128, ram.NewRAMContextManager())

	first, firstEvents, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "first request"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readAllEvents(t, ctx, firstEvents)

	_, secondEvents, err := agent.Do(ctx, &common.AgentDoArgs{
		ContextUID: first.ContextUID,
		UserInput:  common.AgentUserInput{Text: "second request"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readAllEvents(t, ctx, secondEvents)

	llm.mu.Lock()
	defer llm.mu.Unlock()
	if !reflect.DeepEqual(llm.optionCounts, []int{1, 1}) {
		t.Fatalf("model option counts = %v, want one cache option for both generated and provided contexts", llm.optionCounts)
	}
}

func TestDoCreatesDistinctRunSignaturesWithinOneConversation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	manager := ram.NewRAMContextManager()
	agent := NewAgent(&scriptedEventModel{responses: [][]*schema.AgenticMessage{
		{common.AssistantTextMessage("first answer")},
		{common.AssistantTextMessage("second answer")},
	}}, 128, manager)

	first, firstEvents, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "first request"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readAllEvents(t, ctx, firstEvents)

	second, secondEvents, err := agent.Do(ctx, &common.AgentDoArgs{
		ContextUID: first.ContextUID,
		UserInput:  common.AgentUserInput{Text: "second request"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readAllEvents(t, ctx, secondEvents)

	if first.ContextUID != second.ContextUID || first.RunUID == second.RunUID {
		t.Fatalf("run signatures = first %+v, second %+v", first, second)
	}
	preamble, runs := common.SplitMessagesByRun(first.ContextUID, mustLoadHistory(t, ctx, manager, first.ContextUID))
	if len(preamble) != 1 || len(runs) != 2 {
		t.Fatalf("split history = %d preamble messages, %d runs", len(preamble), len(runs))
	}
	if runs[0].Signature != first || runs[1].Signature != second {
		t.Fatalf("split signatures = %+v, want %+v and %+v", runs, first, second)
	}
}

func TestForkCreatesIndependentConversationAtSettledRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	manager := ram.NewRAMContextManager()
	agent := NewAgent(&scriptedEventModel{responses: [][]*schema.AgenticMessage{
		{common.AssistantTextMessage("first answer")},
		{common.AssistantTextMessage("second answer")},
		{common.AssistantTextMessage("branch answer")},
	}}, 128, manager)

	first, firstEvents, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "first request"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readAllEvents(t, ctx, firstEvents)

	second, secondEvents, err := agent.Do(ctx, &common.AgentDoArgs{
		ContextUID: first.ContextUID,
		UserInput:  common.AgentUserInput{Text: "second request"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readAllEvents(t, ctx, secondEvents)

	forkedContextUID, err := agent.Fork(ctx, &common.AgentForkArgs{From: first})
	if err != nil {
		t.Fatalf("Fork(first) error = %v", err)
	}
	if forkedContextUID == "" || forkedContextUID == first.ContextUID {
		t.Fatalf("forked context UID = %q", forkedContextUID)
	}
	_, forkedRuns := common.SplitMessagesByRun(forkedContextUID, mustLoadHistory(t, ctx, manager, forkedContextUID))
	if len(forkedRuns) != 1 || forkedRuns[0].Signature.RunUID != first.RunUID {
		t.Fatalf("forked runs = %+v, want only %s", forkedRuns, first.RunUID)
	}

	branch, branchEvents, err := agent.Do(ctx, &common.AgentDoArgs{
		ContextUID: forkedContextUID,
		UserInput:  common.AgentUserInput{Text: "branch request"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readAllEvents(t, ctx, branchEvents)
	if branch.ContextUID != forkedContextUID || branch.RunUID == first.RunUID || branch.RunUID == second.RunUID {
		t.Fatalf("branch signature = %+v", branch)
	}

	_, sourceRuns := common.SplitMessagesByRun(first.ContextUID, mustLoadHistory(t, ctx, manager, first.ContextUID))
	_, branchRuns := common.SplitMessagesByRun(forkedContextUID, mustLoadHistory(t, ctx, manager, forkedContextUID))
	if len(sourceRuns) != 2 || sourceRuns[1].Signature != second {
		t.Fatalf("source runs changed after branch = %+v", sourceRuns)
	}
	if len(branchRuns) != 2 || branchRuns[1].Signature != branch {
		t.Fatalf("branch runs = %+v", branchRuns)
	}
}

func TestForkValidatesArguments(t *testing.T) {
	agent := NewAgent(&scriptedEventModel{}, 128, ram.NewRAMContextManager())
	ctx := context.Background()

	if _, err := agent.Fork(ctx, nil); err == nil || !strings.Contains(err.Error(), "args is nil") {
		t.Fatalf("Fork(nil) error = %v", err)
	}
	if _, err := agent.Fork(ctx, &common.AgentForkArgs{}); !errors.Is(err, contextmgr.ErrInvalidRunSignature) || !strings.Contains(err.Error(), "context UID is empty") {
		t.Fatalf("Fork(empty context) error = %v", err)
	}
	if _, err := agent.Fork(ctx, &common.AgentForkArgs{From: common.RunSignature{ContextUID: "context"}}); !errors.Is(err, contextmgr.ErrInvalidRunSignature) || !strings.Contains(err.Error(), "run UID is empty") {
		t.Fatalf("Fork(empty run) error = %v", err)
	}
}

func TestDoEmitsRunFailedForBackgroundModelError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store := ram.NewRAMStore()
	manager := contextmgr.NewManager(store)
	agent := NewAgent(&scriptedEventModel{streamErr: errors.New("provider unavailable")}, 128, manager)
	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "fail"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := readAllEvents(t, ctx, eventStream)

	failedCalls := eventsByType[common.ModelCallFailedEvent](events)
	if len(failedCalls) != 1 || failedCalls[0].Error != "provider unavailable" {
		t.Fatalf("model failures = %+v", failedCalls)
	}
	terminals := terminalEvents(events)
	if len(terminals) != 1 {
		t.Fatalf("terminal events = %+v", terminals)
	}
	failed, ok := terminals[0].(common.RunFailedEvent)
	if !ok || failed.Operation != "think" || !containsAll(failed.Error, "think model call", "provider unavailable") {
		t.Fatalf("run failure = %+v", terminals[0])
	}
	forkedContextUID, err := agent.Fork(ctx, &common.AgentForkArgs{From: signature})
	if err != nil {
		t.Fatalf("Fork(failed run) error = %v", err)
	}
	if got := len(mustLoadHistory(t, ctx, manager, forkedContextUID)); got != 2 {
		t.Fatalf("failed-run fork message count = %d, want system and user", got)
	}
	assertRunOutcome(t, ctx, store, signature, contextmgr.RunOutcomeFailed)
}

func TestDoReportsSettleFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store := &failingSettleStore{
		delegate: ram.NewRAMStore(),
		err:      errors.New("state storage unavailable"),
	}
	manager := contextmgr.NewManager(store)
	agent := NewAgent(&scriptedEventModel{responses: [][]*schema.AgenticMessage{{
		common.AssistantTextMessage("answer"),
	}}}, 128, manager)
	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "request"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := readAllEvents(t, ctx, eventStream)

	terminals := terminalEvents(events)
	if len(terminals) != 1 {
		t.Fatalf("terminal events = %+v", terminals)
	}
	failed, ok := terminals[0].(common.RunFailedEvent)
	if !ok || failed.Operation != "settle run" || !strings.Contains(failed.Error, "state storage unavailable") {
		t.Fatalf("run settlement failure = %+v", terminals[0])
	}
	if _, err := agent.Fork(ctx, &common.AgentForkArgs{From: signature}); !errors.Is(err, contextmgr.ErrRunNotSettled) {
		t.Fatalf("Fork(unsettled run) error = %v, want ErrRunNotSettled", err)
	}
	history := mustLoadHistory(t, ctx, manager, signature.ContextUID)
	if len(history) != 2 {
		t.Fatalf("failed settlement partially committed final answer: %d messages", len(history))
	}
}

func TestDoStreamsParallelToolCompletionOrderAndKeepsResultOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	toolCalls := assistantToolCalls(
		&schema.FunctionToolCall{CallID: "slow-call", Name: "slow", Arguments: `{}`},
		&schema.FunctionToolCall{CallID: "fast-call", Name: "fast", Arguments: `{}`},
	)
	llm := &scriptedEventModel{responses: [][]*schema.AgenticMessage{
		{messageWithUsage(toolCalls, 5, 1, 2)},
		{messageWithUsage(common.AssistantTextMessage("finished"), 4, 0, 1)},
	}}
	agent := NewAgent(llm, 128, ram.NewRAMContextManager())
	releaseSlow := make(chan struct{})
	agent.AddTools(ctx,
		common.NewDefaultTool("slow", "slow tool", common.NewToolParameters(), func(*common.AgentContext, map[string]any) common.ToolResult {
			<-releaseSlow
			return common.NewDefaultToolResult("slow result").AddUsage(common.NewAgentUsage(2, 0, 1))
		}),
		common.NewDefaultTool("fast", "fast tool", common.NewToolParameters(), func(*common.AgentContext, map[string]any) common.ToolResult {
			return common.NewDefaultToolResult("fast result").AddUsage(common.NewAgentUsage(3, 1, 2))
		}),
	)

	_, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "use both tools"},
		ToolExecutionOptions: &common.ToolExecutionOptions{
			EnableParallel: true,
			MaxConcurrency: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	events := make([]common.AgentEvent, 0)
	released := false
	for {
		event, readErr := eventStream.ReadWithContext(ctx)
		if errors.Is(readErr, streaming.ErrStreamClosed) {
			break
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		events = append(events, event)
		if completed, ok := event.(common.ToolCallCompletedEvent); ok && completed.Name == "fast" && !released {
			close(releaseSlow)
			released = true
		}
	}

	requested := eventsByType[common.ToolCallRequestedEvent](events)
	if got := []string{requested[0].Name, requested[1].Name}; !reflect.DeepEqual(got, []string{"slow", "fast"}) {
		t.Fatalf("requested order = %v", got)
	}
	completed := eventsByType[common.ToolCallCompletedEvent](events)
	if got := []string{completed[0].Name, completed[1].Name}; !reflect.DeepEqual(got, []string{"fast", "slow"}) {
		t.Fatalf("completion order = %v", got)
	}

	llm.mu.Lock()
	secondInput := common.CloneAgenticMessages(llm.inputs[1])
	llm.mu.Unlock()
	toolResultNames := make([]string, 0, 2)
	for _, message := range secondInput {
		for _, block := range message.ContentBlocks {
			if block != nil && block.FunctionToolResult != nil {
				toolResultNames = append(toolResultNames, block.FunctionToolResult.Name)
			}
		}
	}
	if !reflect.DeepEqual(toolResultNames, []string{"slow", "fast"}) {
		t.Fatalf("model tool result order = %v", toolResultNames)
	}

	terminal := eventsByType[common.RunCompletedEvent](events)
	if len(terminal) != 1 || terminal[0].ToolCalls != 2 || !reflect.DeepEqual(terminal[0].Usage, &common.AgentUsage{
		PromptTokens: 14, CachedTokens: 2, CompletionTokens: 6,
	}) {
		t.Fatalf("run completed events = %+v", terminal)
	}
}

func TestDoEmitsInterruptedAfterWrappedTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	llm := &scriptedEventModel{responses: [][]*schema.AgenticMessage{{assistantToolCalls(
		&schema.FunctionToolCall{CallID: "approval-call", Name: "approval", Arguments: `{}`},
	)}}}
	store := ram.NewRAMStore()
	agent := NewAgent(llm, 128, contextmgr.NewManager(store))
	agent.AddTool(ctx, common.InterruptLoopAfter(common.NewDefaultTool(
		"approval",
		"pause the run",
		common.NewToolParameters(),
		func(*common.AgentContext, map[string]any) common.ToolResult {
			return common.NewDefaultToolResult("waiting")
		},
	)))

	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{UserInput: common.AgentUserInput{Text: "pause"}})
	if err != nil {
		t.Fatal(err)
	}
	events := readAllEvents(t, ctx, eventStream)
	if len(eventsByType[common.ToolCallCompletedEvent](events)) != 1 {
		t.Fatalf("missing tool completed event: %v", eventTypes(events))
	}
	if len(eventsByType[common.FinalAnswerCompletedEvent](events)) != 0 {
		t.Fatalf("interrupted run emitted a final answer: %v", eventTypes(events))
	}
	terminals := terminalEvents(events)
	if len(terminals) != 1 || terminals[0].Type() != common.AgentEventTypeRunInterrupted {
		t.Fatalf("terminal events = %+v", terminals)
	}
	if _, err := agent.Fork(ctx, &common.AgentForkArgs{From: signature}); err != nil {
		t.Fatalf("Fork(interrupted run) error = %v", err)
	}
	assertRunOutcome(t, ctx, store, signature, contextmgr.RunOutcomeInterrupted)
}

func TestDoEmitsCanceledTerminalAfterContextCancellation(t *testing.T) {
	baseCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	runCtx, cancel := context.WithCancel(baseCtx)
	defer cancel()

	llm := &scriptedEventModel{responses: [][]*schema.AgenticMessage{{assistantToolCalls(
		&schema.FunctionToolCall{CallID: "blocking-call", Name: "blocking", Arguments: `{}`},
	)}}}
	store := ram.NewRAMStore()
	agent := NewAgent(llm, 128, contextmgr.NewManager(store))
	agent.AddTool(baseCtx, common.NewDefaultTool(
		"blocking",
		"wait for cancellation",
		common.NewToolParameters(),
		func(ctx *common.AgentContext, _ map[string]any) common.ToolResult {
			<-ctx.Done()
			return common.NewDefaultToolResult("canceled")
		},
	))

	signature, eventStream, err := agent.Do(runCtx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "wait"},
	})
	if err != nil {
		t.Fatal(err)
	}

	events := make([]common.AgentEvent, 0)
	for {
		event, readErr := eventStream.ReadWithContext(baseCtx)
		if errors.Is(readErr, streaming.ErrStreamClosed) {
			break
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		events = append(events, event)
		if _, ok := event.(common.ToolCallStartedEvent); ok {
			cancel()
		}
	}

	terminals := terminalEvents(events)
	if len(terminals) != 1 {
		t.Fatalf("terminal events = %+v", terminals)
	}
	canceled, ok := terminals[0].(common.RunCanceledEvent)
	if !ok || canceled.Reason != context.Canceled.Error() {
		t.Fatalf("run canceled event = %+v", terminals[0])
	}
	if _, err := agent.Fork(baseCtx, &common.AgentForkArgs{From: signature}); err != nil {
		t.Fatalf("Fork(canceled run) error = %v", err)
	}
	assertRunOutcome(t, baseCtx, store, signature, contextmgr.RunOutcomeCanceled)
}

func TestDoTurnsNilToolResultIntoFailureEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	llm := &scriptedEventModel{responses: [][]*schema.AgenticMessage{
		{assistantToolCalls(&schema.FunctionToolCall{CallID: "nil-call", Name: "nil_result", Arguments: `{}`})},
		{common.AssistantTextMessage("recovered")},
	}}
	agent := NewAgent(llm, 128, ram.NewRAMContextManager())
	agent.AddTool(ctx, common.NewDefaultTool(
		"nil_result",
		"return no result",
		common.NewToolParameters(),
		func(*common.AgentContext, map[string]any) common.ToolResult { return nil },
	))

	_, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "handle nil"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := readAllEvents(t, ctx, eventStream)

	failures := eventsByType[common.ToolCallFailedEvent](events)
	if len(failures) != 1 || failures[0].Stage != common.ToolCallFailureStageExecution ||
		failures[0].Error != "tool returned a nil result" {
		t.Fatalf("tool failures = %+v", failures)
	}
	terminals := terminalEvents(events)
	if len(terminals) != 1 || terminals[0].Type() != common.AgentEventTypeRunCompleted {
		t.Fatalf("terminal events = %+v", terminals)
	}

	llm.mu.Lock()
	secondInput := common.CloneAgenticMessages(llm.inputs[1])
	llm.mu.Unlock()
	if !messageInputContains(secondInput, "Error: tool returned a nil result") {
		t.Fatalf("second model input does not contain the tool failure: %+v", secondInput)
	}
}

func mustLoadHistory(
	t *testing.T,
	ctx context.Context,
	manager *contextmgr.Manager,
	contextUID common.ContextUID,
) []*schema.AgenticMessage {
	t.Helper()
	messages, err := manager.Load(ctx, contextUID)
	if err != nil {
		t.Fatal(err)
	}
	return messages
}

func assertRunOutcome(
	t *testing.T,
	ctx context.Context,
	store contextmgr.Store,
	signature common.RunSignature,
	want contextmgr.RunOutcome,
) {
	t.Helper()
	state, err := store.Load(ctx, signature.ContextUID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, exists := state.RunSnapshots[signature.RunUID]
	if !exists || snapshot.Outcome != want {
		t.Fatalf("run snapshot = %+v, exists %v, want outcome %q", snapshot, exists, want)
	}
}

func messageWithUsage(message *schema.AgenticMessage, prompt, cached, completion int) *schema.AgenticMessage {
	message.ResponseMeta = &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      prompt + completion,
		PromptTokenDetails: schema.PromptTokenDetails{
			CachedTokens: cached,
		},
	}}
	return message
}

func assistantToolCalls(calls ...*schema.FunctionToolCall) *schema.AgenticMessage {
	blocks := make([]*schema.ContentBlock, 0, len(calls))
	for _, call := range calls {
		blocks = append(blocks, schema.NewContentBlock(call))
	}
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant, ContentBlocks: blocks}
}

func eventTypes(events []common.AgentEvent) []common.AgentEventType {
	types := make([]common.AgentEventType, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type())
	}
	return types
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}

func messageInputContains(messages []*schema.AgenticMessage, value string) bool {
	for _, message := range messages {
		for _, block := range message.ContentBlocks {
			if block == nil || block.FunctionToolResult == nil {
				continue
			}
			for _, content := range block.FunctionToolResult.Content {
				if content != nil && content.Text != nil && strings.Contains(content.Text.Text, value) {
					return true
				}
			}
		}
	}
	return false
}
