package tools

import (
	"fmt"
	"strings"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/util/logging"
)

const (
	InternalToolSearchTools = "search_tools"
)

// SearchTools returns a tool that allows the agent to search for relevant tools
// based on a task description. The tool returns detailed information about found
// tools, which can then be called directly by the agent. Tools are dynamically
// queried from the registry at execution time, ensuring they are always up-to-date.
func SearchTools(registry common.DynamicToolRegistry) common.Tool {
	f := func(actx *common.AgentContext, args map[string]any) common.ToolResult {
		query, ok := args["query"].(string)
		if !ok || query == "" {
			return common.NewDefaultToolResult("query parameter is missing or invalid.")
		}

		limit := 10 // default limit
		if limitVal, ok := args["limit"].(float64); ok && limitVal > 0 {
			limit = int(limitVal)
		}

		// Search for relevant tools using the captured registry
		tools, err := registry.Search(actx, query, limit)
		if err != nil {
			logging.Errorf("Failed to search tools: %v", err)
			return common.NewDefaultToolResult(fmt.Sprintf("Failed to search tools: %v", err))
		}

		if len(tools) == 0 {
			return common.NewDefaultToolResult(fmt.Sprintf("No tools found for query: %q", query))
		}

		// Format the search results with detailed information
		var builder strings.Builder
		builder.WriteString(fmt.Sprintf("Found %d relevant tools for query: %q\n\n", len(tools), query))
		builder.WriteString("These tools are now available and can be called directly by their names:\n\n")

		for i, tool := range tools {
			builder.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, tool.Name()))
			builder.WriteString(fmt.Sprintf("   %s\n", tool.Description()))

			// Show parameter details
			params := tool.Parameters()
			if params != nil {
				if props, ok := params["properties"].(map[string]any); ok && len(props) > 0 {
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

					builder.WriteString("   Parameters:\n")
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
				}
			}
			builder.WriteString("\n")
		}

		builder.WriteString("You can now call these tools directly by their names, just like your standard tools.")

		return common.NewDefaultToolResult(builder.String())
	}

	return &common.DefaultTool{
		ToolName: InternalToolSearchTools,
		ToolDescription: `Search for relevant tools based on a task description. This tool returns information about available tools that match your query. The tools can then be called directly by their names.

When to use:
- You need a capability that is not provided by your current tools
- You want to discover tools for a specific domain or task
- You are starting a new task and want to find specialized tools

Example queries:
- "image processing" - find tools for working with images
- "database operations" - find tools for database interaction
- "file compression" - find tools for compressing files
- "web scraping" - find tools for extracting web data

After searching, you can immediately call the found tools by their names.`,
		ToolParameters: common.NewToolParameters(
			common.ToolProperty{
				Name:        "query",
				Type:        "string",
				Description: "The task description or capability you're looking for",
				Required:    true,
			},
			common.ToolProperty{
				Name:        "limit",
				Type:        "number",
				Description: "Maximum number of tools to return (default: 10)",
				Required:    false,
			},
		),
		F: f,
	}
}

