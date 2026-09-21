package vector

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

type MilvusVectorRetriever struct {
	RetrieverName      string
	Dimension          int64
	Overwrite          bool
	MilvusClient       *milvusclient.Client
	Embedder           embedder.Embedder
	FieldsIndexManager *milvus.FieldsIndexManager
	AutoIndexFields    bool
}

type MilvusRetrieverSchema struct {
	ID        int64
	Tag       []string
	Embedding []float32
	Fields    milvus.Fields
}

func NewMilvusRetrieverWithMilvus(ctx context.Context, milvusClient *milvusclient.Client, embedder embedder.Embedder, cfg *MilvusRetrieverConfig) (*MilvusVectorRetriever, error) {
	if err := initMilvusRetrieverMilvusCollection(ctx, milvusClient, cfg); err != nil {
		return nil, err
	}
	fieldsIndexManager := milvus.NewFieldsIndexManager(cfg.RetrieverName, milvusClient)
	if err := fieldsIndexManager.Ensure(ctx, cfg.FieldsIndexes); err != nil {
		return nil, err
	}

	return &MilvusVectorRetriever{
		RetrieverName:      cfg.RetrieverName,
		Dimension:          cfg.Dimension,
		Overwrite:          cfg.Overwrite,
		MilvusClient:       milvusClient,
		Embedder:           embedder,
		FieldsIndexManager: fieldsIndexManager,
		AutoIndexFields:    cfg.AutoIndexFields,
	}, nil
}

func NewMilvusRetrieverWithConfig(ctx context.Context, embedder embedder.Embedder, cfg *MilvusRetrieverConfig, mcfg *milvus.MilvusConfig) (*MilvusVectorRetriever, error) {
	milvusClient, err := milvus.NewMilvusClient(ctx, mcfg)
	if err != nil {
		return nil, err
	}

	return NewMilvusRetrieverWithMilvus(ctx, milvusClient, embedder, cfg)
}

func initMilvusRetrieverMilvusCollection(ctx context.Context, milvusClient *milvusclient.Client, cfg *MilvusRetrieverConfig) error {
	exist, err := milvusClient.HasCollection(ctx, milvusclient.NewHasCollectionOption(cfg.RetrieverName))
	if err != nil {
		return err
	}

	if exist && cfg.Overwrite {
		if err := milvusClient.DropCollection(ctx, milvusclient.NewDropCollectionOption(cfg.RetrieverName)); err != nil {
			return err
		}
		if err := createMilvusRetrieverCollection(ctx, milvusClient, cfg); err != nil {
			return err
		}
	}

	if !exist {
		if err := createMilvusRetrieverCollection(ctx, milvusClient, cfg); err != nil {
			return err
		}
	}

	return nil
}

func createMilvusRetrieverCollection(ctx context.Context, milvusClient *milvusclient.Client, cfg *MilvusRetrieverConfig) error {
	schema := entity.NewSchema().WithName(cfg.RetrieverName).
		WithField(entity.NewField().WithName(milvus.ColumnID).WithDataType(entity.FieldTypeInt64).WithIsPrimaryKey(true)).
		WithField(entity.NewField().WithName(milvus.ColumnTag).WithDataType(entity.FieldTypeArray).WithElementType(entity.FieldTypeVarChar).WithMaxLength(256).WithMaxCapacity(16)).
		WithField(entity.NewField().WithName(milvus.ColumnFields).WithDataType(entity.FieldTypeJSON)).
		WithField(entity.NewField().WithName(milvus.ColumnEmbedding).WithDataType(entity.FieldTypeFloatVector).WithDim(cfg.Dimension))

	if err := milvusClient.CreateCollection(ctx, milvusclient.NewCreateCollectionOption(cfg.RetrieverName, schema).WithShardNum(cfg.ShardNum)); err != nil {
		return err
	}

	// construct id index and opt
	idIndex := index.NewSortedIndex()
	idIndexOpt := milvusclient.NewCreateIndexOption(cfg.RetrieverName, "id", idIndex).WithIndexName("id_idx")

	// construct vector index and opt
	var vectorIndex index.Index
	if cfg.OnGPU {
		vectorIndex = index.NewGPUCagraIndex(entity.IP, 64, 32)
	} else {
		vectorIndex = index.NewHNSWIndex(entity.IP, 64, 512)
	}
	vectorIndexOpt := milvusclient.
		NewCreateIndexOption(cfg.RetrieverName, milvus.ColumnEmbedding, vectorIndex).
		WithIndexName("embedding_idx")
	if cfg.OnGPU {
		vectorIndexOpt.WithExtraParam("build_algo", "IVF_PQ")
		vectorIndexOpt.WithExtraParam("cache_dataset_on_device", "true")
	}

	// construct tag index and opt
	var tagIndex index.Index
	if cfg.HasVariableTags {
		tagIndex = index.NewInvertedIndex()
	} else {
		tagIndex = index.NewBitmapIndex()
	}
	tagIndexOpt := milvusclient.NewCreateIndexOption(cfg.RetrieverName, milvus.ColumnTag, tagIndex).WithIndexName("tag_idx")

	// create id index
	idIndexTask, err := milvusClient.CreateIndex(ctx, idIndexOpt)
	if err != nil {
		return err
	}
	if err := idIndexTask.Await(ctx); err != nil {
		return err
	}

	// create vector index
	vectorIndexTask, err := milvusClient.CreateIndex(ctx, vectorIndexOpt)
	if err != nil {
		return err
	}
	if err := vectorIndexTask.Await(ctx); err != nil {
		return err
	}

	// create tag index
	tagIndexTask, err := milvusClient.CreateIndex(ctx, tagIndexOpt)
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

func (mr *MilvusVectorRetriever) Name() string {
	return mr.RetrieverName
}

func (mr *MilvusVectorRetriever) ListPartitions(ctx context.Context) ([]string, error) {
	return mr.MilvusClient.ListPartitions(ctx, milvusclient.NewListPartitionOption(mr.RetrieverName))
}

func (mr *MilvusVectorRetriever) HasPartition(ctx context.Context, partitionName string) (bool, error) {
	return mr.MilvusClient.HasPartition(ctx, milvusclient.NewHasPartitionOption(mr.RetrieverName, partitionName))
}

func (mr *MilvusVectorRetriever) AddPartitions(ctx context.Context, partitionName ...string) error {
	for _, name := range partitionName {
		if err := mr.MilvusClient.CreatePartition(ctx, milvusclient.NewCreatePartitionOption(mr.RetrieverName, name)); err != nil {
			return err
		}
	}

	return nil
}

func (mr *MilvusVectorRetriever) LoadPartitions(ctx context.Context, partitionName ...string) error {
	loadPartitionTask, err := mr.MilvusClient.LoadPartitions(ctx, milvusclient.NewLoadPartitionsOption(mr.RetrieverName, partitionName...))
	if err != nil {
		return err
	}
	if err := loadPartitionTask.Await(ctx); err != nil {
		return err
	}

	return nil
}

func (mr *MilvusVectorRetriever) ReleasePartitions(ctx context.Context, partitionName ...string) error {
	if err := mr.MilvusClient.ReleasePartitions(ctx, milvusclient.NewReleasePartitionsOptions(mr.RetrieverName, partitionName...)); err != nil {
		return err
	}

	return nil
}

func (mr *MilvusVectorRetriever) DeletePartitions(ctx context.Context, partitionName ...string) error {
	for _, name := range partitionName {
		if err := mr.MilvusClient.DropPartition(ctx, milvusclient.NewDropPartitionOption(mr.RetrieverName, name)); err != nil {
			return err
		}
	}

	return nil
}

func (mr *MilvusVectorRetriever) AddElement(ctx context.Context, partitionName string, e *milvus.Element) (int64, error) {
	embeddings, err := mr.Embedder.Embed(ctx, []string{e.TextToEmbed})
	if err != nil {
		return -1, err
	}

	cols := []column.Column{
		column.NewColumnInt64(milvus.ColumnID, []int64{e.ID}),
		column.NewColumnVarCharArray(milvus.ColumnTag, [][]string{e.Tag}),
		column.NewColumnFloatVector(milvus.ColumnEmbedding, int(mr.Dimension), embeddings),
	}
	cols, err = milvus.AppendFieldsJSONColumn(ctx, cols, []*milvus.Element{e}, mr.FieldsIndexManager, mr.AutoIndexFields)
	if err != nil {
		return -1, err
	}

	if _, err := mr.MilvusClient.Insert(
		ctx,
		milvusclient.NewColumnBasedInsertOption(mr.RetrieverName, cols...).WithPartition(partitionName),
	); err != nil {
		return -1, err
	}

	return e.ID, nil
}

func (mr *MilvusVectorRetriever) AddElements(ctx context.Context, partitionName string, elements []*milvus.Element) ([]int64, error) {
	ids := make([]int64, 0, len(elements))
	tags := make([][]string, 0, len(elements))
	for _, element := range elements {
		ids = append(ids, element.ID)
		tags = append(tags, element.Tag)
	}

	textsToEmbed := make([]string, 0, len(elements))
	for _, element := range elements {
		textsToEmbed = append(textsToEmbed, element.TextToEmbed)
	}
	embeddings, err := mr.Embedder.Embed(ctx, textsToEmbed)
	if err != nil {
		return nil, err
	}

	cols := []column.Column{
		column.NewColumnInt64(milvus.ColumnID, ids),
		column.NewColumnVarCharArray(milvus.ColumnTag, tags),
		column.NewColumnFloatVector(milvus.ColumnEmbedding, int(mr.Dimension), embeddings),
	}
	cols, err = milvus.AppendFieldsJSONColumn(ctx, cols, elements, mr.FieldsIndexManager, mr.AutoIndexFields)
	if err != nil {
		return nil, err
	}

	if _, err := mr.MilvusClient.Insert(
		ctx,
		milvusclient.NewColumnBasedInsertOption(mr.RetrieverName, cols...).WithPartition(partitionName),
	); err != nil {
		return nil, err
	}

	return ids, nil
}

func (mr *MilvusVectorRetriever) Search(ctx context.Context, partitionNames []string, args *milvus.SearchArgs) (milvus.Retrievals, error) {
	mode := milvus.ResolveSearchMode(args, milvus.SearchModeVector)
	switch mode {
	case milvus.SearchModeQuery:
		return mr.searchByQuery(ctx, partitionNames, args)
	case milvus.SearchModeVector:
		return mr.searchByVector(ctx, partitionNames, args)
	default:
		return nil, fmt.Errorf("vector retriever does not support search mode %d", mode)
	}
}

func (mr *MilvusVectorRetriever) searchByVector(ctx context.Context, partitionNames []string, args *milvus.SearchArgs) (milvus.Retrievals, error) {
	if args == nil {
		return nil, fmt.Errorf("search args is nil")
	}

	embeddings, err := mr.Embedder.Embed(ctx, []string{args.Text})
	if err != nil {
		return nil, err
	}

	if len(embeddings) == 0 {
		return nil, stderr.ErrNoEmbeddings
	}

	searchOpt := milvusclient.NewSearchOption(
		mr.RetrieverName,
		milvus.SearchLimit(args),
		[]entity.Vector{entity.FloatVector(embeddings[0])},
	).
		WithANNSField(milvus.ColumnEmbedding).
		WithPartitions(partitionNames...).
		WithOutputFields(milvus.MergeOutputFieldsWithFieldsJSON([]string{milvus.ColumnID, milvus.ColumnTag}, args.OutputFields)...).
		WithFilter(args.Filter.String())
	if args.Offset > 0 {
		searchOpt.WithOffset(args.Offset)
	}

	searchResults, err := mr.MilvusClient.Search(ctx, searchOpt)
	if err != nil {
		return nil, err
	}

	return milvus.RetrievalsFromResultSets(searchResults, "", true)
}

func (mr *MilvusVectorRetriever) searchByQuery(ctx context.Context, partitionNames []string, args *milvus.SearchArgs) (milvus.Retrievals, error) {
	if args == nil {
		args = &milvus.SearchArgs{}
	}

	queryOpt := milvusclient.
		NewQueryOption(mr.RetrieverName).
		WithFilter(args.Filter.String()).
		WithPartitions(partitionNames...).
		WithOutputFields(milvus.MergeOutputFieldsWithFieldsJSON([]string{milvus.ColumnID, milvus.ColumnTag}, args.OutputFields)...)
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

	return milvus.RetrievalsFromResultSet(queryResults, "", false)
}

func (mr *MilvusVectorRetriever) Query(ctx context.Context, partitionNames []string, limit, offset int, filter milvus.RetrieveFilterOption) ([]*MilvusRetrieverSchema, error) {
	queryOpt := milvusclient.
		NewQueryOption(mr.RetrieverName).
		WithFilter(filter.String()).
		WithPartitions(partitionNames...).
		WithOutputFields(milvus.ColumnID, milvus.ColumnTag, milvus.ColumnEmbedding, milvus.ColumnFields).
		WithLimit(limit).
		WithOffset(offset)
	queryResults, err := mr.MilvusClient.Query(ctx, queryOpt)
	if err != nil {
		return nil, err
	}

	result := make([]*MilvusRetrieverSchema, 0)
	if queryResults.Err != nil {
		return nil, queryResults.Err
	}

	var idColumn *column.ColumnInt64
	var tagColumn *column.ColumnVarCharArray
	var embeddingColumn *column.ColumnFloatVector
	var fieldsColumn column.Column
	for _, field := range queryResults.Fields {
		if field.Name() == milvus.ColumnID {
			c, ok := field.(*column.ColumnInt64)
			if !ok {
				return nil, stderr.ErrMilvusIDColumnType
			}
			idColumn = c
		}
		if field.Name() == milvus.ColumnTag {
			c, ok := field.(*column.ColumnVarCharArray)
			if !ok {
				return nil, stderr.ErrMilvusTagColumnType
			}
			tagColumn = c
		}
		if field.Name() == milvus.ColumnEmbedding {
			c, ok := field.(*column.ColumnFloatVector)
			if !ok {
				return nil, stderr.ErrMilvusEmbeddingColumnType
			}
			embeddingColumn = c
		}
		if field.Name() == milvus.ColumnFields {
			fieldsColumn = field
		}
	}

	if idColumn == nil || tagColumn == nil || embeddingColumn == nil {
		return nil, stderr.ErrMilvusColumnNotFound
	}

	idColumnArray := idColumn.Data()
	tagColumnArray := tagColumn.Data()
	embeddingColumnArray := embeddingColumn.Data()

	for j := 0; j < len(idColumnArray); j++ {
		fields, err := milvus.FieldsFromColumn(fieldsColumn, j)
		if err != nil {
			return nil, err
		}
		result = append(result, &MilvusRetrieverSchema{
			ID:        idColumnArray[j],
			Tag:       tagColumnArray[j],
			Embedding: embeddingColumnArray[j],
			Fields:    fields,
		})
	}

	return result, nil
}

func (mr *MilvusVectorRetriever) DeleteElement(ctx context.Context, partitionName string, ids []int64) error {
	deleteOpt := milvusclient.
		NewDeleteOption(mr.RetrieverName).
		WithPartition(partitionName).
		WithInt64IDs(milvus.ColumnID, ids)

	if _, err := mr.MilvusClient.Delete(ctx, deleteOpt); err != nil {
		return err
	}

	return nil
}

func (mr *MilvusVectorRetriever) UpsertElement(ctx context.Context, partitionName string, e *milvus.Element) error {
	embeddings, err := mr.Embedder.Embed(ctx, []string{e.TextToEmbed})
	if err != nil {
		return err
	}

	cols := []column.Column{
		column.NewColumnInt64(milvus.ColumnID, []int64{e.ID}),
		column.NewColumnVarCharArray(milvus.ColumnTag, [][]string{e.Tag}),
		column.NewColumnFloatVector(milvus.ColumnEmbedding, int(mr.Dimension), embeddings),
	}
	cols, err = milvus.AppendFieldsJSONColumn(ctx, cols, []*milvus.Element{e}, mr.FieldsIndexManager, mr.AutoIndexFields)
	if err != nil {
		return err
	}

	upsertOpt := milvusclient.NewColumnBasedInsertOption(mr.RetrieverName, cols...).WithPartition(partitionName)

	if _, err := mr.MilvusClient.Upsert(
		ctx,
		upsertOpt,
	); err != nil {
		return err
	}

	return nil
}
