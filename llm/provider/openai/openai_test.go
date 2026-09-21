package openai

import (
	"testing"

	"github.com/torrischen/goat/llm"
)

func TestBuildParamsUsesUnifiedOptions(t *testing.T) {
	model := New(
		llm.WithAPIKey("test-key"),
		llm.WithModel("default-model"),
		llm.WithMaxOutputTokens(128),
		llm.WithPromptCacheKey("default-cache"),
		llm.WithEncryptedReasoning(false),
	).(*client)

	params := model.buildParams(nil,
		llm.WithModel("call-model"),
		llm.WithMaxOutputTokens(256),
		llm.WithPromptCacheKey("call-cache"),
		llm.WithTemperature(0.2),
	)

	if params.Model != "call-model" {
		t.Fatalf("model = %q, want call-model", params.Model)
	}
	if !params.MaxOutputTokens.Valid() || params.MaxOutputTokens.Value != 256 {
		t.Fatalf("max output tokens = %+v, want 256", params.MaxOutputTokens)
	}
	if !params.PromptCacheKey.Valid() || params.PromptCacheKey.Value != "call-cache" {
		t.Fatalf("prompt cache key = %+v, want call-cache", params.PromptCacheKey)
	}
	if !params.Temperature.Valid() || params.Temperature.Value != 0.2 {
		t.Fatalf("temperature = %+v, want 0.2", params.Temperature)
	}
}
