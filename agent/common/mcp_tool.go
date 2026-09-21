package common

import (
	"context"
	"fmt"
	"strings"

	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/util"
	"github.com/torrischen/goat/util/logging"

	"github.com/bytedance/sonic"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// MCPTool adapts an MCP tool to the agent/common.Tool interface.
// It lets the agent call MCP tools the same way as local tools.
type MCPTool struct {
	MCPClient       client.MCPClient
	ToolName        string
	ToolDescription string
	ToolParameters  map[string]any
}

// ListMCPTools fetches all tools exposed by an MCP server and wraps them as agent tools.
func ListMCPTools(ctx context.Context, cli client.MCPClient) ([]Tool, error) {
	if cli == nil {
		return nil, fmt.Errorf("mcp client is nil")
	}

	res, err := cli.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list mcp tools: %w", err)
	}

	tools := make([]Tool, 0, len(res.Tools))
	for _, mt := range res.Tools {
		tools = append(tools, &MCPTool{
			MCPClient:       cli,
			ToolName:        mt.Name,
			ToolDescription: mt.Description,
			ToolParameters:  inputSchemaToMap(mt),
		})
	}

	return tools, nil
}

func (t *MCPTool) Name() string {
	return t.ToolName
}

func (t *MCPTool) Description() string {
	return t.ToolDescription
}

func (t *MCPTool) Parameters() ToolParameters {
	return t.ToolParameters
}

func (t *MCPTool) String() string {
	desc := map[string]any{
		"tool_name":        t.ToolName,
		"tool_description": t.ToolDescription,
		"tool_parameters":  t.ToolParameters,
	}

	jsonByte, err := sonic.MarshalIndent(desc, "", "  ")
	if err != nil {
		return ""
	}

	return util.ByteToString(jsonByte)
}

func (t *MCPTool) Execute(ctx *AgentContext, inputs map[string]any) ToolResult {
	if t.MCPClient == nil {
		return NewDefaultToolResult("mcp client is nil")
	}

	callCtx := context.Background()
	if ctx != nil {
		callCtx = ctx
	}

	result, err := t.MCPClient.CallTool(callCtx, mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      t.ToolName,
			Arguments: inputs,
		},
	})
	if err != nil {
		logging.Errorf("MCPTool.Execute error: %v", err)
		return NewDefaultToolResult(err.Error())
	}

	return newMCPToolResult(result)
}

type mcpToolResult struct {
	Content           []mcp.Content `json:"content,omitempty"`
	StructuredContent any           `json:"structured_content,omitempty"`
	IsError           bool          `json:"is_error,omitempty"`
}

func newMCPToolResult(res *mcp.CallToolResult) ToolResult {
	if res == nil {
		return NewDefaultToolResult("")
	}

	return &mcpToolResult{
		Content:           res.Content,
		StructuredContent: res.StructuredContent,
		IsError:           res.IsError,
	}
}

func (r *mcpToolResult) ImageParts() []*message.ContentBlock {
	if r == nil {
		return nil
	}

	var images []*message.ContentBlock
	for _, c := range r.Content {
		switch v := c.(type) {
		case mcp.ImageContent:
			images = append(images, Base64ImageBlock(v.MIMEType, v.Data))
		case *mcp.ImageContent:
			images = append(images, Base64ImageBlock(v.MIMEType, v.Data))
		}
	}
	return images
}

func (r *mcpToolResult) Usage() *AgentUsage {
	return nil
}

func (r *mcpToolResult) String() string {
	if r == nil {
		return ""
	}

	if r.StructuredContent != nil {
		if raw, err := sonic.MarshalIndent(r.StructuredContent, "", "  "); err == nil {
			return util.ByteToString(raw)
		}
	}

	var parts []string
	for _, c := range r.Content {
		switch v := c.(type) {
		case mcp.TextContent:
			parts = append(parts, v.Text)
		case *mcp.TextContent:
			parts = append(parts, v.Text)
		case mcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[image mime=%s]", v.MIMEType))
		case *mcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[image mime=%s]", v.MIMEType))
		case mcp.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio mime=%s]", v.MIMEType))
		case *mcp.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio mime=%s]", v.MIMEType))
		case mcp.ResourceLink:
			parts = append(parts, fmt.Sprintf("[resource uri=%s name=%s]", v.URI, v.Name))
		case *mcp.ResourceLink:
			parts = append(parts, fmt.Sprintf("[resource uri=%s name=%s]", v.URI, v.Name))
		case mcp.EmbeddedResource:
			if raw, err := sonic.Marshal(v); err == nil {
				parts = append(parts, util.ByteToString(raw))
			}
		case *mcp.EmbeddedResource:
			if raw, err := sonic.Marshal(v); err == nil {
				parts = append(parts, util.ByteToString(raw))
			}
		default:
			if c != nil {
				if raw, err := sonic.Marshal(c); err == nil {
					parts = append(parts, util.ByteToString(raw))
				}
			}
		}
	}

	if len(parts) == 0 {
		if raw, err := sonic.MarshalIndent(r, "", "  "); err == nil {
			return util.ByteToString(raw)
		}
		return ""
	}

	joined := strings.Join(parts, "\n")
	if r.IsError {
		return fmt.Sprintf("error: %s", joined)
	}

	return joined
}

func inputSchemaToMap(t mcp.Tool) ToolParameters {
	if t.RawInputSchema != nil {
		var raw map[string]any
		if err := sonic.Unmarshal(t.RawInputSchema, &raw); err == nil {
			// Ensure object types have properties field
			if rawType, ok := raw["type"].(string); ok && rawType == "object" {
				if _, hasProps := raw["properties"]; !hasProps {
					raw["properties"] = map[string]any{}
				}
			}
			return raw
		}
	}

	if t.InputSchema.Type == "" {
		return nil
	}

	res := map[string]any{
		"type": t.InputSchema.Type,
	}

	// For object types, always include properties field (even if empty)
	// This is required by OpenAI/Anthropic APIs
	if t.InputSchema.Type == "object" {
		if len(t.InputSchema.Properties) > 0 {
			res["properties"] = t.InputSchema.Properties
		} else {
			res["properties"] = map[string]any{}
		}
	}

	if len(t.InputSchema.Required) > 0 {
		res["required"] = t.InputSchema.Required
	}

	if len(t.InputSchema.Defs) > 0 {
		res["$defs"] = t.InputSchema.Defs
	}

	return ToolParameters(res)
}
