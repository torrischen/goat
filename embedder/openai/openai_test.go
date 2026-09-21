package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestOpenAIConfigOptions(t *testing.T) {
	defaults := NewOpenAIConfig()
	if defaults.Alias != "base_llm" || defaults.BaseURL != "https://api.openai.com/v1" || defaults.ApiKey != "123" || defaults.Model != "gpt-4o" || defaults.Dim != 1024 {
		t.Fatalf("default config = %+v", defaults)
	}
	cfg := NewOpenAIConfig(
		WithAlias("embedding"),
		WithBaseURL("http://example.test/v1"),
		WithApiKey("secret"),
		WithModel("text-embedding"),
		WithDim(2),
	)
	if *cfg != (OpenAIConfig{Alias: "embedding", BaseURL: "http://example.test/v1", ApiKey: "secret", Model: "text-embedding", Dim: 2}) {
		t.Fatalf("configured value = %+v", cfg)
	}
}

func TestEmbed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1.25,2.5],"index":0},{"embedding":[3.75,4],"index":1}],"model":"text-embedding","object":"list","usage":{"prompt_tokens":2,"total_tokens":2}}`))
	}))
	defer server.Close()

	embedder := NewOpenAIEmbedder(context.Background(), NewOpenAIConfig(
		WithBaseURL(server.URL+"/v1"),
		WithApiKey("secret"),
		WithModel("text-embedding"),
		WithDim(2),
	))
	got, err := embedder.Embed(context.Background(), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]float32{{1.25, 2.5}, {3.75, 4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Embed() = %v, want %v", got, want)
	}
}

func TestEmbedReturnsAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key","type":"authentication_error"}}`))
	}))
	defer server.Close()

	embedder := NewOpenAIEmbedder(context.Background(), NewOpenAIConfig(WithBaseURL(server.URL+"/v1")))
	if _, err := embedder.Embed(context.Background(), []string{"text"}); err == nil {
		t.Fatal("API error unexpectedly returned nil")
	}
}
