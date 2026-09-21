package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/util"

	"github.com/bytedance/sonic"
)

const (
	InternalToolGeneratePlan = "generate_plan"
	InternalToolUpdatePlan   = "update_plan"
)

var allowedPlanStatuses = map[string]struct{}{
	"pending":     {},
	"in_progress": {},
	"completed":   {},
	"blocked":     {},
	"skipped":     {},
}

func GeneratePlan() common.Tool {
	f := func(actx *common.AgentContext, a map[string]any) common.ToolResult {
		state, err := buildPlanState(a)
		if err != nil {
			return planToolJSONResult(map[string]any{
				"ok":      false,
				"message": err.Error(),
			})
		}

		if actx != nil {
			actx.SetMeta(common.InternalToolPlanMetaKey, state)
		}

		return planToolJSONResult(map[string]any{
			"ok":      true,
			"message": "Plan registered. Execute it step by step and call update_plan with the step index after each completed, blocked, changed, or skipped item.",
			"version": state.Version,
			"plan":    state,
		})
	}

	return &common.DefaultTool{
		ToolName: InternalToolGeneratePlan,
		ToolDescription: `Register the current execution plan using the fixed schema in this tool's parameters.
Construct the plan yourself before calling this tool. Each step must have a stable 1-based index and status so update_plan can update steps by index later.`,
		ToolParameters: generatePlanParameters(),
		F:              f,
	}
}

func UpdatePlan() common.Tool {
	f := func(actx *common.AgentContext, a map[string]any) common.ToolResult {
		state, ok := currentPlanState(actx)
		if !ok {
			return planToolJSONResult(map[string]any{
				"ok":      false,
				"message": "No current plan is registered. Call generate_plan before update_plan.",
			})
		}

		index, err := intParam(a, "index", true)
		if err != nil {
			return planToolJSONResult(map[string]any{
				"ok":      false,
				"message": err.Error(),
			})
		}
		if index <= 0 {
			return planToolJSONResult(map[string]any{
				"ok":      false,
				"message": "index parameter must be a positive integer.",
			})
		}
		status, err := statusParam(a, "status", true)
		if err != nil {
			return planToolJSONResult(map[string]any{
				"ok":      false,
				"message": err.Error(),
			})
		}
		var dependencies []int
		hasDependencies := false
		if _, hasDependencies = a["dependencies"]; hasDependencies {
			dependencies, err = intSliceParam(a, "dependencies")
			if err != nil {
				return planToolJSONResult(map[string]any{
					"ok":      false,
					"message": err.Error(),
				})
			}
			if err := state.validateDependencies(index, dependencies); err != nil {
				return planToolJSONResult(map[string]any{
					"ok":      false,
					"message": err.Error(),
				})
			}
		}

		step := state.stepByIndex(index)
		operation := "updated"
		if step == nil {
			if index != state.nextStepIndex() {
				return planToolJSONResult(map[string]any{
					"ok":      false,
					"message": fmt.Sprintf("step with index %d was not found. New steps must use the next contiguous index %d.", index, state.nextStepIndex()),
				})
			}

			title, ok := stringParam(a, "title", true)
			if !ok {
				return planToolJSONResult(map[string]any{
					"ok":      false,
					"message": "title parameter is required when adding a new plan step.",
				})
			}

			step = &planStep{
				Index:  index,
				Title:  title,
				Status: status,
			}
			if hasDependencies {
				step.Dependencies = dependencies
			}
			state.Steps = append(state.Steps, step)
			operation = "added"
		} else {
			if title, ok := stringParam(a, "title", false); ok {
				step.Title = title
			}
			step.Status = status
			if hasDependencies {
				step.Dependencies = dependencies
			}
		}
		if detail, ok := stringParam(a, "detail", false); ok {
			step.Detail = detail
		}
		if notes, ok := stringParam(a, "notes", false); ok {
			step.Notes = notes
		}

		state.Version++
		state.LastUpdate = &planUpdate{
			Operation:    operation,
			Index:        index,
			Status:       status,
			Title:        step.Title,
			Detail:       step.Detail,
			Dependencies: step.Dependencies,
			Notes:        step.Notes,
		}

		if actx != nil {
			actx.SetMeta(common.InternalToolPlanMetaKey, state)
		}

		return planToolJSONResult(map[string]any{
			"ok":      true,
			"message": fmt.Sprintf("Plan step %s.", operation),
			"version": state.Version,
			"updated": state.LastUpdate,
			"plan":    state,
		})
	}

	return &common.DefaultTool{
		ToolName: InternalToolUpdatePlan,
		ToolDescription: `Update or append one step in the current registered plan by its stable 1-based index.
If the index exists, update that step. If the index is the next contiguous index, append a new step; title is required when appending. Use this after a plan item changes state or when execution reveals a new necessary step.`,
		ToolParameters: updatePlanParameters(),
		F:              f,
	}
}

type planState struct {
	Version     int         `json:"version"`
	Goal        string      `json:"goal"`
	Steps       []*planStep `json:"steps"`
	Constraints []string    `json:"constraints,omitempty"`
	Assumptions []string    `json:"assumptions,omitempty"`
	Notes       string      `json:"notes,omitempty"`
	LastUpdate  *planUpdate `json:"last_update,omitempty"`
}

type planStep struct {
	Index        int    `json:"index"`
	Title        string `json:"title"`
	Detail       string `json:"detail,omitempty"`
	Status       string `json:"status"`
	Dependencies []int  `json:"dependencies,omitempty"`
	Notes        string `json:"notes,omitempty"`
}

type planUpdate struct {
	Operation    string `json:"operation"`
	Index        int    `json:"index"`
	Status       string `json:"status"`
	Title        string `json:"title,omitempty"`
	Detail       string `json:"detail,omitempty"`
	Dependencies []int  `json:"dependencies,omitempty"`
	Notes        string `json:"notes,omitempty"`
}

func (p *planState) stepByIndex(index int) *planStep {
	for _, step := range p.Steps {
		if step != nil && step.Index == index {
			return step
		}
	}
	return nil
}

func (p *planState) nextStepIndex() int {
	maxIndex := 0
	for _, step := range p.Steps {
		if step != nil && step.Index > maxIndex {
			maxIndex = step.Index
		}
	}
	return maxIndex + 1
}

func (p *planState) validateDependencies(stepIndex int, dependencies []int) error {
	seenDependencies := make(map[int]struct{}, len(dependencies))
	for _, dependency := range dependencies {
		if dependency <= 0 {
			return fmt.Errorf("dependencies contains non-positive index %d.", dependency)
		}
		if dependency == stepIndex {
			return fmt.Errorf("step index %d cannot depend on itself.", stepIndex)
		}
		if _, exists := seenDependencies[dependency]; exists {
			return fmt.Errorf("dependencies duplicates step index %d.", dependency)
		}
		seenDependencies[dependency] = struct{}{}
		if p.stepByIndex(dependency) == nil {
			return fmt.Errorf("dependencies references unknown step index %d.", dependency)
		}
	}
	return nil
}

func buildPlanState(a map[string]any) (*planState, error) {
	goal, ok := stringParam(a, "goal", true)
	if !ok {
		return nil, fmt.Errorf("goal parameter is missing or invalid.")
	}

	rawSteps, ok := a["steps"].([]any)
	if !ok || len(rawSteps) == 0 {
		return nil, fmt.Errorf("steps parameter is missing or invalid. Provide at least one step.")
	}

	steps := make([]*planStep, 0, len(rawSteps))
	seen := make(map[int]struct{}, len(rawSteps))
	for i, raw := range rawSteps {
		rawStep, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("steps[%d] must be an object.", i)
		}

		index, err := intParam(rawStep, "index", true)
		if err != nil {
			return nil, fmt.Errorf("steps[%d].%s", i, err.Error())
		}
		if index <= 0 {
			return nil, fmt.Errorf("steps[%d].index must be a positive integer.", i)
		}
		if _, exists := seen[index]; exists {
			return nil, fmt.Errorf("steps[%d].index duplicates step index %d.", i, index)
		}
		seen[index] = struct{}{}

		title, ok := stringParam(rawStep, "title", true)
		if !ok {
			return nil, fmt.Errorf("steps[%d].title parameter is missing or invalid.", i)
		}
		status, err := statusParam(rawStep, "status", true)
		if err != nil {
			return nil, fmt.Errorf("steps[%d].%s", i, err.Error())
		}

		dependencies, err := intSliceParam(rawStep, "dependencies")
		if err != nil {
			return nil, fmt.Errorf("steps[%d].%s", i, err.Error())
		}

		step := &planStep{
			Index:        index,
			Title:        title,
			Status:       status,
			Dependencies: dependencies,
		}
		if detail, ok := stringParam(rawStep, "detail", false); ok {
			step.Detail = detail
		}
		if notes, ok := stringParam(rawStep, "notes", false); ok {
			step.Notes = notes
		}

		steps = append(steps, step)
	}

	for i := 1; i <= len(steps); i++ {
		if _, exists := seen[i]; !exists {
			return nil, fmt.Errorf("steps indexes must be contiguous 1-based integers; missing index %d.", i)
		}
	}
	for _, step := range steps {
		for _, dependency := range step.Dependencies {
			if dependency <= 0 {
				return nil, fmt.Errorf("step index %d dependencies contains non-positive index %d.", step.Index, dependency)
			}
			if dependency == step.Index {
				return nil, fmt.Errorf("step index %d cannot depend on itself.", step.Index)
			}
			if _, exists := seen[dependency]; !exists {
				return nil, fmt.Errorf("step index %d dependencies references unknown step index %d.", step.Index, dependency)
			}
		}
	}

	state := &planState{
		Version:     1,
		Goal:        goal,
		Steps:       steps,
		Constraints: stringSliceParam(a, "constraints"),
		Assumptions: stringSliceParam(a, "assumptions"),
	}
	if notes, ok := stringParam(a, "notes", false); ok {
		state.Notes = notes
	}

	return state, nil
}

func currentPlanState(actx *common.AgentContext) (*planState, bool) {
	if actx == nil {
		return nil, false
	}

	state, ok := actx.GetMeta(common.InternalToolPlanMetaKey).(*planState)
	if !ok || state == nil {
		return nil, false
	}

	return state, true
}

func stringParam(a map[string]any, name string, required bool) (string, bool) {
	raw, exists := a[name]
	if !exists {
		return "", false
	}
	value, ok := raw.(string)
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", false
	}
	return value, value != "" || !required
}

func statusParam(a map[string]any, name string, required bool) (string, error) {
	status, ok := stringParam(a, name, required)
	if !ok {
		return "", fmt.Errorf("%s parameter is missing or invalid.", name)
	}
	if _, allowed := allowedPlanStatuses[status]; !allowed {
		return "", fmt.Errorf("%s parameter must be one of: pending, in_progress, completed, blocked, skipped.", name)
	}
	return status, nil
}

func intParam(a map[string]any, name string, required bool) (int, error) {
	raw, exists := a[name]
	if !exists {
		if required {
			return 0, fmt.Errorf("%s parameter is missing or invalid.", name)
		}
		return 0, nil
	}

	switch v := raw.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if math.Trunc(v) != v {
			return 0, fmt.Errorf("%s parameter must be an integer.", name)
		}
		return int(v), nil
	case json.Number:
		parsed, err := strconv.Atoi(v.String())
		if err != nil {
			return 0, fmt.Errorf("%s parameter must be an integer.", name)
		}
		return parsed, nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, fmt.Errorf("%s parameter must be an integer.", name)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("%s parameter is missing or invalid.", name)
	}
}

func stringSliceParam(a map[string]any, name string) []string {
	rawItems, ok := a[name].([]any)
	if !ok {
		return nil
	}

	items := make([]string, 0, len(rawItems))
	for _, raw := range rawItems {
		if value, ok := raw.(string); ok && strings.TrimSpace(value) != "" {
			items = append(items, strings.TrimSpace(value))
		}
	}
	return items
}

func intSliceParam(a map[string]any, name string) ([]int, error) {
	rawItems, ok := a[name].([]any)
	if !ok {
		return nil, nil
	}

	items := make([]int, 0, len(rawItems))
	for i, raw := range rawItems {
		value, err := intParam(map[string]any{name: raw}, name, true)
		if err != nil {
			return nil, fmt.Errorf("%s[%d] must be an integer.", name, i)
		}
		items = append(items, value)
	}
	return items, nil
}

func planToolJSONResult(payload map[string]any) common.ToolResult {
	b, err := sonic.MarshalIndent(payload, "", "  ")
	if err != nil {
		return common.NewDefaultToolResult(fmt.Sprintf(`{"ok":false,"message":"failed to serialize plan tool result: %s"}`, err.Error()))
	}

	return common.NewDefaultToolResult(util.ByteToString(b))
}

func generatePlanParameters() common.ToolParameters {
	return common.ToolParameters{
		"type": "object",
		"properties": map[string]any{
			"goal": map[string]any{
				"type":        "string",
				"description": "The user's overall objective for this plan.",
			},
			"steps": map[string]any{
				"type":        "array",
				"description": "Actionable plan steps with stable 1-based indexes.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"index": map[string]any{
							"type":        "integer",
							"description": "Stable 1-based step number used by update_plan.",
						},
						"title": map[string]any{
							"type":        "string",
							"description": "Short actionable step title.",
						},
						"detail": map[string]any{
							"type":        "string",
							"description": "Optional concrete detail about what this step covers.",
						},
						"status": planStatusSchema(),
						"dependencies": map[string]any{
							"type":        "array",
							"description": "Optional list of step indexes that should happen before this step.",
							"items": map[string]any{
								"type": "integer",
							},
						},
						"notes": map[string]any{
							"type":        "string",
							"description": "Optional notes, constraints, or rationale for this step.",
						},
					},
					"required": []string{"index", "title", "status"},
				},
			},
			"constraints": map[string]any{
				"type":        "array",
				"description": "Optional constraints that affect execution.",
				"items": map[string]any{
					"type": "string",
				},
			},
			"assumptions": map[string]any{
				"type":        "array",
				"description": "Optional assumptions behind the plan.",
				"items": map[string]any{
					"type": "string",
				},
			},
			"notes": map[string]any{
				"type":        "string",
				"description": "Optional top-level plan notes.",
			},
		},
		"required": []string{"goal", "steps"},
	}
}

func updatePlanParameters() common.ToolParameters {
	return common.ToolParameters{
		"type": "object",
		"properties": map[string]any{
			"index": map[string]any{
				"type":        "integer",
				"description": "The stable 1-based index of the step to update.",
			},
			"status": planStatusSchema(),
			"title": map[string]any{
				"type":        "string",
				"description": "Optional replacement title if the step wording changed. Required when adding a new step.",
			},
			"detail": map[string]any{
				"type":        "string",
				"description": "Optional replacement detail if the step scope changed.",
			},
			"dependencies": map[string]any{
				"type":        "array",
				"description": "Optional replacement list of step indexes that should happen before this step.",
				"items": map[string]any{
					"type": "integer",
				},
			},
			"notes": map[string]any{
				"type":        "string",
				"description": "Optional progress note, blocker, result, or rationale.",
			},
		},
		"required": []string{"index", "status"},
	}
}

func planStatusSchema() map[string]any {
	return map[string]any{
		"type":        "string",
		"enum":        []string{"pending", "in_progress", "completed", "blocked", "skipped"},
		"description": "Step status. Use skipped for no-longer-needed items.",
	}
}
