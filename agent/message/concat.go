package message

import "encoding/json"

// Concat merges a sequence of streamed assistant message chunks into a single
// message. It replaces eino's schema.ConcatAgenticMessages for the streaming
// consumption path.
//
// Merge rules:
//   - Role is taken from the first non-empty chunk (assistant for model output).
//   - Text block content is concatenated in arrival order into one text block.
//   - Reasoning block content is concatenated into one reasoning block; the last
//     non-empty signature/provider data wins (providers emit it on the final
//     reasoning event).
//   - Tool-call blocks are appended in arrival order (the bridge emits each once,
//     fully assembled).
//   - Other block kinds (image, tool_result, opaque) are appended verbatim.
//   - Meta (usage/response) is taken from whichever chunk carries it (the finish
//     chunk); later non-nil values win.
//   - Message-level Extra entries are merged; later chunks win on key conflicts.
func Concat(chunks []*Message) *Message {
	out := &Message{}
	var textBuilder []string
	var reasoningBuilder []string
	var reasoning *ReasoningData
	var reasoningProvider map[string]json.RawMessage
	var textProvider map[string]json.RawMessage
	roleSet := false

	for _, c := range chunks {
		if c == nil {
			continue
		}
		if !roleSet && c.Role != "" {
			out.Role = c.Role
			roleSet = true
		}
		if c.Meta != nil {
			out.Meta = mergeMeta(out.Meta, c.Meta)
		}
		for k, v := range c.Extra {
			if out.Extra == nil {
				out.Extra = make(map[string]json.RawMessage, len(c.Extra))
			}
			out.Extra[k] = v
		}
		for _, b := range c.Blocks {
			if b == nil {
				continue
			}
			switch b.Kind {
			case BlockText:
				if b.Text != nil {
					textBuilder = append(textBuilder, b.Text.Text)
				}
				for k, v := range b.Provider {
					if textProvider == nil {
						textProvider = make(map[string]json.RawMessage, len(b.Provider))
					}
					textProvider[k] = v
				}
			case BlockReasoning:
				if b.Reasoning != nil {
					reasoningBuilder = append(reasoningBuilder, b.Reasoning.Text)
					if reasoning == nil {
						reasoning = &ReasoningData{}
					}
					if b.Reasoning.Signature != "" {
						reasoning.Signature = b.Reasoning.Signature
					}
				}
				// Carry block-level provider data (e.g. OpenAI reasoning item id and
				// encrypted content) onto the coalesced reasoning block so it can be
				// echoed back on the next turn. Later chunks win on key conflicts.
				for k, v := range b.Provider {
					if reasoningProvider == nil {
						reasoningProvider = make(map[string]json.RawMessage, len(b.Provider))
					}
					reasoningProvider[k] = v
				}
			default:
				out.Blocks = append(out.Blocks, b)
			}
		}
	}

	// Prepend the coalesced text and reasoning blocks so they precede tool calls,
	// matching how providers order assistant content.
	var head []*ContentBlock
	if reasoning != nil || len(reasoningBuilder) > 0 || reasoningProvider != nil {
		if reasoning == nil {
			reasoning = &ReasoningData{}
		}
		reasoning.Text = join(reasoningBuilder, "")
		head = append(head, &ContentBlock{Kind: BlockReasoning, Reasoning: reasoning, Provider: reasoningProvider})
	}
	if len(textBuilder) > 0 {
		head = append(head, &ContentBlock{
			Kind:     BlockText,
			Text:     &TextData{Text: join(textBuilder, "")},
			Provider: textProvider,
		})
	}
	if len(head) > 0 {
		out.Blocks = append(head, out.Blocks...)
	}
	if !roleSet {
		out.Role = RoleAssistant
	}
	return out
}

func mergeMeta(dst, src *ResponseMeta) *ResponseMeta {
	if src == nil {
		return dst
	}
	if dst == nil {
		dst = &ResponseMeta{}
	}
	if src.Usage != nil {
		dst.Usage = src.Usage
	}
	for k, v := range src.Provider {
		if dst.Provider == nil {
			dst.Provider = make(map[string]json.RawMessage, len(src.Provider))
		}
		dst.Provider[k] = v
	}
	return dst
}

func join(parts []string, sep string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	n := len(sep) * (len(parts) - 1)
	for _, p := range parts {
		n += len(p)
	}
	var b []byte
	b = make([]byte, 0, n)
	for i, p := range parts {
		if i > 0 {
			b = append(b, sep...)
		}
		b = append(b, p...)
	}
	return string(b)
}
