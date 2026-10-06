package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
)

const serviceLifecycleTestToken = "service-lifecycle-test-token"

type serviceLifecycleUnhealthyEngine struct{ mockEngine }

func (*serviceLifecycleUnhealthyEngine) Health(context.Context) error {
	return errors.New("engine unavailable")
}

// 只替换进程退出边界，HTTP 路由、鉴权和重启去重使用真实实现。
func newServiceLifecycleHTTPFixture(t *testing.T, eng engine.Engine, managed bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Server.APIToken = serviceLifecycleTestToken
	srv := NewServer(cfg, eng, nil, nil)
	exits := &atomic.Int32{}
	if managed {
		srv.SetManagedRestart(func() { exits.Add(1) })
	}
	httpServer := httptest.NewServer(srv.routes())
	t.Cleanup(httpServer.Close)
	return httpServer, exits
}

func callServiceLifecycleHTTP(t *testing.T, server *httptest.Server, method, path, token string, body map[string]string) (int, map[string]any) {
	t.Helper()
	var input io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		input = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, server.URL+path, input)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// 读完整响应，确保同步退出回调已完成；不使用 sleep 或实际终止进程。
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	return resp.StatusCode, decoded
}

func serviceLifecycleHTTPProcessID(t *testing.T, server *httptest.Server) string {
	t.Helper()
	status, body := callServiceLifecycleHTTP(t, server, http.MethodGet, "/health", "", nil)
	id, ok := body["process_instance_id"].(string)
	if status != http.StatusOK || body["status"] != "healthy" || !ok || id == "" {
		t.Fatalf("health response: status=%d body=%v", status, body)
	}
	return id
}

func TestServiceLifecycle_HealthReportsProcessAndManagedCapability(t *testing.T) {
	for _, tc := range []struct {
		name    string
		eng     engine.Engine
		managed bool
		status  int
		state   string
	}{
		{"unmanaged healthy", &mockEngine{}, false, http.StatusOK, "healthy"},
		{"managed healthy", &mockEngine{}, true, http.StatusOK, "healthy"},
		{"managed unhealthy", &serviceLifecycleUnhealthyEngine{}, true, http.StatusServiceUnavailable, "unhealthy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, exits := newServiceLifecycleHTTPFixture(t, tc.eng, tc.managed)
			status, body := callServiceLifecycleHTTP(t, server, http.MethodGet, "/health", "", nil)
			id, ok := body["process_instance_id"].(string)
			if status != tc.status || body["status"] != tc.state || !ok || id == "" || body["restart_supported"] != tc.managed {
				t.Fatalf("health response: status=%d body=%v", status, body)
			}
			if tc.status == http.StatusServiceUnavailable && body["error"] != "engine unavailable" {
				t.Fatalf("engine health error missing: %v", body)
			}
			_, second := callServiceLifecycleHTTP(t, server, http.MethodGet, "/health", "", nil)
			if second["process_instance_id"] != id || exits.Load() != 0 {
				t.Fatalf("health changed generation or triggered exit: body=%v exits=%d", second, exits.Load())
			}
		})
	}
}

func TestServiceLifecycle_RestartRequiresAuthentication(t *testing.T) {
	server, exits := newServiceLifecycleHTTPFixture(t, nil, true)
	id := serviceLifecycleHTTPProcessID(t, server)
	req := map[string]string{"expected_process_instance_id": id, "request_id": "auth-request"}
	for _, token := range []string{"", "invalid-token"} {
		status, _ := callServiceLifecycleHTTP(t, server, http.MethodPost, "/api/v1/service/restart", token, req)
		if status != http.StatusUnauthorized || exits.Load() != 0 {
			t.Fatalf("unauthorized restart: status=%d exits=%d", status, exits.Load())
		}
	}
	status, _ := callServiceLifecycleHTTP(t, server, http.MethodPost, "/api/v1/service/restart", serviceLifecycleTestToken, req)
	if status != http.StatusAccepted || exits.Load() != 1 {
		t.Fatalf("authenticated restart: status=%d exits=%d", status, exits.Load())
	}
}

func TestServiceLifecycle_UnmanagedRestartIsRejected(t *testing.T) {
	server, exits := newServiceLifecycleHTTPFixture(t, nil, false)
	id := serviceLifecycleHTTPProcessID(t, server)
	status, body := callServiceLifecycleHTTP(t, server, http.MethodPost, "/api/v1/service/restart", serviceLifecycleTestToken,
		map[string]string{"expected_process_instance_id": id, "request_id": "unmanaged-request"})
	if status != http.StatusConflict || body["process_instance_id"] != id || body["error"] == nil || exits.Load() != 0 {
		t.Fatalf("unmanaged restart: status=%d body=%v exits=%d", status, body, exits.Load())
	}
}

func TestServiceLifecycle_RestartFencesProcessAndDeduplicates(t *testing.T) {
	server, exits := newServiceLifecycleHTTPFixture(t, nil, true)
	id := serviceLifecycleHTTPProcessID(t, server)
	status, body := callServiceLifecycleHTTP(t, server, http.MethodPost, "/api/v1/service/restart", serviceLifecycleTestToken,
		map[string]string{"expected_process_instance_id": "previous-process-id", "request_id": "stale-request"})
	if status != http.StatusConflict || body["process_instance_id"] != id || exits.Load() != 0 {
		t.Fatalf("stale generation restart: status=%d body=%v exits=%d", status, body, exits.Load())
	}

	req := map[string]string{"expected_process_instance_id": id, "request_id": "first-request"}
	for attempt := 0; attempt < 2; attempt++ {
		status, body = callServiceLifecycleHTTP(t, server, http.MethodPost, "/api/v1/service/restart", serviceLifecycleTestToken, req)
		if status != http.StatusAccepted || body["status"] != "restarting" || body["process_instance_id"] != id || body["request_id"] != "first-request" || exits.Load() != 1 {
			t.Fatalf("same request attempt %d: status=%d body=%v exits=%d", attempt, status, body, exits.Load())
		}
	}
	status, body = callServiceLifecycleHTTP(t, server, http.MethodPost, "/api/v1/service/restart", serviceLifecycleTestToken,
		map[string]string{"expected_process_instance_id": id, "request_id": "different-request"})
	if status != http.StatusConflict || body["process_instance_id"] != id || exits.Load() != 1 {
		t.Fatalf("second restart request: status=%d body=%v exits=%d", status, body, exits.Load())
	}
}
