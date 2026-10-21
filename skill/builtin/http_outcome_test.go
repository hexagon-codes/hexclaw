package builtin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/skill"
)

type outcomeTransport func(*http.Request) (*http.Response, error)

func (f outcomeTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestSearchExecutionOutcome(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("<p>search response</p>"))
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
			result, err := (&SearchSkill{client: client}).Execute(t.Context(), map[string]any{"query": "math"})
			if status == http.StatusOK {
				if err != nil || result.Detail.Outcome != "success" || !strings.Contains(result.Content, "未找到") {
					t.Fatalf("zero results must succeed: result=%+v err=%v", result, err)
				}
				return
			}
			var failure *skill.ExecutionError
			if !errors.As(err, &failure) || failure.Detail.HTTPStatus != status || failure.Detail.Reason != "http_status" || !strings.Contains(result.Content, "search response") {
				t.Fatalf("HTTP failure lost: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestBrowserExecutionOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("<title>upstream diagnosis</title><p>useful body</p>"))
	}))
	defer server.Close()
	for _, action := range []string{"fetch", "extract", "post"} {
		t.Run(action, func(t *testing.T) {
			result, err := (&BrowserSkill{client: server.Client()}).Execute(t.Context(), map[string]any{
				"url": server.URL, "action": action, "selector": "title", "data": map[string]any{"value": "1"},
			})
			var failure *skill.ExecutionError
			if !errors.As(err, &failure) || result == nil || !strings.Contains(result.Content, "upstream diagnosis") {
				t.Fatalf("useful failed response lost: result=%+v err=%v", result, err)
			}
			if result.Detail.HTTPStatus != 401 || !result.Detail.ResponseReceived || result.Detail.Reason != "http_status" || result.Detail.Outcome != "failure" {
				t.Fatalf("upstream rejection mislabeled: %+v", result.Detail)
			}
		})
	}
}

func TestBrowserPostUnknownDoesNotRetry(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: outcomeTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, context.DeadlineExceeded
	})}
	_, err := (&BrowserSkill{client: client}).Execute(t.Context(), map[string]any{
		"url": "https://example.com/submit", "action": "post", "data": map[string]any{"value": "1"},
	})
	var failure *skill.ExecutionError
	if !errors.As(err, &failure) || !errors.Is(err, context.DeadlineExceeded) || failure.Detail.Outcome != "unknown" || calls != 1 {
		t.Fatalf("unknown POST was guessed or repeated: calls=%d err=%v", calls, err)
	}
}
