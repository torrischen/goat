package openai

import (
	"github.com/torrischen/goat/agent/message"
	"github.com/torrischen/goat/llm"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
)

// splitSystem lifts a leading system message's text into the Responses
// "instructions" string, returning the remaining messages. Only the first
// message is considered, matching how the agent loops place the system prompt.
func splitSystem(messages []*message.Message) (string, []*message.Message) {
	if len(messages) > 0 && messages[0] != nil && messages[0].Role == message.RoleSystem {
		return messages[0].Text(), messages[1:]
	}
	return "", messages
}

// encodeInput converts goat messages into a Responses API input item list.
func encodeInput(messages []*message.Message) responses.ResponseInputParam {
	var items responses.ResponseInputParam
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		items = append(items, encodeMessage(msg)...)
	}
	return items
}

// encodeMessage converts one goat message into one or more Responses input
// items, preserving block order.
func encodeMessage(msg *message.Message) []responses.ResponseInputItemUnionParam {
	switch msg.Role {
	case message.RoleSystem:
		// A non-leading system message becomes a developer message.
		return []responses.ResponseInputItemUnionParam{
			responses.ResponseInputItemParamOfMessage(msg.Text(), responses.EasyInputMessageRoleDeveloper),
		}
	case message.RoleAssistant:
		return encodeAssistantBlocks(msg)
	default:
		return encodeUserBlocks(msg)
	}
}

// encodeUserBlocks converts a user-role message. Text and image blocks coalesce
// into a single user message item; tool-result blocks become
// function_call_output items.
func encodeUserBlocks(msg *message.Message) []responses.ResponseInputItemUnionParam {
	var items []responses.ResponseInputItemUnionParam
	var content responses.ResponseInputMessageContentListParam

	for _, block := range msg.Blocks {
		if block == nil {
			continue
		}
		switch block.Kind {
		case message.BlockText:
			if block.Text != nil && block.Text.Text != "" {
				content = append(content, responses.ResponseInputContentParamOfInputText(block.Text.Text))
			}
		case message.BlockImage:
			if block.Image != nil {
				content = append(content, encodeImageContent(block.Image))
			}
		case message.BlockToolResult:
			if block.ToolResult != nil {
				items = append(items, responses.ResponseInputItemParamOfFunctionCallOutput(
					block.ToolResult.CallID,
					block.ToolResult.Text(),
				))
			}
		}
	}

	if len(content) > 0 {
		userItem := responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRoleUser)
		// Tool-result outputs must precede a following user message only when they
		// belong to a prior turn; within one message we emit outputs first, then
		// the user content, matching arrival order of separate messages.
		items = append(items, userItem)
	}
	return items
}

// encodeAssistantBlocks converts an assistant-role message into reasoning,
// message, and function_call input items in block order.
func encodeAssistantBlocks(msg *message.Message) []responses.ResponseInputItemUnionParam {
	var items []responses.ResponseInputItemUnionParam
	for _, block := range msg.Blocks {
		if block == nil {
			continue
		}
		switch block.Kind {
		case message.BlockReasoning:
			if item, ok := encodeReasoning(block); ok {
				items = append(items, item)
			}
		case message.BlockText:
			if block.Text != nil && block.Text.Text != "" {
				items = append(items, responses.ResponseInputItemParamOfMessage(
					block.Text.Text, responses.EasyInputMessageRoleAssistant))
			}
		case message.BlockToolCall:
			if block.ToolCall != nil {
				items = append(items, responses.ResponseInputItemParamOfFunctionCall(
					block.ToolCall.Arguments,
					block.ToolCall.CallID,
					block.ToolCall.Name,
				))
			}
		}
	}
	return items
}

// encodeReasoning reconstructs a Responses reasoning input item from a reasoning
// block. A reasoning item only round-trips when its OpenAI item id is present
// (carried in the block's provider namespace); otherwise it is dropped, since
// the API rejects reasoning items without an id.
func encodeReasoning(block *message.ContentBlock) (responses.ResponseInputItemUnionParam, bool) {
	meta := decodeReasoningMeta(block)
	if meta == nil || meta.ItemID == "" {
		return responses.ResponseInputItemUnionParam{}, false
	}

	// Responses requires the reasoning input item's summary field, even when
	// the provider returned encrypted reasoning without a visible summary.
	// Keep one empty summary part so the SDK serializes `summary` instead of
	// omitting the required field.
	summary := []responses.ResponseReasoningItemSummaryParam{{Text: ""}}
	if block.Reasoning != nil && block.Reasoning.Text != "" {
		summary[0].Text = block.Reasoning.Text
	}

	item := responses.ResponseInputItemParamOfReasoning(meta.ItemID, summary)
	if meta.EncryptedContent != "" && item.OfReasoning != nil {
		item.OfReasoning.EncryptedContent = param.NewOpt(meta.EncryptedContent)
	}
	return item, true
}

// encodeImageContent converts an image block into a Responses input_image
// content part. A base64 payload is emitted as a data: URL.
func encodeImageContent(img *message.ImageData) responses.ResponseInputContentUnionParam {
	detail := responses.ResponseInputImageDetail(img.Detail)
	if detail == "" {
		detail = responses.ResponseInputImageDetailAuto
	}
	part := responses.ResponseInputContentParamOfInputImage(detail)
	if part.OfInputImage != nil {
		if img.URL != "" {
			part.OfInputImage.ImageURL = param.NewOpt(img.URL)
		} else if img.Base64Data != "" {
			mime := img.MIMEType
			if mime == "" {
				mime = "image/png"
			}
			part.OfInputImage.ImageURL = param.NewOpt("data:" + mime + ";base64," + img.Base64Data)
		}
	}
	return part
}

// encodeTools converts neutral tool definitions into Responses function tools.
func encodeTools(tools []llm.ToolDef) []responses.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	out := make([]responses.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tool := responses.ToolParamOfFunction(t.Name, params, false)
		if tool.OfFunction != nil && t.Description != "" {
			tool.OfFunction.Description = param.NewOpt(t.Description)
		}
		out = append(out, tool)
	}
	return out
}
