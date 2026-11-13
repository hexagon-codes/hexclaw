package builtin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

func TestBrowserRequestArtifactModelProjection(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
		attachment              bool
	}{
		{name: "binary", contentType: "application/octet-stream", body: "\x00download\xff", status: http.StatusOK, attachment: true},
		{name: "empty attachment", contentType: "application/octet-stream", status: http.StatusOK, attachment: true},
		{name: "json", contentType: "application/json", body: `{"ok":false,"value":7}`, status: http.StatusOK},
		{name: "text", contentType: "text/plain; charset=utf-8", body: "原始正文\n第二行", status: http.StatusOK},
		{name: "failed response", contentType: "application/json", body: `{"error":"upstream failed"}`, status: http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				if tc.attachment {
					w.Header().Set("Content-Disposition", `attachment; filename="download.bin"`)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "registered-response")
			var registered map[string]any
			writes := 0
			s := &BrowserSkill{client: server.Client()}
			s.SetArtifactWriter(func(_ context.Context, name, contentType string, reader io.Reader) (map[string]any, error) {
				writes++
				data, err := io.ReadAll(reader)
				if err != nil {
					return nil, err
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					return nil, err
				}
				registered = map[string]any{
					"artifact_id": "registered-http-file", "name": name, "mime": contentType,
					"path": "registered/response", "url": "/api/v1/files/generated/registered/response",
					"digest": fmt.Sprintf("sha256:%x", sha256.Sum256(data)), "size": int64(len(data)),
				}
				return registered, nil
			})
			result, err := s.Execute(t.Context(), map[string]any{"action": "request", "method": "GET", "url": server.URL + "/response"})
			if tc.status >= 400 {
				var failure *skill.ExecutionError
				if !errors.As(err, &failure) || failure.Detail.HTTPStatus != tc.status || failure.Detail.Reason != "http_status" {
					t.Fatalf("HTTP failure changed: result=%+v err=%v", result, err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if writes != 1 || result == nil {
				t.Fatalf("response was not registered once: writes=%d result=%+v", writes, result)
			}
			stored, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(stored, []byte(tc.body)) {
				t.Fatalf("original response bytes changed: bytes=%q err=%v", stored, readErr)
			}
			data := result.Data.(map[string]any)
			if !reflect.DeepEqual(data["artifact"], registered) || data["status"] != tc.status || data["response_complete"] != true || data["business_success"] != "not_evaluated" {
				t.Fatalf("response data contract changed: %+v", data)
			}
			if tc.contentType == "application/json" {
				var want any
				if err := json.Unmarshal([]byte(tc.body), &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(data["json"], want) {
					t.Fatalf("structured JSON response changed: got=%+v want=%+v", data["json"], want)
				}
			}
			wantContent := tc.body
			if tc.contentType == "application/octet-stream" {
				wantContent = fmt.Sprintf("HTTP %d; binary response (%d bytes).", tc.status, len(tc.body))
			}
			if result.Content != wantContent {
				t.Fatalf("original response text changed: got=%q want=%q", result.Content, wantContent)
			}
			parts := result.ToolMessageContent()
			if len(parts) != 1 || parts[0].Type != "text" || !strings.HasPrefix(parts[0].Text, "Registered tool files: ") {
				t.Fatalf("model message lost registered file: %+v", parts)
			}
			var refs []map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(parts[0].Text, "Registered tool files: ")), &refs); err != nil || len(refs) != 1 {
				t.Fatalf("model file reference is invalid: refs=%+v err=%v", refs, err)
			}
			for _, key := range []string{"artifact_id", "name", "mime", "path", "url", "digest"} {
				if refs[0][key] != registered[key] {
					t.Errorf("model file %s differs from registration: got=%v want=%v", key, refs[0][key], registered[key])
				}
			}
			if refs[0]["size"] != float64(len(tc.body)) {
				t.Errorf("model file size changed: %+v", refs[0])
			}
		})
	}
}

func TestBrowserRequestRedirectKeepsAuthenticationAtItsOrigin(t *testing.T) {
	for _, declared := range []bool{false, true} {
		t.Run(fmt.Sprintf("declared_api_key=%t", declared), func(t *testing.T) {
			type observation struct {
				stage  string
				header http.Header
			}
			observed := make(chan observation, 3)
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observed <- observation{"other_origin", r.Header.Clone()}
				w.Header().Set("Content-Type", "text/plain")
				_, _ = io.WriteString(w, "response preserved")
			}))
			defer target.Close()
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					observed <- observation{"original", r.Header.Clone()}
					http.Redirect(w, r, "/same-origin", http.StatusFound)
					return
				}
				observed <- observation{"same_origin", r.Header.Clone()}
				http.Redirect(w, r, target.URL, http.StatusFound)
			}))
			defer source.Close()
			args := map[string]any{"action": "request", "url": source.URL + "/start",
				"headers": map[string]any{"Authorization": "Bearer synthetic", "X-Goog-Api-Key": "synthetic-google", "PRIVATE-TOKEN": "synthetic-private", "X-Request-ID": "request-marker", "Accept": "text/plain"}}
			if declared {
				args["auth"] = map[string]any{"type": "api_key", "in": "header", "name": "X-Service-Credential", "value": "synthetic-service"}
			}
			result, err := NewBrowserSkill().Execute(t.Context(), args)
			if err != nil || result == nil || result.Content != "response preserved" {
				t.Fatalf("redirected response changed: result=%+v err=%v", result, err)
			}
			for index := 0; index < 3; index++ {
				var request observation
				select {
				case request = <-observed:
				default:
					t.Fatal("redirect chain lost an observed physical request")
				}
				for _, name := range []string{"Authorization", "X-Goog-Api-Key", "PRIVATE-TOKEN"} {
					if present := request.header.Get(name) != ""; present != (request.stage != "other_origin") {
						t.Errorf("%s authentication header %s present=%t", request.stage, name, present)
					}
				}
				if present := request.header.Get("X-Service-Credential") != ""; present != (declared && request.stage != "other_origin") {
					t.Errorf("%s declared credential present=%t", request.stage, present)
				}
				if request.header.Get("X-Request-ID") != "request-marker" || request.header.Get("Accept") != "text/plain" {
					t.Errorf("%s lost ordinary task headers", request.stage)
				}
			}
		})
	}
}
