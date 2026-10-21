package builtin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/skill"
)

func TestBrowserReadRetriesThreeTimes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		action      string
		failure     int
		lastStatus  int
		wantSuccess bool
	}{
		{name: "fetch recovers on fourth request", action: "fetch", failure: http.StatusServiceUnavailable, lastStatus: http.StatusOK, wantSuccess: true},
		{name: "extract recovers after request timeouts", action: "extract", lastStatus: http.StatusOK, wantSuccess: true},
		{name: "extract stops after fourth failure", action: "extract", failure: http.StatusServiceUnavailable, lastStatus: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count := calls.Add(1)
				if count < 4 && tc.failure == 0 {
					<-r.Context().Done()
					return
				}
				status := tc.failure
				if count >= 4 {
					status = tc.lastStatus
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte("<title>browser retry result</title><p>retained response</p>"))
			}))
			defer server.Close()
			client := server.Client()
			if tc.failure == 0 {
				client.Timeout = 100 * time.Millisecond
			}
			result, err := (&BrowserSkill{client: client}).Execute(t.Context(), map[string]any{
				"action": tc.action, "url": server.URL, "selector": "title",
			})
			if calls.Load() != 4 {
				t.Fatalf("expected exactly four HTTP requests, got %d: %v", calls.Load(), err)
			}
			if (err == nil) != tc.wantSuccess || result == nil || !strings.Contains(result.Content, "browser retry result") {
				t.Fatalf("final response or outcome was lost: result=%+v err=%v", result, err)
			}
			if !tc.wantSuccess {
				var failure *skill.ExecutionError
				if !errors.As(err, &failure) || failure.Detail.HTTPStatus != http.StatusServiceUnavailable || failure.Detail.Outcome != "failure" {
					t.Fatalf("final HTTP failure was changed: %v", err)
				}
			}
		})
	}
}

func TestBrowserReadRetryKeepsSingleAttemptBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
		status int
		cancel bool
	}{
		{name: "unauthorized fetch", action: "fetch", status: http.StatusUnauthorized},
		{name: "missing extract", action: "extract", status: http.StatusNotFound},
		{name: "form submission", action: "post", status: http.StatusServiceUnavailable},
		{name: "cancelled read", action: "fetch", status: http.StatusServiceUnavailable, cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte("<p>request result</p>"))
				if tc.cancel {
					cancel()
				}
			}))
			defer server.Close()
			_, err := (&BrowserSkill{client: server.Client()}).Execute(ctx, map[string]any{
				"action": tc.action, "url": server.URL, "data": map[string]any{"value": "1"},
			})
			if err == nil || calls.Load() != 1 {
				t.Fatalf("non-retryable request repeated: calls=%d err=%v", calls.Load(), err)
			}
			if tc.cancel && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation was lost: %v", err)
			}
		})
	}
}
