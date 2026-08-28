package cohere

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/bytedance/sonic"
)

func TestEmbed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/embed" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request: %s, auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := sonic.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "embed-v4.0" || body["input_type"] != "search_query" {
			t.Fatalf("unexpected body: %#v", body)
		}
		_, _ = w.Write([]byte(`{"embeddings":{"float":[[1,2],[3,4]]}}`))
	}))
	defer server.Close()

	got, err := New(Config{BaseURL: server.URL, APIKey: "secret", InputType: "search_query"}).Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]float32{{1, 2}, {3, 4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
