package common

import (
	"slices"

	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/util"

	"github.com/bytedance/sonic"
)

type ToolResult interface {
	String() string
	// ImageParts returns image content parts from the tool result.
	// Returns nil if the result contains no images.
	ImageParts() []*message.ContentBlock
	// Usage returns any agent usage incurred while producing the tool result.
	Usage() *AgentUsage
}

type DefaultToolResult struct {
	Result    string      `json:"result"`
	ToolUsage *AgentUsage `json:"usage,omitempty"`
}

func (d *DefaultToolResult) String() string {
	return d.Result
}

func (d *DefaultToolResult) ImageParts() []*message.ContentBlock {
	return nil
}

func (d *DefaultToolResult) Usage() *AgentUsage {
	if d == nil {
		return nil
	}
	return d.ToolUsage
}

// AddUsage adds usage incurred while producing this result.
func (d *DefaultToolResult) AddUsage(usage *AgentUsage) *DefaultToolResult {
	if d == nil || usage == nil {
		return d
	}
	if d.ToolUsage == nil {
		d.ToolUsage = usage.Clone()
		return d
	}
	d.ToolUsage.Add(usage)
	return d
}

func NewDefaultToolResult(result string) *DefaultToolResult {
	return &DefaultToolResult{
		Result: result,
	}
}

type Tool interface {
	Name() string
	Description() string
	// Must be constructed by NewToolParameters
	Parameters() ToolParameters
	String() string
	Execute(*AgentContext, map[string]any) ToolResult
}

type DefaultTool struct {
	ToolName        string `json:"tool_name"`
	ToolDescription string `json:"tool_description"`
	// ToolParameters stores the JSON Schema for tool parameters (required by LLM APIs)
	// Must be in format: {"type": "object", "properties": {...}, "required": [...]}
	ToolParameters ToolParameters                                 `json:"tool_parameters"`
	F              func(*AgentContext, map[string]any) ToolResult `json:"-"`
}

type ToolParameters map[string]any

func NewDefaultTool(
	toolName,
	toolDescription string,
	toolParameters map[string]any,
	f func(*AgentContext, map[string]any) ToolResult,
) *DefaultTool {
	return &DefaultTool{
		ToolName:        toolName,
		ToolDescription: toolDescription,
		ToolParameters:  toolParameters,
		F:               f,
	}
}

func (t *DefaultTool) Name() string {
	return t.ToolName
}

func (t *DefaultTool) Description() string {
	return t.ToolDescription
}

func (t *DefaultTool) Parameters() ToolParameters {
	return t.ToolParameters
}

func (t *DefaultTool) String() string {
	t.ToolParameters = normalizeToolSchema(t.ToolParameters)

	jsonByte, err := sonic.MarshalIndent(t, "", "  ")
	if err != nil {
		return ""
	}

	return util.ByteToString(jsonByte)
}

func (t *DefaultTool) Execute(ctx *AgentContext, inputs map[string]any) ToolResult {
	return t.F(ctx, inputs)
}

// SetParameters sets the JSON Schema parameters for the tool
func (t *DefaultTool) SetParameters(params map[string]any) {
	t.ToolParameters = normalizeToolSchema(params)
}

// AddProperty adds a top-level property to the tool's parameters schema.
func (t *DefaultTool) AddProperty(name string, propType string, required bool) {
	if t.ToolParameters == nil {
		t.ToolParameters = ToolParameters{
			"type":       "object",
			"properties": map[string]any{},
		}
	}

	t.ToolParameters = normalizeToolSchema(t.ToolParameters)

	props, ok := t.ToolParameters["properties"].(map[string]any)
	if !ok || props == nil {
		props = map[string]any{}
		t.ToolParameters["properties"] = props
	}

	props[name] = map[string]any{
		"type": propType,
	}

	if required {
		var requiredList []string
		if req, ok := t.ToolParameters["required"]; ok {
			switch v := req.(type) {
			case []string:
				requiredList = append(requiredList, v...)
			case []any:
				for _, r := range v {
					if str, ok := r.(string); ok {
						requiredList = append(requiredList, str)
					}
				}
			}
		}

		if !slices.Contains(requiredList, name) {
			requiredList = append(requiredList, name)
			t.ToolParameters["required"] = requiredList
		}
	}
}

// ToolProperty represents a single property in a tool's parameters schema.
type ToolProperty struct {
	Name        string         `json:"name"`
	Type        string         `json:"type"` // JSON Schema type: string, integer, number, boolean, array, object
	Required    bool           `json:"required"`
	Description string         `json:"description,omitempty"`
	Items       *ToolProperty  `json:"items,omitempty"`      // for array
	Properties  []ToolProperty `json:"properties,omitempty"` // for object
}

// NewToolParameters creates a JSON Schema object from a list of properties.
//
// Example:
//
//	params := NewToolParameters(
//		ToolProperty{
//			Name:        "users",
//			Type:        "array",
//			Required:    true,
//			Description: "user list",
//			Items: &ToolProperty{
//				Type: "object",
//				Properties: []ToolProperty{
//					{
//						Name:        "name",
//						Type:        "string",
//						Required:    true,
//						Description: "user name",
//					},
//					{
//						Name:        "age",
//						Type:        "integer",
//						Description: "user age",
//					},
//				},
//			},
//		},
//	)
func NewToolParameters(props ...ToolProperty) map[string]any {
	properties := make(map[string]any)
	required := make([]string, 0)

	for _, prop := range props {
		properties[prop.Name] = buildPropertySchema(prop)
		if prop.Required {
			required = append(required, prop.Name)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}

	if len(required) > 0 {
		schema["required"] = required
	}

	return normalizeToolSchema(schema)
}

// buildPropertySchema recursively builds JSON Schema for a ToolProperty.
func buildPropertySchema(prop ToolProperty) map[string]any {
	schema := map[string]any{
		"type": prop.Type,
	}

	if prop.Description != "" {
		schema["description"] = prop.Description
	}

	switch prop.Type {
	case "object":
		properties := make(map[string]any)
		required := make([]string, 0)

		for _, child := range prop.Properties {
			properties[child.Name] = buildPropertySchema(child)
			if child.Required {
				required = append(required, child.Name)
			}
		}

		schema["properties"] = properties
		if len(required) > 0 {
			schema["required"] = required
		}

	case "array":
		if prop.Items != nil {
			schema["items"] = buildPropertySchema(*prop.Items)
		} else {
			schema["items"] = map[string]any{}
		}
	}

	return schema
}

// normalizeToolSchema recursively normalizes schema to satisfy LLM APIs.
func normalizeToolSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}
	}

	return normalizeSchemaMap(schema)
}

func normalizeSchemaMap(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{}
	}

	schemaType, _ := schema["type"].(string)

	switch schemaType {
	case "object":
		props, ok := schema["properties"].(map[string]any)
		if !ok || props == nil {
			props = map[string]any{}
			schema["properties"] = props
		}

		for key, raw := range props {
			if child, ok := raw.(map[string]any); ok {
				props[key] = normalizeSchemaMap(child)
			}
		}

	case "array":
		if items, ok := schema["items"].(map[string]any); ok {
			schema["items"] = normalizeSchemaMap(items)
		} else if _, exists := schema["items"]; !exists {
			schema["items"] = map[string]any{}
		}

	default:
		if props, ok := schema["properties"].(map[string]any); ok {
			schema["type"] = "object"
			for key, raw := range props {
				if child, ok := raw.(map[string]any); ok {
					props[key] = normalizeSchemaMap(child)
				}
			}
			schema["properties"] = props
		}
		if items, ok := schema["items"].(map[string]any); ok {
			if _, hasType := schema["type"]; !hasType {
				schema["type"] = "array"
			}
			schema["items"] = normalizeSchemaMap(items)
		}
	}

	return schema
}

// MultimodalToolResult is a tool result that contains both text and image parts.
type MultimodalToolResult struct {
	Text   string
	Images []*message.ContentBlock
}

func (m *MultimodalToolResult) String() string {
	return m.Text
}

func (m *MultimodalToolResult) ImageParts() []*message.ContentBlock {
	return m.Images
}

func (m *MultimodalToolResult) Usage() *AgentUsage {
	return nil
}
