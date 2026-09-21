package react

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/agent/contextmgr/ram"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/agent/react/compression"
	"github.com/torrischen/goat/agent/toolplugin"
	"github.com/torrischen/goat/agent/tools"
	"github.com/torrischen/goat/llm"
	"github.com/torrischen/goat/streaming"
	"github.com/torrischen/goat/util/logging"

	"github.com/bytedance/sonic"
	"github.com/mark3labs/mcp-go/client"
)

var _ common.Agent = (*Agent)(nil)

const settleRunTimeout = 5 * time.Second

type Agent struct {
	mu              *sync.RWMutex
	contextManager  *contextmgr.Manager
	skillsEnabled   bool
	llmClient       llm.Client
	tools           []common.Tool
	toolsMap        map[string]common.Tool
	modelMaxTokensK int
	callbacks       *AgentCallbacks
}

// NewAgent creates a tool-calling agent backed by an llm.Client.
//
// The agent operates on goat's provider-neutral message model. Provider-specific
// translation is handled by the llm.Client implementation.
//
// The OpenAI provider implements llm.Client directly using the Responses API:
//
//	import (
//		"github.com/torrischen/goat/agent/react"
//		"github.com/torrischen/goat/llm"
//		openaiprovider "github.com/torrischen/goat/llm/provider/openai"
//	)
//
//	client := openaiprovider.New(llm.WithModel("gpt-5.2"), llm.WithAPIKey("sk-..."))
//	agent := react.NewAgent(client, 128, nil)
func NewAgent(
	client llm.Client,
	modelMaxTokensK int,
	manager *contextmgr.Manager,
) *Agent {
	a := &Agent{
		mu:              &sync.RWMutex{},
		contextManager:  manager,
		llmClient:       client,
		modelMaxTokensK: modelMaxTokensK,
		toolsMap:        make(map[string]common.Tool),
		callbacks:       nil,
	}

	if a.contextManager == nil {
		a.contextManager = ram.NewRAMContextManager()
	}

	a.AddTools(
		context.TODO(),
		tools.GeneratePlan(),
		tools.UpdatePlan(),
	)

	return a
}

// SetCallbacks sets the agent's callback functions
func (a *Agent) SetCallbacks(callbacks *AgentCallbacks) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.callbacks = cloneCallbacks(callbacks)
}

// GetCallbacks gets a copy of the agent's callback functions
func (a *Agent) GetCallbacks() *AgentCallbacks {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return cloneCallbacks(a.callbacks)
}

func (a *Agent) AddTools(ctx context.Context, tool ...common.Tool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, t := range tool {
		if t == nil {
			continue
		}

		originalName := t.Name()
		sanitized := common.SanitizeToolName(originalName)
		if sanitized == "" {
			sanitized = "tool"
		}

		finalName := sanitized
		if _, exists := a.toolsMap[finalName]; exists {
			for i := 2; ; i++ {
				candidate := fmt.Sprintf("%s_%d", sanitized, i)
				if _, exists := a.toolsMap[candidate]; !exists {
					finalName = candidate
					break
				}
			}
		}

		if finalName != originalName {
			logging.Warnf("Tool name %q sanitized to %q for LLM compatibility", originalName, finalName)
		}

		wrapped := common.WrapToolName(t, finalName)
		a.tools = append(a.tools, wrapped)
		a.toolsMap[finalName] = wrapped
	}
}

func (a *Agent) AddTool(ctx context.Context, tool common.Tool) {
	a.AddTools(ctx, tool)
}

// EnableSkills enables skill discovery for subsequent runs. Skill headers are
// loaded dynamically by the agent through the list_available_skills tool.
func (a *Agent) EnableSkills() {
	a.mu.Lock()
	alreadyEnabled := a.skillsEnabled
	a.skillsEnabled = true
	a.mu.Unlock()

	if alreadyEnabled {
		return
	}
	a.AddTools(
		context.TODO(),
		tools.ListAvailableSkills(),
		tools.LoadSkills(),
		tools.ReadSpecifiedFileInSkill(),
	)
}

func (a *Agent) RegisterMCPTools(ctx context.Context, cli client.MCPClient) error {
	if ctx == nil {
		ctx = context.Background()
	}

	tools, err := common.ListMCPTools(ctx, cli)
	if err != nil {
		return err
	}

	for _, t := range tools {
		a.AddTool(ctx, t)
	}

	return nil
}

func (a *Agent) LoadSharedLibPluginTools(ctx context.Context, pluginDir ...string) error {
	plugins := make([]toolplugin.ToolPlugin, 0)
	for _, dir := range pluginDir {
		ps, err := toolplugin.LoadPluginsFromSharedLib(dir)
		if err != nil {
			logging.Errorf("LoadSharedLibPluginTools error from dir %s: %v", dir, err)
			return err
		}
		plugins = append(plugins, ps...)
	}

	for _, p := range plugins {
		a.AddTools(ctx, common.NewDefaultTool(
			p.Name(),
			p.Description(),
			p.Parameters(),
			p.Execute,
		))
	}

	return nil
}

func (a *Agent) LoadRPCPluginTools(ctx context.Context, address ...string) ([]io.Closer, error) {
	plugins := make([]toolplugin.ToolPlugin, 0, len(address))
	resources := make([]io.Closer, 0, len(address))
	rollback := func() {
		for i := len(resources) - 1; i >= 0; i-- {
			if err := resources[i].Close(); err != nil {
				logging.Errorf("LoadRPCPluginTools close error: %v", err)
			}
		}
	}

	for _, addr := range address {
		ps, closer, err := toolplugin.LoadPluginsFromRPC(addr)
		if err != nil {
			rollback()
			logging.Errorf("LoadRPCPluginTools error from address %s: %v", addr, err)
			return nil, err
		}
		resources = append(resources, closer)

		if err := ps.Ping(); err != nil {
			rollback()
			logging.Errorf("LoadRPCPluginTools ping error for plugin %s: %v", ps.Name(), err)
			return nil, err
		}

		plugins = append(plugins, ps)
	}

	for _, p := range plugins {
		a.AddTools(ctx, common.NewDefaultTool(
			p.Name(),
			p.Description(),
			p.Parameters(),
			p.Execute,
		))
	}

	return resources, nil
}

func (a *Agent) buildSystemPrompt(
	_ *common.AgentContext,
	planMode bool,
	specialRequirements []string,
	skillUsageInstruction string,
	planUsageInstruction string,
) string {
	a.mu.RLock()
	skillsEnabled := a.skillsEnabled
	a.mu.RUnlock()

	return renderReactSystemPrompt(
		planMode,
		skillsEnabled,
		specialRequirements,
		skillUsageInstruction,
		planUsageInstruction,
	)
}

func appendConversationMessage(
	ctx context.Context,
	manager *contextmgr.Manager,
	contextUID common.ContextUID,
	messages *[]*message.Message,
	msg *message.Message,
) error {
	*messages = append(*messages, msg)
	return manager.Append(ctx, contextUID, msg)
}

func commitConversationTurn(
	ctx context.Context,
	manager *contextmgr.Manager,
	contextUID common.ContextUID,
	messages *[]*message.Message,
	turnMessages ...*message.Message,
) ([]*message.Message, error) {
	result, err := manager.CommitTurn(ctx, contextUID, turnMessages)
	if err != nil {
		return nil, err
	}

	*messages = append(*messages, turnMessages...)
	*messages = append(*messages, result.AppliedPendingMessages...)
	return result.AppliedPendingMessages, nil
}

func settleConversationFinal(
	ctx context.Context,
	manager *contextmgr.Manager,
	signature common.RunSignature,
	messages *[]*message.Message,
	msg *message.Message,
) error {
	if err := manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
		Signature:    signature,
		Outcome:      contextmgr.RunOutcomeCompleted,
		FinalMessage: msg,
	}); err != nil {
		return err
	}
	*messages = append(*messages, msg)
	return nil
}

func addRunUsage(total *common.AgentUsage, usage *common.AgentUsage) {
	if total != nil {
		total.Add(usage)
	}
}

type preparedConversationContext struct {
	messages             []*message.Message
	usage                *common.AgentUsage
	compressed           bool
	originalMessageCount int
}

func agenticMessagesChanged(before, after []*message.Message) bool {
	if len(before) != len(after) {
		return true
	}
	for index := range before {
		if before[index] != after[index] {
			return true
		}
	}
	return false
}

func (a *Agent) prepareConversationContext(
	ctx *common.AgentContext,
	contextUID common.ContextUID,
	messages []*message.Message,
	compress bool,
	options common.CompressionOptions,
	opts ...llm.Option,
) (*preparedConversationContext, error) {
	prepared := &preparedConversationContext{
		messages:             messages,
		originalMessageCount: len(messages),
	}
	if !compress || !compression.ShouldCompress(messages, a.modelMaxTokensK) {
		return prepared, nil
	}

	compressionOpts := append([]llm.Option{}, opts...)
	compressionOpts = append(compressionOpts, llm.WithPromptCacheKey("compression:"+contextUID.String()))
	compressedMessages, promptTokens, completionTokens, cachedTokens, err := compression.Compress(
		ctx,
		a.llmClient,
		messages,
		options,
		compressionOpts...,
	)
	if err != nil {
		// Compression is best-effort. A transient compression-model or
		// response-format failure must not prevent the normal model call.
		logging.Warnf("Agent.prepareConversationContext: compression failed, continuing with original context: %v", err)
		return prepared, nil
	}
	if !agenticMessagesChanged(messages, compressedMessages) {
		// Some strategies legitimately have nothing discardable to compact.
		return prepared, nil
	}

	if err := a.contextManager.Replace(ctx, contextUID, compressedMessages); err != nil {
		return nil, fmt.Errorf("replace compressed context: %w", err)
	}

	prepared.messages = compressedMessages
	prepared.usage = common.NewAgentUsage(promptTokens, cachedTokens, completionTokens)
	prepared.compressed = true
	return prepared, nil
}

func snapshotRunUsage(total *common.AgentUsage) *common.AgentUsage {
	if total == nil {
		return nil
	}
	return common.NewAgentUsage(total.PromptTokens, total.CachedTokens, total.CompletionTokens)
}

func cloneToolArguments(arguments map[string]any) map[string]any {
	if arguments == nil {
		return map[string]any{}
	}
	data, err := sonic.Marshal(arguments)
	if err == nil {
		var clone map[string]any
		if err := sonic.Unmarshal(data, &clone); err == nil && clone != nil {
			return clone
		}
	}
	return maps.Clone(arguments)
}

var errAgentLoopInterrupted = errors.New("agent loop interrupted")

type runOperationError struct {
	operation string
	err       error
}

func (e *runOperationError) Error() string {
	return fmt.Sprintf("%s: %v", e.operation, e.err)
}

func (e *runOperationError) Unwrap() error {
	return e.err
}

func operationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &runOperationError{operation: operation, err: err}
}

func cloneAgentDoArgs(args *common.AgentDoArgs) *common.AgentDoArgs {
	if args == nil {
		return nil
	}

	clone := *args
	clone.UserInput.Images = append([]*message.ContentBlock(nil), args.UserInput.Images...)
	clone.SpecialRequirements = append([]string(nil), args.SpecialRequirements...)

	if args.ContextMeta != nil {
		clone.ContextMeta = make(map[common.AgentDoMetaKey]any, len(args.ContextMeta))
		for k, v := range args.ContextMeta {
			clone.ContextMeta[k] = v
		}
	}
	if args.ToolExecutionOptions != nil {
		options := *args.ToolExecutionOptions
		clone.ToolExecutionOptions = &options
	}
	if args.FinalAnswerWebhook != nil {
		webhook := *args.FinalAnswerWebhook
		if args.FinalAnswerWebhook.Headers != nil {
			webhook.Headers = make(map[string]string, len(args.FinalAnswerWebhook.Headers))
			maps.Copy(webhook.Headers, args.FinalAnswerWebhook.Headers)
		}
		clone.FinalAnswerWebhook = &webhook
	}

	return &clone
}

// Fork creates a new conversation from a settled run without starting the
// agent loop. The context manager owns the immutable snapshot and atomic copy.
func (a *Agent) Fork(
	ctx context.Context,
	args *common.AgentForkArgs,
) (common.ContextUID, error) {
	if args == nil {
		return "", fmt.Errorf("agent fork args is nil")
	}
	if args.From.ContextUID == "" {
		return "", fmt.Errorf("agent fork context UID is empty: %w", contextmgr.ErrInvalidRunSignature)
	}
	if args.From.RunUID == "" {
		return "", fmt.Errorf("agent fork run UID is empty: %w", contextmgr.ErrInvalidRunSignature)
	}

	contextUID, err := a.contextManager.Fork(ctx, args.From)
	if err != nil {
		return "", fmt.Errorf("fork context: %w", err)
	}
	return contextUID, nil
}

// Steer queues user messages in the conversation's context-manager-backed
// pending inbox. They are applied after the next complete non-final turn. A
// final answer discards pending messages and closes the inbox until the next Do
// appends a new user input.
func (a *Agent) Steer(ctx context.Context, args *common.AgentSteerArgs) error {
	if args == nil {
		return fmt.Errorf("agent steer args is nil")
	}
	if args.ContextUID == "" {
		return fmt.Errorf("agent steer context UID is empty")
	}
	if len(args.UserInputs) == 0 {
		return fmt.Errorf("agent steer user inputs are empty")
	}

	messages := make([]*message.Message, 0, len(args.UserInputs))
	for _, input := range args.UserInputs {
		input.Images = append([]*message.ContentBlock(nil), input.Images...)
		messages = append(messages, userInputMessage(input))
	}

	if err := a.contextManager.Enqueue(ctx, args.ContextUID, messages); err != nil {
		return fmt.Errorf("enqueue steering messages: %w", err)
	}
	return nil
}

func (a *Agent) Do(
	ctx context.Context,
	args *common.AgentDoArgs,
	opts ...llm.Option,
) (common.RunSignature, streaming.Stream[common.AgentEvent], error) {
	args = cloneAgentDoArgs(args)
	if args == nil {
		return common.RunSignature{}, nil, fmt.Errorf("agent do args is nil")
	}

	actx := common.NewAgentContext(ctx)
	for k, v := range args.ContextMeta {
		actx.SetMeta(k, v)
	}
	args.SkillsDir = strings.TrimSpace(args.SkillsDir)
	if args.SkillsDir == "" {
		args.SkillsDir = common.SkillDefaultFolder
	}
	actx.SetMeta(common.InternalToolSkillsDirMetaKey, args.SkillsDir)

	if args.MaxStep <= 0 {
		args.MaxStep = 8
	}
	maxStep := args.MaxStep

	var contextUID common.ContextUID
	var messages []*message.Message

	systemPrompt := a.buildSystemPrompt(
		actx,
		args.EnablePlanning,
		args.SpecialRequirements,
		args.SkillUsageInstruction,
		args.PlanUsageInstruction,
	)

	// Initialize or restore conversation
	if args.ContextUID == "" {
		systemMessage := message.SystemMessage(systemPrompt)
		messages = []*message.Message{systemMessage}
		var err error
		contextUID, err = a.contextManager.Create(ctx, messages)
		if err != nil {
			return common.RunSignature{}, nil, fmt.Errorf("failed to create conversation: %w", err)
		}
	} else {
		// Continue existing conversation
		contextUID = args.ContextUID

		// Restore the managed conversation history.
		var err error
		messages, err = a.contextManager.Load(ctx, contextUID)
		if errors.Is(err, contextmgr.ErrContextNotFound) {
			systemMessage := message.SystemMessage(systemPrompt)
			if err := a.contextManager.CreateWithUID(ctx, contextUID, []*message.Message{systemMessage}); err != nil {
				return common.RunSignature{}, nil, fmt.Errorf("failed to create conversation %s: %w", contextUID, err)
			}
			messages = []*message.Message{systemMessage}
			logging.Infof("Agent.Do: initialized new conversation %s", contextUID)
		} else if err != nil {
			return common.RunSignature{}, nil, fmt.Errorf("failed to load conversation: %w", err)
		}
		if len(messages) == 0 {
			systemMessage := message.SystemMessage(systemPrompt)
			messages = []*message.Message{systemMessage}
			if err := a.contextManager.Replace(ctx, contextUID, messages); err != nil {
				return common.RunSignature{}, nil, fmt.Errorf("failed to store system message: %w", err)
			}
			logging.Infof("Agent.Do: initialized empty conversation %s", contextUID)
		} else {
			// Update system prompt only if content has changed to maximize prompt cache hits
			newHash := hashSystemPrompt(systemPrompt)
			needsUpdate := false

			if messages[0].Role == message.RoleSystem {
				// Compare hash to detect content changes
				oldText := extractSystemMessageText(messages[0])
				oldHash := hashSystemPrompt(oldText)

				if newHash != oldHash {
					// Content changed, replace system message
					messages[0] = message.SystemMessage(systemPrompt)
					needsUpdate = true
					logging.Infof("Agent.Do: system message updated for conversation %s (hash: %x -> %x)", contextUID, oldHash, newHash)
				} else {
					// Content unchanged, reuse existing message for better cache hits
					logging.Infof("Agent.Do: system message unchanged for conversation %s (hash: %x), reusing cached version", contextUID, newHash)
				}
			} else {
				// Insert system message at the beginning (for legacy conversations)
				messages = append([]*message.Message{message.SystemMessage(systemPrompt)}, messages...)
				needsUpdate = true
				logging.Infof("Agent.Do: system message inserted for conversation %s", contextUID)
			}

			// Update the managed context only if system message changed
			if needsUpdate {
				if err := a.contextManager.Replace(ctx, contextUID, messages); err != nil {
					return common.RunSignature{}, nil, fmt.Errorf("failed to update system message: %w", err)
				}
			}

			logging.Infof("Agent.Do: Restored %d messages from conversation %s", len(messages), contextUID)
		}
	}
	runSignature := common.RunSignature{
		ContextUID: contextUID,
		RunUID:     common.NewRunUID(),
	}
	actx.SetMeta(common.InternalToolContextUIDMetaKey, runSignature.ContextUID.String())
	actx.SetMeta(common.InternalToolRunUIDMetaKey, runSignature.RunUID.String())

	// Apply steering messages left by an interrupted or canceled run before
	// storing this run's explicit user input.
	appliedBeforeRun, err := commitConversationTurn(
		ctx,
		a.contextManager,
		contextUID,
		&messages,
	)
	if err != nil {
		return common.RunSignature{}, nil, fmt.Errorf("failed to apply pending steering messages: %w", err)
	}
	if len(appliedBeforeRun) > 0 {
		logging.Infof(
			"Agent.Do: applied %d pending steering messages before conversation %s resumed",
			len(appliedBeforeRun),
			contextUID,
		)
	}

	// Store this run's user input after any steering messages left by the
	// previous run.
	userMessage := userInputMessage(args.UserInput)
	common.MarkRunStart(userMessage, runSignature.RunUID)
	if err := appendConversationMessage(ctx, a.contextManager, contextUID, &messages, userMessage); err != nil {
		return common.RunSignature{}, nil, fmt.Errorf("failed to store user message: %w", err)
	}

	callOpts := append([]llm.Option{}, opts...)
	callOpts = append(callOpts, llm.WithPromptCacheKey("agent:"+contextUID.String()))
	agenticTools := a.convertToolsToAgenticFormat(args.EnablePlanning)
	if len(agenticTools) > 0 {
		callOpts = append(callOpts, llm.WithTools(agenticTools))
	}

	eventStream := streaming.NewStream[common.AgentEvent](64)
	if err := eventStream.WriteWithContext(ctx, common.RunStartedEvent{
		Signature: runSignature,
		MaxStep:   maxStep,
	}); err != nil {
		_ = eventStream.Close()
		return common.RunSignature{}, nil, fmt.Errorf("write run started event: %w", err)
	}

	// Callback: OnRunStart
	a.mu.RLock()
	cbs := a.callbacks
	a.mu.RUnlock()
	if cbs != nil && cbs.OnRunStart != nil {
		_ = safeCallback(actx, "OnRunStart", func() error {
			return cbs.OnRunStart(actx, &CallbackRunStartArgs{
				Signature:  runSignature,
				ContextUID: contextUID,
				MaxStep:    maxStep,
				UserInput:  args.UserInput.Text,
			})
		})
	}
	if len(appliedBeforeRun) > 0 {
		// Callback: OnSteeringApplied
		if cbs != nil && cbs.OnSteeringApplied != nil {
			_ = safeCallback(actx, "OnSteeringApplied", func() error {
				return cbs.OnSteeringApplied(actx, &CallbackSteeringAppliedArgs{
					Signature: runSignature,
					Count:     len(appliedBeforeRun),
					BeforeRun: true,
				})
			})
		}
	}

	run := &reactRun{
		agent:       a,
		parentCtx:   ctx,
		ctx:         actx,
		args:        args,
		callOpts:    callOpts,
		callbacks:   cbs,
		signature:   runSignature,
		contextUID:  contextUID,
		messages:    messages,
		eventStream: eventStream,
		maxStep:     maxStep,
		usage:       &common.AgentUsage{},
	}

	go run.execute()

	return runSignature, eventStream, nil
}
