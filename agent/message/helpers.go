package message

import "encoding/base64"

// TextBlock builds a text content block (used for user or system text).
func TextBlock(text string) *ContentBlock {
	return &ContentBlock{Kind: BlockText, Text: &TextData{Text: text}}
}

// ReasoningBlock builds a reasoning content block.
func ReasoningBlock(text string) *ContentBlock {
	return &ContentBlock{Kind: BlockReasoning, Reasoning: &ReasoningData{Text: text}}
}

// ImageURLBlock builds an image block from an HTTP(S) URL.
func ImageURLBlock(url string) *ContentBlock {
	return ImageURLWithDetailBlock(url, "")
}

// ImageURLWithDetailBlock builds an image block from a URL with a fidelity hint.
func ImageURLWithDetailBlock(url, detail string) *ContentBlock {
	return &ContentBlock{Kind: BlockImage, Image: &ImageData{URL: url, Detail: detail}}
}

// BinaryImageBlock builds an image block from raw bytes.
func BinaryImageBlock(mimeType string, data []byte) *ContentBlock {
	return &ContentBlock{Kind: BlockImage, Image: &ImageData{
		Base64Data: base64.StdEncoding.EncodeToString(data),
		MIMEType:   mimeType,
	}}
}

// Base64ImageBlock builds an image block from a base64-encoded string.
func Base64ImageBlock(mimeType, base64Data string) *ContentBlock {
	return &ContentBlock{Kind: BlockImage, Image: &ImageData{
		Base64Data: base64Data,
		MIMEType:   mimeType,
	}}
}

// SystemMessage builds a system message with a single text block.
func SystemMessage(text string) *Message {
	return &Message{Role: RoleSystem, Blocks: []*ContentBlock{TextBlock(text)}}
}

// UserMessage builds a user message with a single text block.
func UserMessage(text string) *Message {
	return &Message{Role: RoleUser, Blocks: []*ContentBlock{TextBlock(text)}}
}

// AssistantTextMessage builds an assistant message with a single text block.
func AssistantTextMessage(text string) *Message {
	return &Message{Role: RoleAssistant, Blocks: []*ContentBlock{TextBlock(text)}}
}

// TextMessage builds a message with a single text block for the given role.
func TextMessage(role Role, text string) *Message {
	switch role {
	case RoleAssistant:
		return AssistantTextMessage(text)
	case RoleSystem:
		return SystemMessage(text)
	default:
		return UserMessage(text)
	}
}

// FunctionToolResultMessage wraps a tool result as a user-role message, matching
// how tool results are fed back into the conversation.
func FunctionToolResultMessage(result *ToolResult) *Message {
	return &Message{
		Role:   RoleUser,
		Blocks: []*ContentBlock{{Kind: BlockToolResult, ToolResult: result}},
	}
}

// Clone returns a shallow copy of the message slice. Messages are treated as
// immutable once appended to the conversation, so element pointers are shared;
// this matches the previous CloneAgenticMessages semantics.
func Clone(messages []*Message) []*Message {
	if len(messages) == 0 {
		return []*Message{}
	}
	result := make([]*Message, len(messages))
	copy(result, messages)
	return result
}
