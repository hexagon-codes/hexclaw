package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/knowledge"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/memory"
	"github.com/hexagon-codes/hexclaw/skill"
	sqlitestore "github.com/hexagon-codes/hexclaw/storage/sqlite"
)

// HTTP 边界只提供向量；运行时代、缓存、出口规则、文件记忆与引擎使用真实实现。
type memoryEmbeddingHTTP struct {
	server    *httptest.Server
	mu        sync.Mutex
	calls     [][]string
	models    []string
	started   chan struct{}
	cancelled chan struct{}
}

func newMemoryEmbeddingHTTP(t *testing.T, dimension int, blocking bool) *memoryEmbeddingHTTP {
	t.Helper()
	endpoint := &memoryEmbeddingHTTP{started: make(chan struct{}, 1), cancelled: make(chan struct{}, 1)}
	endpoint.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" {
			http.Error(w, "Unexpected embedding request", 404)
			return
		}
		var input struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		endpoint.mu.Lock()
		endpoint.calls = append(endpoint.calls, append([]string(nil), input.Input...))
		endpoint.models = append(endpoint.models, input.Model)
		endpoint.mu.Unlock()
		if blocking {
			endpoint.started <- struct{}{}
			<-r.Context().Done()
			endpoint.cancelled <- struct{}{}
			return
		}
		data := make([]map[string]any, len(input.Input))
		for i, text := range input.Input {
			vector := make([]float32, dimension)
			if strings.Contains(text, "海景") || strings.Contains(text, "海滨") {
				vector[0] = 1
			} else {
				vector[1] = 1
			}
			data[i] = map[string]any{"object": "embedding", "index": i, "embedding": vector}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "model": input.Model, "data": data, "usage": map[string]int{"total_tokens": 1}})
	}))
	t.Cleanup(endpoint.server.Close)
	return endpoint
}

func (e *memoryEmbeddingHTTP) snapshot() ([][]string, []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([][]string(nil), e.calls...), append([]string(nil), e.models...)
}

func memoryEmbeddingConfig(endpoint string, dimension int) *config.Config {
	cfg := config.DefaultConfig()
	cfg.Knowledge.Embedding.Provider = "memory-cloud"
	cfg.Knowledge.Embedding.Model = "memory-embed"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{}
	if endpoint != "" {
		cfg.LLM.Providers["memory-cloud"] = config.LLMProviderConfig{
			BaseURL: endpoint + "/v1", APIKey: "test-placeholder", Compatible: "openai", Locality: config.ProviderLocalityCloud,
			ModelSpecsMode: config.LLMModelSpecsModeExplicit,
			ModelSpecs:     []config.LLMProviderModelSpec{{ID: "memory-embed", Capabilities: []string{config.LLMModelCapabilityEmbedding}, Embedding: &config.LLMEmbeddingModelSpec{Protocol: config.LLMEmbeddingProtocolOpenAI, Dimension: dimension}}},
		}
	}
	return cfg
}

func memoryRuntimeBundle(ctx context.Context, cfg *config.Config, policy *egress.Policy) knowledgeEmbeddingRuntimeProfiles {
	gate := newKnowledgeSemanticRuntimeGate()
	return knowledgeEmbeddingRuntimeProfiles{
		Resolver:       newKnowledgeEmbeddingProfileResolverFromEntries(nil, gate),
		Registry:       newKnowledgeEmbeddingExecutorRegistryFromEntries(nil, gate),
		MemoryEmbedder: prepareSharedMemoryEmbedding(ctx, cfg, policy, nil).embedder,
	}
}

func memoryDirectorySnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[rel] = string(body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestMemoryEmbeddingReloadReachesChatWithoutRewritingMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	fm, err := memory.New(memory.Options{Enabled: true, Dir: filepath.Join(dir, "memory"), MaxMemory: 200})
	if err != nil {
		t.Fatal(err)
	}
	const fact = "用户偏爱的旅行目的地是海景沙滩"
	if err := fm.SaveStructuredEntry(fact, "fact", "manual", "", memory.EntryMeta{}); err != nil {
		t.Fatal(err)
	}
	if err := fm.SaveStructuredEntry("用户常用的代码编辑器是 Vim", "fact", "manual", "", memory.EntryMeta{}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var payloads []string
	var audits []egress.Request
	policy := &egress.Policy{OnAudit: func(req egress.Request, d egress.Decision) {
		mu.Lock()
		defer mu.Unlock()
		if d.AllowCloud {
			audits = append(audits, req)
		}
	}}
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "Unexpected chat request", 404)
			return
		}
		var input struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var text strings.Builder
		for _, m := range input.Messages {
			text.WriteString(m.Content)
		}
		mu.Lock()
		payloads = append(payloads, text.String())
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer chat.Close()
	cfg := memoryEmbeddingConfig("", 0)
	cfg.Compaction.Enabled = false
	cfg.LLM.Cache.Enabled = false
	cfg.LLM.Default = "chat"
	cfg.LLM.Providers["chat"] = config.LLMProviderConfig{Model: "test-chat", BaseURL: chat.URL + "/v1", Compatible: "openai", Locality: config.ProviderLocalityCloud}
	router := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"chat": hexagon.NewOpenAI("test-placeholder", hexagon.OpenAIWithBaseURL(chat.URL+"/v1"))})
	router.SetEgressPolicy(policy)
	store, err := sqlitestore.New(filepath.Join(dir, "chat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	eng := engine.NewReActEngine(cfg, router, store, skill.NewRegistry())
	eng.SetFileMemory(fm)
	holder, err := newKnowledgeEmbeddingRuntimeHolder(memoryRuntimeBundle(ctx, cfg, policy))
	if err != nil {
		t.Fatal(err)
	}
	eng.SetMemoryEmbedder(&runtimeMemoryEmbedder{holder: holder})
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop(context.Background())
	turn := func(id, query string, wantFact bool) {
		t.Helper()
		mu.Lock()
		before := len(payloads)
		mu.Unlock()
		ch, err := eng.ProcessStream(ctx, &adapter.Message{ID: id, SessionID: id, UserID: "memory-reload-user", Platform: adapter.PlatformAPI, Content: query, Metadata: map[string]string{"memory": "on"}})
		if err != nil {
			t.Fatal(err)
		}
		var output strings.Builder
		for chunk := range ch {
			if chunk.Error != nil {
				t.Fatal(chunk.Error)
			}
			output.WriteString(chunk.Content)
		}
		if output.String() != "ok" {
			t.Fatalf("chat result=%q", output.String())
		}
		mu.Lock()
		defer mu.Unlock()
		if len(payloads) != before+1 {
			t.Fatalf("chat calls=%d, expected one new call", len(payloads)-before)
		}
		if strings.Contains(payloads[before], fact) != wantFact {
			t.Fatalf("memory inclusion=%v, want %v: %s", strings.Contains(payloads[before], fact), wantFact, payloads[before])
		}
	}
	replace := func(next *config.Config) {
		t.Helper()
		before := memoryDirectorySnapshot(t, filepath.Join(dir, "memory"))
		bundle := memoryRuntimeBundle(ctx, next, policy)
		if err := holder.Replace(ctx, bundle); err != nil {
			t.Fatal(err)
		}
		if after := memoryDirectorySnapshot(t, filepath.Join(dir, "memory")); !reflect.DeepEqual(before, after) {
			t.Fatal("configuration reload rewrote memory files")
		}
	}
	// 启动时无客户端，关键词检索仍可消费已有文件；新增配置后无需替换引擎或文件实例。
	turn("before-config", "海景沙滩", true)
	oldEndpoint := newMemoryEmbeddingHTTP(t, 3, false)
	replace(memoryEmbeddingConfig(oldEndpoint.server.URL, 3))
	turn("added-config", "海滨行程推荐", true)
	oldCalls, oldModels := oldEndpoint.snapshot()
	if len(oldCalls) != 1 || len(oldCalls[0]) != 3 || oldModels[0] != "memory-embed" {
		t.Fatalf("old physical batches=%v models=%v", oldCalls, oldModels)
	}
	// 相同输入在新端点、不同维度下须重新构建向量，不能命中旧客户端缓存。
	newEndpoint := newMemoryEmbeddingHTTP(t, 5, false)
	nextConfig := memoryEmbeddingConfig(newEndpoint.server.URL, 5)
	nextConfig.Knowledge.Embedding.Model = "memory-embed-v2"
	nextProvider := nextConfig.LLM.Providers["memory-cloud"]
	nextProvider.ModelSpecs[0].ID = "memory-embed-v2"
	nextConfig.LLM.Providers["memory-cloud"] = nextProvider
	replace(nextConfig)
	turn("changed-config", "海滨行程推荐", true)
	newCalls, newModels := newEndpoint.snapshot()
	afterOld, _ := oldEndpoint.snapshot()
	if len(afterOld) != len(oldCalls) || len(newCalls) != 1 || !reflect.DeepEqual(oldCalls[0], newCalls[0]) || newModels[0] != "memory-embed-v2" {
		t.Fatalf("mixed generation batches: old=%v new=%v", afterOld, newCalls)
	}
	replace(memoryEmbeddingConfig("", 0))
	turn("removed-config", "海景沙滩", true)
	afterNew, _ := newEndpoint.snapshot()
	if len(afterNew) != len(newCalls) {
		t.Fatal("deleted provider received a new request")
	}
	mu.Lock()
	defer mu.Unlock()
	memoryAudits := 0
	for _, request := range audits {
		if request.Purpose != egress.PurposeGeneralChat || request.DataClass == egress.ClassDocument {
			t.Fatalf("memory reload substituted another business envelope: %+v", request)
		}
		if request.DataClass == egress.ClassMemory {
			memoryAudits++
			if request.Purpose != egress.PurposeGeneralChat || !request.ChatMemoryEnabled {
				t.Fatalf("memory envelope changed: %+v", request)
			}
		}
	}
	if memoryAudits < 2 {
		t.Fatalf("missing real memory egress audit: %v", audits)
	}
}

func TestMemoryEmbeddingReloadCancelsOldBatchWithoutResending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	policy := &egress.Policy{}
	oldEndpoint := newMemoryEmbeddingHTTP(t, 3, true)
	holder, err := newKnowledgeEmbeddingRuntimeHolder(memoryRuntimeBundle(ctx, memoryEmbeddingConfig(oldEndpoint.server.URL, 3), policy))
	if err != nil {
		t.Fatal(err)
	}
	emb := &runtimeMemoryEmbedder{holder: holder}
	requestCtx := egress.WithRequest(ctx, egress.PurposeGeneralChat, "memory-reload", egress.ClassMemory)
	egress.EnableChatMemory(requestCtx)
	done := make(chan error, 1)
	go func() { _, err := emb.Embed(requestCtx, []string{"海滨行程", "海景沙滩"}); done <- err }()
	select {
	case <-oldEndpoint.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	nextEndpoint := newMemoryEmbeddingHTTP(t, 5, false)
	if err := holder.Replace(ctx, memoryRuntimeBundle(ctx, memoryEmbeddingConfig(nextEndpoint.server.URL, 5), policy)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("revoked old batch returned success")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-oldEndpoint.cancelled:
	case <-ctx.Done():
		t.Fatal("old HTTP request was not cancelled")
	}
	if calls, _ := nextEndpoint.snapshot(); len(calls) != 0 {
		t.Fatalf("cancelled request automatically resent: %v", calls)
	}
	vectors, err := emb.Embed(requestCtx, []string{"海滨行程", "海景沙滩"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 2 || len(vectors[0]) != 5 || len(vectors[1]) != 5 {
		t.Fatalf("new batch mixed vector dimensions: %v", vectors)
	}
	if calls, _ := oldEndpoint.snapshot(); len(calls) != 1 {
		t.Fatalf("old request repeated: %v", calls)
	}
	if err := holder.Replace(ctx, memoryRuntimeBundle(ctx, memoryEmbeddingConfig("", 0), policy)); err != nil {
		t.Fatal(err)
	}
	if _, err := emb.Embed(requestCtx, []string{"removed"}); !errors.Is(err, knowledge.ErrEmbeddingUnavailable) {
		t.Fatalf("missing provider should signal fallback: %v", err)
	}
}
