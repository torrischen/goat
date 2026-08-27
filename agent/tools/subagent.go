package tools

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/streaming"
	"github.com/torrischen/goat/util/logging"

	"github.com/google/uuid"
	"github.com/torrischen/goat/llm"
)

const (
	InternalToolSpawnSubAgent     = "spawn_subagent"
	InternalToolGetSubAgentStatus = "get_subagent_status"
)

// SubAgentStatus represents the status of a subagent execution
type SubAgentStatus string

const (
	SubAgentStatusRunning   SubAgentStatus = "running"
	SubAgentStatusCompleted SubAgentStatus = "completed"
	SubAgentStatusFailed    SubAgentStatus = "failed"
	SubAgentStatusCanceled  SubAgentStatus = "canceled"
)

// SubAgentInfo stores the execution state and result of a subagent
type SubAgentInfo struct {
	SubID          string              `json:"sub_id"`
	Task           string              `json:"task"`
	Status         SubAgentStatus      `json:"status"`
	Result         string              `json:"result,omitempty"`
	Error          string              `json:"error,omitempty"`
	StartTime      time.Time           `json:"start_time"`
	EndTime        *time.Time          `json:"end_time,omitempty"`
	Usage          *common.AgentUsage  `json:"usage,omitempty"`
	IterationsUsed int                 `json:"iterations_used"`
	ToolCalls      int                 `json:"tool_calls"`
	Signature      common.RunSignature `json:"signature"`
}

// SubAgentRegistry manages running subagents
type SubAgentRegistry struct {
	mu     sync.RWMutex
	agents map[string]*SubAgentInfo
}

var globalRegistry = &SubAgentRegistry{
	agents: make(map[string]*SubAgentInfo),
}

func (r *SubAgentRegistry) Register(subID string, info *SubAgentInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[subID] = info
}

func (r *SubAgentRegistry) Get(subID string) (*SubAgentInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, exists := r.agents[subID]
	return info, exists
}

func (r *SubAgentRegistry) Update(subID string, fn func(*SubAgentInfo)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, exists := r.agents[subID]; exists {
		fn(info)
	}
}

func (r *SubAgentRegistry) Delete(subID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, subID)
}

func (r *SubAgentRegistry) List() []*SubAgentInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	results := make([]*SubAgentInfo, 0, len(r.agents))
	for _, info := range r.agents {
		results = append(results, info)
	}
	return results
}

// SpawnSubAgent creates a tool that spawns a subagent to execute a task in the background
func SpawnSubAgent(agent common.Agent, llmOpts ...llm.Option) common.Tool {
	f := func(actx *common.AgentContext, a map[string]any) common.ToolResult {
		task, ok := a["task"].(string)
		if !ok || task == "" {
			return common.NewDefaultToolResult("task parameter is missing or invalid.")
		}

		// Extract optional parameters
		maxStep := 8
		if ms, ok := a["max_step"].(float64); ok && ms > 0 {
			maxStep = int(ms)
		}

		compress := false
		if c, ok := a["compress"].(bool); ok {
			compress = c
		}

		// Generate unique subagent ID
		subID := uuid.New().String()[:8]

		// Create subagent info entry
		info := &SubAgentInfo{
			SubID:     subID,
			Task:      task,
			Status:    SubAgentStatusRunning,
			StartTime: time.Now(),
		}

		// Register the subagent
		globalRegistry.Register(subID, info)

		// Prepare agent arguments
		doArgs := &common.AgentDoArgs{
			UserInput: common.AgentUserInput{
				Text: task,
			},
			MaxStep:  maxStep,
			Compress: compress,
		}

		// Clone context metadata from parent if available
		if actx != nil {
			doArgs.ContextMeta = actx.GetAllMeta()
			// Copy skills directory
			if skillsDir := common.SkillsDirFromContext(actx); skillsDir != "" {
				doArgs.SkillsDir = skillsDir
			}
		}

		// Start the subagent in background
		go func() {
			ctx := context.Background()
			if actx != nil && actx.Context != nil {
				ctx = actx.Context
			}

			signature, eventStream, err := agent.Do(ctx, doArgs, llmOpts...)
			if err != nil {
				logging.Errorf("SubAgent %s failed to start: %v", subID, err)
				globalRegistry.Update(subID, func(r *SubAgentInfo) {
					r.Status = SubAgentStatusFailed
					r.Error = fmt.Sprintf("Failed to start: %v", err)
					now := time.Now()
					r.EndTime = &now
				})
				return
			}

			// Update signature
			globalRegistry.Update(subID, func(r *SubAgentInfo) {
				r.Signature = signature
			})

			// Process events
			var finalAnswer string
			var usage *common.AgentUsage
			var iterations, toolCalls int

			for {
				event, readErr := eventStream.ReadWithContext(ctx)
				if readErr != nil {
					if readErr != streaming.ErrStreamClosed {
						logging.Errorf("SubAgent %s event stream error: %v", subID, readErr)
					}
					break
				}

				switch typed := event.(type) {
				case common.FinalAnswerCompletedEvent:
					finalAnswer = typed.Answer
				case common.RunCompletedEvent:
					usage = typed.Usage
					iterations = typed.IterationsUsed
					toolCalls = typed.ToolCalls
					globalRegistry.Update(subID, func(r *SubAgentInfo) {
						r.Status = SubAgentStatusCompleted
						r.Result = finalAnswer
						r.Usage = usage
						r.IterationsUsed = iterations
						r.ToolCalls = toolCalls
						now := time.Now()
						r.EndTime = &now
					})
				case common.RunFailedEvent:
					usage = typed.Usage
					iterations = typed.IterationsUsed
					globalRegistry.Update(subID, func(r *SubAgentInfo) {
						r.Status = SubAgentStatusFailed
						r.Error = fmt.Sprintf("%s: %s", typed.Operation, typed.Error)
						r.Usage = usage
						r.IterationsUsed = iterations
						now := time.Now()
						r.EndTime = &now
					})
				case common.RunCanceledEvent:
					usage = typed.Usage
					iterations = typed.IterationsUsed
					globalRegistry.Update(subID, func(r *SubAgentInfo) {
						r.Status = SubAgentStatusCanceled
						r.Error = typed.Reason
						r.Usage = usage
						r.IterationsUsed = iterations
						now := time.Now()
						r.EndTime = &now
					})
				case common.RunInterruptedEvent:
					usage = typed.Usage
					iterations = typed.IterationsUsed
					globalRegistry.Update(subID, func(r *SubAgentInfo) {
						r.Status = SubAgentStatusCanceled
						r.Error = fmt.Sprintf("Interrupted: %s", typed.Reason)
						r.Usage = usage
						r.IterationsUsed = iterations
						now := time.Now()
						r.EndTime = &now
					})
				}
			}

			logging.Infof("SubAgent %s finished with status: %s", subID, info.Status)
		}()

		return common.NewDefaultToolResult(fmt.Sprintf("Subagent spawned successfully with ID: %s\nTask: %s\nStatus: running", subID, task))
	}

	return &common.DefaultTool{
		ToolName: InternalToolSpawnSubAgent,
		ToolDescription: `Spawns a subagent to execute a task in the background.
This tool clones the current agent with its configuration and tools, assigns it a task,
and runs it asynchronously. Returns a unique subagent ID that can be used to query the status later.

Use this when you need to:
- Delegate independent subtasks to run in parallel
- Execute long-running tasks without blocking the current workflow
- Break down complex problems into smaller concurrent tasks

Similar to agent-to-agent (a2a) communication patterns.`,
		ToolParameters: common.NewToolParameters(
			common.ToolProperty{
				Name:        "task",
				Type:        "string",
				Required:    true,
				Description: "The task description for the subagent to execute.",
			},
			common.ToolProperty{
				Name:        "max_step",
				Type:        "integer",
				Description: "Maximum number of steps the subagent can take. Defaults to 8.",
			},
			common.ToolProperty{
				Name:        "compress",
				Type:        "boolean",
				Description: "Whether to enable context compression. Defaults to false.",
			},
		),
		F: f,
	}
}

// GetSubAgentStatus creates a tool that retrieves the status of a running subagent
func GetSubAgentStatus() common.Tool {
	f := func(actx *common.AgentContext, a map[string]any) common.ToolResult {
		subID, ok := a["sub_id"].(string)
		if !ok || subID == "" {
			return common.NewDefaultToolResult("sub_id parameter is missing or invalid.")
		}

		info, exists := globalRegistry.Get(subID)
		if !exists {
			return common.NewDefaultToolResult(fmt.Sprintf("Subagent with ID '%s' not found.", subID))
		}

		// Build status report
		report := fmt.Sprintf("Subagent ID: %s\n", info.SubID)
		report += fmt.Sprintf("Task: %s\n", info.Task)
		report += fmt.Sprintf("Status: %s\n", info.Status)
		report += fmt.Sprintf("Started: %s\n", info.StartTime.Format(time.RFC3339))

		if info.EndTime != nil {
			duration := info.EndTime.Sub(info.StartTime)
			report += fmt.Sprintf("Ended: %s\n", info.EndTime.Format(time.RFC3339))
			report += fmt.Sprintf("Duration: %s\n", duration)
		}

		if info.Status == SubAgentStatusRunning {
			elapsed := time.Since(info.StartTime)
			report += fmt.Sprintf("Running for: %s\n", elapsed)
		}

		if info.IterationsUsed > 0 {
			report += fmt.Sprintf("Iterations: %d\n", info.IterationsUsed)
		}

		if info.ToolCalls > 0 {
			report += fmt.Sprintf("Tool calls: %d\n", info.ToolCalls)
		}

		if info.Usage != nil {
			report += fmt.Sprintf("Token usage: %d prompt + %d completion = %d total\n",
				info.Usage.PromptTokens,
				info.Usage.CompletionTokens,
				info.Usage.PromptTokens+info.Usage.CompletionTokens)
			if info.Usage.CachedTokens > 0 {
				report += fmt.Sprintf("Cached tokens: %d\n", info.Usage.CachedTokens)
			}
		}

		if info.Signature.ContextUID != "" {
			report += fmt.Sprintf("Context UID: %s\n", info.Signature.ContextUID)
			report += fmt.Sprintf("Run UID: %s\n", info.Signature.RunUID)
		}

		switch info.Status {
		case SubAgentStatusCompleted:
			report += fmt.Sprintf("\nResult:\n%s", info.Result)
		case SubAgentStatusFailed, SubAgentStatusCanceled:
			report += fmt.Sprintf("\nError: %s", info.Error)
		}

		return common.NewDefaultToolResult(report)
	}

	return &common.DefaultTool{
		ToolName: InternalToolGetSubAgentStatus,
		ToolDescription: `Retrieves the execution status and result of a subagent.
Use this tool to check on a previously spawned subagent's progress and retrieve its final result.

Returns information including:
- Current status (running, completed, failed, canceled)
- Execution time and duration
- Token usage and iterations
- Final result or error message`,
		ToolParameters: common.NewToolParameters(
			common.ToolProperty{
				Name:        "sub_id",
				Type:        "string",
				Required:    true,
				Description: "The unique ID of the subagent to query.",
			},
		),
		F: f,
	}
}
