// Package openai implements goat's llm.Client against the OpenAI Responses API,
// using the official github.com/openai/openai-go/v3 SDK.
//
// The provider is stateless: the full conversation is replayed as the Responses
// `input` array on every request (store defaults to false). Reasoning items are
// round-tripped across turns by carrying their OpenAI item id and encrypted
// content in message.ContentBlock.Provider under the "openai" namespace, so
// reasoning models retain their chain of thought without server-side state.
package openai

import (
	"encoding/json"

	"github.com/torrischen/goat/agent/message"
)

// providerNamespace namespaces OpenAI-specific opaque data (reasoning item ids
// and encrypted content) inside message.ContentBlock.Provider so it survives a
// persistence round-trip without interpretation by the neutral message model.
const providerNamespace = "openai"

// reasoningMeta is the round-trip carrier for a reasoning item. It mirrors the
// fields the Responses API needs echoed back: the item id and the encrypted
// reasoning payload. The visible summary text lives on ReasoningData.Text.
type reasoningMeta struct {
	ItemID           string `json:"itemId,omitempty"`
	EncryptedContent string `json:"encryptedContent,omitempty"`
}

// encodeReasoningMeta stores reasoning round-trip data in a block's provider
// namespace. Returns nil when there is nothing worth carrying.
func encodeReasoningMeta(itemID, encryptedContent string) map[string]json.RawMessage {
	if itemID == "" && encryptedContent == "" {
		return nil
	}
	raw, err := json.Marshal(reasoningMeta{ItemID: itemID, EncryptedContent: encryptedContent})
	if err != nil {
		return nil
	}
	return map[string]json.RawMessage{providerNamespace: raw}
}

// decodeReasoningMeta reads the reasoning round-trip data from a block's
// provider namespace, or nil when absent/invalid.
func decodeReasoningMeta(block *message.ContentBlock) *reasoningMeta {
	if block == nil || block.Provider == nil {
		return nil
	}
	raw, ok := block.Provider[providerNamespace]
	if !ok {
		return nil
	}
	var meta reasoningMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil
	}
	return &meta
}
