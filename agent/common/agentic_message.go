package common

import "github.com/torrischen/goat/agent/message"

// These helpers forward to the provider-neutral agent/message package. They are
// retained so existing call sites in common's dependents keep working; new code
// may call the message package directly.

func TextBlock(text string) *message.ContentBlock {
	return message.TextBlock(text)
}

func ReasoningBlock(text string) *message.ContentBlock {
	return message.ReasoningBlock(text)
}

func ImageURLBlock(url string) *message.ContentBlock {
	return message.ImageURLBlock(url)
}

func ImageURLWithDetailBlock(url, detail string) *message.ContentBlock {
	return message.ImageURLWithDetailBlock(url, detail)
}

func BinaryImageBlock(mimeType string, data []byte) *message.ContentBlock {
	return message.BinaryImageBlock(mimeType, data)
}

func Base64ImageBlock(mimeType, base64Data string) *message.ContentBlock {
	return message.Base64ImageBlock(mimeType, base64Data)
}

func TextMessage(role message.Role, text string) *message.Message {
	return message.TextMessage(role, text)
}

func AssistantTextMessage(text string) *message.Message {
	return message.AssistantTextMessage(text)
}

func FunctionToolResultMessage(result *message.ToolResult) *message.Message {
	return message.FunctionToolResultMessage(result)
}

func CloneAgenticMessages(messages []*message.Message) []*message.Message {
	return message.Clone(messages)
}
