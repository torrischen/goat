// Package voyage implements embeddings using the Voyage AI API.
package voyage

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/torrischen/goat/embedder"
	"github.com/torrischen/goat/embedder/internal/httpjson"
)

const defaultBaseURL = "https://api.voyageai.com/v1"

var _ embedder.Embedder = (*Embedder)(nil)

type Config struct {
	BaseURL         string
	APIKey          string
	Model           string
	InputType       string
	OutputDimension int
	Client          *http.Client
}

type Embedder struct {
	config Config
}

func New(cfg Config) *Embedder {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = "voyage-3.5"
	}
	return &Embedder{config: cfg}
}

func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	request := struct {
		Input           []string `json:"input"`
		Model           string   `json:"model"`
		InputType       string   `json:"input_type,omitempty"`
		OutputDimension int      `json:"output_dimension,omitempty"`
		OutputDType     string   `json:"output_dtype"`
	}{texts, e.config.Model, e.config.InputType, e.config.OutputDimension, "float"}
	var response struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := httpjson.Post(ctx, e.config.Client, strings.TrimRight(e.config.BaseURL, "/")+"/embeddings", map[string]string{
		"Authorization": "Bearer " + e.config.APIKey,
	}, request, &response); err != nil {
		return nil, fmt.Errorf("voyage embeddings: %w", err)
	}
	if len(response.Data) != len(texts) {
		return nil, fmt.Errorf("voyage embeddings: expected %d vectors, got %d", len(texts), len(response.Data))
	}
	sort.Slice(response.Data, func(i, j int) bool { return response.Data[i].Index < response.Data[j].Index })
	vectors := make([][]float32, len(response.Data))
	for i, item := range response.Data {
		if item.Index != i {
			return nil, fmt.Errorf("voyage embeddings: invalid response index %d", item.Index)
		}
		vectors[i] = item.Embedding
	}
	return vectors, nil
}
