// Package cohere implements embeddings using Cohere's Embed API v2.
package cohere

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/torrischen/goat/embedder"
	"github.com/torrischen/goat/embedder/internal/httpjson"
)

const defaultBaseURL = "https://api.cohere.com"

var _ embedder.Embedder = (*Embedder)(nil)

type Config struct {
	BaseURL   string
	APIKey    string
	Model     string
	InputType string
	Client    *http.Client
}

type Embedder struct {
	config Config
}

func New(cfg Config) *Embedder {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = "embed-v4.0"
	}
	return &Embedder{config: cfg}
}

func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	request := struct {
		Model          string   `json:"model"`
		Texts          []string `json:"texts"`
		InputType      string   `json:"input_type,omitempty"`
		EmbeddingTypes []string `json:"embedding_types"`
	}{e.config.Model, texts, e.config.InputType, []string{"float"}}
	var response struct {
		Embeddings struct {
			Float [][]float32 `json:"float"`
		} `json:"embeddings"`
	}
	if err := httpjson.Post(ctx, e.config.Client, strings.TrimRight(e.config.BaseURL, "/")+"/v2/embed", map[string]string{
		"Authorization": "Bearer " + e.config.APIKey,
	}, request, &response); err != nil {
		return nil, fmt.Errorf("cohere embeddings: %w", err)
	}
	if len(response.Embeddings.Float) != len(texts) {
		return nil, fmt.Errorf("cohere embeddings: expected %d vectors, got %d", len(texts), len(response.Embeddings.Float))
	}
	return response.Embeddings.Float, nil
}
