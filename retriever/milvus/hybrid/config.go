package hybrid

import (
	"github.com/torrischen/goat/retriever/milvus"

	"github.com/milvus-io/milvus/client/v2/entity"
)

type BM25Language string

const (
	BM25LanguageEnglish  BM25Language = "en"
	BM25LanguageChinese  BM25Language = "zh"
	BM25LanguageJapanese BM25Language = "ja"
	BM25LanguageKorean   BM25Language = "ko"
)

type MilvusHybridRetrieverConfig struct {
	RetrieverName   string
	Dimension       int64
	ShardNum        int32
	Overwrite       bool
	HasVariableTags bool
	OnGPU           bool
	MaxTextLength   int64
	DropRatio       float64
	Language        BM25Language
	FieldsIndexes   []milvus.FieldsIndex
	AutoIndexFields bool
}

func NewHybridRetrieverConfig(opts ...MilvusHybridRetrieverConfigOption) *MilvusHybridRetrieverConfig {
	cfg := &MilvusHybridRetrieverConfig{
		RetrieverName:   "default_collection",
		Dimension:       512,
		ShardNum:        entity.DefaultShardNumber,
		Overwrite:       false,
		HasVariableTags: false,
		OnGPU:           true,
		MaxTextLength:   1024,
		DropRatio:       0.2,
		Language:        BM25LanguageJapanese,
		AutoIndexFields: false,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}

type MilvusHybridRetrieverConfigOption func(*MilvusHybridRetrieverConfig)

func WithRetrieverName(retrieverName string) MilvusHybridRetrieverConfigOption {
	return func(cfg *MilvusHybridRetrieverConfig) {
		cfg.RetrieverName = retrieverName
	}
}

func WithDimension(dimension int64) MilvusHybridRetrieverConfigOption {
	return func(cfg *MilvusHybridRetrieverConfig) {
		cfg.Dimension = dimension
	}
}

func WithShardNum(shardNum int32) MilvusHybridRetrieverConfigOption {
	return func(cfg *MilvusHybridRetrieverConfig) {
		cfg.ShardNum = shardNum
	}
}

func WithOverwrite(overwrite bool) MilvusHybridRetrieverConfigOption {
	return func(cfg *MilvusHybridRetrieverConfig) {
		cfg.Overwrite = overwrite
	}
}

func WithVariableTags(has bool) MilvusHybridRetrieverConfigOption {
	return func(cfg *MilvusHybridRetrieverConfig) {
		cfg.HasVariableTags = has
	}
}

func WithOnGPU(onGPU bool) MilvusHybridRetrieverConfigOption {
	return func(cfg *MilvusHybridRetrieverConfig) {
		cfg.OnGPU = onGPU
	}
}

func WithMaxTextLength(maxTextLength int64) MilvusHybridRetrieverConfigOption {
	return func(cfg *MilvusHybridRetrieverConfig) {
		cfg.MaxTextLength = maxTextLength
	}
}

func WithDropRatio(dropRatio float64) MilvusHybridRetrieverConfigOption {
	return func(cfg *MilvusHybridRetrieverConfig) {
		cfg.DropRatio = dropRatio
	}
}

func WithLanguage(language BM25Language) MilvusHybridRetrieverConfigOption {
	return func(cfg *MilvusHybridRetrieverConfig) {
		cfg.Language = language
	}
}

func WithFieldsIndexes(indexes ...milvus.FieldsIndex) MilvusHybridRetrieverConfigOption {
	return func(cfg *MilvusHybridRetrieverConfig) {
		cfg.FieldsIndexes = indexes
	}
}

func WithFieldsAutoIndex(enable bool) MilvusHybridRetrieverConfigOption {
	return func(cfg *MilvusHybridRetrieverConfig) {
		cfg.AutoIndexFields = enable
	}
}
