package react

import (
	"hash/fnv"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/message"
)

func userInputMessage(input common.AgentUserInput) *message.Message {
	parts := []*message.ContentBlock{message.TextBlock(input.Text)}
	parts = append(parts, input.Images...)
	return &message.Message{
		Role:   message.RoleUser,
		Blocks: parts,
	}
}

func messageTokens(msg *message.Message) (int, int, int) {
	return msg.Tokens()
}

func assistantText(msg *message.Message) string {
	return msg.Text()
}

func messageReasoning(msg *message.Message) string {
	return msg.Reasoning()
}

func functionToolCalls(msg *message.Message) []*message.ToolCall {
	return msg.ToolCalls()
}

func toolResultContentBlocks(observation string, images []*message.ContentBlock) []*message.ToolResultContent {
	return message.NewToolResultContent(observation, images)
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
func extractSystemMessageText(msg *message.Message) string {
	if msg == nil || msg.Role != message.RoleSystem {
		return ""
	}
	return msg.Text()
}
