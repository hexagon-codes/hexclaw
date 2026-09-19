package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
)

func TestBackendBusinessAuthenticationPreservesPrincipal(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.APIToken = "server-business-fixture"
	srv := NewServer(cfg, nil, nil, nil)
	srv.SetDesktopAPIToken("desktop-business-fixture")
	srv.SetSidecarCapabilityToken("internal-capability-fixture")
	next := srv.apiAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(httpPrincipalFromRequest(r).userID))
	}))
	for _, tc := range []struct {
		name, remote, token, path, principal string
		status                               int
	}{
		{"anonymous loopback", "127.0.0.1:9000", "", "/api/v1/config", "", 401},
		{"desktop identity", "127.0.0.1:9000", "desktop-business-fixture", "/api/v1/config", "desktop-user", 200},
		{"server identity", "192.0.2.1:9000", "server-business-fixture", "/api/v1/config", "api-user", 200},
		{"business does not grant internal access", "127.0.0.1:9000", "server-business-fixture", "/api/internal/desktop/credentials/hydrate", "", 401},
		{"internal capability", "127.0.0.1:9000", "internal-capability-fixture", "/api/internal/desktop/credentials/hydrate", "desktop-user", 200},
		{"remote cannot use local capability", "192.0.2.1:9000", "internal-capability-fixture", "/api/v1/config", "", 401},
		{"public health", "192.0.2.1:9000", "", "/health", "api-user", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.RemoteAddr = tc.remote
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			next.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d, want=%d", w.Code, tc.status)
			}
			if tc.principal != "" && w.Body.String() != tc.principal {
				t.Fatalf("principal=%s", w.Body.String())
			}
		})
	}
	srv.SetSidecarCapabilityToken("")
	r := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	r.RemoteAddr = "127.0.0.1:9000"
	w := httptest.NewRecorder()
	next.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("missing capability restored anonymous access")
	}
}
