// Package prompt provides a fluent builder for composing structured prompts.
package prompt

import (
	"fmt"
	"strings"
)

// Builder builds a prompt from common prompt-engineering sections.
//
// Builder is mutable and is not safe for concurrent use. Repeated calls to a
// single-value method, such as Role, replace the previous value. Repeated calls
// to a list method, such as Instruction, append values in call order.
type Builder struct {
	role         string
	objective    string
	context      string
	instructions []string
	constraints  []string
	input        string
	outputFormat string
	examples     []example
	sections     []section
}

type example struct {
	input  string
	output string
}

type section struct {
	title   string
	content string
	items   []string
}

// New creates an empty prompt builder.
func New() *Builder {
	return &Builder{}
}

// NewPrompt is an explicit alias for New.
func NewPrompt() *Builder {
	return New()
}

// Role sets the role or persona that should answer the prompt.
func (b *Builder) Role(role string) *Builder {
	b.role = clean(role)
	return b
}

// Objective sets the primary outcome the prompt should achieve.
func (b *Builder) Objective(objective string) *Builder {
	b.objective = clean(objective)
	return b
}

// Context sets background information needed to complete the objective.
func (b *Builder) Context(context string) *Builder {
	b.context = clean(context)
	return b
}

// Instruction appends one instruction. Blank instructions are ignored.
func (b *Builder) Instruction(instruction string) *Builder {
	b.instructions = appendNonBlank(b.instructions, instruction)
	return b
}

// Instructions appends instructions in the order provided. Blank instructions
// are ignored.
func (b *Builder) Instructions(instructions ...string) *Builder {
	b.instructions = appendNonBlank(b.instructions, instructions...)
	return b
}

// Constraint appends one constraint. Blank constraints are ignored.
func (b *Builder) Constraint(constraint string) *Builder {
	b.constraints = appendNonBlank(b.constraints, constraint)
	return b
}

// Constraints appends constraints in the order provided. Blank constraints
// are ignored.
func (b *Builder) Constraints(constraints ...string) *Builder {
	b.constraints = appendNonBlank(b.constraints, constraints...)
	return b
}

// Input sets the input data the model should operate on.
func (b *Builder) Input(input string) *Builder {
	b.input = clean(input)
	return b
}

// OutputFormat describes the required response format.
func (b *Builder) OutputFormat(outputFormat string) *Builder {
	b.outputFormat = clean(outputFormat)
	return b
}

// Example appends an input/output example. Empty examples are ignored.
func (b *Builder) Example(input, output string) *Builder {
	input = clean(input)
	output = clean(output)
	if input != "" || output != "" {
		b.examples = append(b.examples, example{input: input, output: output})
	}
	return b
}

// Section appends a custom text section after the standard sections. A blank
// title or content causes the section to be ignored.
func (b *Builder) Section(title, content string) *Builder {
	title = clean(title)
	content = clean(content)
	if title != "" && content != "" {
		b.sections = append(b.sections, section{title: title, content: content})
	}
	return b
}

// ListSection appends a custom bullet-list section after the standard
// sections. A blank title or an empty item list causes the section to be
// ignored.
func (b *Builder) ListSection(title string, items ...string) *Builder {
	title = clean(title)
	items = appendNonBlank(nil, items...)
	if title != "" && len(items) > 0 {
		b.sections = append(b.sections, section{title: title, items: items})
	}
	return b
}

// Build renders the prompt as Markdown. Standard sections always use the
// following order: Role, Objective, Context, Instructions, Constraints, Input,
// Output Format, and Examples. Empty sections are omitted.
func (b *Builder) Build() string {
	if b == nil {
		return ""
	}

	sections := make([]string, 0, 8+len(b.sections))
	sections = appendTextSection(sections, "Role", b.role)
	sections = appendTextSection(sections, "Objective", b.objective)
	sections = appendTextSection(sections, "Context", b.context)
	sections = appendListSection(sections, "Instructions", b.instructions)
	sections = appendListSection(sections, "Constraints", b.constraints)
	sections = appendTextSection(sections, "Input", b.input)
	sections = appendTextSection(sections, "Output Format", b.outputFormat)
	if len(b.examples) > 0 {
		sections = append(sections, renderExamples(b.examples))
	}
	for _, custom := range b.sections {
		if len(custom.items) > 0 {
			sections = appendListSection(sections, custom.title, custom.items)
			continue
		}
		sections = appendTextSection(sections, custom.title, custom.content)
	}

	return strings.Join(sections, "\n\n")
}

// String implements fmt.Stringer and is equivalent to Build.
func (b *Builder) String() string {
	return b.Build()
}

func appendTextSection(sections []string, title, content string) []string {
	if content == "" {
		return sections
	}
	return append(sections, fmt.Sprintf("## %s\n%s", title, content))
}

func appendListSection(sections []string, title string, items []string) []string {
	if len(items) == 0 {
		return sections
	}

	var rendered strings.Builder
	fmt.Fprintf(&rendered, "## %s", title)
	for _, item := range items {
		fmt.Fprintf(&rendered, "\n- %s", item)
	}
	return append(sections, rendered.String())
}

func renderExamples(examples []example) string {
	var rendered strings.Builder
	rendered.WriteString("## Examples")
	for i, example := range examples {
		fmt.Fprintf(&rendered, "\n\n### Example %d", i+1)
		if example.input != "" {
			fmt.Fprintf(&rendered, "\n\n#### Input\n%s", example.input)
		}
		if example.output != "" {
			fmt.Fprintf(&rendered, "\n\n#### Output\n%s", example.output)
		}
	}
	return rendered.String()
}

func appendNonBlank(dst []string, values ...string) []string {
	for _, value := range values {
		if value = clean(value); value != "" {
			dst = append(dst, value)
		}
	}
	return dst
}

func clean(value string) string {
	return strings.TrimSpace(value)
}
