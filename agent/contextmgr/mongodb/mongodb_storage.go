package mongodb

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/torrischen/goat/agent/contextmgr"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	defaultURI        = "mongodb://127.0.0.1:27017"
	defaultDatabase   = "goat"
	defaultCollection = "context_objects"
	defaultKeyPrefix  = "contextmgr:"
)

// Config configures the MongoDB collection used by Storage.
type Config struct {
	URI        string
	Database   string
	Collection string
	KeyPrefix  string
}

// Storage stores each context manager key as one MongoDB document.
type Storage struct {
	client     *mongo.Client
	collection *mongo.Collection
	keyPrefix  string
	ownsClient bool
}

type record struct {
	ID    string `bson:"_id"`
	Value []byte `bson:"value"`
}

// NewMongoDBStorage creates MongoDB storage. Connectivity is checked by the first operation so startup can tolerate failovers.
func NewMongoDBStorage(config Config) (*Storage, error) {
	uri := config.URI
	if uri == "" {
		uri = defaultURI
	}
	database := config.Database
	if database == "" {
		database = defaultDatabase
	}
	collection := config.Collection
	if collection == "" {
		collection = defaultCollection
	}
	if strings.ContainsAny(database, "/\\\x00") {
		return nil, fmt.Errorf("invalid MongoDB database name %q", database)
	}
	if strings.ContainsRune(collection, '\x00') || collection == "" {
		return nil, fmt.Errorf("invalid MongoDB collection name %q", collection)
	}

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("create MongoDB client: %w", err)
	}
	return newStorage(client, client.Database(database).Collection(collection), config.KeyPrefix, true), nil
}

// NewMongoDBStorageWithCollection wraps an existing collection. The caller retains ownership of its client.
func NewMongoDBStorageWithCollection(collection *mongo.Collection, keyPrefix string) *Storage {
	return newStorage(nil, collection, keyPrefix, false)
}

func newStorage(client *mongo.Client, collection *mongo.Collection, keyPrefix string, ownsClient bool) *Storage {
	if keyPrefix == "" {
		keyPrefix = defaultKeyPrefix
	}
	return &Storage{client: client, collection: collection, keyPrefix: keyPrefix, ownsClient: ownsClient}
}

// NewMongoDBContextManager creates a Manager backed by MongoDB.
func NewMongoDBContextManager(config Config) (*contextmgr.Manager, error) {
	storage, err := NewMongoDBStorage(config)
	if err != nil {
		return nil, err
	}
	return contextmgr.NewManager(storage), nil
}

func (s *Storage) physicalKey(key string) string {
	return s.keyPrefix + key
}

// Get returns an isolated copy of the value stored under key.
func (s *Storage) Get(ctx context.Context, key string) ([]byte, error) {
	var result record
	err := s.collection.FindOne(ctx, bson.D{{Key: "_id", Value: s.physicalKey(key)}}).Decode(&result)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, contextmgr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), result.Value...), nil
}

// Set stores value under key.
func (s *Storage) Set(ctx context.Context, key string, value []byte) error {
	filter := bson.D{{Key: "_id", Value: s.physicalKey(key)}}
	update := bson.D{{Key: "$set", Value: bson.D{{Key: "value", Value: append([]byte(nil), value...)}}}}
	_, err := s.collection.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		// A concurrent upsert may create the key after our match phase.
		_, err = s.collection.UpdateOne(ctx, filter, update)
	}
	return err
}

// CompareAndSwap replaces key only when its current value equals oldValue.
func (s *Storage) CreateIfAbsent(ctx context.Context, key string, value []byte) error {
	_, err := s.collection.InsertOne(ctx, record{ID: s.physicalKey(key), Value: append([]byte(nil), value...)})
	if mongo.IsDuplicateKeyError(err) {
		return contextmgr.ErrCASConflict
	}
	return err
}

func (s *Storage) CompareAndSwap(ctx context.Context, key string, oldValue, newValue []byte) error {
	physicalKey := s.physicalKey(key)
	result, err := s.collection.UpdateOne(
		ctx,
		bson.D{
			{Key: "_id", Value: physicalKey},
			{Key: "value", Value: oldValue},
		},
		bson.D{{Key: "$set", Value: bson.D{{Key: "value", Value: append([]byte(nil), newValue...)}}}},
	)
	if err != nil {
		return err
	}
	if result.MatchedCount == 1 {
		return nil
	}
	var exists struct {
		ID string `bson:"_id"`
	}
	err = s.collection.FindOne(ctx, bson.D{{Key: "_id", Value: physicalKey}}).Decode(&exists)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return contextmgr.ErrNotFound
	}
	if err != nil {
		return err
	}
	return contextmgr.ErrCASConflict
}

// Delete removes key. Deleting a missing key succeeds.
func (s *Storage) Delete(ctx context.Context, key string) error {
	_, err := s.collection.DeleteOne(ctx, bson.D{{Key: "_id", Value: s.physicalKey(key)}})
	return err
}

// List returns all logical keys with prefix in lexical order.
func (s *Storage) List(ctx context.Context, prefix string) ([]string, error) {
	physicalPrefix := s.physicalKey(prefix)
	cursor, err := s.collection.Find(
		ctx,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$regex", Value: "^" + regexp.QuoteMeta(physicalPrefix)}}}},
		options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}}),
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	keys := make([]string, 0)
	for cursor.Next(ctx) {
		var result struct {
			ID string `bson:"_id"`
		}
		if err := cursor.Decode(&result); err != nil {
			return nil, err
		}
		keys = append(keys, strings.TrimPrefix(result.ID, s.keyPrefix))
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return keys, nil
}

// Close disconnects the MongoDB client created by NewMongoDBStorage.
func (s *Storage) Close(ctx context.Context) error {
	if !s.ownsClient || s.client == nil {
		return nil
	}
	return s.client.Disconnect(ctx)
}
