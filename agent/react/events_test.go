package react

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm"
	"github.com/torrischen/goat/llm/llmtest"
	"github.com/torrischen/goat/streaming"
)

type scriptedEventModel struct {
	mu           sync.Mutex
	responses    [][]*message.Message
	inputs       [][]*message.Message
	optionCounts []int
	streamErr    error
}

type preflightCompressionModel struct {
	mu           sync.Mutex
	streamInputs [][]*message.Message
}

func (m *preflightCompressionModel) ModelID() string { return "test-model" }

func (m *preflightCompressionModel) Generate(
	_ context.Context,
	_ []*message.Message,
	_ ...llm.CallOption,
) (*message.Message, error) {
	return common.AssistantTextMessage("compressed summary"), nil
}

func (m *preflightCompressionModel) Stream(
	_ context.Context,
	input []*message.Message,
	_ ...llm.CallOption,
) (llm.StreamReader, error) {
	m.mu.Lock()
	m.streamInputs = append(m.streamInputs, common.CloneAgenticMessages(input))
	m.mu.Unlock()
	return llmtest.NewStreamReader([]*message.Message{
		common.AssistantTextMessage("done"),
	}), nil
}

type failingSettleStore struct {
	delegate contextmgr.Storage
	err      error
}

func (s *failingSettleStore) Get(ctx context.Context, key string) ([]byte, error) {
	return s.delegate.Get(ctx, key)
}

func (s *failingSettleStore) CreateIfAbsent(ctx context.Context, key string, value []byte) error {
	return s.delegate.CreateIfAbsent(ctx, key, value)
}

func (s *failingSettleStore) Set(ctx context.Context, key string, value []byte) error {
	if strings.HasPrefix(key, "objects:runs:") {
		return s.err
	}
	return s.delegate.Set(ctx, key, value)
}

func (s *failingSettleStore) CompareAndSwap(ctx context.Context, key string, oldValue, newValue []byte) error {
	return s.delegate.CompareAndSwap(ctx, key, oldValue, newValue)
}

func (s *failingSettleStore) Delete(ctx context.Context, key string) error {
	return s.delegate.Delete(ctx, key)
}

func (s *failingSettleStore) List(ctx context.Context, prefix string) ([]string, error) {
	return s.delegate.List(ctx, prefix)
}

func (m *scriptedEventModel) ModelID() string { return "test-model" }

func (m *scriptedEventModel) Generate(
	context.Context,
	[]*message.Message,
	...llm.CallOption,
) (*message.Message, error) {
	return nil, errors.New("unexpected Generate call")
}

func (m *scriptedEventModel) Stream(
	_ context.Context,
	input []*message.Message,
	opts ...llm.CallOption,
) (llm.StreamReader, error) {
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
	return llmtest.NewStreamReader(response), nil
}

func TestDoCompressesBeforeFirstModelCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	manager := ram.NewRAMContextManager()
	contextUID, err := manager.Create(ctx, []*message.Message{
		message.SystemMessage("system"),
		message.UserMessage("old question"),
		assistantToolCalls(&message.ToolCall{
			CallID:    "old-call",
			Name:      "search",
			Arguments: `{}`,
		}),
		common.FunctionToolResultMessage(&message.ToolResult{
			CallID: "old-call",
			Name:   "search",
			Content: []*message.ToolResultContent{
				{
					Kind: message.ToolResultText,
					Text: &message.TextData{Text: strings.Repeat("old tool output ", 500)},
				},
			},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	llm := &preflightCompressionModel{}
	agent := NewAgent(llm, 1, manager)
	_, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		ContextUID: contextUID,
		UserInput:  common.AgentUserInput{Text: "continue the task"},
		Compress:   true,
		CompressionOptions: common.CompressionOptions{
			Strategy:       common.CompressionStrategyAggressive,
			RecentMessages: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	readAllEvents(t, ctx, eventStream)

	llm.mu.Lock()
	streamInputs := make([][]*message.Message, len(llm.streamInputs))
	for index, input := range llm.streamInputs {
		streamInputs[index] = common.CloneAgenticMessages(input)
	}
	llm.mu.Unlock()

	if len(streamInputs) != 1 {
		t.Fatalf("normal model call count = %d, want 1", len(streamInputs))
	}
	var firstInputBuilder strings.Builder
	for _, message := range streamInputs[0] {
		firstInputBuilder.WriteString(message.PlainText())
		firstInputBuilder.WriteByte('\n')
	}
	firstInput := firstInputBuilder.String()
	if strings.Contains(firstInput, "old tool output") {
		t.Fatal("first normal model call received the uncompressed tool output")
	}
	if !strings.Contains(firstInput, "[Previous conversation summary]: compressed summary") {
		t.Fatalf("first normal model call did not receive the compressed context: %s", firstInput)
	}

	history, err := manager.Load(ctx, contextUID)
	if err != nil {
		t.Fatal(err)
	}
	if historyContainsText(history, "old tool output") {
		t.Fatal("compressed context persistence retained the old detailed tool output")
	}
}

func TestDoContinuesWhenCompressionMakesNoChange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	manager := ram.NewRAMContextManager()
	contextUID, err := manager.Create(ctx, []*message.Message{
		message.SystemMessage("system"),
		message.UserMessage(strings.Repeat("protected user content ", 500)),
	})
	if err != nil {
		t.Fatal(err)
	}

	llm := &scriptedEventModel{responses: [][]*message.Message{{common.AssistantTextMessage("done")}}}
	agent := NewAgent(llm, 1, manager)
	_, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		ContextUID: contextUID,
		UserInput:  common.AgentUserInput{Text: "continue"},
		Compress:   true,
		CompressionOptions: common.CompressionOptions{
			Strategy: common.CompressionStrategyDiscardHalf,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := readAllEvents(t, ctx, eventStream)

	terminals := terminalEvents(events)
	if len(terminals) != 1 || terminals[0].Type() != common.AgentEventTypeRunCompleted {
		t.Fatalf("terminal events = %+v, want one completed event", terminals)
	}
	llm.mu.Lock()
	inputCount := len(llm.inputs)
	llm.mu.Unlock()
	if inputCount != 1 {
		t.Fatalf("normal model call count = %d, want 1", inputCount)
	}
}

func TestDoFallsBackWhenCompressionFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	manager := ram.NewRAMContextManager()
	contextUID, err := manager.Create(ctx, []*message.Message{
		message.SystemMessage("system"),
		message.UserMessage("old question"),
		assistantToolCalls(&message.ToolCall{
			CallID:    "old-call",
			Name:      "search",
			Arguments: `{}`,
		}),
		common.FunctionToolResultMessage(&message.ToolResult{
			CallID: "old-call",
			Name:   "search",
			Content: []*message.ToolResultContent{
				{
					Kind: message.ToolResultText,
					Text: &message.TextData{Text: strings.Repeat("old tool output ", 500)},
				},
			},
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	llm := &scriptedEventModel{responses: [][]*message.Message{{common.AssistantTextMessage("done")}}}
	agent := NewAgent(llm, 1, manager)
	_, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		ContextUID: contextUID,
		UserInput:  common.AgentUserInput{Text: "continue"},
		Compress:   true,
		CompressionOptions: common.CompressionOptions{
			Strategy:       common.CompressionStrategyAggressive,
			RecentMessages: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := readAllEvents(t, ctx, eventStream)

	terminals := terminalEvents(events)
	if len(terminals) != 1 || terminals[0].Type() != common.AgentEventTypeRunCompleted {
		t.Fatalf("terminal events = %+v, want one completed event", terminals)
	}
	llm.mu.Lock()
	inputs := make([][]*message.Message, len(llm.inputs))
	for index, input := range llm.inputs {
		inputs[index] = common.CloneAgenticMessages(input)
	}
	llm.mu.Unlock()
	if len(inputs) != 1 {
		t.Fatalf("normal model call count = %d, want 1", len(inputs))
	}
	if !historyContainsText(inputs[0], "old tool output") {
		t.Fatal("normal model call did not fall back to the original context")
	}

	history, err := manager.Load(ctx, contextUID)
	if err != nil {
		t.Fatal(err)
	}
	if !historyContainsText(history, "old tool output") {
		t.Fatal("compression failure unexpectedly replaced the persisted context")
	}
}

func TestDoCreatesMissingContextUID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store := ram.NewRAMStorage()
	manager := contextmgr.NewManager(store)
	agent := NewAgent(&scriptedEventModel{responses: [][]*message.Message{{common.AssistantTextMessage("done")}}}, 128, manager)
	wantedUID := common.ContextUID("provided-context")

	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		ContextUID: wantedUID,
		UserInput:  common.AgentUserInput{Text: "start this context"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readAllEvents(t, ctx, eventStream)
	if signature.ContextUID != wantedUID {
		t.Fatalf("ContextUID = %q, want %q", signature.ContextUID, wantedUID)
	}
	history, err := manager.Load(ctx, wantedUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history count = %d, want system, user, final", len(history))
	}
}

func TestDoEmitsDirectAnswerLifecycleAndUsage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	answer := messageWithUsage(common.AssistantTextMessage("done"), 7, 2, 3)
	store := ram.NewRAMStorage()
	manager := contextmgr.NewManager(store)
	agent := NewAgent(&scriptedEventModel{responses: [][]*message.Message{{answer}}}, 128, manager)
	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "answer directly"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := readAllEvents(t, ctx, eventStream)

	wantTypes := []common.AgentEventType{
		common.AgentEventTypeRunStarted,
		common.AgentEventTypeAssistantTextDelta,
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

func TestDoInvokesThinkStartCallbackBeforeModelCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	llm := &scriptedEventModel{responses: [][]*message.Message{{
		common.AssistantTextMessage("done"),
	}}}
	agent := NewAgent(llm, 128, ram.NewRAMContextManager())
	starts := make([]CallbackThinkStartArgs, 0, 1)
	agent.SetCallbacks(&AgentCallbacks{
		OnThinkStart: func(_ context.Context, args *CallbackThinkStartArgs) error {
			starts = append(starts, *args)
			return nil
		},
	})

	_, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "answer directly"},
	})
	if err != nil {
		t.Fatal(err)
	}
	readAllEvents(t, ctx, eventStream)

	if len(starts) != 1 {
		t.Fatalf("OnThinkStart call count = %d, want 1", len(starts))
	}
	if starts[0].Iteration != 0 {
		t.Fatalf("OnThinkStart iteration = %d, want 0", starts[0].Iteration)
	}
	if starts[0].MessageCount != 2 {
		t.Fatalf("OnThinkStart message count = %d, want system and user", starts[0].MessageCount)
	}
	if starts[0].WillCompress {
		t.Fatal("OnThinkStart reported compression for an under-threshold context")
	}
}

func TestDoStreamsReasoningAndAssistantDeltasSeparately(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reasoningBlock := common.ReasoningBlock("thinking")
	reasoningBlock.Provider = map[string]json.RawMessage{"openai-item-id": json.RawMessage(`"rs_123"`), "openai-item-status": json.RawMessage(`"completed"`)}
	reasoning := &message.Message{
		Role:   message.RoleAssistant,
		Blocks: []*message.ContentBlock{reasoningBlock},
	}
	answer := common.AssistantTextMessage("answer")
	answer.Extra = map[string]json.RawMessage{"provider-message-id": json.RawMessage(`"response_123"`)}
	answer.Blocks[0].Provider = map[string]json.RawMessage{"provider-item-id": json.RawMessage(`"message_123"`)}
	manager := ram.NewRAMContextManager()
	agent := NewAgent(&scriptedEventModel{responses: [][]*message.Message{{reasoning, answer}}}, 128, manager)

	contextUID := common.ContextUID("reasoning-metadata")
	_, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		ContextUID: contextUID,
		UserInput:  common.AgentUserInput{Text: "separate output channels"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := readAllEvents(t, ctx, eventStream)

	wantTypes := []common.AgentEventType{
		common.AgentEventTypeRunStarted,
		common.AgentEventTypeReasoningDelta,
		common.AgentEventTypeAssistantTextDelta,
		common.AgentEventTypeFinalAnswerCompleted,
		common.AgentEventTypeRunCompleted,
	}
	if got := eventTypes(events); !reflect.DeepEqual(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	reasoningEvents := eventsByType[common.ReasoningDeltaEvent](events)
	if len(reasoningEvents) != 1 || reasoningEvents[0].Delta != "thinking" {
		t.Fatalf("reasoning events = %+v", reasoningEvents)
	}
	textEvents := eventsByType[common.AssistantTextDeltaEvent](events)
	if len(textEvents) != 1 || textEvents[0].Delta != "answer" {
		t.Fatalf("assistant text events = %+v", textEvents)
	}

	stored, err := manager.Load(ctx, contextUID)
	if err != nil {
		t.Fatal(err)
	}
	final := stored[len(stored)-1]
	if len(final.Blocks) == 0 || final.Blocks[0].Reasoning == nil {
		t.Fatalf("final message reasoning block = %+v", final.Blocks)
	}
	if got := string(final.Blocks[0].Provider["openai-item-id"]); got != `"rs_123"` {
		t.Fatalf("reasoning item ID = %v, want rs_123", got)
	}
	if got := string(final.Extra["provider-message-id"]); got != `"response_123"` {
		t.Fatalf("provider message ID = %v, want response_123", got)
	}
	if len(final.Blocks) != 2 || final.Blocks[1].Text == nil {
		t.Fatalf("final message content blocks = %+v", final.Blocks)
	}
	if got := string(final.Blocks[1].Provider["provider-item-id"]); got != `"message_123"` {
		t.Fatalf("provider text item ID = %v, want message_123", got)
	}
}

func TestDoPreservesForcedFinalProviderMetadata(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	final := common.AssistantTextMessage("forced answer")
	final.Extra = map[string]json.RawMessage{"provider-message-id": json.RawMessage(`"forced_123"`)}
	final.Blocks[0].Provider = map[string]json.RawMessage{"provider-item-id": json.RawMessage(`"forced_item_123"`)}
	llm := &scriptedEventModel{responses: [][]*message.Message{
		{assistantToolCalls(&message.ToolCall{CallID: "call-1", Name: "work", Arguments: `{}`})},
		{final},
	}}
	manager := ram.NewRAMContextManager()
	agent := NewAgent(llm, 128, manager)
	agent.AddTool(ctx, common.NewDefaultTool(
		"work",
		"perform work",
		common.NewToolParameters(),
		func(*common.AgentContext, map[string]any) common.ToolResult {
			return common.NewDefaultToolResult("done")
		},
	))

	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "use the tool"},
		MaxStep:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	readAllEvents(t, ctx, eventStream)

	history := mustLoadHistory(t, ctx, manager, signature.ContextUID)
	stored := history[len(history)-1]
	if got := string(stored.Extra["provider-message-id"]); got != `"forced_123"` {
		t.Fatalf("provider message ID = %v, want forced_123", got)
	}
	if got := string(stored.Blocks[0].Provider["provider-item-id"]); got != `"forced_item_123"` {
		t.Fatalf("provider item ID = %v, want forced_item_123", got)
	}
}

func TestDoUsesContextUIDAsPromptCacheKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	llm := &scriptedEventModel{responses: [][]*message.Message{
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
	agent := NewAgent(&scriptedEventModel{responses: [][]*message.Message{
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
	agent := NewAgent(&scriptedEventModel{responses: [][]*message.Message{
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

	store := ram.NewRAMStorage()
	manager := contextmgr.NewManager(store)
	agent := NewAgent(&scriptedEventModel{streamErr: errors.New("provider unavailable")}, 128, manager)
	signature, eventStream, err := agent.Do(ctx, &common.AgentDoArgs{
		UserInput: common.AgentUserInput{Text: "fail"},
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
		delegate: ram.NewRAMStorage(),
		err:      errors.New("state storage unavailable"),
	}
	manager := contextmgr.NewManager(store)
	agent := NewAgent(&scriptedEventModel{responses: [][]*message.Message{{
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
		&message.ToolCall{CallID: "slow-call", Name: "slow", Arguments: `{}`},
		&message.ToolCall{CallID: "fast-call", Name: "fast", Arguments: `{}`},
	)
	llm := &scriptedEventModel{responses: [][]*message.Message{
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

	started := eventsByType[common.ToolCallStartedEvent](events)
	if len(started) != 2 {
		t.Fatalf("started tool events = %+v", started)
	}
	startedNames := map[string]bool{started[0].Name: true, started[1].Name: true}
	if !startedNames["slow"] || !startedNames["fast"] {
		t.Fatalf("started tool names = %+v", startedNames)
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
		for _, block := range message.Blocks {
			if block != nil && block.ToolResult != nil {
				toolResultNames = append(toolResultNames, block.ToolResult.Name)
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

	llm := &scriptedEventModel{responses: [][]*message.Message{{assistantToolCalls(
		&message.ToolCall{CallID: "approval-call", Name: "approval", Arguments: `{}`},
	)}}}
	store := ram.NewRAMStorage()
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

	llm := &scriptedEventModel{responses: [][]*message.Message{{assistantToolCalls(
		&message.ToolCall{CallID: "blocking-call", Name: "blocking", Arguments: `{}`},
	)}}}
	store := ram.NewRAMStorage()
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

	llm := &scriptedEventModel{responses: [][]*message.Message{
		{assistantToolCalls(&message.ToolCall{CallID: "nil-call", Name: "nil_result", Arguments: `{}`})},
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
) []*message.Message {
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
	store contextmgr.Storage,
	signature common.RunSignature,
	want contextmgr.RunOutcome,
) {
	t.Helper()
	keys, err := store.List(ctx, "objects:runs:")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		payload, err := store.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		var node struct {
			ContextUID common.ContextUID      `json:"context_uid"`
			RunUID     common.RunUID          `json:"run_uid"`
			Snapshot   contextmgr.RunSnapshot `json:"snapshot"`
		}
		if err := json.Unmarshal(payload, &node); err != nil {
			t.Fatal(err)
		}
		if node.ContextUID == signature.ContextUID && node.RunUID == signature.RunUID {
			if node.Snapshot.Outcome != want {
				t.Fatalf("run outcome = %q, want %q", node.Snapshot.Outcome, want)
			}
			return
		}
	}
	t.Fatalf("run snapshot not found for %+v", signature)
}

func messageWithUsage(msg *message.Message, prompt, cached, completion int) *message.Message {
	msg.Meta = &message.ResponseMeta{Usage: &message.Usage{PromptTokens: prompt, CachedTokens: cached, CompletionTokens: completion}}
	return msg
}

func assistantToolCalls(calls ...*message.ToolCall) *message.Message {
	blocks := make([]*message.ContentBlock, 0, len(calls))
	for _, call := range calls {
		blocks = append(blocks, &message.ContentBlock{Kind: message.BlockToolCall, ToolCall: call})
	}
	return &message.Message{Role: message.RoleAssistant, Blocks: blocks}
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

func historyContainsText(messages []*message.Message, value string) bool {
	for _, message := range messages {
		if strings.Contains(message.PlainText(), value) {
			return true
		}
	}
	return false
}

func messageInputContains(messages []*message.Message, value string) bool {
	for _, message := range messages {
		for _, block := range message.Blocks {
			if block == nil || block.ToolResult == nil {
				continue
			}
			for _, content := range block.ToolResult.Content {
				if content != nil && content.Text != nil && strings.Contains(content.Text.Text, value) {
					return true
				}
			}
		}
	}
	return false
}
