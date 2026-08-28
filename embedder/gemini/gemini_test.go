package gemini

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
		if r.URL.Path != "/v1beta/models/gemini-embedding-001:batchEmbedContents" || r.URL.RawQuery != "" {
			t.Fatalf("unexpected URL %s", r.URL.String())
		}
		if r.Header.Get("x-goog-api-key") != "secret" {
			t.Fatalf("unexpected API key header %q", r.Header.Get("x-goog-api-key"))
		}
		var body struct {
			Requests []struct {
				Model string `json:"model"`
			} `json:"requests"`
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := sonic.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Requests) != 2 || body.Requests[0].Model != "models/gemini-embedding-001" {
			t.Fatalf("unexpected body: %#v", body)
		}
		_, _ = w.Write([]byte(`{"embeddings":[{"values":[1,2]},{"values":[3,4]}]}`))
	}))
	defer server.Close()

	got, err := New(Config{BaseURL: server.URL + "/v1beta", APIKey: "secret"}).Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]float32{{1, 2}, {3, 4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
