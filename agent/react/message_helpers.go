package react

import (
	"hash/fnv"
	"strings"

	"github.com/torrischen/goat/agent/common"

	"github.com/cloudwego/eino/schema"
)

func userInputMessage(input common.AgentUserInput) *schema.AgenticMessage {
	parts := []*schema.ContentBlock{common.TextBlock(input.Text)}
	parts = append(parts, input.Images...)
	return &schema.AgenticMessage{
		Role:          schema.AgenticRoleTypeUser,
		ContentBlocks: parts,
	}
}

func messageTokens(msg *schema.AgenticMessage) (int, int, int) {
	if msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.TokenUsage == nil {
		return 0, 0, 0
	}
	usage := msg.ResponseMeta.TokenUsage
	return usage.PromptTokens, usage.CompletionTokens, usage.PromptTokenDetails.CachedTokens
}

func assistantText(msg *schema.AgenticMessage) string {
	if msg == nil {
		return ""
	}

	var parts []string
	for _, block := range msg.ContentBlocks {
		if block == nil || block.AssistantGenText == nil {
			continue
		}
		parts = append(parts, block.AssistantGenText.Text)
	}
	return strings.Join(parts, "")
}

func messageReasoning(msg *schema.AgenticMessage) string {
	if msg == nil {
		return ""
	}

	var parts []string
	for _, block := range msg.ContentBlocks {
		if block == nil || block.Reasoning == nil {
			continue
		}
		parts = append(parts, block.Reasoning.Text)
	}
	return strings.Join(parts, "\n")
}

func messagePlainText(msg *schema.AgenticMessage) string {
	if msg == nil {
		return ""
	}

	var parts []string
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		switch {
		case block.UserInputText != nil:
			parts = append(parts, block.UserInputText.Text)
		case block.AssistantGenText != nil:
			parts = append(parts, block.AssistantGenText.Text)
		case block.Reasoning != nil:
			parts = append(parts, block.Reasoning.Text)
		case block.FunctionToolCall != nil:
			parts = append(parts, block.FunctionToolCall.Name, block.FunctionToolCall.Arguments)
		case block.FunctionToolResult != nil:
			parts = append(parts, functionToolResultText(block.FunctionToolResult))
		}
	}

	return strings.Join(parts, " ")
}

func functionToolCalls(msg *schema.AgenticMessage) []*schema.FunctionToolCall {
	if msg == nil {
		return nil
	}

	calls := make([]*schema.FunctionToolCall, 0)
	for _, block := range msg.ContentBlocks {
		if block == nil || block.FunctionToolCall == nil {
			continue
		}
		calls = append(calls, block.FunctionToolCall)
	}
	return calls
}

func functionToolResultText(result *schema.FunctionToolResult) string {
	if result == nil {
		return ""
	}

	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		if block == nil {
			continue
		}
		if block.Type == schema.FunctionToolResultContentBlockTypeText && block.Text != nil {
			parts = append(parts, block.Text.Text)
		}
	}
	return strings.Join(parts, " ")
}

func toolResultContentBlocks(observation string, images []*schema.ContentBlock) []*schema.FunctionToolResultContentBlock {
	blocks := []*schema.FunctionToolResultContentBlock{
		{
			Type: schema.FunctionToolResultContentBlockTypeText,
			Text: &schema.UserInputText{Text: observation},
		},
	}

	for _, image := range images {
		if image == nil || image.UserInputImage == nil {
			continue
		}
		blocks = append(blocks, &schema.FunctionToolResultContentBlock{
			Type:  schema.FunctionToolResultContentBlockTypeImage,
			Image: image.UserInputImage,
		})
	}

	return blocks
}

// hashSystemPrompt computes a 64-bit FNV-1a hash of a system prompt string.
// This hash is used to efficiently detect when system prompt content has changed,
// enabling prompt cache optimization by avoiding unnecessary message replacements.
func hashSystemPrompt(prompt string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(prompt))
	return h.Sum64()
}

// extractSystemMessageText extracts the text content from a system message.
// Returns an empty string if the message is nil or not a system message.
func extractSystemMessageText(msg *schema.AgenticMessage) string {
	if msg == nil || msg.Role != schema.AgenticRoleTypeSystem {
		return ""
	}

	var text strings.Builder
	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		if block.AssistantGenText != nil {
			text.WriteString(block.AssistantGenText.Text)
		}
	}
	return text.String()
}
