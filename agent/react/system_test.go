package react

import (
	"fmt"
	"strings"
	"testing"
)

func TestReactPromptTemplatesRemainFormattingCompatible(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		template string
		args     []any
		want     string
	}{
		{
			name:     "without planning or skills",
			template: ReactSystemPromptTemplate,
			args:     []any{},
			want:     buildReactPrompt(false, false, "", ""),
		},
		{
			name:     "without planning but with skills",
			template: ReactSystemPromptWithSkillsTemplate,
			args:     []any{"Use matching skills."},
			want:     buildReactPrompt(false, true, "Use matching skills.", ""),
		},
		{
			name:     "with planning but without skills",
			template: ReactWithPlanSystemPromptTemplate,
			args:     []any{"Plan complex changes."},
			want:     buildReactPrompt(true, false, "", "Plan complex changes."),
		},
		{
			name:     "with planning and skills",
			template: ReactWithPlanAndSkillsSystemPromptTemplate,
			args:     []any{"Use matching skills.", "Plan complex changes."},
			want:     buildReactPrompt(true, true, "Use matching skills.", "Plan complex changes."),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := fmt.Sprintf(tt.template, tt.args...); got != tt.want {
				t.Fatalf("formatted template mismatch\n--- got ---\n%s\n--- want ---\n%s", got, tt.want)
			}
		})
	}
}

func TestRenderReactSystemPromptWithoutPlanning(t *testing.T) {
	t.Parallel()

	got := renderReactSystemPrompt(
		false,
		true, // skills enabled
		[]string{"Return JSON.", " ", "Keep the answer concise."},
		"  Prefer the narrowest matching skill.  ",
		"this must not be rendered",
	)

	assertPromptContains(t, got,
		"## Role\n"+reactRole,
		"## Skills",
		"## Skill Usage Instructions\nPrefer the narrowest matching skill.",
		"## Special Requirements\n- Return JSON.\n- Keep the answer concise.",
		"list_available_skills",
	)
	assertPromptOmits(t, got,
		"## Planning",
		"this must not be rendered",
		"%!",
	)
}

func TestRenderReactSystemPromptWithPlanningAndDefaults(t *testing.T) {
	t.Parallel()

	got := renderReactSystemPrompt(true, false, nil, " \n", "")

	assertPromptContains(t, got,
		"## Planning\n"+reactPlanningCore,
		"## Caller Plan Usage Instructions",
		"NONE",
		"## Execution",
	)
	assertPromptOmits(t, got,
		"## Skills",
		"## Skill Usage Instructions",
		"## Special Requirements",
		"list_available_skills",
		"%!",
	)
}

func assertPromptContains(t *testing.T, prompt string, values ...string) {
	t.Helper()
	for _, value := range values {
		if !strings.Contains(prompt, value) {
			t.Errorf("prompt does not contain %q\n--- prompt ---\n%s", value, prompt)
		}
	}
}

func assertPromptOmits(t *testing.T, prompt string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(prompt, value) {
			t.Errorf("prompt unexpectedly contains %q\n--- prompt ---\n%s", value, prompt)
		}
	}
}
