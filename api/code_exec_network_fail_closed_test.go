package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
)

func TestHandleUpdateFullConfigPersistsCodeExecHostNetworkWithoutRuntime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := &Server{
		cfg:          config.DefaultConfig(),
		logCollector: NewLogCollector(10),
	}

	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPut,
		"/api/v1/config",
		strings.NewReader(`{"sandbox":{"network_enabled":true}}`),
	)
	w := httptest.NewRecorder()
	s.handleUpdateFullConfig(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if !s.cfg.Skill.Builtin.CodeExecPolicy.CodeExecNetworkAllowed() {
		t.Fatal("host-network request did not update in-memory configuration")
	}
	persisted, err := config.Load(filepath.Join(home, ".hexclaw", "hexclaw.yaml"))
	if err != nil {
		t.Fatalf("load persisted configuration: %v", err)
	}
	if !persisted.Skill.Builtin.CodeExecPolicy.CodeExecNetworkAllowed() {
		t.Fatal("host-network request did not persist the enabled policy")
	}
}
