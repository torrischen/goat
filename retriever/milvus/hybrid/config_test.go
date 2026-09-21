package hybrid

import (
	"testing"

	"github.com/torrischen/goat/retriever/milvus"
)

func TestHybridConfigOptions(t *testing.T) {
	defaults := NewHybridRetrieverConfig()
	if defaults.RetrieverName != "default_collection" || defaults.Dimension != 512 || !defaults.OnGPU || defaults.MaxTextLength != 1024 || defaults.Language != BM25LanguageJapanese {
		t.Fatalf("defaults = %+v", defaults)
	}
	idx := milvus.NewFieldsIndex("age", milvus.JSONFieldCastInt)
	cfg := NewHybridRetrieverConfig(
		WithRetrieverName("collection"), WithDimension(10), WithShardNum(2), WithOverwrite(true),
		WithVariableTags(true), WithOnGPU(false), WithMaxTextLength(200), WithDropRatio(0.5),
		WithLanguage(BM25LanguageEnglish), WithFieldsIndexes(idx), WithFieldsAutoIndex(true),
	)
	if cfg.RetrieverName != "collection" || cfg.Dimension != 10 || cfg.ShardNum != 2 || !cfg.Overwrite || !cfg.HasVariableTags || cfg.OnGPU || cfg.MaxTextLength != 200 || cfg.DropRatio != 0.5 || cfg.Language != BM25LanguageEnglish || len(cfg.FieldsIndexes) != 1 || !cfg.AutoIndexFields {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestRetrieverName(t *testing.T) {
	r := &MilvusHybridRetriever{RetrieverName: "hybrid"}
	if r.Name() != "hybrid" {
		t.Fatalf("Name() = %q", r.Name())
	}
}
