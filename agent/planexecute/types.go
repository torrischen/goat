// Package planexecute implements a plan-and-execute agent that delegates each
// plan step to a React agent.
package planexecute

import (
	"fmt"
	"strings"

	"github.com/torrischen/goat/agent/common"
)

const (
	EventTypePlanCreated   common.AgentEventType = "plan_created"
	EventTypePlanRevised   common.AgentEventType = "plan_revised"
	EventTypeStepStarted   common.AgentEventType = "plan_step_started"
	EventTypeStepCompleted common.AgentEventType = "plan_step_completed"
)

type Config struct {
	MaxPlanSteps     int
	ExecutorMaxSteps int
	MaxReplans       int
}

func (c Config) normalized() Config {
	if c.MaxPlanSteps <= 0 {
		c.MaxPlanSteps = 8
	}
	if c.ExecutorMaxSteps <= 0 {
		c.ExecutorMaxSteps = 8
	}
	if c.MaxReplans < 0 {
		c.MaxReplans = 0
	}
	if c.MaxReplans == 0 {
		c.MaxReplans = 2
	}
	return c
}

type Plan struct {
	Goal  string `json:"goal"`
	Steps []Step `json:"steps"`
}

type Step struct {
	ID           string   `json:"id"`
	Description  string   `json:"description"`
	Dependencies []string `json:"dependencies,omitempty"`
}

type StepResult struct {
	StepID string `json:"step_id"`
	Output string `json:"output"`
}

type PlanCreatedEvent struct {
	Plan Plan `json:"plan"`
}

func (PlanCreatedEvent) Type() common.AgentEventType { return EventTypePlanCreated }

type PlanRevisedEvent struct {
	Plan   Plan   `json:"plan"`
	Reason string `json:"reason"`
}

func (PlanRevisedEvent) Type() common.AgentEventType { return EventTypePlanRevised }

type StepStartedEvent struct {
	Step Step `json:"step"`
}

func (StepStartedEvent) Type() common.AgentEventType { return EventTypeStepStarted }

type StepCompletedEvent struct {
	Step   Step       `json:"step"`
	Result StepResult `json:"result"`
}

func (StepCompletedEvent) Type() common.AgentEventType { return EventTypeStepCompleted }

func validatePlan(plan *Plan, maxSteps int, completed map[string]StepResult) error {
	if plan == nil {
		return fmt.Errorf("plan is nil")
	}
	plan.Goal = strings.TrimSpace(plan.Goal)
	if plan.Goal == "" {
		return fmt.Errorf("plan goal is empty")
	}
	if len(plan.Steps) == 0 {
		return fmt.Errorf("plan has no steps")
	}
	if len(plan.Steps) > maxSteps {
		return fmt.Errorf("plan has %d steps, maximum is %d", len(plan.Steps), maxSteps)
	}

	steps := make(map[string]struct{}, len(plan.Steps))
	for i := range plan.Steps {
		step := &plan.Steps[i]
		step.ID = strings.TrimSpace(step.ID)
		step.Description = strings.TrimSpace(step.Description)
		if step.ID == "" {
			return fmt.Errorf("plan step %d has an empty ID", i+1)
		}
		if step.Description == "" {
			return fmt.Errorf("plan step %q has an empty description", step.ID)
		}
		if _, exists := steps[step.ID]; exists {
			return fmt.Errorf("plan step ID %q is duplicated", step.ID)
		}
		if _, exists := completed[step.ID]; exists {
			return fmt.Errorf("plan step ID %q was already completed", step.ID)
		}
		steps[step.ID] = struct{}{}
	}

	for i := range plan.Steps {
		step := &plan.Steps[i]
		seen := make(map[string]struct{}, len(step.Dependencies))
		for j, dependency := range step.Dependencies {
			dependency = strings.TrimSpace(dependency)
			step.Dependencies[j] = dependency
			if dependency == step.ID {
				return fmt.Errorf("plan step %q depends on itself", step.ID)
			}
			if _, exists := seen[dependency]; exists {
				return fmt.Errorf("plan step %q duplicates dependency %q", step.ID, dependency)
			}
			seen[dependency] = struct{}{}
			if _, exists := steps[dependency]; !exists {
				if _, completedDependency := completed[dependency]; !completedDependency {
					return fmt.Errorf("plan step %q depends on unknown step %q", step.ID, dependency)
				}
			}
		}
	}

	visited := make(map[string]bool, len(plan.Steps))
	visiting := make(map[string]bool, len(plan.Steps))
	byID := make(map[string]Step, len(plan.Steps))
	for _, step := range plan.Steps {
		byID[step.ID] = step
	}
	var visit func(string) error
	visit = func(id string) error {
		if visited[id] {
			return nil
		}
		if visiting[id] {
			return fmt.Errorf("plan contains a dependency cycle at step %q", id)
		}
		visiting[id] = true
		for _, dependency := range byID[id].Dependencies {
			if _, exists := byID[dependency]; exists {
				if err := visit(dependency); err != nil {
					return err
				}
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for id := range byID {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func nextStep(plan Plan, completed map[string]StepResult) (Step, bool) {
	for _, step := range plan.Steps {
		if _, done := completed[step.ID]; done {
			continue
		}
		ready := true
		for _, dependency := range step.Dependencies {
			if _, done := completed[dependency]; !done {
				ready = false
				break
			}
		}
		if ready {
			return step, true
		}
	}
	return Step{}, false
}
