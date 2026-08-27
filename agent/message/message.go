// Package message defines goat's own provider-neutral message model.
//
// It is the shared currency between the context manager (persistence), the
// public agent API (common), and the react/planexecute loops. It intentionally
// depends on NO LLM provider library: neither cloudwego/eino nor goai. Provider
// specifics are confined to the llmbridge codec, which converts between this
// model and a concrete provider's message type.
//
// Anything a provider requires to be echoed back verbatim (reasoning
// signatures, OpenAI response/item ids, Gemini thought signatures) is carried
// as opaque bytes in ContentBlock.Provider and never interpreted here.
package message

import "encoding/json"

// Role is the author of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockKind identifies which payload a ContentBlock carries.
type BlockKind string

const (
	// BlockText is user-authored or assistant-generated text. The Role of the
	// enclosing Message distinguishes the two.
	BlockText BlockKind = "text"
	// BlockImage is image content, provided by the user or generated.
	BlockImage BlockKind = "image"
	// BlockReasoning is model reasoning / thinking content.
	BlockReasoning BlockKind = "reasoning"
	// BlockToolCall is a function tool invocation produced by the assistant.
	BlockToolCall BlockKind = "tool_call"
	// BlockToolResult is the result of a function tool call, fed back to the model.
	BlockToolResult BlockKind = "tool_result"
	// BlockOpaque is a whole content block a provider produced that this model
	// does not explicitly type. The codec round-trips it verbatim so no data is
	// lost and migration is not blocked on modeling every provider construct.
	BlockOpaque BlockKind = "opaque"
)

// Message is a single conversation message.
type Message struct {
	Role   Role            `json:"role"`
	Blocks []*ContentBlock `json:"blocks,omitempty"`
	Meta   *ResponseMeta   `json:"meta,omitempty"`
	// Extra carries message-level provider data that must round-trip verbatim,
	// namespaced by provider (e.g. "openai", "gemini"). Never interpreted here.
	Extra map[string]json.RawMessage `json:"extra,omitempty"`
}

// ContentBlock is one piece of a message's content. Kind selects which of the
// typed payload fields is populated (at most one), except BlockOpaque which
// uses Opaque.
type ContentBlock struct {
	Kind BlockKind `json:"kind"`

	Text       *TextData       `json:"text,omitempty"`
	Image      *ImageData      `json:"image,omitempty"`
	Reasoning  *ReasoningData  `json:"reasoning,omitempty"`
	ToolCall   *ToolCall       `json:"tool_call,omitempty"`
	ToolResult *ToolResult     `json:"tool_result,omitempty"`
	Opaque     json.RawMessage `json:"opaque,omitempty"`

	// Provider carries block-level opaque data a provider requires echoed back
	// (reasoning signatures, OpenAI item ids, Gemini thought signatures),
	// namespaced by provider. Stored and round-tripped as raw bytes; the
	// persistence layer never decodes it.
	Provider map[string]json.RawMessage `json:"provider,omitempty"`
}

// TextData is plain text content.
type TextData struct {
	Text string `json:"text,omitempty"`
}

// ImageData is image content. Exactly one of URL or Base64Data is typically set.
type ImageData struct {
	URL        string `json:"url,omitempty"`
	Base64Data string `json:"base64_data,omitempty"`
	MIMEType   string `json:"mime_type,omitempty"`
	// Detail is the requested image fidelity ("low"/"high"/"auto"); provider-specific.
	Detail string `json:"detail,omitempty"`
}

// ReasoningData is model reasoning content.
type ReasoningData struct {
	Text string `json:"text,omitempty"`
	// Signature carries encrypted reasoning tokens some providers require when
	// reasoning text is passed back. Opaque; not interpreted here.
	Signature string `json:"signature,omitempty"`
}

// ToolCall is a function tool invocation produced by the assistant.
type ToolCall struct {
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"` // JSON-encoded arguments
}

// ToolResultBlockKind identifies which media a ToolResultContent carries.
type ToolResultBlockKind string

const (
	ToolResultText  ToolResultBlockKind = "text"
	ToolResultImage ToolResultBlockKind = "image"
)

// ToolResultContent is one piece of a tool result (text or image).
type ToolResultContent struct {
	Kind  ToolResultBlockKind `json:"kind"`
	Text  *TextData           `json:"text,omitempty"`
	Image *ImageData          `json:"image,omitempty"`
}

// ToolResult is the outcome of a function tool call, fed back to the model.
type ToolResult struct {
	CallID  string               `json:"call_id,omitempty"`
	Name    string               `json:"name"`
	Content []*ToolResultContent `json:"content,omitempty"`
}

// ResponseMeta is model-response metadata attached to an assistant message.
type ResponseMeta struct {
	Usage *Usage `json:"usage,omitempty"`
	// Provider carries response-level opaque data namespaced by provider.
	Provider map[string]json.RawMessage `json:"provider,omitempty"`
}

// Usage is token accounting for a model response.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
}
