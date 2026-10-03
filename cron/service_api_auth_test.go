package cron

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestServiceAPIAuthStaysOnOriginalAPIAndOutOfRequest(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen["other"] = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer other.Close()
	self := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()
		if r.URL.Path == "/api/v1/redirect" {
			http.Redirect(w, r, other.URL, http.StatusFound)
			return
		}
		w.WriteHeader(200)
	}))
	defer self.Close()
	engine := NewStarlarkEngine()
	engine.SetServiceAPIAuth(self.URL, "fixture-service-token")
	for _, path := range []string{"/api/v1/redirect", "/ordinary-page"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, self.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer script-owned-value")
		resp, err := engine.serviceClient().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if req.Header.Get("Authorization") != "Bearer script-owned-value" {
			t.Fatal("automatic credential mutated logged request")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if seen["/api/v1/redirect"] != "Bearer fixture-service-token" {
		t.Fatal("own API did not receive service credential")
	}
	if seen["other"] != "Bearer script-owned-value" || seen["/ordinary-page"] != "Bearer script-owned-value" {
		t.Fatal("automatic auth changed unrelated request credentials")
	}
}
