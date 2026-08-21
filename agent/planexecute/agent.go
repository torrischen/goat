package planexecute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	filectx "github.com/torrischen/goat/agent/contextmgr/file"
	"github.com/torrischen/goat/agent/react"
	"github.com/torrischen/goat/streaming"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

const settleRunTimeout = 5 * time.Second

var (
	_              common.Agent = (*Agent)(nil)
	errInterrupted              = errors.New("step executor interrupted")
)

// Agent plans a task, delegates each dependency-ready step to a React agent,
// and synthesizes one final answer in the parent conversation.
type Agent struct {
	planner  model.AgenticModel
	executor *react.Agent
	manager  *contextmgr.Manager
	config   Config
}

func NewAgent(
	planner model.AgenticModel,
	executor *react.Agent,
	manager *contextmgr.Manager,
	config *Config,
) *Agent {
	if manager == nil {
		manager = filectx.NewFileContextManager("")
	}
	resolved := Config{}.normalized()
	if config != nil {
		resolved = config.normalized()
	}
	return &Agent{planner: planner, executor: executor, manager: manager, config: resolved}
}

func (a *Agent) AddTools(ctx context.Context, tools ...common.Tool) {
	if a != nil && a.executor != nil {
		a.executor.AddTools(ctx, tools...)
	}
}

func (a *Agent) AddTool(ctx context.Context, tool common.Tool) {
	a.AddTools(ctx, tool)
}

func (a *Agent) EnableSkills() {
	if a != nil && a.executor != nil {
		a.executor.EnableSkills()
	}
}

func (a *Agent) Fork(ctx context.Context, args *common.AgentForkArgs) (common.ContextUID, error) {
	if args == nil {
		return "", fmt.Errorf("agent fork args is nil")
	}
	uid, err := a.manager.Fork(ctx, args.From)
	if err != nil {
		return "", fmt.Errorf("fork context: %w", err)
	}
	return uid, nil
}

func (a *Agent) Steer(ctx context.Context, args *common.AgentSteerArgs) error {
	if args == nil || args.ContextUID == "" || len(args.UserInputs) == 0 {
		return fmt.Errorf("invalid agent steer args")
	}
	messages := make([]*schema.AgenticMessage, 0, len(args.UserInputs))
	for _, input := range args.UserInputs {
		messages = append(messages, userInputMessage(input))
	}
	if err := a.manager.Enqueue(ctx, args.ContextUID, messages); err != nil {
		return fmt.Errorf("enqueue steering messages: %w", err)
	}
	return nil
}

func (a *Agent) Do(
	ctx context.Context,
	args *common.AgentDoArgs,
	opts ...model.Option,
) (common.RunSignature, streaming.Stream[common.AgentEvent], error) {
	args = cloneDoArgs(args)
	if args == nil {
		return common.RunSignature{}, nil, fmt.Errorf("agent do args is nil")
	}
	if a == nil || a.planner == nil {
		return common.RunSignature{}, nil, fmt.Errorf("planner model is nil")
	}
	if a.executor == nil {
		return common.RunSignature{}, nil, fmt.Errorf("React executor is nil")
	}

	signature, messages, err := a.startRun(ctx, args)
	if err != nil {
		return common.RunSignature{}, nil, err
	}
	events := streaming.NewStream[common.AgentEvent](64)
	maxSteps := a.config.MaxPlanSteps
	if args.MaxStep > 0 {
		maxSteps = args.MaxStep
	}
	if err := events.WriteWithContext(ctx, common.RunStartedEvent{Signature: signature, MaxStep: maxSteps}); err != nil {
		_ = events.Close()
		return common.RunSignature{}, nil, err
	}
	go a.run(ctx, signature, messages, args, maxSteps, events, opts...)
	return signature, events, nil
}

type runStats struct {
	usage      common.AgentUsage
	iterations int
	toolCalls  int
}

func (a *Agent) run(
	ctx context.Context,
	signature common.RunSignature,
	messages []*schema.AgenticMessage,
	args *common.AgentDoArgs,
	maxPlanSteps int,
	events streaming.Stream[common.AgentEvent],
	opts ...model.Option,
) {
	defer events.Close()
	stats := &runStats{}
	completed := make(map[string]StepResult)
	results := make([]StepResult, 0)
	childContextUID := common.ContextUID("")

	plan, usage, err := a.makePlan(ctx, messages, nil, maxPlanSteps, opts...)
	stats.usage.Add(usage)
	stats.iterations++
	if err == nil {
		err = events.WriteWithContext(ctx, PlanCreatedEvent{Plan: *plan})
	}

	replans := 0
	for err == nil {
		step, ok := nextStep(*plan, completed)
		if !ok {
			if planCompleted(*plan, completed) {
				break
			}
			err = fmt.Errorf("plan has unfinished steps but none are dependency-ready")
			break
		}
		if err = events.WriteWithContext(ctx, StepStartedEvent{Step: step}); err != nil {
			break
		}

		var result StepResult
		var childUsage *common.AgentUsage
		var iterations, toolCalls int
		result, childContextUID, childUsage, iterations, toolCalls, err = a.executeStep(
			ctx, childContextUID, *plan, step, results, args, events, opts...,
		)
		stats.usage.Add(childUsage)
		stats.iterations += iterations
		stats.toolCalls += toolCalls
		if err != nil {
			break
		}
		completed[step.ID] = result
		results = append(results, result)
		if err = events.WriteWithContext(ctx, StepCompletedEvent{Step: step, Result: result}); err != nil {
			break
		}

		commit, commitErr := a.manager.CommitTurn(ctx, signature.ContextUID, nil)
		if commitErr != nil {
			err = fmt.Errorf("apply steering messages: %w", commitErr)
			break
		}
		if len(commit.AppliedPendingMessages) == 0 {
			continue
		}
		messages = append(messages, commit.AppliedPendingMessages...)
		if replans >= a.config.MaxReplans {
			continue
		}
		replans++
		plan, usage, err = a.makePlan(ctx, messages, results, maxPlanSteps, opts...)
		stats.usage.Add(usage)
		stats.iterations++
		if err == nil {
			err = events.WriteWithContext(ctx, PlanRevisedEvent{Plan: *plan, Reason: "user steering"})
		}
	}

	if err == nil {
		var answer string
		var usage *common.AgentUsage
		answer, usage, err = a.finalAnswer(ctx, messages, plan, results, args.SpecialRequirements, events, opts...)
		stats.usage.Add(usage)
		stats.iterations++
		if err == nil {
			final := common.AssistantTextMessage(answer)
			err = a.manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
				Signature: signature, Outcome: contextmgr.RunOutcomeCompleted, FinalMessage: final,
			})
			if err == nil {
				err = events.WriteWithContext(ctx, common.FinalAnswerCompletedEvent{Answer: answer})
			}
		}
	}

	if err == nil {
		_ = events.WriteWithTimeout(common.RunCompletedEvent{
			Usage: stats.usage.Clone(), IterationsUsed: stats.iterations, ToolCalls: stats.toolCalls,
		}, time.Second)
		return
	}

	outcome := contextmgr.RunOutcomeFailed
	var terminal common.AgentEvent
	switch {
	case errors.Is(err, errInterrupted):
		outcome = contextmgr.RunOutcomeInterrupted
		terminal = common.RunInterruptedEvent{Usage: stats.usage.Clone(), IterationsUsed: stats.iterations, Reason: err.Error()}
	case ctx.Err() != nil:
		outcome = contextmgr.RunOutcomeCanceled
		terminal = common.RunCanceledEvent{Usage: stats.usage.Clone(), IterationsUsed: stats.iterations, Reason: ctx.Err().Error()}
	default:
		terminal = common.RunFailedEvent{Usage: stats.usage.Clone(), IterationsUsed: stats.iterations, Operation: "plan and execute", Error: err.Error()}
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleRunTimeout)
	settleErr := a.manager.SettleRun(settleCtx, &contextmgr.SettleRunArgs{Signature: signature, Outcome: outcome})
	cancel()
	if settleErr != nil {
		terminal = common.RunFailedEvent{Usage: stats.usage.Clone(), IterationsUsed: stats.iterations, Operation: "settle run", Error: settleErr.Error()}
	}
	_ = events.WriteWithTimeout(terminal, time.Second)
}

func planCompleted(plan Plan, completed map[string]StepResult) bool {
	for _, step := range plan.Steps {
		if _, done := completed[step.ID]; !done {
			return false
		}
	}
	return true
}

func (a *Agent) startRun(
	ctx context.Context,
	args *common.AgentDoArgs,
) (common.RunSignature, []*schema.AgenticMessage, error) {
	system := schema.SystemAgenticMessage("You are a plan-and-execute agent. Produce one final answer after all planned steps finish.")
	var (
		uid      common.ContextUID
		messages []*schema.AgenticMessage
		err      error
	)
	if args.ContextUID == "" {
		messages = []*schema.AgenticMessage{system}
		uid, err = a.manager.Create(ctx, messages)
	} else {
		uid = args.ContextUID
		messages, err = a.manager.Load(ctx, uid)
		if err == nil {
			if len(messages) == 0 {
				messages = []*schema.AgenticMessage{system}
			} else if messages[0].Role == schema.AgenticRoleTypeSystem {
				messages[0] = system
			} else {
				messages = append([]*schema.AgenticMessage{system}, messages...)
			}
			err = a.manager.Replace(ctx, uid, messages)
		}
	}
	if err != nil {
		return common.RunSignature{}, nil, fmt.Errorf("initialize conversation: %w", err)
	}

	commit, err := a.manager.CommitTurn(ctx, uid, nil)
	if err != nil {
		return common.RunSignature{}, nil, fmt.Errorf("apply pending steering messages: %w", err)
	}
	messages = append(messages, commit.AppliedPendingMessages...)
	signature := common.RunSignature{ContextUID: uid, RunUID: common.NewRunUID()}
	user := userInputMessage(args.UserInput)
	common.MarkRunStart(user, signature.RunUID)
	if err := a.manager.Append(ctx, uid, user); err != nil {
		return common.RunSignature{}, nil, fmt.Errorf("store user input: %w", err)
	}
	messages = append(messages, user)
	return signature, messages, nil
}

func (a *Agent) makePlan(
	ctx context.Context,
	messages []*schema.AgenticMessage,
	completed []StepResult,
	maxSteps int,
	opts ...model.Option,
) (*Plan, *common.AgentUsage, error) {
	prompt := fmt.Sprintf(`Create an execution plan for the user's request.
Return JSON only, with exactly this shape:
{"goal":"...","steps":[{"id":"step_1","description":"...","dependencies":[]}]}
Use at most %d steps. IDs must be unique. Dependencies must reference step IDs.
Each step will be executed by a tool-using React agent.`, maxSteps)
	known := make(map[string]StepResult, len(completed))
	if len(completed) > 0 {
		data, _ := json.Marshal(completed)
		prompt += "\nThe following steps are already completed. Plan only the remaining work and do not reuse their IDs:\n" + string(data)
		for _, result := range completed {
			known[result.StepID] = result
		}
	}
	input := common.CloneAgenticMessages(messages)
	input = append(input, schema.UserAgenticMessage(prompt))
	callOpts := append([]model.Option{}, opts...)
	callOpts = append(callOpts, model.WithTools(nil))
	response, err := a.planner.Generate(ctx, input, callOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("generate plan: %w", err)
	}
	usage := messageUsage(response)
	var plan Plan
	if err := decodeJSONResponse(messageText(response), &plan); err != nil {
		return nil, usage, fmt.Errorf("decode plan: %w", err)
	}
	if err := validatePlan(&plan, maxSteps, known); err != nil {
		return nil, usage, err
	}
	return &plan, usage, nil
}

func (a *Agent) executeStep(
	ctx context.Context,
	childContextUID common.ContextUID,
	plan Plan,
	step Step,
	completed []StepResult,
	args *common.AgentDoArgs,
	events streaming.Stream[common.AgentEvent],
	opts ...model.Option,
) (StepResult, common.ContextUID, *common.AgentUsage, int, int, error) {
	data, _ := json.Marshal(completed)
	prompt := fmt.Sprintf(
		"Overall goal: %s\nCurrent step (%s): %s\nCompleted step results: %s\nExecute only the current step. Use tools when needed, then report the concrete result.",
		plan.Goal, step.ID, step.Description, data,
	)
	childArgs := &common.AgentDoArgs{
		ContextUID:            childContextUID,
		UserInput:             common.AgentUserInput{Text: prompt},
		SpecialRequirements:   append([]string(nil), args.SpecialRequirements...),
		Compress:              args.Compress,
		CompressionOptions:    args.CompressionOptions,
		ContextMeta:           args.ContextMeta,
		MaxStep:               a.config.ExecutorMaxSteps,
		SkillsDir:             args.SkillsDir,
		SkillUsageInstruction: args.SkillUsageInstruction,
		ToolExecutionOptions:  args.ToolExecutionOptions,
	}
	signature, childEvents, err := a.executor.Do(ctx, childArgs, opts...)
	if err != nil {
		return StepResult{}, childContextUID, nil, 0, 0, fmt.Errorf("start step %q: %w", step.ID, err)
	}
	answer := ""
	var usage *common.AgentUsage
	iterations, toolCalls := 0, 0
	terminalSeen := false
	for {
		event, readErr := childEvents.ReadWithContext(ctx)
		if errors.Is(readErr, streaming.ErrStreamClosed) {
			break
		}
		if readErr != nil {
			return StepResult{}, signature.ContextUID, usage, iterations, toolCalls, readErr
		}
		switch typed := event.(type) {
		case common.ReasoningDeltaEvent, common.AssistantTextDeltaEvent,
			common.ToolCallStartedEvent, common.ToolCallCompletedEvent, common.ToolCallFailedEvent:
			if err := events.WriteWithContext(ctx, typed); err != nil {
				return StepResult{}, signature.ContextUID, usage, iterations, toolCalls, err
			}
		case common.FinalAnswerCompletedEvent:
			answer = typed.Answer
		case common.RunCompletedEvent:
			terminalSeen = true
			usage, iterations, toolCalls = typed.Usage, typed.IterationsUsed, typed.ToolCalls
		case common.RunInterruptedEvent:
			return StepResult{}, signature.ContextUID, typed.Usage, typed.IterationsUsed, toolCalls, fmt.Errorf("%w: %s", errInterrupted, typed.Reason)
		case common.RunCanceledEvent:
			return StepResult{}, signature.ContextUID, typed.Usage, typed.IterationsUsed, toolCalls, fmt.Errorf("step %q canceled: %s", step.ID, typed.Reason)
		case common.RunFailedEvent:
			return StepResult{}, signature.ContextUID, typed.Usage, typed.IterationsUsed, toolCalls, fmt.Errorf("step %q failed during %s: %s", step.ID, typed.Operation, typed.Error)
		}
	}
	if !terminalSeen {
		return StepResult{}, signature.ContextUID, usage, iterations, toolCalls, fmt.Errorf("step %q event stream closed without completion", step.ID)
	}
	return StepResult{StepID: step.ID, Output: answer}, signature.ContextUID, usage, iterations, toolCalls, nil
}

func (a *Agent) finalAnswer(
	ctx context.Context,
	messages []*schema.AgenticMessage,
	plan *Plan,
	results []StepResult,
	requirements []string,
	events streaming.Stream[common.AgentEvent],
	opts ...model.Option,
) (string, *common.AgentUsage, error) {
	data, _ := json.Marshal(struct {
		Plan    *Plan        `json:"plan"`
		Results []StepResult `json:"results"`
	}{plan, results})
	prompt := "Answer the user's request using the completed plan results below. Do not mention internal orchestration unless relevant.\n" + string(data)
	if len(requirements) > 0 {
		prompt += "\nSpecial requirements:\n- " + strings.Join(requirements, "\n- ")
	}
	input := common.CloneAgenticMessages(messages)
	input = append(input, schema.UserAgenticMessage(prompt))
	callOpts := append([]model.Option{}, opts...)
	callOpts = append(callOpts, model.WithTools(nil))
	reader, err := a.planner.Stream(ctx, input, callOpts...)
	if err != nil {
		return "", nil, fmt.Errorf("stream final answer: %w", err)
	}
	defer reader.Close()
	chunks := make([]*schema.AgenticMessage, 0)
	for {
		chunk, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return "", nil, recvErr
		}
		if chunk == nil {
			continue
		}
		chunks = append(chunks, chunk)
		if delta := messageReasoning(chunk); delta != "" {
			if err := events.WriteWithContext(ctx, common.ReasoningDeltaEvent{Delta: delta}); err != nil {
				return "", nil, err
			}
		}
		if delta := messageText(chunk); delta != "" {
			if err := events.WriteWithContext(ctx, common.AssistantTextDeltaEvent{Delta: delta}); err != nil {
				return "", nil, err
			}
		}
	}
	if len(chunks) == 0 {
		return "", nil, fmt.Errorf("final model stream returned no messages")
	}
	response, err := schema.ConcatAgenticMessages(chunks)
	if err != nil {
		return "", nil, err
	}
	usage := messageUsage(response)
	return messageText(response), usage, nil
}

func userInputMessage(input common.AgentUserInput) *schema.AgenticMessage {
	blocks := make([]*schema.ContentBlock, 0, len(input.Images)+1)
	if input.Text != "" {
		blocks = append(blocks, common.TextBlock(input.Text))
	}
	blocks = append(blocks, input.Images...)
	return &schema.AgenticMessage{Role: schema.AgenticRoleTypeUser, ContentBlocks: blocks}
}

func cloneDoArgs(args *common.AgentDoArgs) *common.AgentDoArgs {
	if args == nil {
		return nil
	}
	clone := *args
	clone.UserInput.Images = append([]*schema.ContentBlock(nil), args.UserInput.Images...)
	clone.SpecialRequirements = append([]string(nil), args.SpecialRequirements...)
	if args.ContextMeta != nil {
		clone.ContextMeta = make(map[common.AgentDoMetaKey]any, len(args.ContextMeta))
		for key, value := range args.ContextMeta {
			clone.ContextMeta[key] = value
		}
	}
	if args.ToolExecutionOptions != nil {
		options := *args.ToolExecutionOptions
		clone.ToolExecutionOptions = &options
	}
	return &clone
}

func decodeJSONResponse(text string, target any) error {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) >= 3 && strings.HasPrefix(lines[0], "```") && strings.TrimSpace(lines[len(lines)-1]) == "```" {
			text = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func messageText(message *schema.AgenticMessage) string {
	if message == nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range message.ContentBlocks {
		if block == nil {
			continue
		}
		if block.AssistantGenText != nil {
			builder.WriteString(block.AssistantGenText.Text)
		}
		if block.UserInputText != nil {
			builder.WriteString(block.UserInputText.Text)
		}
	}
	return builder.String()
}

func messageReasoning(message *schema.AgenticMessage) string {
	if message == nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range message.ContentBlocks {
		if block != nil && block.Reasoning != nil {
			builder.WriteString(block.Reasoning.Text)
		}
	}
	return builder.String()
}

func messageUsage(message *schema.AgenticMessage) *common.AgentUsage {
	if message == nil || message.ResponseMeta == nil || message.ResponseMeta.TokenUsage == nil {
		return nil
	}
	usage := message.ResponseMeta.TokenUsage
	return common.NewAgentUsage(usage.PromptTokens, usage.PromptTokenDetails.CachedTokens, usage.CompletionTokens)
}
