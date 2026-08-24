package file

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/torrischen/goat/agent/contextmgr"
)

func TestFileStorageCRUD(t *testing.T) {
	ctx := context.Background()
	storage, err := NewFileStorage(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}

	if err := storage.Set(ctx, "ctx:a:head", []byte("v1")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	got, err := storage.Get(ctx, "ctx:a:head")
	if err != nil || string(got) != "v1" {
		t.Fatalf("Get() = %q, %v", got, err)
	}

	if _, err := storage.Get(ctx, "ctx:missing:head"); err != contextmgr.ErrNotFound {
		t.Fatalf("Get(missing) error = %v", err)
	}

	if err := storage.CompareAndSwap(ctx, "ctx:a:head", []byte("v1"), []byte("v2")); err != nil {
		t.Fatalf("CAS failed: %v", err)
	}
	if err := storage.CompareAndSwap(ctx, "ctx:a:head", []byte("v1"), []byte("v3")); err != contextmgr.ErrCASConflict {
		t.Fatalf("CAS conflict error = %v", err)
	}
	if err := storage.CompareAndSwap(ctx, "ctx:missing:head", nil, []byte("v")); err != contextmgr.ErrNotFound {
		t.Fatalf("CAS(missing) error = %v", err)
	}

	if err := storage.Set(ctx, "ctx:a:messages", []byte("m")); err != nil {
		t.Fatal(err)
	}
	if err := storage.Set(ctx, "ctx:a:pending", []byte("p")); err != nil {
		t.Fatal(err)
	}

	keys, err := storage.List(ctx, "ctx:a:")
	if err != nil || len(keys) != 3 {
		t.Fatalf("List() = %v, %v", keys, err)
	}

	if err := storage.Delete(ctx, "ctx:a:head"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if err := storage.Delete(ctx, "ctx:a:head"); err != nil {
		t.Fatalf("Delete was not idempotent: %v", err)
	}
}

func TestNewFileContextManager(t *testing.T) {
	manager, err := NewFileContextManager(filepath.Join(t.TempDir(), "contexts"))
	if err != nil || manager == nil {
		t.Fatalf("NewFileContextManager() = %v, %v", manager, err)
	}
}
