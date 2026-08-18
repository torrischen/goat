package react

import (
	"fmt"
	"strings"

	"github.com/torrischen/goat/prompt"
)

const reactRole = "You are a helpful assistant that can use tools to complete tasks."

const reactSkillsOverview = `You have access to skills for specialized tasks.

Before handling a user request, check whether it is related to any available skill. If it is, use the relevant skill. If multiple skills apply, use the most appropriate one or combine them when helpful.`

const reactSkillPriority = `1. User instructions
2. Skill instructions
3. General system/tool rules
4. General assistant behavior`

// Planning Core: Tools and Responsibility
const reactPlanningCore = `Planning Tools:
- 'generate_plan': Register a structured JSON plan (you construct the JSON yourself)
- 'update_plan': Update step status by index, or append new steps

You construct the plan JSON before calling 'generate_plan'. The tool doesn't infer structure for you.`

// When to Use Planning
const reactPlanningDecision = `When to Plan:
- Multi-step tasks with dependencies or coordination needs
- Tasks requiring multiple tools/skills
- Complex workflows where tracking progress matters
- Tasks with unclear scope that becomes clear after inspection

Skip Planning for:
- Direct answers or simple rewrites
- Single obvious tool call
- Tasks where a plan would just restate the request

Information Gathering Rule:
- If the task depends on unknown external context (files, APIs, docs, specs), use information-gathering tools FIRST before planning
- Don't create plans from assumptions when tools can gather facts`

// Plan Structure Guidelines
const reactPlanningStructure = `Plan Guidelines:
- Default to 3-7 steps for multi-step tasks
- Each step should be a meaningful milestone with clear completion criteria
- Use outcome-oriented titles: "inspect files", "implement change", "validate results"
- Avoid micro-steps (individual commands, small edits, single file reads)
- Split steps only when they can be independently completed, blocked, or reordered
- Include validation as separate step when correctness requires verification

Plan JSON should include:
- goal: the user's objective
- steps: array of {index, title, status, detail?, dependencies?, notes?}
- constraints/assumptions: only if they affect execution`

// Execution Workflow
const reactPlanningExecution = `Execution Workflow:
1. Determine relevant skills first
2. Gather information if external context is unknown
3. Construct well-organized JSON plan
4. Call 'generate_plan' with the JSON
5. Execute steps sequentially
6. After completing EACH step, call 'update_plan' (MANDATORY)
7. Append new steps or update existing ones as scope changes

Update Rules:
- Every completed step REQUIRES 'update_plan' call
- Update status to: pending/in_progress/completed/blocked/skipped
- Append new steps with next contiguous index when discovered`

// ReactSystemPromptTemplate is the non-planning prompt template. Its two
// formatting arguments are the available skills and skill-usage instructions.
var ReactSystemPromptTemplate = buildReactPrompt(false, "%s", "%s", "")

// ReactWithPlanSystemPromptTemplate is the planning prompt template. Its
// three formatting arguments are the available skills, skill-usage
// instructions, and plan-usage instructions.
var ReactWithPlanSystemPromptTemplate = buildReactPrompt(true, "%s", "%s", "%s")

func buildReactPrompt(planMode bool, availableSkills, skillUsageInstruction, planUsageInstruction string) string {
	if strings.TrimSpace(availableSkills) == "" {
		availableSkills = "NONE"
	}
	if strings.TrimSpace(skillUsageInstruction) == "" {
		skillUsageInstruction = "NONE"
	}
	if strings.TrimSpace(planUsageInstruction) == "" {
		planUsageInstruction = "NONE"
	}

	builder := prompt.New().
		Role(reactRole).
		Instructions(
			"You MUST think through problems step by step and use the available tools when needed.",
			"You can use more than one tool at a time, and you can use the same tool multiple times if needed.",
		).
		Constraint("Always try to call more than one tool if doing so would help solve the problem.")

	if planMode {
		builder.Constraints(
			"During execution, once a plan exists, every completed item MUST be followed by an 'update_plan' call.",
			"Treat every step in the plan with caution and seriousness. Use tools as much as possible to complete each step to the best of your ability. Only after carefully verifying and confirming that everything is correct may you update the status of that step.",
		)
	}

	builder.
		Section("Skills", reactSkillsOverview).
		ListSection(
			"How to Load Skills",
			"Use the 'load_skills' tool to get the full image of the skills, including the content in SKILL.md and the tree of files in the specified skills folder.",
			"The 'load_skills' tool lists file paths from the current run's skills directory. Pass one of those paths to the 'read_specified_file_in_skill' tool to read a file inside a skill.",
		).
		ListSection(
			"When Using a Skill",
			"Follow the skill's own instructions strictly.",
			"Skill-specific instructions take priority over general prompt instructions.",
			"If the user names a skill (by skill name or plain text), or the task clearly matches a skill description shown above, you must use that skill for that turn. Multiple mentions mean use them all. Do not carry skills across turns unless re-mentioned.",
			"Do not use unrelated skills.",
			"Do not invent capabilities the skill does not provide.",
			"If the skill references files or documentation, read them with the 'read_specified_file_in_skill' tool before applying the skill.",
		).
		Section("Priority Order", reactSkillPriority).
		Section("Available Skills", availableSkills).
		Section("Skill Usage Instructions", skillUsageInstruction)

	if planMode {
		addPlanningPrompt(builder, planUsageInstruction)
	}

	return builder.Build()
}

func addPlanningPrompt(builder *prompt.Builder, planUsageInstruction string) {
	builder.
		Section("Planning", reactPlanningCore).
		Section(
			"Caller Plan Usage Instructions",
			"The caller may provide additional instructions that calibrate when to plan and how granular the plan should be. Follow these instructions when they are present.\n\n"+planUsageInstruction,
		).
		Section("When to Plan", reactPlanningDecision).
		Section("Plan Structure", reactPlanningStructure).
		Section("Execution", reactPlanningExecution)
}

func renderReactSystemPrompt(
	planMode bool,
	skills []string,
	specialRequirements []string,
	skillUsageInstruction string,
	planUsageInstruction string,
) string {
	availableSkills := strings.TrimSpace(strings.Join(skills, "\n"))
	if availableSkills == "" {
		availableSkills = "NONE"
	}
	skillUsageInstruction = strings.TrimSpace(skillUsageInstruction)
	if skillUsageInstruction == "" {
		skillUsageInstruction = "NONE"
	}
	planUsageInstruction = strings.TrimSpace(planUsageInstruction)
	if planUsageInstruction == "" {
		planUsageInstruction = "NONE"
	}

	template := ReactSystemPromptTemplate
	args := []any{availableSkills, skillUsageInstruction}
	if planMode {
		template = ReactWithPlanSystemPromptTemplate
		args = append(args, planUsageInstruction)
	}

	systemPrompt := fmt.Sprintf(template, args...)
	requirementsPrompt := prompt.New().
		ListSection("Special Requirements", specialRequirements...).
		Build()
	if requirementsPrompt != "" {
		systemPrompt += "\n\n" + requirementsPrompt
	}

	return systemPrompt
}
