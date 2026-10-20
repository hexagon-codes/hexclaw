package builtin

import "testing"

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
