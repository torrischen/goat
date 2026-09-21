# Embedders

Every provider implements the small `embedder.Embedder` interface:

```go
type Embedder interface {
    Embed(context.Context, []string) ([][]float32, error)
}
```

## Supported providers

| Package | Default model | Notes |
|---|---|---|
| `embedder/openai` | Configurable | OpenAI, Azure OpenAI, and OpenAI-compatible endpoints |
| `embedder/gemini` | `gemini-embedding-001` | Gemini `batchEmbedContents` API |
| `embedder/cohere` | `embed-v4.0` | Cohere Embed API v2 |
| `embedder/voyage` | `voyage-3.5` | Voyage AI embeddings API |
| `embedder/ollama` | `nomic-embed-text` | Local Ollama `/api/embed` API |

## Examples

```go
import (
    "github.com/torrischen/goat/embedder/cohere"
    "github.com/torrischen/goat/embedder/gemini"
    "github.com/torrischen/goat/embedder/ollama"
    "github.com/torrischen/goat/embedder/voyage"
)

ge := gemini.New(gemini.Config{
    APIKey: os.Getenv("GEMINI_API_KEY"),
    TaskType: "RETRIEVAL_DOCUMENT",
    OutputDimensionality: 768,
})

ce := cohere.New(cohere.Config{
    APIKey: os.Getenv("COHERE_API_KEY"),
    InputType: "search_document",
})

ve := voyage.New(voyage.Config{
    APIKey: os.Getenv("VOYAGE_API_KEY"),
    InputType: "document",
})

oe := ollama.New(ollama.Config{
    Model: "nomic-embed-text",
})

vectors, err := ge.Embed(ctx, []string{"first document", "second document"})
```

Use the provider's document input/task type while indexing and its query input/task type while searching. The output dimension must match the vector field dimension in your retriever. `BaseURL` and `Client` are configurable for gateways, testing, and custom transports.
