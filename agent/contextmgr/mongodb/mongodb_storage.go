package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/torrischen/goat/agent/common"
	"github.com/torrischen/goat/agent/contextmgr"
	"github.com/torrischen/goat/agent/message"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	defaultURI               = "mongodb://127.0.0.1:27017"
	defaultDatabase          = "goat"
	defaultContextCollection = "contexts"
	defaultMessageCollection = "messages"
)

// Config configures the MongoDB collections used by MongoDBStore.
type Config struct {
	URI               string
	Database          string
	ContextCollection string
	MessageCollection string
	// Deprecated: KeyPrefix is no longer used in the new Store design
	KeyPrefix string
	// Collection is a shorthand for ContextCollection; messages use Collection + "_messages".
	Collection string
}

// MongoDBStore stores context heads as documents and messages in an indexed log.
type MongoDBStore struct {
	client     *mongo.Client
	contexts   *mongo.Collection
	messages   *mongo.Collection
	ownsClient bool
}

// NewMongoDBStore creates MongoDB storage with head and message collections.
func NewMongoDBStore(config Config) (*MongoDBStore, error) {
	uri := config.URI
	if uri == "" {
		uri = defaultURI
	}
	database := config.Database
	if database == "" {
		database = defaultDatabase
	}
	contextColl, messageColl := resolveCollectionNames(config)

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("create MongoDB client: %w", err)
	}

	db := client.Database(database)
	store := newMongoDBStore(
		client,
		db.Collection(contextColl),
		db.Collection(messageColl),
		true,
	)
	indexCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.ensureIndexes(indexCtx); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("create MongoDB indexes: %w", err)
	}
	return store, nil
}

func resolveCollectionNames(config Config) (string, string) {
	contextColl := config.ContextCollection
	if contextColl == "" {
		contextColl = config.Collection
	}
	if contextColl == "" {
		contextColl = defaultContextCollection
	}

	messageColl := config.MessageCollection
	if messageColl == "" && config.Collection != "" {
		messageColl = config.Collection + "_messages"
	}
	if messageColl == "" {
		messageColl = defaultMessageCollection
	}
	return contextColl, messageColl
}

// NewMongoDBStoreWithCollections wraps existing collections and creates the message index.
// The caller retains ownership of the collections and their client.
func NewMongoDBStoreWithCollections(contexts, messages *mongo.Collection) (*MongoDBStore, error) {
	store := newMongoDBStore(nil, contexts, messages, false)
	indexCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.ensureIndexes(indexCtx); err != nil {
		return nil, fmt.Errorf("create MongoDB indexes: %w", err)
	}
	return store, nil
}

func newMongoDBStore(client *mongo.Client, contexts, messages *mongo.Collection, ownsClient bool) *MongoDBStore {
	return &MongoDBStore{
		client:     client,
		contexts:   contexts,
		messages:   messages,
		ownsClient: ownsClient,
	}
}

// NewMongoDBContextManager creates a Manager backed by MongoDB.
func NewMongoDBContextManager(config Config) (*contextmgr.Manager, error) {
	store, err := NewMongoDBStore(config)
	if err != nil {
		return nil, err
	}
	return contextmgr.NewManager(store), nil
}

func mongoMessageIndexModel() mongo.IndexModel {
	return mongo.IndexModel{
		Keys: bson.D{
			{Key: "uid", Value: 1},
			{Key: "lane", Value: 1},
			{Key: "seq", Value: 1},
		},
		Options: options.Index().SetName("uid_lane_seq"),
	}
}

func (s *MongoDBStore) ensureIndexes(ctx context.Context) error {
	_, err := s.messages.Indexes().CreateOne(ctx, mongoMessageIndexModel())
	return err
}

func (s *MongoDBStore) LoadHead(ctx context.Context, uid common.ContextUID) (*contextmgr.Head, error) {
	var doc HeadDocument
	err := s.contexts.FindOne(ctx, bson.D{{Key: "_id", Value: string(uid)}}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, contextmgr.ErrContextNotFound
	}
	if err != nil {
		return nil, err
	}
	return docToHead(&doc), nil
}

func (s *MongoDBStore) CommitAppend(ctx context.Context, rows []contextmgr.MessageRow, next *contextmgr.Head, expectVersion uint64) error {
	if next == nil {
		return errors.New("commit append requires a head")
	}
	docs := make([]interface{}, len(rows))
	for i, row := range rows {
		doc, err := messageRowToDoc(row)
		if err != nil {
			return err
		}
		docs[i] = doc
	}
	headDoc := headToDoc(next)
	session, err := s.contexts.Database().Client().StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		if expectVersion == 0 {
			if _, err := s.contexts.InsertOne(txCtx, headDoc); err != nil {
				if mongo.IsDuplicateKeyError(err) {
					return nil, contextmgr.ErrCASConflict
				}
				return nil, err
			}
		} else {
			result, err := s.contexts.UpdateOne(txCtx,
				bson.D{{Key: "_id", Value: string(next.UID)}, {Key: "version", Value: expectVersion}},
				bson.D{{Key: "$set", Value: headUpdateFields(next)}},
			)
			if err != nil {
				return nil, err
			}
			if result.MatchedCount == 0 {
				if err := s.contexts.FindOne(txCtx, bson.D{{Key: "_id", Value: string(next.UID)}}).Err(); errors.Is(err, mongo.ErrNoDocuments) {
					return nil, contextmgr.ErrContextNotFound
				} else if err != nil {
					return nil, err
				}
				return nil, contextmgr.ErrCASConflict
			}
		}
		if len(docs) > 0 {
			if _, err := s.messages.InsertMany(txCtx, docs); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	return err
}

func (s *MongoDBStore) ReadMessages(ctx context.Context, uid common.ContextUID, lane contextmgr.Lane, fromSeq, toSeq uint64) ([]contextmgr.MessageRow, error) {
	filter := bson.D{
		{Key: "uid", Value: string(uid)},
		{Key: "lane", Value: string(lane)},
		{Key: "seq", Value: bson.D{{Key: "$gte", Value: fromSeq}, {Key: "$lte", Value: toSeq}}},
	}
	cursor, err := s.messages.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "seq", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var rows []contextmgr.MessageRow
	for cursor.Next(ctx) {
		var doc MessageDocument
		if err := cursor.Decode(&doc); err != nil {
			return nil, err
		}
		row, err := docToMessageRow(&doc)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, cursor.Err()
}

func (s *MongoDBStore) ReplaceCommitted(ctx context.Context, uid common.ContextUID, rows []contextmgr.MessageRow, next *contextmgr.Head, expectVersion uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if next == nil || next.UID != uid {
		return fmt.Errorf("replacement head UID does not match context UID")
	}

	docs := make([]any, len(rows))
	for i, row := range rows {
		doc, err := messageRowToDoc(row)
		if err != nil {
			return err
		}
		docs[i] = doc
	}

	session, err := s.contexts.Database().Client().StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		filter := bson.D{
			{Key: "uid", Value: string(uid)},
			{Key: "lane", Value: string(contextmgr.LaneCommitted)},
		}
		if _, err := s.messages.DeleteMany(txCtx, filter); err != nil {
			return nil, err
		}
		if len(docs) > 0 {
			if _, err := s.messages.InsertMany(txCtx, docs); err != nil {
				return nil, err
			}
		}

		result, err := s.contexts.UpdateOne(txCtx, bson.D{
			{Key: "_id", Value: string(uid)},
			{Key: "version", Value: expectVersion},
		}, bson.D{{Key: "$set", Value: headUpdateFields(next)}})
		if err != nil {
			return nil, err
		}
		if result.MatchedCount == 0 {
			if err := s.contexts.FindOne(txCtx, bson.D{{Key: "_id", Value: string(uid)}}).Err(); err != nil {
				if errors.Is(err, mongo.ErrNoDocuments) {
					return nil, contextmgr.ErrContextNotFound
				}
				return nil, err
			}
			return nil, contextmgr.ErrCASConflict
		}
		return nil, nil
	})
	return err
}

func (s *MongoDBStore) DeleteContext(ctx context.Context, uid common.ContextUID) error {
	_, err := s.contexts.DeleteOne(ctx, bson.D{{Key: "_id", Value: string(uid)}})
	if err != nil {
		return err
	}
	_, err = s.messages.DeleteMany(ctx, bson.D{{Key: "uid", Value: string(uid)}})
	return err
}

// Close disconnects the MongoDB client created by NewMongoDBStore.
func (s *MongoDBStore) Close(ctx context.Context) error {
	if !s.ownsClient || s.client == nil {
		return nil
	}
	return s.client.Disconnect(ctx)
}

func headToDoc(head *contextmgr.Head) *HeadDocument {
	return &HeadDocument{
		ID:           string(head.UID),
		Generation:   head.Generation,
		Version:      head.Version,
		CommittedSeq: head.CommittedSeq,
		PendingStart: head.PendingStart,
		PendingSeq:   head.PendingSeq,
		Finalized:    head.Finalized,
		Runs:         runsToDoc(head.Runs),
	}
}

func headUpdateFields(head *contextmgr.Head) bson.D {
	return bson.D{
		{Key: "generation", Value: head.Generation},
		{Key: "version", Value: head.Version},
		{Key: "committed_seq", Value: head.CommittedSeq},
		{Key: "pending_start", Value: head.PendingStart},
		{Key: "pending_seq", Value: head.PendingSeq},
		{Key: "finalized", Value: head.Finalized},
		{Key: "runs", Value: runsToDoc(head.Runs)},
	}
}

func runsToDoc(runs map[common.RunUID]contextmgr.RunSnapshot) map[string]contextmgr.RunSnapshot {
	result := make(map[string]contextmgr.RunSnapshot, len(runs))
	for k, v := range runs {
		result[string(k)] = v
	}
	return result
}

func docToHead(doc *HeadDocument) *contextmgr.Head {
	runs := make(map[common.RunUID]contextmgr.RunSnapshot)
	for k, v := range doc.Runs {
		runs[common.RunUID(k)] = v
	}
	return &contextmgr.Head{
		UID:          common.ContextUID(doc.ID),
		Generation:   doc.Generation,
		Version:      doc.Version,
		CommittedSeq: doc.CommittedSeq,
		PendingStart: doc.PendingStart,
		PendingSeq:   doc.PendingSeq,
		Finalized:    doc.Finalized,
		Runs:         runs,
	}
}

func messageRowToDoc(row contextmgr.MessageRow) (*MessageDocument, error) {
	msgBytes, err := bson.Marshal(row.Message)
	if err != nil {
		return nil, fmt.Errorf("marshal message: %w", err)
	}
	return &MessageDocument{
		ID: MessageDocumentID{
			UID:  string(row.UID),
			Lane: string(row.Lane),
			Seq:  row.Seq,
		},
		UID:     string(row.UID),
		Lane:    string(row.Lane),
		Seq:     row.Seq,
		Message: msgBytes,
	}, nil
}

func docToMessageRow(doc *MessageDocument) (contextmgr.MessageRow, error) {
	var msg message.Message
	if err := bson.Unmarshal(doc.Message, &msg); err != nil {
		return contextmgr.MessageRow{}, fmt.Errorf("unmarshal message: %w", err)
	}
	return contextmgr.MessageRow{
		UID:     common.ContextUID(doc.UID),
		Lane:    contextmgr.Lane(doc.Lane),
		Seq:     doc.Seq,
		Message: &msg,
	}, nil
}
