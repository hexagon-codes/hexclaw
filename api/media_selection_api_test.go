package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/toolkit/blobstore"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
)

type mediaSelectionProvider struct{ hint string }

func (*mediaSelectionProvider) Name() string                           { return "chat" }
func (*mediaSelectionProvider) Models() []llm.ModelInfo                { return []llm.ModelInfo{{ID: "fixture-chat"}} }
func (*mediaSelectionProvider) CountTokens([]llm.Message) (int, error) { return 1, nil }
func (*mediaSelectionProvider) Complete(context.Context, llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return nil, errors.New("streaming fixture required")
}
func (p *mediaSelectionProvider) Stream(_ context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	completed := false
	for _, m := range req.Messages {
		if m.Role == llm.RoleTool {
			completed = true
		}
	}
	delta := map[string]any{"content": "Media state recorded."}
	finish := "stop"
	if !completed {
		args, _ := json.Marshal(map[string]any{"kind": "image", "prompt": "a flower", "model": p.hint})
		delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "draw", "type": "function", "function": map[string]string{"name": "media_generate", "arguments": string(args)}}}}
		finish = "tool_calls"
	}
	frame, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	if err != nil {
		return nil, err
	}
	return llm.NewStream(strings.NewReader("data: "+string(frame)+"\n\ndata: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
}

func TestMediaHTTPUserSelectionAndBoundedFallback(t *testing.T) {
	for _, tc := range []struct {
		name, text, hint          string
		firstStatus, wantA, wantB int
		state, model              string
	}{
		{"pinned_overrides_tool", "请使用 alpha-draw 画一朵花", "beta-draw", 200, 1, 0, "succeeded", "alpha-draw"},
		{"pinned_after_chinese_comma", "名称：《花园》，使用 alpha-draw 模型画一朵花", "beta-draw", 200, 1, 0, "succeeded", "alpha-draw"},
		{"pinned_after_chinese_question", "能画花吗？请使用 alpha-draw", "beta-draw", 200, 1, 0, "succeeded", "alpha-draw"},
		{"pinned_after_ascii_comma", "Title: flower, use alpha-draw", "beta-draw", 200, 1, 0, "succeeded", "alpha-draw"},
		{"pinned_failure", "只用 alpha-draw，失败也不要换模型", "beta-draw", 429, 1, 0, "failed", "alpha-draw"},
		{"preferred_fallback", "优先 alpha-draw，失败可以换其他模型", "beta-draw", 429, 1, 1, "succeeded", "beta-draw"},
		{"auto_fallback", "画一朵花", "alpha-draw", 404, 1, 1, "succeeded", "beta-draw"},
		{"incidental_provider_name", "Draw a flower", "alpha-draw", 429, 1, 1, "succeeded", "beta-draw"},
		{"incidental_model_name", "画一块牌子，上面写 alpha-draw", "beta-draw", 200, 0, 1, "succeeded", "beta-draw"},
		{"repeated_model_name", "画一块写着 alpha-draw 的牌子，使用 alpha-draw 生成图片", "beta-draw", 200, 1, 0, "succeeded", "alpha-draw"},
		{"repeated_provider_name", "画一块写着 a 的牌子，使用 a 生成图片", "beta-draw", 200, 1, 0, "succeeded", "alpha-draw"},
		{"excluded_model", "不要用 alpha-draw，请使用 beta-draw", "alpha-draw", 200, 0, 1, "succeeded", "beta-draw"},
		{"preferred_provider", "优先用 a，失败可以换 b", "beta-draw", 429, 1, 1, "succeeded", "beta-draw"},
		{"excluded_provider", "不要用 a 生成图片", "alpha-draw", 200, 0, 1, "succeeded", "beta-draw"},
		{"unknown_no_fallback", "画一朵花", "alpha-draw", 500, 1, 0, "unknown", "alpha-draw"},
		{"unclassified_pinned", "请只用 alpha-draw 画一朵花", "beta-draw", 200, 1, 0, "succeeded", "alpha-draw"},
		{"unclassified_rejected", "请只用 alpha-draw 画一朵花", "beta-draw", 403, 1, 0, "failed", "alpha-draw"},
		{"unclassified_not_auto", "画一朵花", "alpha-draw", 200, 0, 1, "succeeded", "beta-draw"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var callsA, callsB atomic.Int32
			png64 := lifecyclePNG(t)
			upstream := func(counter *atomic.Int32, status int, model string) *httptest.Server {
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
						_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": model}}})
						return
					}
					counter.Add(1)
					if status != 200 {
						w.WriteHeader(status)
						_, _ = w.Write([]byte(`{"error":{"message":"fixture unavailable"}}`))
						return
					}
					lifecycleImage(w, png64)
				}))
				t.Cleanup(s.Close)
				return s
			}
			a, b := upstream(&callsA, tc.firstStatus, "alpha-draw"), upstream(&callsB, 200, "beta-draw")
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
			cfg.Server.APIToken = "media-selection-fixture"
			cfg.Compaction.Enabled = false
			cfg.FileMemory.Enabled = false
			cfg.LLM.Cache.Enabled = false
			cfg.LLM.Default = "chat"
			cfg.LLM.Tools.Enabled = "on"
			cfg.Skill.Builtin.MediaGen = true
			cfg.LLM.Providers = map[string]config.LLMProviderConfig{"chat": {Model: "fixture-chat"}}
			for key, item := range map[string]struct{ url, model string }{"a": {a.URL, "alpha-draw"}, "b": {b.URL, "beta-draw"}} {
				capabilities := []string{config.LLMModelCapabilityImageGeneration}
				if strings.HasPrefix(tc.name, "unclassified_") && key == "a" {
					capabilities = []string{}
				}
				cfg.LLM.Providers[key] = config.LLMProviderConfig{ProviderInstanceID: "pvd_v1_0000000000000000000000000000000" + key, APIKey: "fixture-key", Compatible: "openai", BaseURL: item.url, ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: []config.LLMProviderModelSpec{{ID: item.model, Capabilities: capabilities}}}
			}
			mediaRouter := mediaflow.NewRouter()
			mediaRouter.Reload(cfg, nil)
			flow, err := mediaflow.NewService(t.Context(), mediaRouter, store.MediaRepository(), blobs, filepath.Join(root, "staging"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(flow.Close)
			registry := skill.NewRegistry()
			if err = registry.Register(builtin.NewMediaFlowGenerateSkill(flow)); err != nil {
				t.Fatal(err)
			}
			model := &mediaSelectionProvider{hint: tc.hint}
			eng := engine.NewReActEngine(cfg, llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"chat": model}), store, registry)
			eng.SetToolCollector(engine.NewToolCollector(registry, nil, 40))
			eng.SetToolExecutor(engine.NewToolExecutor(registry, nil))
			eng.SetMediaFlow(flow)
			if err = eng.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = eng.Stop(context.Background()) })
			server := NewServer(cfg, eng, nil, store)
			server.SetMediaFlow(flow)
			server.SetGenStore(blobs)
			app := httptest.NewServer(server.routes())
			t.Cleanup(app.Close)
			if strings.HasPrefix(tc.name, "unclassified_") {
				statusReq, _ := http.NewRequestWithContext(t.Context(), "GET", app.URL+"/api/v1/images/status", nil)
				statusReq.Header.Set("Authorization", "Bearer media-selection-fixture")
				statusResp, statusErr := app.Client().Do(statusReq)
				if statusErr != nil {
					t.Fatal(statusErr)
				}
				var status struct {
					Models             []string `json:"models"`
					ExplicitCandidates []struct {
						Model string `json:"model"`
					} `json:"explicit_candidates"`
				}
				decodeErr := json.NewDecoder(statusResp.Body).Decode(&status)
				_ = statusResp.Body.Close()
				if decodeErr != nil || statusResp.StatusCode != 200 || len(status.Models) != 1 || status.Models[0] != "beta-draw" || len(status.ExplicitCandidates) != 1 || status.ExplicitCandidates[0].Model != "alpha-draw" {
					t.Fatalf("explicit-only discovery missing or promoted to auto: %+v, error=%v", status, decodeErr)
				}
			}
			body, _ := json.Marshal(map[string]string{"request_id": "selection-request", "message": tc.text})
			request, err := http.NewRequestWithContext(t.Context(), "POST", app.URL+"/api/v1/chat", strings.NewReader(string(body)))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer media-selection-fixture")
			request.Header.Set("Content-Type", "application/json")
			resp, err := app.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 200 {
				t.Fatalf("chat status=%d %s", resp.StatusCode, raw)
			}
			var reply ChatResponse
			if err = json.Unmarshal(raw, &reply); err != nil {
				t.Fatal(err)
			}
			ops, err := flow.RequestOperations(t.Context(), "api-user", mediaflow.Origin{Kind: "conversation", RequestID: "selection-request", SessionID: reply.SessionID})
			if err != nil || len(ops) != 1 {
				t.Fatalf("operation count=%d error=%v reply=%s", len(ops), err, raw)
			}
			op := ops[0]
			if int(callsA.Load()) != tc.wantA || int(callsB.Load()) != tc.wantB || op.GenerationState != tc.state || op.Route.Model != tc.model {
				t.Fatalf("wrong selection: a=%d b=%d state=%s model=%s policy=%+v", callsA.Load(), callsB.Load(), op.GenerationState, op.Route.Model, op.Request.Selection)
			}
			if len(op.Attempts) != tc.wantA+tc.wantB {
				t.Fatalf("attempt receipts missing: %+v", op.Attempts)
			}
			if tc.state == "succeeded" && (op.SaveState != "ready" || len(op.Assets) != 1) {
				t.Fatalf("actual output missing: %+v", op)
			}
			if tc.name == "unclassified_pinned" {
				catalogReq, _ := http.NewRequestWithContext(t.Context(), "POST", app.URL+"/api/v1/config/llm/models", strings.NewReader(`{"provider_instance_id":"pvd_v1_0000000000000000000000000000000a"}`))
				catalogReq.Header.Set("Authorization", "Bearer media-selection-fixture")
				catalogReq.Header.Set("Content-Type", "application/json")
				catalogResp, catalogErr := app.Client().Do(catalogReq)
				if catalogErr != nil {
					t.Fatal(catalogErr)
				}
				var catalog struct {
					Models []providerModelInfo `json:"models"`
				}
				decodeErr := json.NewDecoder(catalogResp.Body).Decode(&catalog)
				_ = catalogResp.Body.Close()
				if decodeErr != nil || catalogResp.StatusCode != 200 || len(catalog.Models) != 1 || catalog.Models[0].ID != "alpha-draw" || catalog.Models[0].Capabilities == nil || len(*catalog.Models[0].Capabilities) != 1 || (*catalog.Models[0].Capabilities)[0] != "image_generation" {
					t.Fatalf("successful configured model lost its verified catalog capability: %+v, error=%v", catalog, decodeErr)
				}
			}
		})
	}
}
