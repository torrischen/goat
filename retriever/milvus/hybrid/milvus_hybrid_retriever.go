package hybrid

import (
	"context"
	"fmt"

	"github.com/torrischen/goat/embedder"
	"github.com/torrischen/goat/retriever/milvus"
	"github.com/torrischen/goat/util/stderr"

	"github.com/milvus-io/milvus/client/v2/column"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/index"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

type MilvusHybridRetriever struct {
	RetrieverName      string
	Dimension          int64
	Overwrite          bool
	MilvusClient       *milvusclient.Client
	Embedder           embedder.Embedder
	FieldsIndexManager *milvus.FieldsIndexManager
	AutoIndexFields    bool
}

func NewMilvusHybridRetrieverWithMilvus(ctx context.Context, milvusClient *milvusclient.Client, embedder embedder.Embedder, cfg *MilvusHybridRetrieverConfig) (*MilvusHybridRetriever, error) {
	if err := initMilvusHybridRetrieverCollection(ctx, milvusClient, cfg); err != nil {
		return nil, err
	}
	fieldsIndexManager := milvus.NewFieldsIndexManager(cfg.RetrieverName, milvusClient)
	if err := fieldsIndexManager.Ensure(ctx, cfg.FieldsIndexes); err != nil {
		return nil, err
	}

	return &MilvusHybridRetriever{
		RetrieverName:      cfg.RetrieverName,
		Dimension:          cfg.Dimension,
		Overwrite:          cfg.Overwrite,
		MilvusClient:       milvusClient,
		Embedder:           embedder,
		FieldsIndexManager: fieldsIndexManager,
		AutoIndexFields:    cfg.AutoIndexFields,
	}, nil
}

func NewMilvusHybridRetrieverWithConfig(ctx context.Context, embedder embedder.Embedder, cfg *MilvusHybridRetrieverConfig, mcfg *milvus.MilvusConfig) (*MilvusHybridRetriever, error) {
	milvusClient, err := milvus.NewMilvusClient(ctx, mcfg)
	if err != nil {
		return nil, err
	}

	return NewMilvusHybridRetrieverWithMilvus(ctx, milvusClient, embedder, cfg)
}

func initMilvusHybridRetrieverCollection(ctx context.Context, milvusClient *milvusclient.Client, cfg *MilvusHybridRetrieverConfig) error {
	exist, err := milvusClient.HasCollection(ctx, milvusclient.NewHasCollectionOption(cfg.RetrieverName))
	if err != nil {
		return err
	}

	if exist && cfg.Overwrite {
		if err := milvusClient.DropCollection(ctx, milvusclient.NewDropCollectionOption(cfg.RetrieverName)); err != nil {
			return err
		}
		if err := createMilvusHybridRetrieverCollection(ctx, milvusClient, cfg); err != nil {
			return err
		}
	}

	if !exist {
		if err := createMilvusHybridRetrieverCollection(ctx, milvusClient, cfg); err != nil {
			return err
		}
	}

	return nil
}

func createMilvusHybridRetrieverCollection(ctx context.Context, milvusClient *milvusclient.Client, cfg *MilvusHybridRetrieverConfig) error {
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
	anaTokenizerParams := map[string]any{"type": tokenizer}
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
		WithField(entity.NewField().WithName(milvus.ColumnEmbedding).WithDataType(entity.FieldTypeFloatVector).WithDim(cfg.Dimension)).
		WithField(entity.NewField().WithName(milvus.ColumnSparse).WithDataType(entity.FieldTypeSparseVector)).
		WithFunction(bm25Function)

	if err := milvusClient.CreateCollection(ctx, milvusclient.NewCreateCollectionOption(cfg.RetrieverName, schema).WithShardNum(cfg.ShardNum)); err != nil {
		return err
	}

	idIndex := index.NewSortedIndex()
	var vectorIndex index.Index
	if cfg.OnGPU {
		vectorIndex = index.NewGPUCagraIndex(entity.IP, 64, 32)
	} else {
		vectorIndex = index.NewHNSWIndex(entity.IP, 64, 512)
	}
	sparseIndex := index.NewSparseInvertedIndex(entity.BM25, cfg.DropRatio)
	var tagIndex index.Index
	if cfg.HasVariableTags {
		tagIndex = index.NewInvertedIndex()
	} else {
		tagIndex = index.NewBitmapIndex()
	}

	if err := awaitIndex(ctx, milvusClient, milvusclient.NewCreateIndexOption(cfg.RetrieverName, milvus.ColumnID, idIndex).WithIndexName("id_idx")); err != nil {
		return err
	}
	vectorIndexOpt := milvusclient.NewCreateIndexOption(cfg.RetrieverName, milvus.ColumnEmbedding, vectorIndex).WithIndexName("embedding_idx")
	if cfg.OnGPU {
		vectorIndexOpt.WithExtraParam("build_algo", "IVF_PQ")
		vectorIndexOpt.WithExtraParam("cache_dataset_on_device", "true")
	}
	if err := awaitIndex(ctx, milvusClient, vectorIndexOpt); err != nil {
		return err
	}
	sparseIndexOpt := milvusclient.NewCreateIndexOption(cfg.RetrieverName, milvus.ColumnSparse, sparseIndex).WithIndexName("sparse_idx")
	sparseIndexOpt.WithExtraParam("inverted_index_algo", "DAAT_MAXSCORE")
	sparseIndexOpt.WithExtraParam("bm25_k1", 1.2)
	sparseIndexOpt.WithExtraParam("bm25_b", 0.75)
	if err := awaitIndex(ctx, milvusClient, sparseIndexOpt); err != nil {
		return err
	}
	if err := awaitIndex(ctx, milvusClient, milvusclient.NewCreateIndexOption(cfg.RetrieverName, milvus.ColumnTag, tagIndex).WithIndexName("tag_idx")); err != nil {
		return err
	}

	return nil
}

func awaitIndex(ctx context.Context, milvusClient *milvusclient.Client, opt milvusclient.CreateIndexOption) error {
	task, err := milvusClient.CreateIndex(ctx, opt)
	if err != nil {
		return err
	}

	return task.Await(ctx)
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

func (mhr *MilvusHybridRetriever) Name() string {
	return mhr.RetrieverName
}

func (mhr *MilvusHybridRetriever) ListPartitions(ctx context.Context) ([]string, error) {
	return mhr.MilvusClient.ListPartitions(ctx, milvusclient.NewListPartitionOption(mhr.RetrieverName))
}

func (mhr *MilvusHybridRetriever) HasPartition(ctx context.Context, partitionName string) (bool, error) {
	return mhr.MilvusClient.HasPartition(ctx, milvusclient.NewHasPartitionOption(mhr.RetrieverName, partitionName))
}

func (mhr *MilvusHybridRetriever) AddPartitions(ctx context.Context, partitionName ...string) error {
	for _, name := range partitionName {
		if err := mhr.MilvusClient.CreatePartition(ctx, milvusclient.NewCreatePartitionOption(mhr.RetrieverName, name)); err != nil {
			return err
		}
	}

	return nil
}

func (mhr *MilvusHybridRetriever) LoadPartitions(ctx context.Context, partitionName ...string) error {
	loadPartitionTask, err := mhr.MilvusClient.LoadPartitions(ctx, milvusclient.NewLoadPartitionsOption(mhr.RetrieverName, partitionName...))
	if err != nil {
		return err
	}
	if err := loadPartitionTask.Await(ctx); err != nil {
		return err
	}

	return nil
}

func (mhr *MilvusHybridRetriever) ReleasePartitions(ctx context.Context, partitionName ...string) error {
	if err := mhr.MilvusClient.ReleasePartitions(ctx, milvusclient.NewReleasePartitionsOptions(mhr.RetrieverName, partitionName...)); err != nil {
		return err
	}

	return nil
}

func (mhr *MilvusHybridRetriever) DeletePartitions(ctx context.Context, partitionName ...string) error {
	for _, name := range partitionName {
		if err := mhr.MilvusClient.DropPartition(ctx, milvusclient.NewDropPartitionOption(mhr.RetrieverName, name)); err != nil {
			return err
		}
	}

	return nil
}

func (mhr *MilvusHybridRetriever) AddElement(ctx context.Context, partitionName string, e *milvus.Element) (int64, error) {
	embeddings, err := mhr.Embedder.Embed(ctx, []string{e.TextToEmbed})
	if err != nil {
		return -1, err
	}

	if len(embeddings) == 0 {
		return -1, stderr.ErrNoEmbeddings
	}

	cols, err := mhr.insertColumns(ctx, []*milvus.Element{e}, embeddings)
	if err != nil {
		return -1, err
	}

	if _, err := mhr.MilvusClient.Insert(ctx, milvusclient.NewColumnBasedInsertOption(mhr.RetrieverName, cols...).WithPartition(partitionName)); err != nil {
		return -1, err
	}

	return e.ID, nil
}

func (mhr *MilvusHybridRetriever) AddElements(ctx context.Context, partitionName string, elements []*milvus.Element) ([]int64, error) {
	texts := make([]string, 0, len(elements))
	for _, element := range elements {
		texts = append(texts, element.TextToEmbed)
	}
	embeddings, err := mhr.Embedder.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}

	cols, err := mhr.insertColumns(ctx, elements, embeddings)
	if err != nil {
		return nil, err
	}

	if _, err := mhr.MilvusClient.Insert(ctx, milvusclient.NewColumnBasedInsertOption(mhr.RetrieverName, cols...).WithPartition(partitionName)); err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(elements))
	for _, element := range elements {
		ids = append(ids, element.ID)
	}

	return ids, nil
}

func (mhr *MilvusHybridRetriever) Search(ctx context.Context, partitionNames []string, args *milvus.SearchArgs) (milvus.Retrievals, error) {
	mode := milvus.ResolveSearchMode(args, milvus.SearchModeHybrid)
	switch mode {
	case milvus.SearchModeQuery:
		return mhr.searchByQuery(ctx, partitionNames, args)
	case milvus.SearchModeVector:
		return mhr.searchByVector(ctx, partitionNames, args)
	case milvus.SearchModeBM25:
		return mhr.searchByBM25(ctx, partitionNames, args)
	case milvus.SearchModeHybrid:
		return mhr.searchHybrid(ctx, partitionNames, args)
	default:
		return nil, fmt.Errorf("hybrid retriever does not support search mode %d", mode)
	}
}

func (mhr *MilvusHybridRetriever) Query(ctx context.Context, partitionNames []string, limit, offset int, filter milvus.RetrieveFilterOption) (milvus.Retrievals, error) {
	return mhr.Search(ctx, partitionNames, &milvus.SearchArgs{
		Limit:      limit,
		Offset:     offset,
		Filter:     filter,
		SearchMode: milvus.SearchModeQuery,
	})
}

func (mhr *MilvusHybridRetriever) DeleteElement(ctx context.Context, partitionName string, ids []int64) error {
	deleteOpt := milvusclient.
		NewDeleteOption(mhr.RetrieverName).
		WithPartition(partitionName).
		WithInt64IDs(milvus.ColumnID, ids)

	if _, err := mhr.MilvusClient.Delete(ctx, deleteOpt); err != nil {
		return err
	}

	return nil
}

func (mhr *MilvusHybridRetriever) UpsertElement(ctx context.Context, partitionName string, e *milvus.Element) error {
	embeddings, err := mhr.Embedder.Embed(ctx, []string{e.TextToEmbed})
	if err != nil {
		return err
	}
	if len(embeddings) == 0 {
		return stderr.ErrNoEmbeddings
	}

	cols, err := mhr.insertColumns(ctx, []*milvus.Element{e}, embeddings)
	if err != nil {
		return err
	}

	upsertOpt := milvusclient.NewColumnBasedInsertOption(mhr.RetrieverName, cols...).WithPartition(partitionName)
	if _, err := mhr.MilvusClient.Upsert(ctx, upsertOpt); err != nil {
		return err
	}

	return nil
}

func (mhr *MilvusHybridRetriever) insertColumns(ctx context.Context, elements []*milvus.Element, embeddings [][]float32) ([]column.Column, error) {
	if len(elements) != len(embeddings) {
		return nil, fmt.Errorf("embedding count %d does not match element count %d", len(embeddings), len(elements))
	}

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
		column.NewColumnFloatVector(milvus.ColumnEmbedding, int(mhr.Dimension), embeddings),
	}
	cols, err := milvus.AppendFieldsJSONColumn(ctx, cols, elements, mhr.FieldsIndexManager, mhr.AutoIndexFields)
	if err != nil {
		return nil, err
	}

	return cols, nil
}

func (mhr *MilvusHybridRetriever) searchByQuery(ctx context.Context, partitionNames []string, args *milvus.SearchArgs) (milvus.Retrievals, error) {
	if args == nil {
		args = &milvus.SearchArgs{}
	}

	queryOpt := milvusclient.
		NewQueryOption(mhr.RetrieverName).
		WithFilter(args.Filter.String()).
		WithPartitions(partitionNames...).
		WithOutputFields(milvus.MergeOutputFieldsWithFieldsJSON([]string{milvus.ColumnID, milvus.ColumnTag, milvus.ColumnText}, args.OutputFields)...)
	if args.Limit > 0 {
		queryOpt.WithLimit(args.Limit)
	}
	if args.Offset > 0 {
		queryOpt.WithOffset(args.Offset)
	}
	queryResults, err := mhr.MilvusClient.Query(ctx, queryOpt)
	if err != nil {
		return nil, err
	}
	if queryResults.Err != nil {
		return nil, queryResults.Err
	}

	return milvus.RetrievalsFromResultSet(queryResults, milvus.ColumnText, false)
}

func (mhr *MilvusHybridRetriever) searchByVector(ctx context.Context, partitionNames []string, args *milvus.SearchArgs) (milvus.Retrievals, error) {
	embeddings, err := mhr.searchEmbeddings(ctx, args)
	if err != nil {
		return nil, err
	}

	searchOpt := milvusclient.NewSearchOption(
		mhr.RetrieverName,
		milvus.SearchLimit(args),
		[]entity.Vector{entity.FloatVector(embeddings[0])},
	).WithANNSField(milvus.ColumnEmbedding).
		WithPartitions(partitionNames...).
		WithOutputFields(milvus.MergeOutputFieldsWithFieldsJSON([]string{milvus.ColumnID, milvus.ColumnTag, milvus.ColumnText}, args.OutputFields)...).
		WithFilter(args.Filter.String())
	if args.Offset > 0 {
		searchOpt.WithOffset(args.Offset)
	}

	searchResults, err := mhr.MilvusClient.Search(ctx, searchOpt)
	if err != nil {
		return nil, err
	}

	return milvus.RetrievalsFromResultSets(searchResults, milvus.ColumnText, true)
}

func (mhr *MilvusHybridRetriever) searchByBM25(ctx context.Context, partitionNames []string, args *milvus.SearchArgs) (milvus.Retrievals, error) {
	if args == nil {
		return nil, fmt.Errorf("search args is nil")
	}

	searchOpt := milvusclient.NewSearchOption(
		mhr.RetrieverName,
		milvus.SearchLimit(args),
		[]entity.Vector{entity.Text(args.Text)},
	).WithANNSField(milvus.ColumnSparse).
		WithPartitions(partitionNames...).
		WithOutputFields(milvus.MergeOutputFieldsWithFieldsJSON([]string{milvus.ColumnID, milvus.ColumnTag, milvus.ColumnText}, args.OutputFields)...).
		WithFilter(args.Filter.String())
	if args.Offset > 0 {
		searchOpt.WithOffset(args.Offset)
	}

	searchResults, err := mhr.MilvusClient.Search(ctx, searchOpt)
	if err != nil {
		return nil, err
	}

	return milvus.RetrievalsFromResultSets(searchResults, milvus.ColumnText, true)
}

func (mhr *MilvusHybridRetriever) searchHybrid(ctx context.Context, partitionNames []string, args *milvus.SearchArgs) (milvus.Retrievals, error) {
	embeddings, err := mhr.searchEmbeddings(ctx, args)
	if err != nil {
		return nil, err
	}

	limit := milvus.SearchLimit(args)
	vectorReq := milvusclient.NewAnnRequest(milvus.ColumnEmbedding, limit, entity.FloatVector(embeddings[0])).
		WithFilter(args.Filter.String())
	bm25Req := milvusclient.NewAnnRequest(milvus.ColumnSparse, limit, entity.Text(args.Text)).
		WithFilter(args.Filter.String())

	var reranker milvusclient.Reranker
	if len(args.RerankWeights) > 0 {
		reranker = milvusclient.NewWeightedReranker(args.RerankWeights)
	} else {
		reranker = milvusclient.NewRRFReranker()
	}

	searchOpt := milvusclient.NewHybridSearchOption(mhr.RetrieverName, limit, vectorReq, bm25Req).
		WithPartitions(partitionNames...).
		WithOutputFields(milvus.MergeOutputFieldsWithFieldsJSON([]string{milvus.ColumnID, milvus.ColumnTag, milvus.ColumnText}, args.OutputFields)...).
		WithReranker(reranker)
	if args.Offset > 0 {
		searchOpt.WithOffset(args.Offset)
	}

	searchResults, err := mhr.MilvusClient.HybridSearch(ctx, searchOpt)
	if err != nil {
		return nil, err
	}

	return milvus.RetrievalsFromResultSets(searchResults, milvus.ColumnText, true)
}

func (mhr *MilvusHybridRetriever) searchEmbeddings(ctx context.Context, args *milvus.SearchArgs) ([][]float32, error) {
	if args == nil {
		return nil, fmt.Errorf("search args is nil")
	}

	embeddings, err := mhr.Embedder.Embed(ctx, []string{args.Text})
	if err != nil {
		return nil, err
	}
	if len(embeddings) == 0 {
		return nil, stderr.ErrNoEmbeddings
	}

	return embeddings, nil
}
