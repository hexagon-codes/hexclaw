package builtin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchSourceLinksPreserveOriginalURLs(t *testing.T) {
	for _, pair := range []struct{ html, want string }{
		{`<a href="https://school.example/exercise?year=6&amp;term=1">Original</a>`, "https://school.example/exercise?year=6&term=1"},
		{`<a href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fpublisher.example%2Fpage">Original</a>`, "https://publisher.example/page"},
		{`<a href="/not-an-original-page">Snippet</a>`, ""},
	} {
		if got := searchSourceURL(pair.html); got != pair.want {
			t.Fatalf("got %q want %q", got, pair.want)
		}
	}
}

func TestSearchExecutionPreservesSourceInContent(t *testing.T) {
	const sourceURL = "https://publisher.example/original"
	const snippet = "Original source excerpt"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<h2 class="result__title"><a href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fpublisher.example%2Foriginal">Example source</a></h2><a class="result__snippet">` + snippet + `</a>`))
	}))
	defer server.Close()
	client := server.Client()
	transport := client.Transport
	client.Transport = outcomeTransport(func(req *http.Request) (*http.Response, error) {
		forward := req.Clone(req.Context())
		forward.URL.Scheme = "http"
		forward.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return transport.RoundTrip(forward)
	})
	result, err := (&SearchSkill{client: client}).Execute(t.Context(), map[string]any{"query": "example source"})
	if err != nil {
		t.Fatal(err)
	}
	linkIndex := strings.Index(result.Content, "[Example source]("+sourceURL+")")
	snippetIndex := strings.Index(result.Content, snippet)
	if linkIndex < 0 || snippetIndex < 0 || linkIndex >= snippetIndex {
		t.Fatalf("source URL must remain in model evidence before the excerpt: %q", result.Content)
	}
}
