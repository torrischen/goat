package openai

type OpenAIConfig struct {
	Alias   string
	BaseURL string
	ApiKey  string
	Model   string
	Dim     int64
}

func NewOpenAIConfig(opts ...OpenAIConfigOption) *OpenAIConfig {
	cfg := &OpenAIConfig{
		Alias:   "base_llm",
		BaseURL: "https://api.openai.com/v1",
		ApiKey:  "123",
		Model:   "gpt-4o",
		Dim:     1024,
	}

	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}

type OpenAIConfigOption func(*OpenAIConfig)

func WithAlias(name string) OpenAIConfigOption {
	return func(cfg *OpenAIConfig) {
		cfg.Alias = name
	}
}

func WithBaseURL(baseURL string) OpenAIConfigOption {
	return func(cfg *OpenAIConfig) {
		cfg.BaseURL = baseURL
	}
}

func WithApiKey(apiKey string) OpenAIConfigOption {
	return func(cfg *OpenAIConfig) {
		cfg.ApiKey = apiKey
	}
}

func WithModel(model string) OpenAIConfigOption {
	return func(cfg *OpenAIConfig) {
		cfg.Model = model
	}
}

func WithDim(dim int64) OpenAIConfigOption {
	return func(cfg *OpenAIConfig) {
		cfg.Dim = dim
	}
}
