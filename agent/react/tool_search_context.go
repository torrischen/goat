package react

import (
	"fmt"
	"strings"

	"github.com/torrischen/goat/agent/common"
)

// buildToolSearchContext generates the tool search context for the system prompt
func buildToolSearchContext(task string, tools []common.Tool) string {
	if len(tools) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("For your task: \"%s\"\n\n", task))
	builder.WriteString("The following specialized tools have been selected and are available:\n\n")

	for i, tool := range tools {
		builder.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, tool.Name()))
		builder.WriteString(fmt.Sprintf("   %s\n\n", tool.Description()))

		params := tool.Parameters()
		if params != nil {
			if props, ok := params["properties"].(map[string]any); ok && len(props) > 0 {
				builder.WriteString("   Parameters:\n")

				var required []string
				if req, ok := params["required"].([]any); ok {
					for _, r := range req {
						if str, ok := r.(string); ok {
							required = append(required, str)
						}
					}
				} else if req, ok := params["required"].([]string); ok {
					required = req
				}

				for propName, propVal := range props {
					if propMap, ok := propVal.(map[string]any); ok {
						propType, _ := propMap["type"].(string)
						propDesc, _ := propMap["description"].(string)

						isRequired := false
						for _, r := range required {
							if r == propName {
								isRequired = true
								break
							}
						}

						requiredTag := ""
						if isRequired {
							requiredTag = " (required)"
						}

						if propDesc != "" {
							builder.WriteString(fmt.Sprintf("   - `%s` (%s)%s: %s\n", propName, propType, requiredTag, propDesc))
						} else {
							builder.WriteString(fmt.Sprintf("   - `%s` (%s)%s\n", propName, propType, requiredTag))
						}
					}
				}
				builder.WriteString("\n")
			}
		}
	}

	return builder.String()
}
