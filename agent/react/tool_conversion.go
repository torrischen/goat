package react

import (
	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/llm"
	"github.com/torrischen/goat/agent/tools"

	"github.com/bytedance/sonic"
	"github.com/torrischen/goat/util/logging"
)

// convertToolsToAgenticFormat converts agent tools to the neutral LLM tool format.
func (a *Agent) convertToolsToAgenticFormat(planMode bool) []llm.ToolDef {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if len(a.tools) == 0 {
		return nil
	}

	toolDefs := make([]llm.ToolDef, 0, len(a.tools))
	for _, tool := range a.tools {
		if !planMode &&
			(tool.Name() == tools.InternalToolGeneratePlan || tool.Name() == tools.InternalToolUpdatePlan) {
			continue
		}

		toolDefs = append(toolDefs, llm.ToolDef{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  a.extractToolParameters(tool),
		})
	}

	return toolDefs
}

// extractToolParameters extracts parameters from a tool in JSON schema format
func (a *Agent) extractToolParameters(tool common.Tool) map[string]any {
	if tool == nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}

	// Prefer the structured parameters directly.
	if params := tool.Parameters(); params != nil {
		cast := map[string]any(params)
		// Ensure object type schemas always have properties field (required by OpenAI/Anthropic APIs)
		if schemaType, ok := cast["type"].(string); ok && schemaType == "object" {
			if _, hasProps := cast["properties"]; !hasProps {
				cast["properties"] = map[string]any{}
			}
		}
		return cast
	}

	// Fallback: Parse the JSON to extract parameters (legacy paths).
	var toolDesc map[string]any
	if err := sonic.UnmarshalString(tool.String(), &toolDesc); err != nil {
		logging.Errorf("Failed to parse tool JSON: %v", err)
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}

	if params, ok := toolDesc["tool_parameters"].(map[string]any); ok {
		if schemaType, ok := params["type"].(string); ok && schemaType == "object" {
			if _, hasProps := params["properties"]; !hasProps {
				params["properties"] = map[string]any{}
			}
		}
		return params
	}
	if params, ok := toolDesc["parameters"].(map[string]any); ok {
		if schemaType, ok := params["type"].(string); ok && schemaType == "object" {
			if _, hasProps := params["properties"]; !hasProps {
				params["properties"] = map[string]any{}
			}
		}
		return params
	}

	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}
