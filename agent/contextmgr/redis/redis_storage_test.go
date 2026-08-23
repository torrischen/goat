package redis

import (
	"context"
	"errors"
	"testing"

	miniredis "github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/torrischen/goat/agent/contextmgr"
)

func TestStorageNamespaceAndCAS(t *testing.T) {
	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	storage := NewRedisStorageWithClient(client, "test:")
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()

	if err := storage.Set(ctx, "key", []byte("old")); err != nil {
		t.Fatal(err)
	}
	if got, err := server.Get("test:key"); err != nil || got != "old" {
		t.Fatalf("physical Redis value = %q, %v", got, err)
	}
	if err := storage.CompareAndSwap(ctx, "key", []byte("wrong"), []byte("new")); !errors.Is(err, contextmgr.ErrCASConflict) {
		t.Fatalf("CAS mismatch error = %v", err)
	}
	if err := storage.CompareAndSwap(ctx, "key", []byte("old"), []byte("new")); err != nil {
		t.Fatal(err)
	}
	if value, err := storage.Get(ctx, "key"); err != nil || string(value) != "new" {
		t.Fatalf("Get() = %q, %v", value, err)
	}
}

func TestNewStorageRejectsInvalidURL(t *testing.T) {
	storage, err := NewRedisStorage(Config{URL: "://invalid"})
	if err == nil {
		_ = storage.Close()
		t.Fatal("NewRedisStorage accepted an invalid URL")
	}
}
