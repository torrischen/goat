package prompt

import "testing"

func TestBuilderBuildsStructuredPrompt(t *testing.T) {
	t.Parallel()

	got := New().
		Role("You are a senior Go engineer.").
		Objective("Review the supplied code.").
		Context("The service handles payment events.").
		Instructions("Find correctness issues.", "Suggest focused fixes.").
		Constraints("Do not change public APIs.", "Keep the answer concise.").
		Input("func handle() {}").
		OutputFormat("Return a Markdown checklist.").
		Example("empty handler", "- [ ] Add error handling").
		Section("Audience", "Backend engineers").
		ListSection("Quality Bar", "Actionable", "Evidence-based").
		Build()

	want := `## Role
You are a senior Go engineer.

## Objective
Review the supplied code.

## Context
The service handles payment events.

## Instructions
- Find correctness issues.
- Suggest focused fixes.

## Constraints
- Do not change public APIs.
- Keep the answer concise.

## Input
func handle() {}

## Output Format
Return a Markdown checklist.

## Examples

### Example 1

#### Input
empty handler

#### Output
- [ ] Add error handling

## Audience
Backend engineers

## Quality Bar
- Actionable
- Evidence-based`

	if got != want {
		t.Fatalf("Build() mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestBuilderOmitsBlankSectionsAndItems(t *testing.T) {
	t.Parallel()

	got := NewPrompt().
		Role("  assistant  ").
		Objective("\n\t").
		Instructions("", "  answer directly  ", "   ").
		Constraints(" ").
		Example("", "").
		Section("Ignored", "  ").
		ListSection("Also ignored", "", "  ").
		String()

	want := "## Role\nassistant\n\n## Instructions\n- answer directly"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestSingleValueMethodsReplaceAndListMethodsAppend(t *testing.T) {
	t.Parallel()

	got := New().
		Role("first role").
		Role("final role").
		Instruction("first").
		Instructions("second", "third").
		Constraint("one constraint").
		Build()

	want := `## Role
final role

## Instructions
- first
- second
- third

## Constraints
- one constraint`
	if got != want {
		t.Fatalf("Build() = %q, want %q", got, want)
	}
}

func TestNilBuilderBuildIsEmpty(t *testing.T) {
	t.Parallel()

	var builder *Builder
	if got := builder.Build(); got != "" {
		t.Fatalf("Build() = %q, want empty string", got)
	}
}
