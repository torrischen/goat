package streaming

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestStreamLifecycle(t *testing.T) {
	s := NewStream[int](2)
	if s.IsClosed() || s.Len() != 0 {
		t.Fatal("new stream has invalid state")
	}
	if err := s.WriteAll([]int{1, 2}); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", s.Len())
	}
	if err := s.TryWrite(3); !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("TryWrite on full stream = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_ = s.Close() // Close is idempotent.
	if !s.IsClosed() {
		t.Fatal("closed stream reports open")
	}
	items, err := s.ReadAll()
	if err != nil || !reflect.DeepEqual(items, []int{1, 2}) {
		t.Fatalf("ReadAll() = %v, %v", items, err)
	}
	if _, err := s.Read(); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Read after drain = %v", err)
	}
	if err := s.Write(3); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Write after close = %v", err)
	}
	if err := s.TryWrite(3); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("TryWrite after close = %v", err)
	}
	if err := s.WriteAll([]int{3}); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("WriteAll after close = %v", err)
	}
}

func TestStreamTimeoutAndContext(t *testing.T) {
	s := NewUnbufferedStream[int]()
	if _, err := s.ReadWithTimeout(5 * time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ReadWithTimeout = %v", err)
	}
	if err := s.WriteWithTimeout(1, 5*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WriteWithTimeout = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ReadWithContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadWithContext = %v", err)
	}
	if err := s.WriteWithContext(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteWithContext = %v", err)
	}

	go func() { _ = s.Write(7) }()
	value, err := s.ReadWithTimeout(time.Second)
	if err != nil || value != 7 {
		t.Fatalf("ReadWithTimeout successful read = %d, %v", value, err)
	}
	_ = s.Close()
	if _, err := s.ReadWithContext(context.Background()); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("ReadWithContext after close = %v", err)
	}
	if err := s.WriteWithContext(context.Background(), 1); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("WriteWithContext after close = %v", err)
	}
}

func TestTransformFilterAndMerge(t *testing.T) {
	source := NewStream[int](4)
	_ = source.WriteAll([]int{1, 2, 3, 4})
	_ = source.Close()
	filtered := Filter[int](source, func(v int) bool { return v%2 == 0 })
	transformed := Transform[int, int](filtered, func(v int) int { return v * 10 })
	items := readAll(t, transformed)
	if !reflect.DeepEqual(items, []int{20, 40}) {
		t.Fatalf("filtered and transformed values = %v", items)
	}

	first := NewStream[int](2)
	second := NewStream[int](2)
	_ = first.WriteAll([]int{1, 3})
	_ = second.WriteAll([]int{2, 4})
	_ = first.Close()
	_ = second.Close()
	merged := readAll(t, Merge[int](first, second))
	sort.Ints(merged)
	if !reflect.DeepEqual(merged, []int{1, 2, 3, 4}) {
		t.Fatalf("merged values = %v", merged)
	}

	empty := readAll(t, Merge[int]())
	if len(empty) != 0 {
		t.Fatalf("Merge() = %v, want empty", empty)
	}
}

func readAll[T any](t *testing.T, stream Stream[T]) []T {
	t.Helper()
	var result []T
	for {
		item, err := stream.Read()
		if errors.Is(err, ErrStreamClosed) {
			return result
		}
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, item)
	}
}
