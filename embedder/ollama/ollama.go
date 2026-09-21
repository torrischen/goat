// Package ollama implements embeddings using Ollama's local HTTP API.
package ollama

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/torrischen/goat/embedder"
	"github.com/torrischen/goat/embedder/internal/httpjson"
)

const defaultBaseURL = "http://localhost:11434"

var _ embedder.Embedder = (*Embedder)(nil)

type Config struct {
	BaseURL    string
	Model      string
	Dimensions int
	Truncate   *bool
	Client     *http.Client
}

type Embedder struct {
	config Config
}

func New(cfg Config) *Embedder {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = "nomic-embed-text"
	}
	return &Embedder{config: cfg}
}

func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	request := struct {
		Model      string   `json:"model"`
		Input      []string `json:"input"`
		Dimensions int      `json:"dimensions,omitempty"`
		Truncate   *bool    `json:"truncate,omitempty"`
	}{e.config.Model, texts, e.config.Dimensions, e.config.Truncate}
	var response struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := httpjson.Post(ctx, e.config.Client, strings.TrimRight(e.config.BaseURL, "/")+"/api/embed", nil, request, &response); err != nil {
		return nil, fmt.Errorf("ollama embeddings: %w", err)
	}
	if len(response.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama embeddings: expected %d vectors, got %d", len(texts), len(response.Embeddings))
	}
	return response.Embeddings, nil
}
