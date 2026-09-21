package streaming

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrStreamClosed = errors.New("stream is closed")
	ErrTimeout      = errors.New("operation timeout")
	ErrWouldBlock   = errors.New("stream write would block")
)

type Stream[T any] interface {
	Read() (T, error)
	ReadWithTimeout(timeout time.Duration) (T, error)
	ReadWithContext(ctx context.Context) (T, error)
	Write(item T) error
	WriteWithTimeout(item T, timeout time.Duration) error
	WriteWithContext(ctx context.Context, item T) error
	Close() error
	IsClosed() bool
	Len() int
}

type StreamImpl[T any] struct {
	buffer    chan T
	done      chan struct{}
	closed    atomic.Bool
	closeOnce sync.Once
}

func NewStream[T any](bufferSize int) *StreamImpl[T] {
	return &StreamImpl[T]{
		buffer: make(chan T, bufferSize),
		done:   make(chan struct{}),
	}
}
func NewUnbufferedStream[T any]() *StreamImpl[T] {
	return NewStream[T](0)
}

func (s *StreamImpl[T]) Read() (T, error) {
	var zero T

	for {
		select {
		case item := <-s.buffer:
			return item, nil
		case <-s.done:
			select {
			case item := <-s.buffer:
				return item, nil
			default:
				return zero, ErrStreamClosed
			}
		}
	}
}

func (s *StreamImpl[T]) ReadWithTimeout(timeout time.Duration) (T, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return s.ReadWithContext(ctx)
}

func (s *StreamImpl[T]) ReadWithContext(ctx context.Context) (T, error) {
	var zero T
	select {
	case item := <-s.buffer:
		return item, nil
	case <-s.done:
		select {
		case item := <-s.buffer:
			return item, nil
		default:
			return zero, ErrStreamClosed
		}
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func (s *StreamImpl[T]) Write(item T) error {
	if s.closed.Load() {
		return ErrStreamClosed
	}
	select {
	case <-s.done:
		return ErrStreamClosed
	case s.buffer <- item:
		return nil
	}
}

func (s *StreamImpl[T]) WriteWithTimeout(item T, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return s.WriteWithContext(ctx, item)
}

func (s *StreamImpl[T]) WriteWithContext(ctx context.Context, item T) error {
	if s.closed.Load() {
		return ErrStreamClosed
	}
	select {
	case <-s.done:
		return ErrStreamClosed
	case s.buffer <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *StreamImpl[T]) Close() error {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		close(s.done)
	})
	return nil
}

func (s *StreamImpl[T]) IsClosed() bool {
	return s.closed.Load()
}

func (s *StreamImpl[T]) Len() int {
	return len(s.buffer)
}

func (s *StreamImpl[T]) TryWrite(item T) error {
	if s.closed.Load() {
		return ErrStreamClosed
	}
	select {
	case <-s.done:
		return ErrStreamClosed
	case s.buffer <- item:
		return nil
	default:
		if s.closed.Load() {
			return ErrStreamClosed
		}
		return ErrWouldBlock
	}
}

func (s *StreamImpl[T]) ReadAll() ([]T, error) {
	var items []T
	for {
		item, err := s.Read()
		if err != nil {
			if err == ErrStreamClosed {
				break
			}
			return items, err
		}
		items = append(items, item)
	}

	return items, nil
}

func (s *StreamImpl[T]) WriteAll(items []T) error {
	for _, item := range items {
		if err := s.Write(item); err != nil {
			return err
		}
	}

	return nil
}

func Transform[T, U any](source Stream[T], transformer func(T) U) Stream[U] {
	target := NewStream[U](0)

	go func() {
		defer target.Close()
		for {
			item, err := source.Read()
			if err != nil {
				if err == ErrStreamClosed {
					break
				}
				continue
			}
			transformed := transformer(item)
			if err := target.Write(transformed); err != nil {
				break
			}
		}
	}()

	return target
}

func Filter[T any](source Stream[T], predicate func(T) bool) Stream[T] {
	target := NewStream[T](0)

	go func() {
		defer target.Close()
		for {
			item, err := source.Read()
			if err != nil {
				if err == ErrStreamClosed {
					break
				}
				continue
			}
			if predicate(item) {
				if err := target.Write(item); err != nil {
					break
				}
			}
		}
	}()

	return target
}

func Merge[T any](streams ...Stream[T]) Stream[T] {
	target := NewStream[T](0)

	go func() {
		defer target.Close()
		var wg sync.WaitGroup

		for _, stream := range streams {
			wg.Add(1)
			go func(s Stream[T]) {
				defer wg.Done()
				for {
					item, err := s.Read()
					if err != nil {
						if err == ErrStreamClosed {
							break
						}
						continue
					}
					if err := target.Write(item); err != nil {
						break
					}
				}
			}(stream)
		}

		wg.Wait()
	}()

	return target
}
