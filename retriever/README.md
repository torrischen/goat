# Retriever SDK

`retriever` provides Milvus-backed data ingestion and retrieval. It currently includes dense vector search, BM25 full-text search, and hybrid Vector + BM25 search.

All implementations support collection initialization, partition management, batch writes, filtered retrieval, pagination, deletion, upsert, and custom JSON fields. `retriever/aisearch` is still under development and does not currently provide a usable implementation.

## Features

- `vector` generates dense vectors through `embedder.Embedder` and performs ANN search.
- `bm25` uses a Milvus BM25 Function and sparse vectors for keyword search.
- `hybrid` performs vector and BM25 searches together, then combines results with RRF or a weighted reranker.
- Query, Vector, BM25, and Hybrid search modes.
- Milvus partition creation, loading, release, and deletion.
- A fixed `fields` JSON column with JSON-path indexes.
- Filter expressions for tags, scalar fields, and JSON fields.

## Directory structure

```text
retriever/
├── aisearch/               # Reserved module; still under development
└── milvus/
    ├── config.go           # Milvus connection configuration
    ├── model.go            # Element, Retrieval, SearchArgs, and Fields
    ├── filter.go           # Filter-expression helpers
    ├── fields_index.go     # JSON-path indexes for the fields column
    ├── vector/             # Dense vector retriever
    ├── bm25/               # BM25 retriever
    └── hybrid/             # Vector + BM25 hybrid retriever
```

## Installation and prerequisites

```bash
go get github.com/torrischen/goat/retriever/...
```

Before using the retrievers, make sure you have:

- Access to a Milvus 2.6 service.
- An implementation of `embedder.Embedder` for Vector and Hybrid modes.
- An embedder output dimension that exactly matches the retriever's `Dimension`.
- Milvus support for the selected index type when GPU indexes are enabled.

## Choosing a retriever

| Implementation | Best for | Embedder required | Stored columns |
| --- | --- | --- | --- |
| `vector` | Semantic similarity and natural-language recall | Yes | `id`, `tag`, `embedding`, `fields` |
| `bm25` | Keywords, proper nouns, and exact text recall | No | `id`, `tag`, `content`, `sparse`, `fields` |
| `hybrid` | Combined semantic and keyword recall | Yes | `id`, `tag`, `text`, `embedding`, `sparse`, `fields` |

## Quick start: Hybrid Retriever

The following example connects to Milvus, creates a Hybrid Retriever, inserts a document, and performs a hybrid search.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/torrischen/goat/embedder/openai"
	"github.com/torrischen/goat/retriever/milvus"
	"github.com/torrischen/goat/retriever/milvus/hybrid"
)

func main() {
	ctx := context.Background()

	embedder := openai.NewOpenAIEmbedder(ctx, &openai.OpenAIConfig{
		BaseURL: "https://api.openai.com/v1",
		ApiKey:  os.Getenv("OPENAI_API_KEY"),
		Model:   "text-embedding-3-small",
		Dim:     1536,
	})

	retriever, err := hybrid.NewMilvusHybridRetrieverWithConfig(
		ctx,
		embedder,
		hybrid.NewHybridRetrieverConfig(
			hybrid.WithRetrieverName("documents"),
			hybrid.WithDimension(1536),
			hybrid.WithLanguage(hybrid.BM25LanguageEnglish),
			hybrid.WithOnGPU(false),
			hybrid.WithFieldsIndexes(
				milvus.NewFieldsIndex("category", milvus.JSONFieldCastVarchar),
			),
		),
		milvus.NewMilvusConfig(
			milvus.WithMilvusAddress("http://localhost:19530"),
		),
	)
	if err != nil {
		log.Fatal(err)
	}

	const partition = "knowledge"
	exists, err := retriever.HasPartition(ctx, partition)
	if err != nil {
		log.Fatal(err)
	}
	if !exists {
		if err := retriever.AddPartitions(ctx, partition); err != nil {
			log.Fatal(err)
		}
	}
	if err := retriever.LoadPartitions(ctx, partition); err != nil {
		log.Fatal(err)
	}

	_, err = retriever.AddElement(ctx, partition, milvus.NewElement(
		1,
		"goat is a Go toolkit for building agents and retrieval applications.",
		[]string{"goat", "golang"},
		milvus.NewFieldsFromJSONString(`{"category":"documentation","year":2026}`),
	))
	if err != nil {
		log.Fatal(err)
	}

	results, err := retriever.Search(ctx, []string{partition}, &milvus.SearchArgs{
		Text:       "Go agent SDK",
		Limit:      10,
		SearchMode: milvus.SearchModeHybrid,
		Filter: milvus.StringEquals(
			milvus.FieldsPath("category"),
			"documentation",
		),
	})
	if err != nil {
		log.Fatal(err)
	}

	for _, result := range results {
		fmt.Printf("id=%d score=%f text=%q fields=%s\n",
			result.ID,
			result.Distance,
			result.Content,
			result.Fields.ToJSONString(),
		)
	}
}
```

The constructor creates a collection from the configuration or reuses a compatible existing collection. This example creates and loads the `knowledge` partition only when necessary.

## Milvus connections

```go
config := milvus.NewMilvusConfig(
	milvus.WithMilvusAddress("http://localhost:19530"),
	milvus.WithMilvusUsername("username"),
	milvus.WithMilvusPassword("password"),
)

client, err := milvus.NewMilvusClient(ctx, config)
```

The default address is `http://localhost:19530`. To share one connection across multiple retrievers, create a `*milvusclient.Client` first and use each implementation's `WithMilvus` constructor.

```go
vectorRetriever, err := vector.NewMilvusRetrieverWithMilvus(ctx, client, embedder, vectorConfig)
bm25Retriever, err := bm25.NewMilvusBM25RetrieverWithMilvus(ctx, client, bm25Config)
hybridRetriever, err := hybrid.NewMilvusHybridRetrieverWithMilvus(ctx, client, embedder, hybridConfig)
```

When retrievers share a client, the caller is responsible for closing it after all retrievers have finished using it.

## Data model

### Element

`Element` is the common data type accepted by every retriever:

```go
element := milvus.NewElement(
	1001,
	"Text to store and retrieve.",
	[]string{"manual", "golang"},
	milvus.NewFieldsFromJSONString(`{"author":"team-a","year":2026}`),
)
```

| Field | Description |
| --- | --- |
| `ID` | An Int64 primary key supplied by the caller. |
| `TextToEmbed` | Embedded in Vector mode, stored as full text in BM25 mode, and used for both in Hybrid mode. |
| `Tag` | A string array available for classification and filtering. |
| `Fields` | Custom data stored in the fixed `fields` JSON column. |

Use `SetField` and `GetField` to modify or read individual custom fields.

### Retrieval

```go
type Retrieval struct {
	ID       int64
	Tag      []string
	Distance float32
	Content  string
	Fields   Fields
}
```

- Scores from Vector, BM25, and Hybrid searches are returned in `Distance`.
- BM25 and Hybrid results return the original text in `Content`.
- A pure Vector collection does not store the original text, so `Content` is empty.
- Query mode does not compute similarity, so `Distance` does not represent a relevance score.

`Retrievals` provides `Len`, `Index`, `Max`, and `Min` helpers. Milvus returns search results in relevance order; `Max` returns the first result and `Min` returns the last.

## Fields JSON

Every custom field is stored in the fixed `fields` JSON column.

```go
fields := milvus.NewFields()
fields.Set("author", "team-a")
fields.Set("year", 2026)

author := fields.Get("author")
rawJSON := fields.ToJSONString()

fromStruct := milvus.NewFieldsFromObject(struct {
	Category string `json:"category"`
}{Category: "guide"})
```

Values in `Fields` must be JSON serializable. If a constructor cannot serialize a value, it logs the error and returns `nil`. Do not pass unsupported values such as channels or functions.

### JSON paths

Use `FieldsPath` to build a Milvus JSON path into the `fields` column:

```go
milvus.FieldsPath("year")             // fields["year"]
milvus.FieldsPath("metadata", "lang") // fields["metadata"]["lang"]
```

Field names must satisfy the safe-name rules supported by the index implementation. Never construct field paths directly from untrusted user input.

### JSON field indexes

Declare indexes while initializing a retriever:

```go
milvus.NewFieldsIndex("category", milvus.JSONFieldCastVarchar)
milvus.NewFieldsPathIndex(
	milvus.FieldsPath("metadata", "year"),
	milvus.JSONFieldCastDouble,
)
```

Supported cast types:

- `JSONFieldCastBool`
- `JSONFieldCastDouble`
- `JSONFieldCastVarchar`

Vector retrievers use `WithAutoIndexFields(true)`, while BM25 and Hybrid retrievers use `WithFieldsAutoIndex(true)`, to discover fields and create indexes from `Fields` during writes. In production, prefer explicit declarations through `WithFieldsIndexes` so changing data types cannot create unexpected indexes.

## Filter expressions

### Scalar filters

```go
filter := milvus.IntGreaterThan(milvus.FieldsPath("year"), 2024)
filter = milvus.StringEquals(milvus.FieldsPath("status"), "published")
filter = milvus.StringLike(milvus.FieldsPath("title"), "%goat%")
```

### Collection filters

```go
filter := milvus.IntIn(milvus.FieldsPath("category_id"), []int64{1, 2, 3})
filter = milvus.StringIn(milvus.FieldsPath("lang"), []string{"en", "fr"})
filter = milvus.ArrayContainsAny(milvus.ColumnTag, []string{"golang", "agent"})
```

### Combined filters

```go
filter := milvus.And([]milvus.RetrieveFilterOption{
	milvus.IntGreaterThan(milvus.FieldsPath("year"), 2024),
	milvus.ArrayContainsAny(milvus.ColumnTag, []string{"documentation"}),
})

filter = milvus.Or([]milvus.RetrieveFilterOption{
	milvus.StringEquals(milvus.FieldsPath("status"), "published"),
	milvus.StringEquals(milvus.FieldsPath("status"), "featured"),
})
```

`RetrieveFilterOption` is a Milvus expression string. The helper functions escape string literals, but field paths must still come from trusted application code.

## Search arguments and modes

```go
type SearchArgs struct {
	Text          string
	Limit         int
	Offset        int
	Filter        RetrieveFilterOption
	OutputFields  []string
	SearchMode    SearchMode
	RerankWeights []float64
}
```

| Field | Description |
| --- | --- |
| `Text` | Query text used for embedding or BM25. |
| `Limit` | Number of results. The internal search default is `8` when this value is not positive. |
| `Offset` | Pagination offset. |
| `Filter` | Milvus filter expression. The zero value disables filtering. |
| `OutputFields` | Field names to return from the `fields` JSON column. |
| `SearchMode` | Query, Vector, BM25, Hybrid, or Auto. |
| `RerankWeights` | Weights for the Hybrid weighted reranker. An empty slice uses RRF. |

Available modes:

- `SearchModeQuery` queries only by filter and does not calculate similarity.
- `SearchModeVector` performs dense vector search.
- `SearchModeBM25` performs full-text BM25 search.
- `SearchModeHybrid` combines Vector and BM25 results.
- `SearchModeAuto` uses the current retriever's default mode.

With `SearchModeAuto` and non-empty `Text`, a Vector Retriever defaults to Vector, a BM25 Retriever defaults to BM25, and a Hybrid Retriever defaults to Hybrid. Auto falls back to Query when the arguments or `Text` are empty. Each retriever supports only modes compatible with its collection schema.


## Vector Retriever

### Configuration

```go
config := vector.NewMilvusRetrieverConfig(
	vector.WithRetrieverName("vector_documents"),
	vector.WithDimension(1536),
	vector.WithShardNum(2),
	vector.WithOverwrite(false),
	vector.WithVariableTags(true),
	vector.WithOnGPU(false),
	vector.WithFieldsIndexes(
		milvus.NewFieldsIndex("category", milvus.JSONFieldCastVarchar),
	),
	vector.WithAutoIndexFields(false),
)
```

The main defaults are collection name `default_collection`, dimension `512`, no overwrite, GPU indexes enabled, and automatic `Fields` indexing disabled.

### Construction

```go
retriever, err := vector.NewMilvusRetrieverWithConfig(ctx, embedder, config, milvusConfig)
```

The Vector Retriever also provides `Query` for reading raw ID, Tag, Embedding, and Fields data. Prefer `Search` for general result retrieval.

## BM25 Retriever

The BM25 Retriever does not require an embedder:

```go
config := bm25.NewBM25RetrieverConfig(
	bm25.WithRetrieverName("keyword_documents"),
	bm25.WithLanguage(bm25.BM25LanguageEnglish),
	bm25.WithMaxTextLength(4096),
	bm25.WithDropRatio(0.2),
	bm25.WithOverwrite(false),
	bm25.WithFieldsAutoIndex(false),
)

retriever, err := bm25.NewMilvusBM25RetrieverWithConfig(ctx, config, milvusConfig)
```

Available analyzers include English, Chinese, Japanese, and Korean. The current default is Japanese, so always set the language explicitly when indexing other languages.

## Hybrid Retriever

```go
config := hybrid.NewHybridRetrieverConfig(
	hybrid.WithRetrieverName("hybrid_documents"),
	hybrid.WithDimension(1536),
	hybrid.WithLanguage(hybrid.BM25LanguageEnglish),
	hybrid.WithOnGPU(false),
)

retriever, err := hybrid.NewMilvusHybridRetrieverWithConfig(
	ctx,
	embedder,
	config,
	milvusConfig,
)
```

Hybrid mode combines results with RRF by default. To control the relative vector and keyword weights, supply a weighted reranker configuration:

```go
results, err := retriever.Search(ctx, partitions, &milvus.SearchArgs{
	Text:          "query text",
	Limit:         10,
	SearchMode:    milvus.SearchModeHybrid,
	RerankWeights: []float64{0.8, 0.2}, // Vector, BM25
})
```

Weight order matches the internal request order: Vector first and BM25 second.

## Partition management

All three retrievers provide the same partition-management API:

```go
exists, err := retriever.HasPartition(ctx, "tenant_a")
if !exists {
	err = retriever.AddPartitions(ctx, "tenant_a")
}

err = retriever.LoadPartitions(ctx, "tenant_a")
partitions, err := retriever.ListPartitions(ctx)
err = retriever.ReleasePartitions(ctx, "tenant_a")
err = retriever.DeletePartitions(ctx, "tenant_a")
```

- Create a target partition before writing and load it before searching.
- `milvus.DefaultPartition` is `_default`.
- Release a partition before deleting it, and verify that it contains no data you still need.
- Partitions are suitable for tenant or data-domain isolation; do not create one partition per record.

## Writing, updating, and deleting

```go
id, err := retriever.AddElement(ctx, partition, element)

ids, err := retriever.AddElements(ctx, partition, []*milvus.Element{
	elementA,
	elementB,
})

err = retriever.UpsertElement(ctx, partition, updatedElement)
err = retriever.DeleteElement(ctx, partition, []int64{1001, 1002})
```

Prefer `AddElements` for batch ingestion to reduce network round trips. The caller is responsible for ensuring that IDs and document contents satisfy application constraints within each batch.

## Collection lifecycle

Every retriever configuration supports `WithOverwrite(true)`. When enabled, constructing a retriever may drop and recreate an existing collection with the same name, destroying its data. Use this option only in tests, initialization workflows, or an intentional index rebuild.

You can also destroy a collection explicitly:

```go
err := vector.TruncateAndDestroy(ctx, client, "vector_documents")
err := bm25.TruncateAndDestroy(ctx, client, "keyword_documents")
err := hybrid.TruncateAndDestroy(ctx, client, "hybrid_documents")
```

These functions are destructive. Add authorization and explicit confirmation before exposing them in production.

## Best practices

- The retriever's `Dimension` must exactly match the embedder's output dimension.
- Select the analyzer that matches your BM25 or Hybrid document language; do not rely on the default.
- Keep `WithOverwrite(false)` in production.
- Declare frequently used JSON field indexes before applying high-volume filters to those fields.
- Do not mix string, numeric, and Boolean values at the same JSON path.
- Use stable, unique Int64 IDs so upserts and deletions are safe.
- After batch ingestion, wait for Milvus visibility according to your application's consistency requirements before searching immediately.
- Use context deadlines to limit connection, index creation, load, and search operations.
- Never use unvalidated user input directly as a field path or raw Milvus filter.

## Testing and compile checks

Retriever integration depends on an external Milvus service, so the repository primarily performs compile checks:

```bash
go test ./retriever/...
```

End-to-end validation should also cover:

1. Creating an isolated test collection.
2. Creating and loading a partition.
3. Inserting deterministic test documents.
4. Running Query, Vector, BM25, and Hybrid searches.
5. Verifying filters, fields, and result ordering.
6. Deleting the test collection.
