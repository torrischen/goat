package react

import (
	"fmt"
	"strings"

	"github.com/torrischen/goat/prompt"
)

const reactRole = "You are a helpful assistant that can use tools to complete tasks."

const reactSkillsOverview = `You have access to skills for specialized tasks.

Before handling a user request, you MUST first call 'list_available_skills' to discover what skills are available. Then check whether the request is related to any available skill. If it is, use the relevant skill. If multiple skills apply, use the most appropriate one or combine them when helpful.`

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

// ReactSystemPromptTemplate is the non-planning prompt template without skills.
var ReactSystemPromptTemplate = buildReactPrompt(false, false, "", "")

// ReactSystemPromptWithSkillsTemplate is the non-planning prompt template with skills enabled.
var ReactSystemPromptWithSkillsTemplate = buildReactPrompt(false, true, "%s", "")

// ReactWithPlanSystemPromptTemplate is the planning prompt template without skills.
var ReactWithPlanSystemPromptTemplate = buildReactPrompt(true, false, "", "%s")

// ReactWithPlanAndSkillsSystemPromptTemplate is the planning prompt template with skills enabled.
var ReactWithPlanAndSkillsSystemPromptTemplate = buildReactPrompt(true, true, "%s", "%s")

func buildReactPrompt(planMode bool, skillsEnabled bool, skillUsageInstruction, planUsageInstruction string) string {
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

	if skillsEnabled {
		builder.
			Section("Skills", reactSkillsOverview).
			ListSection(
				"How to Discover and Load Skills",
				"Use 'list_available_skills' to discover what skills are available and their descriptions.",
				"Use 'load_skills' to get the full details of specific skills, including the content in SKILL.md and the tree of files in the specified skills folder.",
				"The 'load_skills' tool lists file paths from the current run's skills directory. Pass one of those paths to the 'read_specified_file_in_skill' tool to read a file inside a skill.",
			).
			ListSection(
				"When Using a Skill",
				"Follow the skill's own instructions strictly.",
				"Skill-specific instructions take priority over general prompt instructions.",
				"If the user names a skill (by skill name or plain text), or the task clearly matches a skill description, you must use that skill for that turn. Multiple mentions mean use them all. Do not carry skills across turns unless re-mentioned.",
				"Do not use unrelated skills.",
				"Do not invent capabilities the skill does not provide.",
				"If the skill references files or documentation, read them with the 'read_specified_file_in_skill' tool before applying the skill.",
			).
			Section("Priority Order", reactSkillPriority).
			Section("Skill Usage Instructions", skillUsageInstruction).
			Constraint("IMPORTANT: When skills are enabled, you MUST call 'list_available_skills' at the beginning of each conversation turn to discover available skills before proceeding with the user's request.")
	}

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

const reactToolSearchOverview = `Dynamic Tool Discovery

You have access to a tool registry containing specialized tools for various tasks. When you need capabilities beyond your current tools, you can use the search_tools tool to dynamically discover relevant tools.`

const reactToolSearchGuidance = `How to Use Dynamic Tool Search:

1. **Search for tools**: Call search_tools with a descriptive query
   - Example: search_tools(query="image processing")
   - Example: search_tools(query="database operations", limit=5)

2. **Tool information becomes available in context**: The search results provide detailed information about each tool (name, description, parameters) which is now visible in your conversation context

3. **Call the tools directly by name**: After search_tools returns, you can call any of the discovered tools by their names, just like your standard built-in tools

Key points:
- Search when you need capabilities not available in your current tool set
- The search results show tool names, descriptions, and detailed parameter information
- Tool information remains available in your context for the rest of the conversation
- Tools are retrieved from the registry on-demand when you call them
- Call discovered tools directly by name, treating them as first-class tools
- Use descriptive search queries to find the most relevant tools (e.g., "web scraping", "file compression", "data visualization")

**Important**: After using search_tools, you can immediately call the discovered tools by their names. The tools will be dynamically retrieved from the registry when invoked.`

func renderReactSystemPrompt(
	planMode bool,
	skillsEnabled bool,
	toolSearchEnabled bool,
	specialRequirements []string,
	skillUsageInstruction string,
	planUsageInstruction string,
	toolSearchInstruction string,
) string {
	skillUsageInstruction = strings.TrimSpace(skillUsageInstruction)
	if skillUsageInstruction == "" {
		skillUsageInstruction = "NONE"
	}
	planUsageInstruction = strings.TrimSpace(planUsageInstruction)
	if planUsageInstruction == "" {
		planUsageInstruction = "NONE"
	}
	toolSearchInstruction = strings.TrimSpace(toolSearchInstruction)
	if toolSearchInstruction == "" {
		toolSearchInstruction = "NONE"
	}

	var systemPrompt string

	if planMode {
		if skillsEnabled {
			systemPrompt = fmt.Sprintf(ReactWithPlanAndSkillsSystemPromptTemplate, skillUsageInstruction, planUsageInstruction)
		} else {
			systemPrompt = fmt.Sprintf(ReactWithPlanSystemPromptTemplate, planUsageInstruction)
		}
	} else {
		if skillsEnabled {
			systemPrompt = fmt.Sprintf(ReactSystemPromptWithSkillsTemplate, skillUsageInstruction)
		} else {
			systemPrompt = ReactSystemPromptTemplate
		}
	}

	requirementsPrompt := prompt.New().
		ListSection("Special Requirements", specialRequirements...).
		Build()
	if requirementsPrompt != "" {
		systemPrompt += "\n\n" + requirementsPrompt
	}

	// Tool Search section (placed at the end to maximize cache hits)
	if toolSearchEnabled {
		toolSearchPrompt := prompt.New().
			Section("Tool Search", reactToolSearchOverview).
			Section("Using Dynamic Tool Search", reactToolSearchGuidance).
			Section("Tool Search Instructions", toolSearchInstruction).
			Build()

		if toolSearchPrompt != "" {
			systemPrompt += "\n\n" + toolSearchPrompt
		}
	}

	return systemPrompt
}
