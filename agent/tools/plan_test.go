package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/torrischen/goat/agent/common"

	"github.com/bytedance/sonic"
)

func TestUpdatePlanAppendsNextStep(t *testing.T) {
	t.Parallel()

	actx := newPlanTestContext(t)
	result := UpdatePlan().Execute(actx, map[string]any{
		"index":        3,
		"title":        "verify change",
		"status":       "pending",
		"detail":       "run focused tests",
		"dependencies": []any{2},
	})

	var payload map[string]any
	if err := sonic.UnmarshalString(result.String(), &payload); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if payload["ok"] != true {
		t.Fatalf("ok = %v, want true; result = %s", payload["ok"], result.String())
	}

	state := currentPlanStateForTest(t, actx)
	if got, want := len(state.Steps), 3; got != want {
		t.Fatalf("len(state.Steps) = %d, want %d", got, want)
	}
	added := state.Steps[2]
	if added.Index != 3 || added.Title != "verify change" || added.Status != "pending" {
		t.Fatalf("added step = %+v", added)
	}
	if got, want := len(added.Dependencies), 1; got != want || added.Dependencies[0] != 2 {
		t.Fatalf("added dependencies = %#v, want [2]", added.Dependencies)
	}
	if state.LastUpdate == nil || state.LastUpdate.Operation != "added" {
		t.Fatalf("LastUpdate = %+v, want added operation", state.LastUpdate)
	}
}

func TestUpdatePlanRejectsNonContiguousAppend(t *testing.T) {
	t.Parallel()

	actx := newPlanTestContext(t)
	result := UpdatePlan().Execute(actx, map[string]any{
		"index":  4,
		"title":  "skip an index",
		"status": "pending",
	})
	if !strings.Contains(result.String(), `"ok": false`) {
		t.Fatalf("result = %s, want ok false", result.String())
	}

	state := currentPlanStateForTest(t, actx)
	if got, want := len(state.Steps), 2; got != want {
		t.Fatalf("len(state.Steps) = %d, want %d", got, want)
	}
}

func TestUpdatePlanRequiresTitleForAppend(t *testing.T) {
	t.Parallel()

	actx := newPlanTestContext(t)
	result := UpdatePlan().Execute(actx, map[string]any{
		"index":  3,
		"status": "pending",
	})
	if !strings.Contains(result.String(), `"ok": false`) {
		t.Fatalf("result = %s, want ok false", result.String())
	}

	state := currentPlanStateForTest(t, actx)
	if got, want := len(state.Steps), 2; got != want {
		t.Fatalf("len(state.Steps) = %d, want %d", got, want)
	}
}

func TestUpdatePlanRejectsInvalidAppendDependenciesWithoutMutation(t *testing.T) {
	t.Parallel()

	actx := newPlanTestContext(t)
	result := UpdatePlan().Execute(actx, map[string]any{
		"index":        3,
		"title":        "bad dependency",
		"status":       "pending",
		"dependencies": []any{99},
	})
	if !strings.Contains(result.String(), `"ok": false`) {
		t.Fatalf("result = %s, want ok false", result.String())
	}

	state := currentPlanStateForTest(t, actx)
	if got, want := len(state.Steps), 2; got != want {
		t.Fatalf("len(state.Steps) = %d, want %d", got, want)
	}
	if state.stepByIndex(3) != nil {
		t.Fatalf("invalid append mutated state: %+v", state.stepByIndex(3))
	}
}

func newPlanTestContext(t *testing.T) *common.AgentContext {
	t.Helper()

	actx := common.NewAgentContext(context.Background())
	result := GeneratePlan().Execute(actx, map[string]any{
		"goal": "ship feature",
		"steps": []any{
			map[string]any{"index": 1, "title": "inspect code", "status": "completed"},
			map[string]any{"index": 2, "title": "implement change", "status": "in_progress"},
		},
	})
	if !strings.Contains(result.String(), `"ok": true`) {
		t.Fatalf("generate_plan result = %s, want ok true", result.String())
	}
	return actx
}

func currentPlanStateForTest(t *testing.T, actx *common.AgentContext) *planState {
	t.Helper()

	state, ok := actx.GetMeta(common.InternalToolPlanMetaKey).(*planState)
	if !ok || state == nil {
		t.Fatalf("current plan state missing")
	}
	return state
}
