package anthropic

import (
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/torrischen/goat/agent/message"
)

func decodeResponse(resp *anthropic.Message) *message.Message {
	msg := &message.Message{Role: message.RoleAssistant}
	if resp == nil {
		return msg
	}
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				msg.Blocks = append(msg.Blocks, message.TextBlock(block.Text))
			}
		case "thinking":
			msg.Blocks = append(msg.Blocks, &message.ContentBlock{
				Kind: message.BlockReasoning,
				Reasoning: &message.ReasoningData{
					Text:      block.Thinking,
					Signature: block.Signature,
				},
			})
		case "redacted_thinking":
			if block.Data != "" {
				msg.Blocks = append(msg.Blocks, &message.ContentBlock{
					Kind:      message.BlockReasoning,
					Reasoning: &message.ReasoningData{},
					Provider:  encodeRedactedThinkingMeta(block.Data),
				})
			}
		case "tool_use":
			arguments := string(block.Input)
			if arguments == "" {
				arguments = "{}"
			}
			msg.Blocks = append(msg.Blocks, &message.ContentBlock{
				Kind: message.BlockToolCall,
				ToolCall: &message.ToolCall{
					CallID:    block.ID,
					Name:      block.Name,
					Arguments: arguments,
				},
			})
		case "":
			// A manually constructed SDK block may not carry a discriminator.
			// There is no useful neutral representation without one.
		default:
			// Keep provider blocks that goat does not model. They are intentionally
			// opaque; the neutral agent will not mistake server-side tools for a
			// client tool call.
			if raw := block.RawJSON(); raw != "" {
				msg.Blocks = append(msg.Blocks, &message.ContentBlock{
					Kind:   message.BlockOpaque,
					Opaque: []byte(raw),
				})
			}
		}
	}
	msg.Meta = &message.ResponseMeta{Usage: decodeUsage(resp.Usage)}
	return msg
}

func decodeUsage(usage anthropic.Usage) *message.Usage {
	prompt := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	return &message.Usage{
		PromptTokens:     int(prompt),
		CompletionTokens: int(usage.OutputTokens),
		CachedTokens:     int(usage.CacheReadInputTokens),
	}
}
