package voyage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestEmbedRestoresInputOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request")
		}
		_, _ = w.Write([]byte(`{"data":[{"embedding":[3,4],"index":1},{"embedding":[1,2],"index":0}]}`))
	}))
	defer server.Close()

	got, err := New(Config{BaseURL: server.URL + "/v1", APIKey: "secret"}).Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]float32{{1, 2}, {3, 4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
