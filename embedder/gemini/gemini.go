// Package gemini implements embeddings using the Gemini batchEmbedContents API.
package gemini

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/torrischen/goat/embedder"
	"github.com/torrischen/goat/embedder/internal/httpjson"
)

const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

var _ embedder.Embedder = (*Embedder)(nil)

type Config struct {
	BaseURL              string
	APIKey               string
	Model                string
	TaskType             string
	Title                string
	OutputDimensionality int
	Client               *http.Client
}

type Embedder struct {
	config Config
}

func New(cfg Config) *Embedder {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = "gemini-embedding-001"
	}
	return &Embedder{config: cfg}
}

func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	model := e.config.Model
	if !strings.HasPrefix(model, "models/") {
		model = "models/" + model
	}
	type part struct {
		Text string `json:"text"`
	}
	type requestItem struct {
		Model   string `json:"model"`
		Content struct {
			Parts []part `json:"parts"`
		} `json:"content"`
		TaskType             string `json:"taskType,omitempty"`
		Title                string `json:"title,omitempty"`
		OutputDimensionality int    `json:"outputDimensionality,omitempty"`
	}
	request := struct {
		Requests []requestItem `json:"requests"`
	}{Requests: make([]requestItem, len(texts))}
	for i, text := range texts {
		request.Requests[i] = requestItem{Model: model, TaskType: e.config.TaskType, Title: e.config.Title, OutputDimensionality: e.config.OutputDimensionality}
		request.Requests[i].Content.Parts = []part{{Text: text}}
	}
	var response struct {
		Embeddings []struct {
			Values []float32 `json:"values"`
		} `json:"embeddings"`
	}
	endpoint := strings.TrimRight(e.config.BaseURL, "/") + "/" + model + ":batchEmbedContents"
	if err := httpjson.Post(ctx, e.config.Client, endpoint, map[string]string{
		"x-goog-api-key": e.config.APIKey,
	}, request, &response); err != nil {
		return nil, fmt.Errorf("gemini embeddings: %w", err)
	}
	if len(response.Embeddings) != len(texts) {
		return nil, fmt.Errorf("gemini embeddings: expected %d vectors, got %d", len(texts), len(response.Embeddings))
	}
	vectors := make([][]float32, len(response.Embeddings))
	for i, item := range response.Embeddings {
		vectors[i] = item.Values
	}
	return vectors, nil
}
