package vector

import (
	"testing"

	"github.com/torrischen/goat/retriever/milvus"
)

func TestMilvusRetrieverConfigOptions(t *testing.T) {
	defaults := NewMilvusRetrieverConfig()
	if defaults.RetrieverName != "default_collection" || defaults.Dimension != 512 || !defaults.OnGPU || defaults.AutoIndexFields {
		t.Fatalf("defaults = %+v", defaults)
	}
	idx := milvus.NewFieldsIndex("age", milvus.JSONFieldCastInt)
	cfg := NewMilvusRetrieverConfig(
		WithRetrieverName("collection"), WithDimension(10), WithShardNum(2), WithOverwrite(true),
		WithVariableTags(true), WithOnGPU(false), WithFieldsIndexes(idx), WithAutoIndexFields(true),
	)
	if cfg.RetrieverName != "collection" || cfg.Dimension != 10 || cfg.ShardNum != 2 || !cfg.Overwrite || !cfg.HasVariableTags || cfg.OnGPU || len(cfg.FieldsIndexes) != 1 || !cfg.AutoIndexFields {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestRetrieverName(t *testing.T) {
	r := &MilvusVectorRetriever{RetrieverName: "vectors"}
	if r.Name() != "vectors" {
		t.Fatalf("Name() = %q", r.Name())
	}
}
