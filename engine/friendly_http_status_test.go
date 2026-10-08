package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/egress"
)

func TestFriendlyLLMErrorPreservesOnlySanitizedHTTPFailureStatus(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 502, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			raw := &llm.ProviderError{
				Provider: "sensitive-provider", Action: "sensitive-action", Method: "sensitive-method",
				URL: "https://example.invalid/?token=sensitive-url", StatusCode: status,
				Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Body: "sensitive-body",
				BodyTruncated: true, RequestPreview: "sensitive-messages", RequestPreviewTruncated: true,
				RequestHeadersBlocked: []string{"sensitive-request-header"}, ResponseHeaders: map[string]string{"Authorization": "sensitive-credential"},
				RequestID: "sensitive-request-id", RetryAfter: time.Second, Elapsed: time.Second,
				Cause: errors.New("sensitive-cause"),
			}
			got := friendlyLLMError(fmt.Errorf("sensitive-wrap: %w", raw))
			wantMessage := "模型服务暂时不可用，请稍后重试或切换模型。"
			if status == 401 || status == 403 {
				wantMessage = "模型服务鉴权失败，请检查 API Key 配置或切换到其他模型。"
			} else if status == 429 {
				wantMessage = "请求过于频繁，已被上游限流。请稍等片刻再试。"
			} else if status == 504 {
				wantMessage = "模型响应超时，请稍后重试或切换到更快的模型。"
			}
			if got.Error() != wantMessage {
				t.Fatalf("existing friendly copy changed: %q", got.Error())
			}
			var sanitized *llm.ProviderError
			if !errors.As(got, &sanitized) || !reflect.DeepEqual(sanitized, &llm.ProviderError{StatusCode: status}) {
				t.Fatalf("trusted HTTP failure did not retain only its status: %T", errors.Unwrap(got))
			}
			if sanitized == raw || errors.Is(got, raw) || errors.Unwrap(sanitized) != nil {
				t.Fatal("friendly error retained the original provider or raw cause")
			}
			for current := got; current != nil; current = errors.Unwrap(current) {
				for _, rendered := range []string{current.Error(), fmt.Sprintf("%v", current), fmt.Sprintf("%+v", current), fmt.Sprintf("%#v", current)} {
					if strings.Contains(rendered, "sensitive-") || strings.Contains(rendered, "example.invalid") {
						t.Fatal("friendly error chain leaked upstream details")
					}
				}
			}
			if raw.Body != "sensitive-body" || raw.ResponseHeaders["Authorization"] != "sensitive-credential" {
				t.Fatal("sanitization modified the caller's original error")
			}
		})
	}
}

func TestFriendlyLLMErrorHTTPAndUnknownBoundaries(t *testing.T) {
	for _, contextCause := range []error{context.DeadlineExceeded, context.Canceled} {
		t.Run("known HTTP with "+contextCause.Error(), func(t *testing.T) {
			got := newFriendlyLLMError("unchanged friendly copy", &llm.ProviderError{StatusCode: 503, Cause: contextCause})
			var provider *llm.ProviderError
			if !errors.As(got, &provider) || !reflect.DeepEqual(provider, &llm.ProviderError{StatusCode: 503}) || got.Error() != "unchanged friendly copy" {
				t.Fatal("known HTTP response became an unknown context failure")
			}
		})
	}
	for _, tc := range []struct {
		name      string
		raw       error
		wantCause error
	}{
		{"status zero transport", &llm.ProviderError{Cause: io.ErrUnexpectedEOF}, nil},
		{"HTTP 200 interrupted body", &llm.ProviderError{StatusCode: 200, Status: "200 OK", Cause: io.ErrUnexpectedEOF}, nil},
		{"status zero auth-looking message", &llm.ProviderError{Cause: errors.New("401 invalid api key sensitive-credential")}, nil},
		{"plain status-looking message", errors.New("500 internal server error sensitive-body"), nil},
		{"wrapped deadline", fmt.Errorf("sensitive-wrap: %w", context.DeadlineExceeded), context.DeadlineExceeded},
		{"HTTP 200 cancelled body", &llm.ProviderError{StatusCode: 200, Cause: context.Canceled}, context.Canceled},
		{"denied before HTTP", errors.Join(egress.ErrDenied, &llm.ProviderError{StatusCode: 503, Body: "sensitive-body"}), egress.ErrDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := friendlyLLMError(tc.raw)
			var provider *llm.ProviderError
			if errors.As(got, &provider) || errors.Unwrap(got) != tc.wantCause {
				t.Fatal("non-HTTP failure acquired a definitive provider response or lost its standard cause")
			}
			if strings.Contains(fmt.Sprintf("%#v", got), "sensitive-") || strings.Contains(got.Error(), "sensitive-") {
				t.Fatal("unknown failure leaked raw details")
			}
		})
	}
}

func TestFriendlyLLMErrorBackendCarriesSanitizedHTTPFailure(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 502, 503, 504} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/stream=%t", status, streaming), func(t *testing.T) {
				raw := &llm.ProviderError{
					Provider: "sensitive-provider", StatusCode: status,
					Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
					URL:    "https://example.invalid/?token=sensitive-credential", Body: "sensitive-body",
					RequestPreview: "sensitive-messages", ResponseHeaders: map[string]string{"Authorization": "sensitive-credential"},
				}
				eng := newDegradeEngine(t, &degradeProvider{name: "test", fn: func(int) (string, error) {
					return "", fmt.Errorf("sensitive-wrap: %w", raw)
				}})
				var got error
				if streaming {
					ch, err := eng.ProcessStream(context.Background(), kbQueryMsg("typed-http-stream"))
					got = err
					if err == nil {
						_, got = drainStream(t, ch)
					}
				} else {
					_, got = eng.Process(context.Background(), kbQueryMsg("typed-http-complete"))
				}
				var sanitized *llm.ProviderError
				if !errors.As(got, &sanitized) || !reflect.DeepEqual(sanitized, &llm.ProviderError{StatusCode: status}) || errors.Is(got, raw) {
					t.Fatalf("backend lost or exposed the provider response: %T", got)
				}
				for current := got; current != nil; current = errors.Unwrap(current) {
					for _, rendered := range []string{current.Error(), fmt.Sprintf("%v", current), fmt.Sprintf("%+v", current), fmt.Sprintf("%#v", current)} {
						if strings.Contains(rendered, "sensitive-") || strings.Contains(rendered, "example.invalid") {
							t.Fatal("backend error chain leaked upstream details")
						}
					}
				}
			})
		}
	}
}
