package vertex

import (
	"context"
	"io"
	"iter"
	"sync"

	"github.com/torrischen/goat/agent/message"
	"google.golang.org/genai"
)

type streamReader struct {
	responses chan streamResult
	closed    chan struct{}
	cancel    context.CancelFunc
	closeOnce sync.Once
	done      bool
}

// newStreamReader adapts the SDK's iterator to llm.StreamReader.
func newStreamReader(seq iter.Seq2[*genai.GenerateContentResponse, error], cancel context.CancelFunc) *streamReader {
	r := &streamReader{responses: make(chan streamResult), closed: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(r.responses)
		for resp, err := range seq {
			select {
			case r.responses <- streamResult{resp, err}:
			case <-r.closed:
				return
			}
		}
	}()
	return r
}

type streamResult struct {
	resp *genai.GenerateContentResponse
	err  error
}

func (r *streamReader) Recv() (*message.Message, error) {
	if r.done {
		return nil, io.EOF
	}
	for item := range r.responses {
		if item.err != nil {
			r.done = true
			return nil, item.err
		}
		if item.resp == nil {
			continue
		}
		return decodeResponse(item.resp), nil
	}
	r.done = true
	return nil, io.EOF
}

func (r *streamReader) Close() {
	r.closeOnce.Do(func() {
		r.done = true
		close(r.closed)
		if r.cancel != nil {
			r.cancel()
		}
	})
}
