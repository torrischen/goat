package bm25

import (
	"context"
	"fmt"

	"github.com/torrischen/goat/retriever/milvus"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

type MilvusBM25Retriever struct {
	RetrieverName      string
	Dimension          int64
	Overwrite          bool
	MilvusClient       *milvusclient.Client
	FieldsIndexManager *milvus.FieldsIndexManager
	AutoIndexFields    bool
}

type MilvusBM25RetrieverSchema struct {
	ID     int64
	Tag    []string
	Text   string
	Sparse []float32
	Fields milvus.Fields
}

func NewMilvusBM25RetrieverWithMilvus(ctx context.Context, milvusClient *milvusclient.Client, cfg *MilvusBM25RetrieverConfig) (*MilvusBM25Retriever, error) {
	if err := initMilvusBM25RetrieverMilvusCollection(ctx, milvusClient, cfg); err != nil {
		return nil, err
	}
	fieldsIndexManager := milvus.NewFieldsIndexManager(cfg.RetrieverName, milvusClient)
	if err := fieldsIndexManager.Ensure(ctx, cfg.FieldsIndexes); err != nil {
		return nil, err
	}

	return &MilvusBM25Retriever{
		RetrieverName:      cfg.RetrieverName,
		Dimension:          cfg.Dimension,
		Overwrite:          cfg.Overwrite,
		MilvusClient:       milvusClient,
		FieldsIndexManager: fieldsIndexManager,
		AutoIndexFields:    cfg.AutoIndexFields,
	}, nil
}

func NewMilvusBM25RetrieverWithConfig(ctx context.Context, cfg *MilvusBM25RetrieverConfig, mcfg *milvus.MilvusConfig) (*MilvusBM25Retriever, error) {
	milvusClient, err := milvus.NewMilvusClient(ctx, mcfg)
	if err != nil {
		return nil, err
	}

	return NewMilvusBM25RetrieverWithMilvus(ctx, milvusClient, cfg)
}

func initMilvusBM25RetrieverMilvusCollection(ctx context.Context, milvusClient *milvusclient.Client, cfg *MilvusBM25RetrieverConfig) error {
	exist, err := milvusClient.HasCollection(ctx, milvusclient.NewHasCollectionOption(cfg.RetrieverName))
	if err != nil {
		return err
	}

	if exist && cfg.Overwrite {
		if err := milvusClient.DropCollection(ctx, milvusclient.NewDropCollectionOption(cfg.RetrieverName)); err != nil {
			return err
		}
		if err := createMilvusBM25RetrieverCollection(ctx, milvusClient, cfg); err != nil {
			return err
		}
	}

	if !exist {
		if err := createMilvusBM25RetrieverCollection(ctx, milvusClient, cfg); err != nil {
			return err
		}
	}

	return nil
}

func createMilvusBM25RetrieverCollection(ctx context.Context, milvusClient *milvusclient.Client, cfg *MilvusBM25RetrieverConfig) error {
	bm25Function := entity.NewFunction().
		WithName("bm25").
		WithType(entity.FunctionTypeBM25).
		WithInputFields(milvus.ColumnText).
		WithOutputFields(milvus.ColumnSparse)

	var dictKind string
	var tokenizer string
	switch cfg.Language {
	case BM25LanguageEnglish:
		tokenizer = "standard"
	case BM25LanguageChinese:
		tokenizer = "jieba"
	case BM25LanguageJapanese:
		tokenizer = "lindera"
		dictKind = "ipadic"
	case BM25LanguageKorean:
		tokenizer = "lindera"
		dictKind = "ko-dic"
	default:
		tokenizer = "lindera"
		dictKind = "ipadic"
	}
	anaTokenizerParams := make(map[string]any)
	anaTokenizerParams["type"] = tokenizer
	if dictKind != "" {
		anaTokenizerParams["dict_kind"] = dictKind
	}
	schema := entity.NewSchema().WithName(cfg.RetrieverName).
		WithField(entity.NewField().WithName(milvus.ColumnID).WithDataType(entity.FieldTypeInt64).WithIsPrimaryKey(true)).
		WithField(entity.NewField().WithName(milvus.ColumnTag).WithDataType(entity.FieldTypeArray).WithElementType(entity.FieldTypeVarChar).WithMaxLength(256).WithMaxCapacity(16)).
		WithField(entity.NewField().WithName(milvus.ColumnText).WithDataType(entity.FieldTypeVarChar).WithMaxLength(cfg.MaxTextLength).WithEnableAnalyzer(true).WithAnalyzerParams(map[string]any{
			"tokenizer": anaTokenizerParams,
		})).
		WithField(entity.NewField().WithName(milvus.ColumnFields).WithDataType(entity.FieldTypeJSON)).
		WithField(entity.NewField().WithName(milvus.ColumnSparse).WithDataType(entity.FieldTypeSparseVector)).
		WithFunction(bm25Function)

	if err := milvusClient.CreateCollection(
		ctx,
		milvusclient.NewCreateCollectionOption(cfg.RetrieverName, schema).WithShardNum(cfg.ShardNum),
	); err != nil {
		return err
	}

	idIndex := index.NewSortedIndex()
	sparseIndex := index.NewSparseInvertedIndex(entity.BM25, cfg.DropRatio)
	var tagIndex index.Index
	if cfg.HasVariableTags {
		tagIndex = index.NewInvertedIndex()
	} else {
		tagIndex = index.NewBitmapIndex()
	}

	idIndexTask, err := milvusClient.CreateIndex(
		ctx,
		milvusclient.NewCreateIndexOption(cfg.RetrieverName, milvus.ColumnID, idIndex).WithIndexName("id_idx"),
	)
	if err != nil {
		return err
	}
	if err := idIndexTask.Await(ctx); err != nil {
		return err
	}

	sparseIndexOpt := milvusclient.NewCreateIndexOption(
		cfg.RetrieverName,
		milvus.ColumnSparse,
		sparseIndex,
	).WithIndexName("sparse_idx")
	sparseIndexOpt.WithExtraParam("inverted_index_algo", "DAAT_MAXSCORE")
	sparseIndexOpt.WithExtraParam("bm25_k1", 1.2)
	sparseIndexOpt.WithExtraParam("bm25_b", 0.75)
	sparseIndexTask, err := milvusClient.CreateIndex(
		ctx,
		sparseIndexOpt,
	)
	if err != nil {
		return err
	}
	if err := sparseIndexTask.Await(ctx); err != nil {
		return err
	}

	tagIndexTask, err := milvusClient.CreateIndex(
		ctx,
		milvusclient.NewCreateIndexOption(cfg.RetrieverName, milvus.ColumnTag, tagIndex).WithIndexName("tag_idx"),
	)
	if err != nil {
		return err
	}
	if err := tagIndexTask.Await(ctx); err != nil {
		return err
	}

	return nil
}

func TruncateAndDestroy(ctx context.Context, milvusClient *milvusclient.Client, retrieverName string) error {
	exist, err := milvusClient.HasCollection(ctx, milvusclient.NewHasCollectionOption(retrieverName))
	if err != nil {
		return err
	}

	if exist {
		if err := milvusClient.DropCollection(ctx, milvusclient.NewDropCollectionOption(retrieverName)); err != nil {
			return err
		}
	}

	return nil
}

func (mbr *MilvusBM25Retriever) Name() string {
	return mbr.RetrieverName
}

func (mbr *MilvusBM25Retriever) ListPartitions(ctx context.Context) ([]string, error) {
	return mbr.MilvusClient.ListPartitions(ctx, milvusclient.NewListPartitionOption(mbr.RetrieverName))
}

func (mbr *MilvusBM25Retriever) HasPartition(ctx context.Context, partitionName string) (bool, error) {
	return mbr.MilvusClient.HasPartition(ctx, milvusclient.NewHasPartitionOption(mbr.RetrieverName, partitionName))
}

func (mbr *MilvusBM25Retriever) AddPartitions(ctx context.Context, partitionName ...string) error {
	for _, name := range partitionName {
		if err := mbr.MilvusClient.CreatePartition(ctx, milvusclient.NewCreatePartitionOption(mbr.RetrieverName, name)); err != nil {
			return err
		}
	}

	return nil
}

func (mbr *MilvusBM25Retriever) LoadPartitions(ctx context.Context, partitionName ...string) error {
	loadPartitionTask, err := mbr.MilvusClient.LoadPartitions(ctx, milvusclient.NewLoadPartitionsOption(mbr.RetrieverName, partitionName...))
	if err != nil {
		return err
	}
	if err := loadPartitionTask.Await(ctx); err != nil {
		return err
	}

	return nil
}

func (mbr *MilvusBM25Retriever) ReleasePartitions(ctx context.Context, partitionName ...string) error {
	if err := mbr.MilvusClient.ReleasePartitions(ctx, milvusclient.NewReleasePartitionsOptions(mbr.RetrieverName, partitionName...)); err != nil {
		return err
	}

	return nil
}

func (mbr *MilvusBM25Retriever) DeletePartitions(ctx context.Context, partitionName ...string) error {
	for _, name := range partitionName {
		if err := mbr.MilvusClient.DropPartition(ctx, milvusclient.NewDropPartitionOption(mbr.RetrieverName, name)); err != nil {
			return err
		}
	}

	return nil
}

func (mbr *MilvusBM25Retriever) AddElement(ctx context.Context, partitionName string, e *milvus.Element) (int64, error) {
	cols := []column.Column{
		column.NewColumnInt64(milvus.ColumnID, []int64{e.ID}),
		column.NewColumnVarCharArray(milvus.ColumnTag, [][]string{e.Tag}),
		column.NewColumnVarChar(milvus.ColumnText, []string{e.TextToEmbed}),
	}
	cols, err := milvus.AppendFieldsJSONColumn(ctx, cols, []*milvus.Element{e}, mbr.FieldsIndexManager, mbr.AutoIndexFields)
	if err != nil {
		return -1, err
	}

	if _, err := mbr.MilvusClient.Insert(
		ctx,
		milvusclient.NewColumnBasedInsertOption(mbr.RetrieverName, cols...).WithPartition(partitionName),
	); err != nil {
		return -1, err
	}

	return e.ID, nil
}

func (mbr *MilvusBM25Retriever) AddElements(ctx context.Context, partitionName string, elements []*milvus.Element) ([]int64, error) {
	ids := make([]int64, 0, len(elements))
	tags := make([][]string, 0, len(elements))
	texts := make([]string, 0, len(elements))

	for _, element := range elements {
		ids = append(ids, element.ID)
		tags = append(tags, element.Tag)
		texts = append(texts, element.TextToEmbed)
	}

	cols := []column.Column{
		column.NewColumnInt64(milvus.ColumnID, ids),
		column.NewColumnVarCharArray(milvus.ColumnTag, tags),
		column.NewColumnVarChar(milvus.ColumnText, texts),
	}
	cols, err := milvus.AppendFieldsJSONColumn(ctx, cols, elements, mbr.FieldsIndexManager, mbr.AutoIndexFields)
	if err != nil {
		return nil, err
	}

	if _, err := mbr.MilvusClient.Insert(
		ctx,
		milvusclient.NewColumnBasedInsertOption(mbr.RetrieverName, cols...).WithPartition(partitionName),
	); err != nil {
		return nil, err
	}

	return ids, nil
}

func (mbr *MilvusBM25Retriever) Search(ctx context.Context, partitionNames []string, args *milvus.SearchArgs) (milvus.Retrievals, error) {
	mode := milvus.ResolveSearchMode(args, milvus.SearchModeBM25)
	switch mode {
	case milvus.SearchModeQuery:
		return mbr.searchByQuery(ctx, partitionNames, args)
	case milvus.SearchModeBM25:
		return mbr.searchByBM25(ctx, partitionNames, args)
	default:
		return nil, fmt.Errorf("bm25 retriever does not support search mode %d", mode)
	}
}

func (mbr *MilvusBM25Retriever) searchByBM25(ctx context.Context, partitionNames []string, args *milvus.SearchArgs) (milvus.Retrievals, error) {
	if args == nil {
		return nil, fmt.Errorf("search args is nil")
	}

	searchOpt := milvusclient.NewSearchOption(
		mbr.RetrieverName,
		milvus.SearchLimit(args),
		[]entity.Vector{entity.Text(args.Text)},
	).WithANNSField(milvus.ColumnSparse).
		WithPartitions(partitionNames...).
		WithOutputFields(milvus.MergeOutputFieldsWithFieldsJSON([]string{milvus.ColumnID, milvus.ColumnTag, milvus.ColumnText}, args.OutputFields)...).
		WithFilter(args.Filter.String())
	if args.Offset > 0 {
		searchOpt.WithOffset(args.Offset)
	}

	searchResults, err := mbr.MilvusClient.Search(ctx, searchOpt)
	if err != nil {
		return nil, err
	}

	return milvus.RetrievalsFromResultSets(searchResults, milvus.ColumnText, true)
}

func (mr *MilvusBM25Retriever) Query(ctx context.Context, partitionNames []string, limit, offset int, filter milvus.RetrieveFilterOption) (milvus.Retrievals, error) {
	return mr.Search(ctx, partitionNames, &milvus.SearchArgs{
		Limit:      limit,
		Offset:     offset,
		Filter:     filter,
		SearchMode: milvus.SearchModeQuery,
	})
}

func (mr *MilvusBM25Retriever) searchByQuery(ctx context.Context, partitionNames []string, args *milvus.SearchArgs) (milvus.Retrievals, error) {
	if args == nil {
		args = &milvus.SearchArgs{}
	}

	queryOpt := milvusclient.
		NewQueryOption(mr.RetrieverName).
		WithFilter(args.Filter.String()).
		WithPartitions(partitionNames...).
		WithOutputFields(milvus.MergeOutputFieldsWithFieldsJSON([]string{milvus.ColumnID, milvus.ColumnTag, milvus.ColumnText}, args.OutputFields)...)
	if args.Limit > 0 {
		queryOpt.WithLimit(args.Limit)
	}
	if args.Offset > 0 {
		queryOpt.WithOffset(args.Offset)
	}
	queryResults, err := mr.MilvusClient.Query(ctx, queryOpt)
	if err != nil {
		return nil, err
	}

	if queryResults.Err != nil {
		return nil, queryResults.Err
	}

	return milvus.RetrievalsFromResultSet(queryResults, milvus.ColumnText, false)
}

func (mbr *MilvusBM25Retriever) DeleteElement(ctx context.Context, partitionName string, ids []int64) error {
	deleteOpt := milvusclient.
		NewDeleteOption(mbr.RetrieverName).
		WithPartition(partitionName).
		WithInt64IDs(milvus.ColumnID, ids)

	if _, err := mbr.MilvusClient.Delete(ctx, deleteOpt); err != nil {
		return err
	}

	return nil
}

func (mbr *MilvusBM25Retriever) UpsertElement(ctx context.Context, partitionName string, e *milvus.Element) error {
	cols := []column.Column{
		column.NewColumnInt64(milvus.ColumnID, []int64{e.ID}),
		column.NewColumnVarCharArray(milvus.ColumnTag, [][]string{e.Tag}),
		column.NewColumnVarChar(milvus.ColumnText, []string{e.TextToEmbed}),
	}
	cols, err := milvus.AppendFieldsJSONColumn(ctx, cols, []*milvus.Element{e}, mbr.FieldsIndexManager, mbr.AutoIndexFields)
	if err != nil {
		return err
	}

	upsertOpt := milvusclient.NewColumnBasedInsertOption(mbr.RetrieverName, cols...).WithPartition(partitionName)

	if _, err := mbr.MilvusClient.Upsert(
		ctx,
		upsertOpt,
	); err != nil {
		return err
	}

	return nil
}
