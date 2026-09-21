package message

import "strings"

// Text returns the concatenated text of all text blocks in the message.
// It applies to both user and assistant messages (Role disambiguates authorship).
func (m *Message) Text() string {
	if m == nil {
		return ""
	}
	var parts []string
	for _, b := range m.Blocks {
		if b == nil || b.Kind != BlockText || b.Text == nil {
			continue
		}
		parts = append(parts, b.Text.Text)
	}
	return strings.Join(parts, "")
}

// Reasoning returns the concatenated reasoning text of the message.
func (m *Message) Reasoning() string {
	if m == nil {
		return ""
	}
	var parts []string
	for _, b := range m.Blocks {
		if b == nil || b.Kind != BlockReasoning || b.Reasoning == nil {
			continue
		}
		parts = append(parts, b.Reasoning.Text)
	}
	return strings.Join(parts, "\n")
}

// ToolCalls returns all tool-call blocks in the message.
func (m *Message) ToolCalls() []*ToolCall {
	if m == nil {
		return nil
	}
	calls := make([]*ToolCall, 0)
	for _, b := range m.Blocks {
		if b == nil || b.Kind != BlockToolCall || b.ToolCall == nil {
			continue
		}
		calls = append(calls, b.ToolCall)
	}
	return calls
}

// PlainText returns a space-joined textual rendering of every block, used for
// coarse token estimation and compression heuristics.
func (m *Message) PlainText() string {
	if m == nil {
		return ""
	}
	var parts []string
	for _, b := range m.Blocks {
		if b == nil {
			continue
		}
		switch b.Kind {
		case BlockText:
			if b.Text != nil {
				parts = append(parts, b.Text.Text)
			}
		case BlockReasoning:
			if b.Reasoning != nil {
				parts = append(parts, b.Reasoning.Text)
			}
		case BlockToolCall:
			if b.ToolCall != nil {
				parts = append(parts, b.ToolCall.Name, b.ToolCall.Arguments)
			}
		case BlockToolResult:
			if b.ToolResult != nil {
				parts = append(parts, b.ToolResult.Text())
			}
		}
	}
	return strings.Join(parts, " ")
}

// Tokens returns (prompt, completion, cached) token counts from the message's
// response metadata, or zeros when absent.
func (m *Message) Tokens() (prompt, completion, cached int) {
	if m == nil || m.Meta == nil || m.Meta.Usage == nil {
		return 0, 0, 0
	}
	u := m.Meta.Usage
	return u.PromptTokens, u.CompletionTokens, u.CachedTokens
}

// Text returns the concatenated text content of a tool result.
func (r *ToolResult) Text() string {
	if r == nil {
		return ""
	}
	parts := make([]string, 0, len(r.Content))
	for _, c := range r.Content {
		if c == nil || c.Kind != ToolResultText || c.Text == nil {
			continue
		}
		parts = append(parts, c.Text.Text)
	}
	return strings.Join(parts, " ")
}

// NewToolResultContent builds tool-result content from an observation string and
// optional image blocks.
func NewToolResultContent(observation string, images []*ContentBlock) []*ToolResultContent {
	content := []*ToolResultContent{
		{Kind: ToolResultText, Text: &TextData{Text: observation}},
	}
	for _, img := range images {
		if img == nil || img.Kind != BlockImage || img.Image == nil {
			continue
		}
		content = append(content, &ToolResultContent{Kind: ToolResultImage, Image: img.Image})
	}
	return content
}
