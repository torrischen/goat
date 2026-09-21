package openai

import (
	"context"

	"github.com/torrischen/goat/embedder"
	"github.com/torrischen/goat/util"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
)

var _ embedder.Embedder = (*OpenAIEmbedder)(nil)

type OpenAIEmbedder struct {
	client *openai.Client
	model  string
	dim    int64
}

func NewOpenAIEmbedder(ctx context.Context, cfg *OpenAIConfig) *OpenAIEmbedder {
	c := openai.NewClient(
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(cfg.ApiKey),
	)

	return &OpenAIEmbedder{
		client: &c,
		model:  cfg.Model,
		dim:    cfg.Dim,
	}
}

func (oe *OpenAIEmbedder) Embed(ctx context.Context, text []string) ([][]float32, error) {
	resp, err := oe.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Model: openai.EmbeddingModel(oe.model),
		Input: openai.EmbeddingNewParamsInputUnion{
			OfArrayOfStrings: text,
		},
		Dimensions: param.NewOpt(oe.dim),
	})
	if err != nil {
		return nil, err
	}

	embeddings := util.Map(resp.Data, func(e openai.Embedding) []float32 {
		return util.Map(e.Embedding, func(embedding float64) float32 {
			return float32(embedding)
		})
	})

	return embeddings, nil
}
