package vector

import (
	"github.com/torrischen/goat/retriever/milvus"

	"github.com/milvus-io/milvus/client/v2/entity"
)

type MilvusRetrieverConfig struct {
	RetrieverName   string
	Dimension       int64
	ShardNum        int32
	Overwrite       bool
	HasVariableTags bool
	OnGPU           bool
	FieldsIndexes   []milvus.FieldsIndex
	AutoIndexFields bool
}

func NewMilvusRetrieverConfig(opts ...MilvusRetrieverConfigOption) *MilvusRetrieverConfig {
	cfg := &MilvusRetrieverConfig{
		RetrieverName:   "default_collection",
		Dimension:       512,
		ShardNum:        entity.DefaultShardNumber,
		Overwrite:       false,
		HasVariableTags: false,
		OnGPU:           true,
		AutoIndexFields: false,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}

type MilvusRetrieverConfigOption func(*MilvusRetrieverConfig)

func WithRetrieverName(retrieverName string) MilvusRetrieverConfigOption {
	return func(cfg *MilvusRetrieverConfig) {
		cfg.RetrieverName = retrieverName
	}
}

func WithDimension(dimension int64) MilvusRetrieverConfigOption {
	return func(cfg *MilvusRetrieverConfig) {
		cfg.Dimension = dimension
	}
}

func WithShardNum(shardNum int32) MilvusRetrieverConfigOption {
	return func(cfg *MilvusRetrieverConfig) {
		cfg.ShardNum = shardNum
	}
}

func WithOverwrite(overwrite bool) MilvusRetrieverConfigOption {
	return func(cfg *MilvusRetrieverConfig) {
		cfg.Overwrite = overwrite
	}
}

func WithVariableTags(has bool) MilvusRetrieverConfigOption {
	return func(cfg *MilvusRetrieverConfig) {
		cfg.HasVariableTags = has
	}
}

func WithOnGPU(onGPU bool) MilvusRetrieverConfigOption {
	return func(cfg *MilvusRetrieverConfig) {
		cfg.OnGPU = onGPU
	}
}

func WithFieldsIndexes(indexes ...milvus.FieldsIndex) MilvusRetrieverConfigOption {
	return func(cfg *MilvusRetrieverConfig) {
		cfg.FieldsIndexes = indexes
	}
}

func WithAutoIndexFields(autoIndex bool) MilvusRetrieverConfigOption {
	return func(cfg *MilvusRetrieverConfig) {
		cfg.AutoIndexFields = autoIndex
	}
}
