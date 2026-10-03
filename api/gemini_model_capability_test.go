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

// 目录方法声明贯通标准 DTO，名称相似不能冒充能力声明。
func TestGeminiCatalogCapabilities(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-embedding-001","displayName":"Gemini Embedding 001","supportedGenerationMethods":["embedContent"]},{"name":"models/gemini-embedding-2-preview","supportedGenerationMethods":["embedContent"]},{"name":"models/gemini-embedding-2","supportedGenerationMethods":["embedContent"]},{"name":"models/gemini-2.5-flash","supportedGenerationMethods":["generateContent","countTokens"]},{"id":"acme/embed-chat-pro"}]}`))
	}))
	defer upstream.Close()
	srv := NewServer(config.DefaultConfig(), &mockEngine{}, nil, nil)
	w := httptest.NewRecorder()
	srv.handleFetchProviderModels(w, httptest.NewRequest(http.MethodPost, "/api/v1/config/llm/models", strings.NewReader(`{"base_url":"`+upstream.URL+`/v1beta","api_key":"fixture"}`)))
	var body struct {
		Models []struct {
			Capabilities *[]string `json:"capabilities"`
		} `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Models) != 5 {
		t.Fatalf("catalog: %s, %v", w.Body.String(), err)
	}
	for i, want := range [][]string{{"embedding"}, {"embedding"}, {"embedding"}, {"text"}} {
		if body.Models[i].Capabilities == nil || !reflect.DeepEqual(*body.Models[i].Capabilities, want) {
			t.Fatalf("model %d capabilities=%v", i, body.Models[i].Capabilities)
		}
	}
	if body.Models[4].Capabilities != nil {
		t.Fatal("model name was treated as a capability")
	}
}

// 使用真实 HTTP 客户端和临时 SQLite，断言接口、正文与连接回执。
func TestGeminiConnectionProbeRoutesByCapability(t *testing.T) {
	for _, kind := range []string{"text", "embedding"} {
		t.Run(kind, func(t *testing.T) {
			calls := []string{}
			model := "models/gemini-2.5-flash"
			if kind == "embedding" {
				model = "models/gemini-embedding-2"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.URL.Path)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["model"] != model {
					t.Errorf("model=%v", body["model"])
				}
				w.Header().Set("Content-Type", "application/json")
				if kind == "embedding" {
					if body["messages"] != nil || body["input"] == nil {
						t.Errorf("embedding body=%v", body)
					}
					_, _ = w.Write([]byte(`{"data":[{"object":"embedding","index":0,"embedding":[0.6,0.8]}],"model":"models/gemini-embedding-2"}`))
				} else {
					if body["input"] != nil || body["messages"] == nil {
						t.Errorf("chat body=%v", body)
					}
					_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`))
				}
			}))
			defer upstream.Close()
			cfg := config.DefaultConfig()
			provider := config.LLMProviderConfig{ProviderInstanceID: bug20260728ProviderInstanceID, APIKey: "fixture", BaseURL: upstream.URL + "/v1beta/openai", Compatible: "openai", Models: []string{model}, ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: []config.LLMProviderModelSpec{{ID: model, Capabilities: []string{kind}}}}
			if kind == "text" {
				provider.Model = model
			}
			cfg.LLM.Providers = map[string]config.LLMProviderConfig{"gemini": provider}
			srv := NewServer(cfg, &mockEngine{activeLLM: cfg.LLM}, nil, bug20260728OpenStore(t))
			w := httptest.NewRecorder()
			srv.handleTestLLMConfig(w, httptest.NewRequest(http.MethodPost, "/api/v1/config/llm/test", strings.NewReader(`{"provider":{"provider_instance_id":"`+bug20260728ProviderInstanceID+`","model":"`+model+`","type":"gemini"}}`)))
			var result LLMConnectionTestResponse
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.OK || !result.Persisted {
				t.Fatalf("probe: %s, %v", w.Body.String(), err)
			}
			want := "/v1beta/openai/chat/completions"
			if kind == "embedding" {
				want = "/v1beta/openai/embeddings"
			}
			if !reflect.DeepEqual(calls, []string{want}) {
				t.Fatalf("calls=%v want one %s", calls, want)
			}
		})
	}
}

// 新增纯向量服务时，不改已保存的聊天 Provider 与默认模型。
func TestGeminiEmbeddingSavePreservesChatDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := bug20260728ProviderConfig()
	srv := NewServer(cfg, &mockEngine{activeLLM: cfg.LLM}, nil, bug20260728OpenStore(t))
	body := `{"default":"custom","providers":{"custom":{"provider_instance_id":"` + bug20260728ProviderInstanceID + `","api_key":"****-red","base_url":"https://provider.example.test/v1","model":"gpt-5.6-sol","models":["gpt-5.6-sol"],"model_specs_mode":"explicit","model_specs":[{"id":"gpt-5.6-sol","capabilities":["text"]}],"compatible":"openai","locality":"cloud"},"gemini":{"api_key":"fixture","base_url":"https://generativelanguage.googleapis.com/v1beta/openai","model":"","models":["models/gemini-embedding-2"],"model_specs_mode":"explicit","model_specs":[{"id":"models/gemini-embedding-2","capabilities":["embedding"]}],"compatible":"openai","enabled":true}}}`
	w := httptest.NewRecorder()
	srv.handleUpdateLLMConfig(w, httptest.NewRequest(http.MethodPut, "/api/v1/config/llm", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("save: %s", w.Body.String())
	}
	got := srv.persistedLLMConfig()
	if got.Default != "custom" || got.Providers["custom"].Model != "gpt-5.6-sol" {
		t.Fatalf("chat default changed: %s/%s", got.Default, got.Providers["custom"].Model)
	}
	gemini := got.Providers["gemini"]
	if gemini.Model != "" || !config.ModelHasCapability(gemini, "models/gemini-embedding-2", "embedding") || config.ModelHasCapability(gemini, "models/gemini-embedding-2", "text") {
		t.Fatalf("embedding configuration=%+v", gemini.ModelSpecs)
	}
}
