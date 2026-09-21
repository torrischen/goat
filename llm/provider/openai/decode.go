package openai

import (
	"strings"

	"github.com/torrischen/goat/agent/message"

	"github.com/openai/openai-go/v3/responses"
)

// summarySeparator joins multiple reasoning summary segments, matching how the
// Responses API delimits summaries only by index.
const summarySeparator = "\n\n"

// decodeResponse converts a completed Responses API response into a goat
// assistant message, preserving reasoning round-trip data and usage.
func decodeResponse(resp *responses.Response) *message.Message {
	msg := &message.Message{Role: message.RoleAssistant}
	if resp == nil {
		return msg
	}

	for _, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			if block := decodeReasoningItem(item.AsReasoning()); block != nil {
				msg.Blocks = append(msg.Blocks, block)
			}
		case "message":
			if text := outputMessageText(item.AsMessage()); text != "" {
				msg.Blocks = append(msg.Blocks, message.TextBlock(text))
			}
		case "function_call":
			call := item.AsFunctionCall()
			msg.Blocks = append(msg.Blocks, &message.ContentBlock{
				Kind: message.BlockToolCall,
				ToolCall: &message.ToolCall{
					CallID:    call.CallID,
					Name:      call.Name,
					Arguments: call.Arguments,
				},
			})
		}
	}

	msg.Meta = &message.ResponseMeta{Usage: decodeUsage(resp.Usage)}
	return msg
}

// decodeReasoningItem converts a reasoning output item into a reasoning block,
// carrying the item id + encrypted content for round-tripping. Returns nil when
// there is neither summary text nor an id/encrypted payload to preserve.
func decodeReasoningItem(item responses.ResponseReasoningItem) *message.ContentBlock {
	var parts []string
	for _, s := range item.Summary {
		if s.Text != "" {
			parts = append(parts, s.Text)
		}
	}
	text := strings.Join(parts, summarySeparator)

	if text == "" && item.ID == "" && item.EncryptedContent == "" {
		return nil
	}

	block := &message.ContentBlock{
		Kind:      message.BlockReasoning,
		Reasoning: &message.ReasoningData{Text: text},
	}
	if meta := encodeReasoningMeta(item.ID, item.EncryptedContent); meta != nil {
		block.Provider = meta
	}
	return block
}

// outputMessageText concatenates the output_text content of an assistant
// message output item.
func outputMessageText(msg responses.ResponseOutputMessage) string {
	var b strings.Builder
	for _, content := range msg.Content {
		if content.Type == "output_text" {
			b.WriteString(content.AsOutputText().Text)
		}
	}
	return b.String()
}

// decodeUsage maps Responses usage into goat usage. Cached tokens are reported
// separately (as goat's CachedTokens) and left included in prompt tokens.
func decodeUsage(u responses.ResponseUsage) *message.Usage {
	return &message.Usage{
		PromptTokens:     int(u.InputTokens),
		CompletionTokens: int(u.OutputTokens),
		CachedTokens:     int(u.InputTokensDetails.CachedTokens),
	}
}
