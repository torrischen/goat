package anthropic

import (
	"encoding/json"

	"github.com/torrischen/goat/agent/message"
)

const providerNamespace = "anthropic"

type redactedThinkingMeta struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

func encodeRedactedThinkingMeta(data string) map[string]json.RawMessage {
	if data == "" {
		return nil
	}
	raw, err := json.Marshal(redactedThinkingMeta{
		Type: "redacted_thinking",
		Data: data,
	})
	if err != nil {
		return nil
	}
	return map[string]json.RawMessage{providerNamespace: raw}
}

func decodeRedactedThinkingMeta(block *message.ContentBlock) *redactedThinkingMeta {
	if block == nil || block.Provider == nil {
		return nil
	}
	raw, ok := block.Provider[providerNamespace]
	if !ok {
		return nil
	}
	var meta redactedThinkingMeta
	if err := json.Unmarshal(raw, &meta); err != nil || meta.Type != "redacted_thinking" || meta.Data == "" {
		return nil
	}
	return &meta
}
