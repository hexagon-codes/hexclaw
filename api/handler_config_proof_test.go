package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/config"
)

func TestConfigProofReadsOnlyCommittedReceiptAndExplicitKey(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.APIToken = "fixture-access-token"
	p := config.LLMProviderConfig{ProviderInstanceID: "pi-fixture", APIKey: "fixture-model-secret", Model: "fixture"}
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"fixture": p}
	cfg.LLM.MutationReceipts = map[string]config.LLMConfigMutationReceipt{
		"original-write": {RequestID: "original-write", RequestDigest: "private-request-digest", ConfigDigest: "public-config-digest", Revision: 7, CommittedAt: 1234},
	}
	before := cloneLLMConfigSnapshot(cfg.LLM)
	srv := NewServer(cfg, &mockEngine{activeLLM: cfg.LLM}, nil, nil)
	srv.SetBackendID("fixture-backend-id")
	router := srv.routes()
	for _, tc := range []struct {
		method, path string
		status       int
		contains     string
	}{
		{http.MethodGet, "/api/v1/config", 200, "fixture-backend-id"},
		{http.MethodGet, "/api/v1/config/mutations/original-write?operation_kind=llm", 200, "public-config-digest"},
		{http.MethodGet, "/api/v1/config/mutations/missing?operation_kind=llm", 404, "Committed mutation not found"},
		{http.MethodGet, "/api/v1/config/mutations/original-write?operation_kind=invalid", 400, "Unsupported operation kind"},
		{http.MethodPost, "/api/v1/config/llm/providers/pi-fixture/reveal-key", 200, "fixture-model-secret"},
		{http.MethodPost, "/api/v1/config/llm/providers/pi-other/reveal-key", 404, "Provider not found"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.Header.Set("Authorization", "Bearer fixture-access-token")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
				t.Fatalf("response: %d %s", w.Code, w.Body.String())
			}
			var decoded map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(tc.path, "/mutations/") && (strings.Contains(w.Body.String(), p.APIKey) || strings.Contains(w.Body.String(), "private-request-digest")) {
				t.Fatal("receipt exposed private content")
			}
		})
	}
	if !reflect.DeepEqual(before, cfg.LLM) {
		t.Fatal("read-only query changed configuration")
	}
}
