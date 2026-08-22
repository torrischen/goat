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
	filectx "github.com/torrischen/goat/agent/contextmgr/file"
	"github.com/torrischen/goat/agent/react/compression"
	"github.com/torrischen/goat/agent/toolplugin"
	"github.com/torrischen/goat/agent/tools"
	"github.com/torrischen/goat/streaming"
	"github.com/torrischen/goat/util/logging"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/mark3labs/mcp-go/client"
)

var _ common.Agent = (*Agent)(nil)

const settleRunTimeout = 5 * time.Second

type Agent struct {
	mu              *sync.RWMutex
	contextManager  *contextmgr.Manager
	skillsEnabled   bool
	llmClient       model.AgenticModel
	tools           []common.Tool
	toolsMap        map[string]common.Tool
	modelMaxTokensK int
	callbacks       *AgentCallbacks
}

// NewAgent creates a tool-calling agent backed by Eino's model.AgenticModel.
//
// The agent intentionally trusts the AgenticModel contract and does not branch on
// OpenAI/Claude/Gemini-specific message quirks. Provider differences should be
// handled by the Eino agentic model implementations and provider-specific
// model.Option values passed by callers.
//
// Typical provider construction:
//
// OpenAI Responses API:
//
//	import (
//	    "github.com/cloudwego/eino-ext/components/model/agenticopenai"
//	)
//
//	llm, err := agenticopenai.NewResponsesModel(ctx, &agenticopenai.ResponsesConfig{
//	    APIKey: "sk-...",
//	    Model:  "gpt-5.2",
//	    // BaseURL is optional for OpenAI-compatible gateways.
//	    // ByAzure can be set when using Azure OpenAI.
//	})
//	agent := react.NewAgent(llm, 128, nil)
//
// Claude:
//
//	import (
//	    "github.com/cloudwego/eino-ext/components/model/agenticclaude"
//	)
//
//	llm, err := agenticclaude.New(ctx, &agenticclaude.Config{
//	    APIKey:    "sk-ant-...",
//	    Model:     "claude-sonnet-4-5",
//	    MaxTokens: 4096,
//	    // ByBedrock or ByGoogleVertexAI can be set for hosted Claude.
//	})
//	agent := react.NewAgent(llm, 128, nil)
//
// Gemini on Vertex AI:
//
//	import (
//	    "cloud.google.com/go/auth/credentials"
//	    "github.com/cloudwego/eino-ext/components/model/agenticgemini"
//	    "google.golang.org/genai"
//	    "os"
//	)
//
//	client, err := genai.NewClient(ctx, &genai.ClientConfig{
//	    Backend:  genai.BackendVertexAI,
//	    Project:  "your-gcp-project",
//	    Location: "global", // or a region such as "us-central1"
//	    // Credentials may be omitted when Application Default Credentials are available.
//	})
//
//	// Or initialize Vertex AI with service account credentials JSON.
//	credentialsJSON, err := os.ReadFile("/path/to/service-account.json")
//	if err != nil {
//	    return err
//	}
//	creds, err := credentials.DetectDefault(&credentials.DetectOptions{
//	    Scopes:          []string{"https://www.googleapis.com/auth/cloud-platform"},
//	    CredentialsJSON: credentialsJSON,
//	})
//	if err != nil {
//	    return err
//	}
//	client, err = genai.NewClient(ctx, &genai.ClientConfig{
//	    Backend:     genai.BackendVertexAI,
//	    Project:     "your-gcp-project",
//	    Location:    "global",
//	    Credentials: creds,
//	})
//
//	llm, err := agenticgemini.New(ctx, &agenticgemini.Config{
//	    Client: client,
//	    Model:  "gemini-2.5-flash",
//	})
//	agent := react.NewAgent(llm, 128, nil)
//
// Gemini Developer API uses the same agenticgemini model with a genai client
// configured by API key instead of BackendVertexAI.
func NewAgent(
	llm model.AgenticModel,
	modelMaxTokensK int,
	manager *contextmgr.Manager,
) *Agent {
	a := &Agent{
		mu:              &sync.RWMutex{},
		contextManager:  manager,
		llmClient:       llm,
		modelMaxTokensK: modelMaxTokensK,
		toolsMap:        make(map[string]common.Tool),
		callbacks:       nil,
	}

	if a.contextManager == nil {
		a.contextManager = filectx.NewFileContextManager("")
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
	planMode bool,
	specialRequirements []string,
	skillUsageInstruction string,
	planUsageInstruction string,
	actx *common.AgentContext,
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
	messages *[]*schema.AgenticMessage,
	message *schema.AgenticMessage,
) error {
	*messages = append(*messages, message)
	return manager.Append(ctx, contextUID, message)
}

func commitConversationTurn(
	ctx context.Context,
	manager *contextmgr.Manager,
	contextUID common.ContextUID,
	messages *[]*schema.AgenticMessage,
	turnMessages ...*schema.AgenticMessage,
) ([]*schema.AgenticMessage, error) {
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
	messages *[]*schema.AgenticMessage,
	message *schema.AgenticMessage,
) error {
	if err := manager.SettleRun(ctx, &contextmgr.SettleRunArgs{
		Signature:    signature,
		Outcome:      contextmgr.RunOutcomeCompleted,
		FinalMessage: message,
	}); err != nil {
		return err
	}
	*messages = append(*messages, message)
	return nil
}

func responseMetaFromUsage(usage *common.AgentUsage) *schema.AgenticResponseMeta {
	if usage == nil {
		return nil
	}
	return &schema.AgenticResponseMeta{
		TokenUsage: &schema.TokenUsage{
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			TotalTokens:      usage.PromptTokens + usage.CompletionTokens,
			PromptTokenDetails: schema.PromptTokenDetails{
				CachedTokens: usage.CachedTokens,
			},
		},
	}
}

func addRunUsage(total *common.AgentUsage, usage *common.AgentUsage) {
	if total != nil {
		total.Add(usage)
	}
}

type preparedConversationContext struct {
	messages             []*schema.AgenticMessage
	usage                *common.AgentUsage
	compressed           bool
	originalMessageCount int
}

func agenticMessagesChanged(before, after []*schema.AgenticMessage) bool {
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
	messages []*schema.AgenticMessage,
	compress bool,
	options common.CompressionOptions,
	opts ...model.Option,
) (*preparedConversationContext, error) {
	prepared := &preparedConversationContext{
		messages:             messages,
		originalMessageCount: len(messages),
	}
	if !compress || !compression.ShouldCompress(messages, a.modelMaxTokensK) {
		return prepared, nil
	}

	compressedMessages, promptTokens, completionTokens, cachedTokens, err := compression.Compress(
		ctx,
		a.llmClient,
		messages,
		options,
		opts...,
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
	clone.UserInput.Images = append([]*schema.ContentBlock(nil), args.UserInput.Images...)
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

	messages := make([]*schema.AgenticMessage, 0, len(args.UserInputs))
	for _, input := range args.UserInputs {
		input.Images = append([]*schema.ContentBlock(nil), input.Images...)
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
	opts ...model.Option,
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
	var messages []*schema.AgenticMessage

	systemPrompt := a.buildSystemPrompt(
		args.EnablePlanning,
		args.SpecialRequirements,
		args.SkillUsageInstruction,
		args.PlanUsageInstruction,
		actx,
	)

	// Initialize or restore conversation
	if args.ContextUID == "" {
		systemMessage := schema.SystemAgenticMessage(systemPrompt)
		messages = []*schema.AgenticMessage{systemMessage}
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
			systemMessage := schema.SystemAgenticMessage(systemPrompt)
			if err := a.contextManager.CreateWithUID(ctx, contextUID, []*schema.AgenticMessage{systemMessage}); err != nil {
				return common.RunSignature{}, nil, fmt.Errorf("failed to create conversation %s: %w", contextUID, err)
			}
			messages = []*schema.AgenticMessage{systemMessage}
			logging.Infof("Agent.Do: initialized new conversation %s", contextUID)
		} else if err != nil {
			return common.RunSignature{}, nil, fmt.Errorf("failed to load conversation: %w", err)
		}
		if len(messages) == 0 {
			systemMessage := schema.SystemAgenticMessage(systemPrompt)
			messages = []*schema.AgenticMessage{systemMessage}
			if err := a.contextManager.Replace(ctx, contextUID, messages); err != nil {
				return common.RunSignature{}, nil, fmt.Errorf("failed to store system message: %w", err)
			}
			logging.Infof("Agent.Do: initialized empty conversation %s", contextUID)
		} else {
			// Always update system prompt to reflect current mode and requirements
			systemMessage := schema.SystemAgenticMessage(systemPrompt)
			if messages[0].Role == schema.AgenticRoleTypeSystem {
				// Replace existing system message
				messages[0] = systemMessage
				logging.Infof("Agent.Do: updated system message for conversation %s", contextUID)
			} else {
				// Insert system message at the beginning (for legacy conversations)
				messages = append([]*schema.AgenticMessage{systemMessage}, messages...)
				logging.Infof("Agent.Do: inserted system message for conversation %s", contextUID)
			}
			// Update the managed context with the new system prompt.
			if err := a.contextManager.Replace(ctx, contextUID, messages); err != nil {
				return common.RunSignature{}, nil, fmt.Errorf("failed to update system message: %w", err)
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

	callOpts := append([]model.Option{}, opts...)
	callOpts = append(callOpts, agenticopenai.WithResponsesPromptCacheKey(contextUID.String()))
	agenticTools := a.convertToolsToAgenticFormat(args.EnablePlanning)
	if len(agenticTools) > 0 {
		callOpts = append(callOpts, model.WithTools(agenticTools))
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
