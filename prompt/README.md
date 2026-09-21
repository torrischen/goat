# Prompt Builder

`prompt` provides a lightweight fluent API for organizing Role, Objective, Context, Instructions, Constraints, Input, Output Format, and Examples into a Markdown prompt with a stable section order.

## Quick start

```go
package main

import (
	"fmt"

	"github.com/torrischen/goat/prompt"
)

func main() {
	p := prompt.New().
		Role("You are a senior Go engineer").
		Objective("Review the input code and identify high-risk issues").
		Context("The code runs in the request path of a payment service").
		Instructions(
			"Prioritize correctness and concurrency safety",
			"Provide an actionable fix for every issue",
		).
		Constraints("Do not change the public API", "Respond in English").
		Input("func handle() {}").
		OutputFormat("Use a Markdown checklist").
		Build()

	fmt.Println(p)
}
```

Output:

```markdown
## Role
You are a senior Go engineer

## Objective
Review the input code and identify high-risk issues

## Context
The code runs in the request path of a payment service

## Instructions
- Prioritize correctness and concurrency safety
- Provide an actionable fix for every issue

## Constraints
- Do not change the public API
- Respond in English

## Input
func handle() {}

## Output Format
Use a Markdown checklist
```

Repeated calls to `Role`, `Objective`, `Context`, `Input`, or `OutputFormat` replace the previous value. Calls to `Instruction(s)`, `Constraint(s)`, and `Example` append values in call order. Blank content is ignored.

Use `Section(title, content)` and `ListSection(title, items...)` to append custom sections. Custom sections appear after the standard sections in call order. `String()` is equivalent to `Build()`.
