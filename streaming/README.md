# Streaming SDK

`streaming` provides a lightweight, concurrency-safe data stream abstraction built with Go generics and channels. It is suitable for passing type-safe values between producers and consumers, asynchronous tasks, and agent execution loops.

The package depends only on the Go standard library. It supports buffered and unbuffered streams, blocking and non-blocking writes, timeouts, context cancellation, batch operations, and simple stream transformations.

## Features

- Compile-time type safety through Go generics.
- Buffered and unbuffered streams.
- Blocking, timeout-based, and context-aware reads.
- Blocking, non-blocking, timeout-based, and context-aware writes.
- Idempotent close operations with buffered values remaining readable after close.
- `Transform`, `Filter`, and `Merge` stream composition.
- `ReadAll` and `WriteAll` batch operations.

## Installation

```bash
go get github.com/torrischen/goat/streaming
```

## Quick start

```go
package main

import (
	"errors"
	"fmt"
	"log"

	"github.com/torrischen/goat/streaming"
)

func main() {
	stream := streaming.NewStream[int](4)

	go func() {
		defer stream.Close()
		for i := 1; i <= 5; i++ {
			if err := stream.Write(i); err != nil {
				log.Printf("write: %v", err)
				return
			}
		}
	}()

	for {
		value, err := stream.Read()
		if errors.Is(err, streaming.ErrStreamClosed) {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(value)
	}
}
```

The producer normally owns the stream and closes it. The consumer continues reading until it receives `ErrStreamClosed`. Calling `Close` does not discard values that are already buffered.

## Core interface

```go
type Stream[T any] interface {
	Read() (T, error)
	ReadWithTimeout(time.Duration) (T, error)
	ReadWithContext(context.Context) (T, error)
	Write(T) error
	WriteWithTimeout(T, time.Duration) error
	WriteWithContext(context.Context, T) error
	Close() error
	IsClosed() bool
	Len() int
}
```

`NewStream` returns the concrete type `*StreamImpl[T]`. In addition to the interface methods, the concrete type provides `TryWrite`, `ReadAll`, and `WriteAll`.

## Creating a stream

### Buffered stream

```go
stream := streaming.NewStream[string](16)
```

A producer can write without waiting while the buffer has capacity. A buffer size of `0` behaves like an unbuffered channel.

### Unbuffered stream

```go
stream := streaming.NewUnbufferedStream[string]()
```

An unbuffered stream synchronizes the producer and consumer: a write waits until a consumer receives the value.

## Reading values

### Blocking read

```go
value, err := stream.Read()
```

`Read` waits until one of the following occurs:

- A value becomes available.
- The stream is closed and its buffer is empty, in which case it returns `ErrStreamClosed`.

### Timed read

```go
value, err := stream.ReadWithTimeout(2 * time.Second)
if errors.Is(err, context.DeadlineExceeded) {
	// No value arrived before the timeout.
}
```

The current implementation uses `context.WithTimeout` and returns `context.DeadlineExceeded` when the timeout expires.

### Context-aware read

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

value, err := stream.ReadWithContext(ctx)
if errors.Is(err, context.Canceled) {
	// The caller canceled this wait.
}
```

Canceling the context only cancels the current wait; it does not close the stream.

### Read all values

```go
items, err := stream.ReadAll()
```

`ReadAll` continues reading until the stream is closed and its buffer is empty. Before calling it, make sure a producer will eventually call `Close`; otherwise, `ReadAll` may block forever.

## Writing values

### Blocking write

```go
err := stream.Write(value)
```

When the buffer is full, or when the stream is unbuffered, `Write` waits for a consumer. It returns `ErrStreamClosed` if the stream has already been closed.

### Non-blocking write

```go
err := stream.TryWrite(value)
switch {
case err == nil:
	// The value was written.
case errors.Is(err, streaming.ErrWouldBlock):
	// No buffer capacity or consumer is currently available.
case errors.Is(err, streaming.ErrStreamClosed):
	// The stream is closed.
}
```

`TryWrite` is an extension method on `StreamImpl`; it is not part of the `Stream` interface.

### Timed and context-aware writes

```go
err := stream.WriteWithTimeout(value, time.Second)

ctx, cancel := context.WithTimeout(context.Background(), time.Second)
defer cancel()
err = stream.WriteWithContext(ctx, value)
```

A timeout returns `context.DeadlineExceeded`, while explicit cancellation returns `context.Canceled`. Canceling the context does not close the stream.

### Batch write

```go
err := stream.WriteAll([]int{1, 2, 3})
```

`WriteAll` calls `Write` for each value in order. It returns on the first error, and values written before that error are not rolled back.

## Lifecycle

```go
stream := streaming.NewStream[int](8)

fmt.Println(stream.IsClosed()) // false
fmt.Println(stream.Len())      // Number of values currently buffered

_ = stream.Close()
_ = stream.Close() // Close is idempotent
```

Important lifecycle behavior:

- `Close` closes the stream's lifecycle signal; it does not directly close the internal data channel.
- Writes are rejected after close, but consumers can still drain buffered values.
- `Len` returns the current buffer length and does not include values being processed or waiting to be written.
- `IsClosed` reports whether `Close` has been called, not whether the buffer is empty.
- Multiple goroutines may read or write concurrently, but ownership of the final close operation should be explicit.

## Stream transformations

### Transform

```go
source := streaming.NewStream[int](4)
squares := streaming.Transform[int, int](source, func(value int) int {
	return value * value
})
```

`Transform` calls the transformer for every source value and returns a new stream. The target stream closes automatically after the source stream is closed and drained.

### Filter

```go
evens := streaming.Filter[int](source, func(value int) bool {
	return value%2 == 0
})
```

Only values for which the predicate returns `true` are written to the target stream.

### Merge

```go
merged := streaming.Merge[int](streamA, streamB, streamC)
```

`Merge` reads all input streams concurrently. The merged stream closes automatically after every input is closed and drained. Ordering between different inputs is not deterministic, but the order of values from each individual input is preserved.

### Composition example

```go
source := streaming.NewStream[int](8)

filtered := streaming.Filter[int](source, func(value int) bool {
	return value%2 == 0
})
labels := streaming.Transform[int, string](filtered, func(value int) string {
	return fmt.Sprintf("even:%d", value)
})

go func() {
	defer source.Close()
	_ = source.WriteAll([]int{1, 2, 3, 4})
}()

items, err := labels.(*streaming.StreamImpl[string]).ReadAll()
```

Because `Transform`, `Filter`, and `Merge` return the `Stream[T]` interface, consumers can normally read from them directly in a loop. Assert the concrete type only when an extension method is required.

## Errors

| Error | When it is returned |
| --- | --- |
| `ErrStreamClosed` | A write targets a closed stream, or a read targets a closed and drained stream. |
| `ErrWouldBlock` | `TryWrite` cannot complete immediately. |
| `context.DeadlineExceeded` | A timed read or write exceeds its deadline. |
| `context.Canceled` | The supplied context is explicitly canceled. |

The package retains `ErrTimeout`, but `ReadWithTimeout` and `WriteWithTimeout` currently return `context.DeadlineExceeded`. Prefer `errors.Is` when checking context errors.

## Best practices

- Give the producer explicit ownership of closing the stream to avoid coordination deadlocks.
- Use `defer stream.Close()` so the producer closes the stream on every return path.
- Prefer `ReadWithContext` and `WriteWithContext` for long-running tasks.
- Do not poll `IsClosed` to detect completion; keep reading until `ErrStreamClosed`.
- Use unbuffered streams carefully because a producer blocks when no consumer is available.
- The target streams created by `Transform` and `Filter` are unbuffered, so consume downstream values promptly to avoid blocking the pipeline.
- Transformer and predicate functions should not panic; the current helpers do not recover panics from user functions.

## Testing

```bash
go test ./streaming
```
