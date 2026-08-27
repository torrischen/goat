package llm

import "testing"

func TestApplyOptions(t *testing.T) {
	cfg := ApplyOptions(
		WithAPIKey("key"),
		WithBaseURL("https://example.com"),
		WithModel("model"),
		WithMaxOutputTokens(128),
		WithToolChoice(ToolChoiceRequired),
		WithReasoningEffort("high"),
		WithEncryptedReasoning(false),
	)

	if cfg.APIKey != "key" || cfg.BaseURL != "https://example.com" || cfg.Model != "model" {
		t.Fatalf("unexpected provider configuration: %+v", cfg)
	}
	if cfg.MaxOutputTokens != 128 || cfg.ToolChoice != ToolChoiceRequired {
		t.Fatalf("unexpected call configuration: %+v", cfg)
	}
	if cfg.ReasoningEffort != "high" || cfg.IncludeEncryptedReasoning {
		t.Fatalf("unexpected OpenAI configuration: %+v", cfg)
	}
}

func TestApplyOptionsToOverridesDefaults(t *testing.T) {
	cfg := ApplyOptions(WithModel("default"), WithMaxOutputTokens(128))
	ApplyOptionsTo(&cfg, WithModel("override"), WithMaxOutputTokens(256))

	if cfg.Model != "override" || cfg.MaxOutputTokens != 256 {
		t.Fatalf("options did not override defaults: %+v", cfg)
	}
}
