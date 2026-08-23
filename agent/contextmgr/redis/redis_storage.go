package redis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	goredis "github.com/redis/go-redis/v9"
	"github.com/torrischen/goat/agent/contextmgr"
)

const (
	defaultURL       = "redis://127.0.0.1:6379/0"
	defaultKeyPrefix = "goat:contextmgr:"
)

var createIfAbsentScript = goredis.NewScript(`
if redis.call("EXISTS", KEYS[1]) == 1 then
  return 0
end
redis.call("SET", KEYS[1], ARGV[1])
return 1
`)

var compareAndSwapScript = goredis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current then
  return -1
end
if current ~= ARGV[1] then
  return 0
end
redis.call("SET", KEYS[1], ARGV[2])
return 1
`)

// Config configures a Redis Storage namespace.
type Config struct {
	URL       string
	KeyPrefix string
}

// Storage stores context manager values in Redis.
type Storage struct {
	client     goredis.UniversalClient
	keyPrefix  string
	ownsClient bool
}

// NewRedisStorage creates Redis storage from a redis:// or rediss:// URL.
func NewRedisStorage(config Config) (*Storage, error) {
	url := config.URL
	if url == "" {
		url = defaultURL
	}
	options, err := goredis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse Redis URL: %w", err)
	}
	return newStorage(goredis.NewClient(options), config.KeyPrefix, true), nil
}

// NewRedisStorageWithClient wraps an existing Redis client. The caller retains ownership of the client.
func NewRedisStorageWithClient(client goredis.UniversalClient, keyPrefix string) *Storage {
	return newStorage(client, keyPrefix, false)
}

func newStorage(client goredis.UniversalClient, keyPrefix string, ownsClient bool) *Storage {
	if keyPrefix == "" {
		keyPrefix = defaultKeyPrefix
	}
	return &Storage{client: client, keyPrefix: keyPrefix, ownsClient: ownsClient}
}

// NewRedisContextManager creates a Manager backed by Redis.
func NewRedisContextManager(config Config) (*contextmgr.Manager, error) {
	storage, err := NewRedisStorage(config)
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
	value, err := s.client.Get(ctx, s.physicalKey(key)).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, contextmgr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return value, nil
}

// Set stores value under key.
func (s *Storage) Set(ctx context.Context, key string, value []byte) error {
	return s.client.Set(ctx, s.physicalKey(key), value, 0).Err()
}

// CompareAndSwap replaces key only when its current value equals oldValue.
func (s *Storage) CreateIfAbsent(ctx context.Context, key string, value []byte) error {
	result, err := createIfAbsentScript.Run(ctx, s.client, []string{s.physicalKey(key)}, value).Int64()
	if err != nil {
		return err
	}
	if result == 0 {
		return contextmgr.ErrCASConflict
	}
	return nil
}

func (s *Storage) CompareAndSwap(ctx context.Context, key string, oldValue, newValue []byte) error {
	result, err := compareAndSwapScript.Run(ctx, s.client, []string{s.physicalKey(key)}, oldValue, newValue).Int64()
	if err != nil {
		return err
	}
	switch result {
	case 1:
		return nil
	case -1:
		return contextmgr.ErrNotFound
	default:
		return contextmgr.ErrCASConflict
	}
}

// Delete removes key. Deleting a missing key succeeds.
func (s *Storage) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, s.physicalKey(key)).Err()
}

// List returns all logical keys with prefix in lexical order.
func (s *Storage) List(ctx context.Context, prefix string) ([]string, error) {
	physicalPrefix := s.physicalKey(prefix)
	pattern := escapeGlob(physicalPrefix) + "*"
	keys := make([]string, 0)
	var cursor uint64
	for {
		batch, next, err := s.client.Scan(ctx, cursor, pattern, 256).Result()
		if err != nil {
			return nil, err
		}
		for _, key := range batch {
			if strings.HasPrefix(key, physicalPrefix) {
				keys = append(keys, strings.TrimPrefix(key, s.keyPrefix))
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func escapeGlob(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[")
	return replacer.Replace(value)
}

// Close closes the Redis client created by NewRedisStorage.
func (s *Storage) Close() error {
	if !s.ownsClient {
		return nil
	}
	return s.client.Close()
}
