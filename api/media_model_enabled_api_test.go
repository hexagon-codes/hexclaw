package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/toolkit/blobstore"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/skill"
)

// 设置实际生效后，冻结候选不能再授权一次新的生成调用。
func TestMediaHTTPDisabledFallbackModelIsNotCalled(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var firstCalls, disabledCalls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	releaseFirst := func() { once.Do(func() { close(release) }) }
	defer releaseFirst()
	png64 := lifecyclePNG(t)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"fixture quota refused"}}`))
	}))
	t.Cleanup(first.Close)
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		disabledCalls.Add(1)
		lifecycleImage(w, png64)
	}))
	t.Cleanup(second.Close)
	root := t.TempDir()
	store, err := sqlitefixture.New(filepath.Join(root, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	blobs, err := blobstore.NewStore(filepath.Join(root, "generated"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blobs.Close() })
	cfg := config.DefaultConfig()
	cfg.Server.APIToken = "media-enabled-fixture"
	cfg.Compaction.Enabled, cfg.FileMemory.Enabled, cfg.LLM.Cache.Enabled = false, false, false
	cfg.LLM.Default = "first"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{
		"first": {
			ProviderInstanceID: "pvd_v1_00000000000000000000000000000001", APIKey: "fixture-key",
			BaseURL: first.URL, Compatible: "openai", Model: "fixture-text", Models: []string{"fixture-text", "alpha-draw"},
			ModelSpecsMode: config.LLMModelSpecsModeExplicit,
			ModelSpecs: []config.LLMProviderModelSpec{
				{ID: "fixture-text", Capabilities: []string{config.LLMModelCapabilityText}},
				{ID: "alpha-draw", Capabilities: []string{config.LLMModelCapabilityImageGeneration}},
			},
		},
		"second": {
			ProviderInstanceID: "pvd_v1_00000000000000000000000000000002", APIKey: "fixture-key",
			BaseURL: second.URL, Compatible: "openai", Models: []string{"beta-draw"},
			ModelSpecsMode: config.LLMModelSpecsModeExplicit,
			ModelSpecs:     []config.LLMProviderModelSpec{{ID: "beta-draw", Capabilities: []string{config.LLMModelCapabilityImageGeneration}}},
		},
	}
	router := mediaflow.NewRouter()
	router.Reload(cfg, nil)
	flow, err := mediaflow.NewService(ctx, router, store.MediaRepository(), blobs, filepath.Join(root, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(flow.Close)
	modelRouter, err := llmrouter.New(cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.NewReActEngine(cfg, modelRouter, store, skill.NewRegistry())
	eng.SetMediaFlow(flow)
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	server := NewServer(cfg, eng, nil, store)
	configPath := filepath.Join(root, "runtime.yaml")
	server.SetRuntimeConfigPath(configPath)
	server.SetMediaFlow(flow)
	server.SetGenStore(blobs)
	server.SetGenServicesReloader(func() { router.Reload(cfg, nil) })
	app := httptest.NewServer(server.routes())
	t.Cleanup(app.Close)
	newRequest := func(method, path string, body any) *http.Request {
		t.Helper()
		var input io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			input = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(ctx, method, app.URL+path, input)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer media-enabled-fixture")
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	call := func(method, path string, body any, result any) int {
		t.Helper()
		response, err := app.Client().Do(newRequest(method, path, body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if result != nil {
			if err := json.NewDecoder(response.Body).Decode(result); err != nil {
				t.Fatal(err)
			}
		}
		return response.StatusCode
	}
	type responseResult struct {
		response *http.Response
		err      error
	}
	completed := make(chan responseResult, 1)
	request := newRequest(http.MethodPost, "/api/v1/images/generate", map[string]string{"prompt": "a flower", "idempotency_key": "disabled-fallback"})
	go func() {
		response, err := app.Client().Do(request)
		completed <- responseResult{response, err}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first generation did not enter the controlled upstream")
	}
	providers := make(map[string]any)
	for key, pc := range cfg.LLM.Providers {
		models, specs := pc.Models, pc.ModelSpecs
		if key == "second" {
			models, specs = []string{}, []config.LLMProviderModelSpec{}
		}
		providers[key] = map[string]any{
			"provider_instance_id": pc.ProviderInstanceID, "api_key": config.MaskAPIKey(pc.APIKey),
			"base_url": pc.BaseURL, "compatible": pc.Compatible, "model": pc.Model,
			"models": models, "model_specs": specs, "enabled": true,
		}
	}
	if status := call(http.MethodPut, "/api/v1/config/llm", map[string]any{"default": "first", "providers": providers}, nil); status != http.StatusOK {
		t.Fatalf("settings update HTTP%d", status)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	saved := loaded.LLM.Providers["second"]
	if loaded.LLM.Default != "first" || len(saved.ModelSpecs) != 0 || saved.ModelSpecsMode != config.LLMModelSpecsModeExplicit || saved.APIKey == "" || saved.Enabled == nil || !*saved.Enabled {
		t.Fatal("independent configuration reload did not retain the model-only disable")
	}
	var status struct {
		Models []string `json:"models"`
	}
	if code := call(http.MethodGet, "/api/v1/images/status", nil, &status); code != http.StatusOK || len(status.Models) != 1 || status.Models[0] != "alpha-draw" {
		t.Fatalf("current media routes did not remove beta-draw: %+v", status)
	}
	releaseFirst()
	var result responseResult
	select {
	case result = <-completed:
	case <-ctx.Done():
		t.Fatal("original operation did not settle")
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer result.response.Body.Close()
	operationID := result.response.Header.Get("X-Media-Operation-ID")
	if operationID == "" {
		t.Fatal("original operation identity missing")
	}
	var operation struct {
		State    string                        `json:"generation_state"`
		Model    string                        `json:"model"`
		Assets   []mediaflow.Asset             `json:"assets"`
		Attempts []mediaflow.GenerationAttempt `json:"attempts"`
	}
	if code := call(http.MethodGet, "/api/v1/media/operations/"+operationID, nil, &operation); code != http.StatusOK {
		t.Fatalf("original operation read HTTP%d", code)
	}
	if result.response.StatusCode < 400 || operation.State != "failed" || operation.Model != "alpha-draw" || len(operation.Assets) != 0 || len(operation.Attempts) != 1 || firstCalls.Load() != 1 || disabledCalls.Load() != 0 {
		t.Fatalf("disabled model was called or original outcome changed: HTTP%d first=%d disabled=%d operation=%+v", result.response.StatusCode, firstCalls.Load(), disabledCalls.Load(), operation)
	}
}
