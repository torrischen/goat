package bm25

import (
	"testing"

	"github.com/torrischen/goat/retriever/milvus"
)

func TestBM25ConfigOptions(t *testing.T) {
	defaults := NewBM25RetrieverConfig()
	if defaults.RetrieverName != "default_collection" || defaults.Dimension != 512 || defaults.MaxTextLength != 1024 || defaults.DropRatio != 0.2 || defaults.Language != BM25LanguageJapanese {
		t.Fatalf("defaults = %+v", defaults)
	}
	idx := milvus.NewFieldsIndex("age", milvus.JSONFieldCastInt)
	cfg := NewBM25RetrieverConfig(
		WithRetrieverName("collection"), WithDimension(10), WithShardNum(2), WithOverwrite(true),
		WithVariableTags(true), WithMaxTextLength(200), WithDropRatio(0.5), WithLanguage(BM25LanguageEnglish),
		WithFieldsIndexes(idx), WithFieldsAutoIndex(true),
	)
	if cfg.RetrieverName != "collection" || cfg.Dimension != 10 || cfg.ShardNum != 2 || !cfg.Overwrite || !cfg.HasVariableTags || cfg.MaxTextLength != 200 || cfg.DropRatio != 0.5 || cfg.Language != BM25LanguageEnglish || len(cfg.FieldsIndexes) != 1 || !cfg.AutoIndexFields {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestRetrieverName(t *testing.T) {
	r := &MilvusBM25Retriever{RetrieverName: "bm25"}
	if r.Name() != "bm25" {
		t.Fatalf("Name() = %q", r.Name())
	}
}
