package anthropic

import (
	"encoding/json"

	anthropicapi "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm"
)

// splitSystem lifts system messages into Anthropic's top-level system field.
// The Messages API has no ordinary message-level system role, so all system
// text blocks are collected while the remaining conversation order is kept.
// The last system block is marked for caching to enable prompt caching.
func splitSystem(messages []*message.Message) ([]anthropicapi.TextBlockParam, []*message.Message) {
	var system []anthropicapi.TextBlockParam
	rest := make([]*message.Message, 0, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role != message.RoleSystem {
			rest = append(rest, msg)
			continue
		}
		for _, block := range msg.Blocks {
			if block == nil || block.Kind != message.BlockText || block.Text == nil {
				continue
			}
			if block.Text.Text != "" {
				system = append(system, anthropicapi.TextBlockParam{Text: block.Text.Text})
			}
		}
	}
	// Mark the last system block for caching if present
	if len(system) > 0 {
		system[len(system)-1].CacheControl = anthropicapi.NewCacheControlEphemeralParam()
	}
	return system, rest
}

func encodeMessages(messages []*message.Message) []anthropicapi.MessageParam {
	encoded := make([]anthropicapi.MessageParam, 0, len(messages))
	for _, msg := range messages {
		if msg == nil || msg.Role == message.RoleSystem {
			continue
		}
		content := encodeContentBlocks(msg)
		if len(content) == 0 {
			continue
		}

		role := anthropicapi.MessageParamRoleUser
		if msg.Role == message.RoleAssistant {
			role = anthropicapi.MessageParamRoleAssistant
		}
		current := anthropicapi.MessageParam{Role: role, Content: content}
		// A tool turn can produce several tool-result messages in goat's neutral
		// history. Anthropic requires all results for one assistant turn in one
		// user message, so coalesce adjacent result-only messages.
		if len(encoded) > 0 && isToolResultMessage(encoded[len(encoded)-1]) && isToolResultMessage(current) {
			encoded[len(encoded)-1].Content = append(encoded[len(encoded)-1].Content, current.Content...)
			continue
		}
		encoded = append(encoded, current)
	}

	// Mark recent conversation turns for caching
	// Cache the last 2-3 turns (user-assistant pairs) to enable efficient caching
	// across multiple agent steps
	markConversationForCaching(encoded)

	return encoded
}

func isToolResultMessage(msg anthropicapi.MessageParam) bool {
	if msg.Role != anthropicapi.MessageParamRoleUser || len(msg.Content) == 0 {
		return false
	}
	for _, block := range msg.Content {
		if block.OfToolResult == nil {
			return false
		}
	}
	return true
}

func encodeContentBlocks(msg *message.Message) []anthropicapi.ContentBlockParamUnion {
	content := make([]anthropicapi.ContentBlockParamUnion, 0, len(msg.Blocks))
	for _, block := range msg.Blocks {
		if block == nil {
			continue
		}
		switch block.Kind {
		case message.BlockText:
			if block.Text != nil && block.Text.Text != "" {
				content = append(content, anthropicapi.NewTextBlock(block.Text.Text))
			}
		case message.BlockImage:
			if image, ok := encodeImage(block.Image); ok {
				content = append(content, image)
			}
		case message.BlockReasoning:
			if msg.Role == message.RoleAssistant {
				if reasoning, ok := encodeReasoning(block); ok {
					content = append(content, reasoning)
				}
			}
		case message.BlockToolCall:
			if msg.Role == message.RoleAssistant && block.ToolCall != nil {
				content = append(content, encodeToolCall(block.ToolCall))
			}
		case message.BlockToolResult:
			if msg.Role != message.RoleAssistant {
				if result, ok := encodeToolResult(block.ToolResult); ok {
					content = append(content, result)
				}
			}
		}
	}
	return content
}

func encodeImage(img *message.ImageData) (anthropicapi.ContentBlockParamUnion, bool) {
	if img == nil {
		return anthropicapi.ContentBlockParamUnion{}, false
	}
	if img.URL != "" {
		return anthropicapi.NewImageBlock(anthropicapi.URLImageSourceParam{URL: img.URL}), true
	}
	if img.Base64Data == "" {
		return anthropicapi.ContentBlockParamUnion{}, false
	}
	mimeType := img.MIMEType
	if mimeType == "" {
		mimeType = "image/png"
	}
	return anthropicapi.NewImageBlock(anthropicapi.Base64ImageSourceParam{
		Data:      img.Base64Data,
		MediaType: anthropicapi.Base64ImageSourceMediaType(mimeType),
	}), true
}

func encodeReasoning(block *message.ContentBlock) (anthropicapi.ContentBlockParamUnion, bool) {
	if meta := decodeRedactedThinkingMeta(block); meta != nil {
		return anthropicapi.NewRedactedThinkingBlock(meta.Data), true
	}
	if block == nil || block.Reasoning == nil || block.Reasoning.Signature == "" {
		// Anthropic rejects ordinary thinking blocks without the signature that
		// authenticates them as model-generated content.
		return anthropicapi.ContentBlockParamUnion{}, false
	}
	return anthropicapi.NewThinkingBlock(block.Reasoning.Signature, block.Reasoning.Text), true
}

func encodeToolCall(call *message.ToolCall) anthropicapi.ContentBlockParamUnion {
	arguments := call.Arguments
	if arguments == "" {
		arguments = "{}"
	}
	return anthropicapi.NewToolUseBlock(call.CallID, json.RawMessage(arguments), call.Name)
}

func encodeToolResult(result *message.ToolResult) (anthropicapi.ContentBlockParamUnion, bool) {
	if result == nil {
		return anthropicapi.ContentBlockParamUnion{}, false
	}
	toolResult := &anthropicapi.ToolResultBlockParam{ToolUseID: result.CallID}
	for _, part := range result.Content {
		if part == nil {
			continue
		}
		switch part.Kind {
		case message.ToolResultText:
			if part.Text != nil {
				toolResult.Content = append(toolResult.Content, anthropicapi.ToolResultBlockParamContentUnion{
					OfText: &anthropicapi.TextBlockParam{Text: part.Text.Text},
				})
			}
		case message.ToolResultImage:
			if image, ok := encodeImage(part.Image); ok {
				toolResult.Content = append(toolResult.Content, anthropicapi.ToolResultBlockParamContentUnion{
					OfImage: image.OfImage,
				})
			}
		}
	}
	return anthropicapi.ContentBlockParamUnion{OfToolResult: toolResult}, true
}

func encodeTools(tools []llm.ToolDef) []anthropicapi.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	encoded := make([]anthropicapi.ToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		inputSchema := toolInputSchema(tool.Parameters)
		paramTool := anthropicapi.ToolUnionParamOfTool(inputSchema, tool.Name)
		if paramTool.OfTool != nil && tool.Description != "" {
			paramTool.OfTool.Description = param.NewOpt(tool.Description)
		}
		encoded = append(encoded, paramTool)
	}
	// Mark the last tool for caching to enable tool definition caching
	// This is especially useful when tools remain constant across multiple agent steps
	if len(encoded) > 0 && encoded[len(encoded)-1].OfTool != nil {
		encoded[len(encoded)-1].OfTool.CacheControl = anthropicapi.NewCacheControlEphemeralParam()
	}
	return encoded
}

func toolInputSchema(schema map[string]any) anthropicapi.ToolInputSchemaParam {
	if schema == nil {
		schema = map[string]any{}
	}
	properties := schema["properties"]
	if properties == nil {
		properties = map[string]any{}
	}
	result := anthropicapi.ToolInputSchemaParam{Properties: properties}
	if required, ok := stringSlice(schema["required"]); ok {
		result.Required = required
	}

	// ToolInputSchemaParam exposes the common schema fields directly. Preserve
	// other JSON Schema keywords through its extra-field support.
	extra := make(map[string]any)
	for key, value := range schema {
		switch key {
		case "type", "properties", "required":
			continue
		default:
			extra[key] = value
		}
	}
	if len(extra) > 0 {
		result.ExtraFields = extra
	}
	return result
}

func stringSlice(value any) ([]string, bool) {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...), true
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			item, ok := value.(string)
			if !ok {
				return nil, false
			}
			result = append(result, item)
		}
		return result, true
	default:
		return nil, false
	}
}

// markConversationForCaching adds cache_control breakpoints to recent conversation
// turns to enable Anthropic's prompt caching. This marks the last few turns so that
// subsequent agent steps can reuse the cached conversation context.
func markConversationForCaching(messages []anthropicapi.MessageParam) {
	if len(messages) == 0 {
		return
	}

	// Mark the last user message's last content block for caching
	// This creates a cache breakpoint after the most recent user input
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == anthropicapi.MessageParamRoleUser && len(messages[i].Content) > 0 {
			lastContent := &messages[i].Content[len(messages[i].Content)-1]
			markContentBlockForCaching(lastContent)
			break
		}
	}

	// Also mark tools in the last assistant turn if present
	// This helps cache tool definitions across multiple calls
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == anthropicapi.MessageParamRoleAssistant {
			for j := len(messages[i].Content) - 1; j >= 0; j-- {
				if messages[i].Content[j].OfToolUse != nil {
					markContentBlockForCaching(&messages[i].Content[j])
					break
				}
			}
			break
		}
	}
}

// markContentBlockForCaching adds a cache_control breakpoint to a content block
func markContentBlockForCaching(block *anthropicapi.ContentBlockParamUnion) {
	cacheControl := anthropicapi.NewCacheControlEphemeralParam()

	if block.OfText != nil {
		block.OfText.CacheControl = cacheControl
	} else if block.OfImage != nil {
		block.OfImage.CacheControl = cacheControl
	} else if block.OfToolUse != nil {
		block.OfToolUse.CacheControl = cacheControl
	} else if block.OfToolResult != nil {
		block.OfToolResult.CacheControl = cacheControl
	}
}
