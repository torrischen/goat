package bm25

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

type MilvusBM25RetrieverConfig struct {
	RetrieverName   string
	Dimension       int64
	ShardNum        int32
	Overwrite       bool
	HasVariableTags bool
	MaxTextLength   int64
	DropRatio       float64
	Language        BM25Language
	FieldsIndexes   []milvus.FieldsIndex
	AutoIndexFields bool
}

func NewBM25RetrieverConfig(opts ...MilvusBM25RetrieverConfigOption) *MilvusBM25RetrieverConfig {
	bm25Cfg := &MilvusBM25RetrieverConfig{
		RetrieverName:   "default_collection",
		Dimension:       512,
		ShardNum:        entity.DefaultShardNumber,
		Overwrite:       false,
		HasVariableTags: false,
		MaxTextLength:   1024,
		DropRatio:       0.2,
		Language:        BM25LanguageJapanese,
		AutoIndexFields: false,
	}
	for _, opt := range opts {
		opt(bm25Cfg)
	}

	return bm25Cfg
}

type MilvusBM25RetrieverConfigOption func(*MilvusBM25RetrieverConfig)

func WithRetrieverName(retrieverName string) MilvusBM25RetrieverConfigOption {
	return func(cfg *MilvusBM25RetrieverConfig) {
		cfg.RetrieverName = retrieverName
	}
}

func WithDimension(dimension int64) MilvusBM25RetrieverConfigOption {
	return func(cfg *MilvusBM25RetrieverConfig) {
		cfg.Dimension = dimension
	}
}

func WithShardNum(shardNum int32) MilvusBM25RetrieverConfigOption {
	return func(cfg *MilvusBM25RetrieverConfig) {
		cfg.ShardNum = shardNum
	}
}

func WithOverwrite(overwrite bool) MilvusBM25RetrieverConfigOption {
	return func(cfg *MilvusBM25RetrieverConfig) {
		cfg.Overwrite = overwrite
	}
}

func WithVariableTags(has bool) MilvusBM25RetrieverConfigOption {
	return func(cfg *MilvusBM25RetrieverConfig) {
		cfg.HasVariableTags = has
	}
}

func WithMaxTextLength(maxTextLength int64) MilvusBM25RetrieverConfigOption {
	return func(cfg *MilvusBM25RetrieverConfig) {
		cfg.MaxTextLength = maxTextLength
	}
}

func WithDropRatio(dropRatio float64) MilvusBM25RetrieverConfigOption {
	return func(cfg *MilvusBM25RetrieverConfig) {
		cfg.DropRatio = dropRatio
	}
}

func WithLanguage(language BM25Language) MilvusBM25RetrieverConfigOption {
	return func(cfg *MilvusBM25RetrieverConfig) {
		cfg.Language = language
	}
}

func WithFieldsIndexes(indexes ...milvus.FieldsIndex) MilvusBM25RetrieverConfigOption {
	return func(cfg *MilvusBM25RetrieverConfig) {
		cfg.FieldsIndexes = indexes
	}
}

func WithFieldsAutoIndex(enable bool) MilvusBM25RetrieverConfigOption {
	return func(cfg *MilvusBM25RetrieverConfig) {
		cfg.AutoIndexFields = enable
	}
}
