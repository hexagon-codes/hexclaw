package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/memory"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

// 真实模型验收显式启用；凭据仅从原本机配置读入内存，全部业务数据使用临时目录。
func TestMemoryPersonaLiveHTTP(t *testing.T) {
	if os.Getenv("HEXCLAW_MEMORY_PERSONA_LIVE") != "1" {
		t.Skip("Set HEXCLAW_MEMORY_PERSONA_LIVE=1 for the explicitly approved six-call acceptance")
	}
	loaded, err := config.Load(os.Getenv("HEXCLAW_MEMORY_PERSONA_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	providerName := os.Getenv("HEXCLAW_MEMORY_PERSONA_PROVIDER")
	pc, ok := loaded.LLM.Providers[providerName]
	if !ok || pc.APIKey == "" {
		t.Fatal("Approved provider is unavailable")
	}
	target, err := url.Parse(pc.BaseURL)
	if err != nil || target.Host == "" {
		t.Fatal("Configured provider endpoint is invalid")
	}
	var mu sync.Mutex
	activeCase, calls := -1, 0
	caseCalls := make(map[int]int)
	inputs := make(map[int]string)
	proxy := &httputil.ReverseProxy{Director: func(request *http.Request) {
		request.URL.Scheme, request.URL.Host, request.Host = target.Scheme, target.Host, target.Host
	}}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			http.Error(w, "Cannot read acceptance request", http.StatusBadRequest)
			return
		}
		var input struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &input); err != nil || input.Model != "gpt-6-luna" {
			http.Error(w, "Acceptance must use gpt-6-luna", http.StatusBadRequest)
			return
		}
		mu.Lock()
		index := activeCase
		if index < 0 || calls >= 6 || caseCalls[index] != 0 {
			mu.Unlock()
			http.Error(w, "Acceptance call budget exhausted; no automatic resend", http.StatusBadGateway)
			return
		}
		calls++
		caseCalls[index]++
		for _, message := range input.Messages {
			inputs[index] += message.Content + "\n"
		}
		mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(body))
		proxy.ServeHTTP(w, r)
	}))
	defer relay.Close()
	dir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Compaction.Enabled, cfg.LLM.Cache.Enabled, cfg.LLM.Routing.Enabled = false, false, false
	cfg.FileMemory.Enabled, cfg.FileMemory.Profile, cfg.FileMemory.Dreaming = true, false, false
	cfg.FileMemory.AutoMemory = "off"
	cfg.LLM.Tools.Enabled = "off"
	cfg.Server.APIToken = "memory-acceptance-token"
	pc.BaseURL, pc.Model, pc.Locality = relay.URL+strings.TrimRight(target.Path, "/"), "gpt-6-luna", "cloud"
	pc.Models, pc.ModelSpecsMode, pc.ModelSpecs = []string{"gpt-6-luna"}, "legacy", nil
	enabled := true
	pc.Enabled = &enabled
	cfg.LLM.Default, cfg.LLM.Providers = providerName, map[string]config.LLMProviderConfig{providerName: pc}
	selector, err := llmrouter.New(cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlitefixture.New(filepath.Join(dir, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	fm, err := memory.New(memory.Options{Enabled: true, Dir: filepath.Join(dir, "memory"), MaxMemory: 200})
	if err != nil {
		t.Fatal(err)
	}
	registry := skill.NewRegistry()
	persona, err := os.ReadFile(os.Getenv("HEXCLAW_MEMORY_PERSONA_SOURCE"))
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(dir, "persona.md")
	if err := os.WriteFile(inputPath, persona, 0600); err != nil {
		t.Fatal(err)
	}
	mp := marketplace.NewMarketplace(filepath.Join(dir, "skills"))
	if err := os.MkdirAll(mp.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	installed, err := mp.Install(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(marketplace.WrapAsSkill(installed)); err != nil {
		t.Fatal(err)
	}
	eng := engine.NewReActEngine(cfg, selector, store, registry)
	eng.SetFileMemory(fm)
	if err := eng.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop(t.Context())
	server := NewServer(cfg, eng, nil, store)
	server.SetFileMemory(fm)
	httpServer := httptest.NewServer(server.routes())
	defer httpServer.Close()
	client := &http.Client{Timeout: 150 * time.Second}
	request := func(method, path string, payload any, result any) {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(t.Context(), method, httpServer.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+cfg.Server.APIToken)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s returned %d", method, path, resp.StatusCode)
		}
		if result != nil {
			if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
				t.Fatal(err)
			}
		}
	}
	const answer = "几几黏你去洗啊，麻利滴儿"
	forms := []string{"用户问：知道现在几几年吗？\n回答：" + answer, "在girlfriend闲聊中，当我问知道现在几几年吗时，只回复：" + answer, "知道现在几几年吗？" + answer}
	var saved memory.MemoryEntry
	request(http.MethodPost, "/api/v1/memory", SaveMemoryRequest{Content: forms[0], Type: "fact", Source: "manual", Scope: memory.Scope{Skills: []string{"girlfriend"}}}, &saved)
	var receipts []map[string]any
	defer func() {
		if path := os.Getenv("HEXCLAW_MEMORY_PERSONA_REPORT"); path != "" {
			data, err := json.MarshalIndent(map[string]any{"model": "gpt-6-luna", "calls": calls, "cases": receipts}, "", "  ")
			if err != nil {
				t.Error(err)
				return
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Error(err)
			}
		}
	}()
	for index, test := range []struct {
		name, query              string
		form                     int
		mounted, off, wantAnswer bool
	}{
		{"original question", "你的设定是2012年，你现在穿越了，你知道现在几几年吗？", 0, true, false, true},
		{"natural conditional convention", "知道现在几几年吗？", 1, true, false, true},
		{"plain question answer and paraphrase", "你能告诉我现在是几几年吗？", 2, true, false, true},
		{"other assistant", "知道现在几几年吗？", 2, false, false, false},
		{"memory disabled", "知道现在几几年吗？", 2, true, true, false},
		{"explicit real date", "请按真实日期回答，不使用互动暗号：现在是几几年？", 2, true, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request(http.MethodPut, "/api/v1/memory/"+saved.ID, map[string]string{"content": forms[test.form]}, nil)
			mu.Lock()
			activeCase = index
			mu.Unlock()
			metadata := map[string]string{"memory": "on"}
			if test.mounted {
				metadata["skills"] = "girlfriend"
			}
			if test.off {
				metadata["memory"] = "off"
			}
			var reply ChatResponse
			request(http.MethodPost, "/api/v1/chat", ChatRequest{Message: test.query, Provider: providerName, Model: "gpt-6-luna", Metadata: metadata, RequestID: fmt.Sprintf("memory-persona-live-%d", index)}, &reply)
			mu.Lock()
			input := inputs[index]
			count := caseCalls[index]
			mu.Unlock()
			injected := strings.Contains(input, forms[test.form])
			receipts = append(receipts, map[string]any{"name": test.name, "request_id": fmt.Sprintf("memory-persona-live-%d", index), "session_id": reply.SessionID, "query": test.query, "reply": reply.Reply, "model": reply.Metadata["model"], "memory_injected": injected, "provider_calls": count, "memory_hits": reply.MemoryHits, "usage": reply.Usage})
			t.Logf("case=%s calls=%d injected=%t reply=%s", test.name, count, injected, reply.Reply)
			if count != 1 {
				t.Errorf("provider call count=%d", count)
			}
			if injected != (test.mounted && !test.off) {
				t.Errorf("memory injection=%t", injected)
			}
			if test.wantAnswer && strings.TrimSpace(reply.Reply) != answer {
				t.Errorf("fixed answer was not preserved")
			}
			if !test.wantAnswer && (strings.Contains(reply.Reply, answer) || !strings.Contains(reply.Reply, fmt.Sprint(time.Now().Year()))) {
				t.Errorf("real date was replaced by the persona convention")
			}
		})
	}
}
