package httpjson

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request = %s with content type %q", r.Method, r.Header.Get("Content-Type"))
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Empty"); got != "" {
			t.Errorf("empty header was sent as %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":"ok"}`))
	}))
	defer server.Close()

	var output struct {
		Value string `json:"value"`
	}
	err := Post(context.Background(), nil, server.URL, map[string]string{
		"Authorization": "Bearer token",
		"X-Empty":       "",
	}, map[string]string{"input": "hello"}, &output)
	if err != nil || output.Value != "ok" {
		t.Fatalf("Post() output = %+v, err = %v", output, err)
	}
}

func TestPostErrors(t *testing.T) {
	t.Run("marshal", func(t *testing.T) {
		err := Post(context.Background(), nil, "http://example.com", nil, make(chan int), nil)
		if err == nil || !strings.Contains(err.Error(), "marshal embedding request") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("invalid URL", func(t *testing.T) {
		err := Post(context.Background(), nil, "://bad", nil, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "create embedding request") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("transport", func(t *testing.T) {
		client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		})}
		err := Post(context.Background(), client, "http://example.com", nil, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "send embedding request") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(" bad input \n"))
		}))
		defer server.Close()
		err := Post(context.Background(), server.Client(), server.URL, nil, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "400 Bad Request: bad input") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("decode", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not-json"))
		}))
		defer server.Close()
		var output any
		err := Post(context.Background(), server.Client(), server.URL, nil, nil, &output)
		if err == nil || !strings.Contains(err.Error(), "decode embedding response") {
			t.Fatalf("error = %v", err)
		}
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
