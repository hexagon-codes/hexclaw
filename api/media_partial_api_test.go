package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
	"github.com/hexagon-codes/toolkit/blobstore"
)

// 模型边界按 OpenAI SSE 协议提供工具调用，其余 HTTP、引擎和持久化均使用真实实现。
type partialMediaProtocolProvider struct {
	streamCalls   atomic.Int32
	completeCalls atomic.Int32
	resumeID      atomic.Value
}

func (*partialMediaProtocolProvider) Name() string { return "test" }
func (*partialMediaProtocolProvider) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "fixture-model"}}
}
func (*partialMediaProtocolProvider) CountTokens([]llm.Message) (int, error) { return 1, nil }
func (p *partialMediaProtocolProvider) Complete(context.Context, llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.completeCalls.Add(1)
	return nil, errors.New("fixture requires the streaming model path")
}

func (p *partialMediaProtocolProvider) Stream(_ context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	p.streamCalls.Add(1)
	tools := 0
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == llm.RoleUser {
			break
		}
		if req.Messages[i].Role == llm.RoleTool {
			tools++
		}
	}
	var arguments, callID, reply string
	if resumeID, _ := p.resumeID.Load().(string); resumeID != "" {
		if tools == 0 {
			encoded, err := json.Marshal(map[string]string{"action": "resume", "operation_id": resumeID})
			if err != nil {
				return nil, err
			}
			arguments, callID = string(encoded), "resume-call"
		} else {
			reply = "Both original images are now saved."
		}
	} else if tools < 2 {
		arguments = []string{`{"kind":"image","prompt":"two fixture outputs","n":2}`, `{"n":2,"prompt":"two fixture outputs","kind":"image"}`}[tools]
		callID = []string{"generation-first", "generation-reordered"}[tools]
	} else {
		reply = "The first image is saved. The second image still needs storage recovery."
	}
	delta := map[string]any{"content": reply}
	finish := "stop"
	if arguments != "" {
		delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": callID, "type": "function", "function": map[string]string{"name": "media_generate", "arguments": arguments}}}}
		finish = "tool_calls"
	}
	frame, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	if err != nil {
		return nil, err
	}
	return llm.NewStream(strings.NewReader("data: "+string(frame)+"\n\ndata: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
}

// 第一个输出真实落盘，故障标记期间仅拒绝后续保存，恢复后继续复用原 BlobStore。
type partialMediaBlobStore struct {
	mediaflow.BlobStore
	saves   atomic.Int32
	blocked atomic.Bool
}

func (s *partialMediaBlobStore) SaveStream(ctx context.Context, reader io.Reader, extension string) (string, error) {
	if s.saves.Add(1) > 1 && s.blocked.Load() {
		return "", errors.New("fixture asset storage interrupted")
	}
	return s.BlobStore.SaveStream(ctx, reader, extension)
}

func TestMediaHTTPPartialSaveAndResumeKeepOriginalAssets(t *testing.T) {
	var pngs [][]byte
	for i := 0; i < 2; i++ {
		img := image.NewNRGBA(image.Rect(0, 0, 2+i, 2))
		img.Set(0, 0, color.NRGBA{R: uint8(100 + i*100), A: 255})
		var data bytes.Buffer
		if err := png.Encode(&data, img); err != nil {
			t.Fatal(err)
		}
		pngs = append(pngs, data.Bytes())
	}
	var generations atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		generations.Add(1)
		var request struct {
			N int `json:"n"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.N != 2 || r.URL.Path != "/images/generations" {
			t.Errorf("unexpected image request: path=%s n=%d err=%v", r.URL.Path, request.N, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(pngs[0])}, {"b64_json": base64.StdEncoding.EncodeToString(pngs[1])}}})
	}))
	defer upstream.Close()
	root := t.TempDir()
	store, err := sqlitefixture.New(filepath.Join(root, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	blobs, err := blobstore.NewStore(filepath.Join(root, "generated"))
	if err != nil {
		t.Fatal(err)
	}
	defer blobs.Close()
	interrupted := &partialMediaBlobStore{BlobStore: blobs}
	interrupted.blocked.Store(true)
	cfg := config.DefaultConfig()
	cfg.Server.APIToken = "partial-media-fixture"
	cfg.Compaction.Enabled = false
	cfg.FileMemory.Enabled = false
	cfg.FileMemory.AutoMemory = "off"
	cfg.LLM.Cache.Enabled = false
	cfg.LLM.Default = "test"
	cfg.LLM.Tools.Enabled = "on"
	cfg.Skill.Builtin.MediaGen = true
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{
		"test":          {Model: "fixture-model"},
		"image-fixture": {APIKey: "fixture-key", Compatible: "openai", BaseURL: upstream.URL, ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: []config.LLMProviderModelSpec{{ID: "fixture-image", Capabilities: []string{config.LLMModelCapabilityImageGeneration}}}},
	}
	mediaRouter := mediaflow.NewRouter()
	mediaRouter.Reload(cfg, nil)
	flow, err := mediaflow.NewService(t.Context(), mediaRouter, store.MediaRepository(), interrupted, filepath.Join(root, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	registry := skill.NewRegistry()
	if err := registry.Register(builtin.NewMediaFlowGenerateSkill(flow)); err != nil {
		t.Fatal(err)
	}
	model := &partialMediaProtocolProvider{}
	textRouter := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"test": model})
	eng := engine.NewReActEngine(cfg, textRouter, store, registry)
	eng.SetToolCollector(engine.NewToolCollector(registry, nil, 10))
	eng.SetToolExecutor(engine.NewToolExecutor(registry, nil))
	eng.SetMediaFlow(flow)
	if err := eng.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = eng.Stop(context.Background()) }()
	server := NewServer(cfg, eng, nil, store)
	server.SetGenStore(blobs)
	server.SetMediaFlow(flow)
	app := httptest.NewServer(server.routes())
	defer app.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	call := func(method, path string, body any) []byte {
		t.Helper()
		var encoded []byte
		if body != nil {
			var err error
			encoded, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		request, err := http.NewRequestWithContext(ctx, method, app.URL+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+cfg.Server.APIToken)
		request.Header.Set("Content-Type", "application/json")
		response, err := app.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("HTTP request failed: status=%d err=%v body=%s", response.StatusCode, err, data)
		}
		return data
	}
	assertHistory := func(reply ChatResponse, assets []mediaflow.Asset) {
		t.Helper()
		var history struct {
			Messages []struct {
				ID             string                         `json:"id"`
				Content        string                         `json:"content"`
				MessageContent *messagecontent.MessageContent `json:"message_content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(call(http.MethodGet, "/api/v1/sessions/"+reply.SessionID+"/messages", nil), &history); err != nil {
			t.Fatal(err)
		}
		for _, record := range history.Messages {
			if record.ID != reply.AssistantMessageID {
				continue
			}
			if record.Content != reply.Reply || record.MessageContent == nil || len(record.MessageContent.Attachments) != len(assets) {
				t.Fatalf("public history lost canonical media: %+v", record)
			}
			for i, asset := range assets {
				if record.MessageContent.Attachments[i].AssetID != asset.ID {
					t.Fatal("public history changed the original asset identity")
				}
			}
			return
		}
		t.Fatal("public history is missing the assistant message")
	}
	firstRequest := ChatRequest{Message: "Generate two fixture images", RequestID: "partial-api-source", Provider: "test", Model: "fixture-model", Metadata: map[string]string{"tools": "on"}}
	var first ChatResponse
	if err := json.Unmarshal(call(http.MethodPost, "/api/v1/chat", firstRequest), &first); err != nil {
		t.Fatal(err)
	}
	if generations.Load() != 1 || first.AssistantMessageID == "" || first.MessageContent == nil || len(first.MessageContent.Attachments) != 1 || !strings.Contains(first.Reply, "still needs storage recovery") {
		t.Fatalf("partial reply was lost or regenerated: generations=%d reply=%+v", generations.Load(), first)
	}
	if len(first.ToolCalls) != 2 {
		t.Fatalf("both generation attempts must remain visible: %+v", first.ToolCalls)
	}
	for _, tool := range first.ToolCalls {
		if tool.Status != "error" || tool.Detail == nil || tool.Detail.Outcome != "failed" {
			t.Fatalf("HTTP 200 hid the tool failure: %+v", tool)
		}
	}
	var firstAssets []mediaflow.Asset
	if err := json.Unmarshal([]byte(first.Metadata["artifacts"]), &firstAssets); err != nil || len(firstAssets) != 1 || firstAssets[0].OperationID == "" || first.MessageContent.Attachments[0].AssetID != firstAssets[0].ID {
		t.Fatalf("partial reply lost durable operation metadata: assets=%+v err=%v", firstAssets, err)
	}
	assertHistory(first, firstAssets)
	originalRecord, err := store.GetMessage(ctx, first.AssistantMessageID)
	if err != nil {
		t.Fatal(err)
	}
	originalBytes, err := json.Marshal(originalRecord)
	if err != nil {
		t.Fatal(err)
	}
	interrupted.blocked.Store(false)
	model.resumeID.Store(firstAssets[0].OperationID)
	resumeRequest := ChatRequest{Message: "Resume the saved media operation", SessionID: first.SessionID, RequestID: "partial-api-resume", Provider: "test", Model: "fixture-model", Metadata: map[string]string{"tools": "on"}}
	var resumed ChatResponse
	if err := json.Unmarshal(call(http.MethodPost, "/api/v1/chat", resumeRequest), &resumed); err != nil {
		t.Fatal(err)
	}
	var resumedAssets []mediaflow.Asset
	if err := json.Unmarshal([]byte(resumed.Metadata["artifacts"]), &resumedAssets); err != nil {
		t.Fatal(err)
	}
	if generations.Load() != 1 || resumed.MessageContent == nil || len(resumed.MessageContent.Attachments) != 2 || len(resumedAssets) != 2 || len(resumed.ToolCalls) != 1 || resumed.ToolCalls[0].Status != "success" {
		t.Fatalf("resume did not return both original outputs: generations=%d reply=%+v assets=%+v", generations.Load(), resumed, resumedAssets)
	}
	if resumedAssets[0].ID != firstAssets[0].ID || resumed.MessageContent.Attachments[0].AssetID != first.MessageContent.Attachments[0].AssetID {
		t.Fatal("resume replaced the previously saved asset")
	}
	for i, asset := range resumedAssets {
		if asset.OperationID != firstAssets[0].OperationID || asset.Index != i || resumed.MessageContent.Attachments[i].AssetID != asset.ID {
			t.Fatalf("resume changed the original output association: %+v", asset)
		}
		data := call(http.MethodGet, "/api/v1/files/generated/"+asset.Path, nil)
		if _, err := png.Decode(bytes.NewReader(data)); err != nil || !bytes.Equal(data, pngs[i]) {
			t.Fatalf("saved image %d differs from the original upstream output: %v", i, err)
		}
	}
	assertHistory(resumed, resumedAssets)
	assertHistory(first, firstAssets)
	readyRequest := ChatRequest{Message: "Resume the already saved media operation", SessionID: first.SessionID, RequestID: "partial-api-ready-resume", Provider: "test", Model: "fixture-model", Metadata: map[string]string{"tools": "on"}}
	var ready ChatResponse
	if err := json.Unmarshal(call(http.MethodPost, "/api/v1/chat", readyRequest), &ready); err != nil {
		t.Fatal(err)
	}
	if generations.Load() != 1 || ready.MessageContent == nil || len(ready.MessageContent.Attachments) != 0 || ready.Metadata["artifacts"] != "" || len(ready.ToolCalls) != 1 || ready.ToolCalls[0].Status != "success" {
		t.Fatalf("ready resume attached or generated the original assets again: generations=%d reply=%+v", generations.Load(), ready)
	}
	afterRecord, err := store.GetMessage(ctx, first.AssistantMessageID)
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, err := json.Marshal(afterRecord)
	if err != nil || !bytes.Equal(afterBytes, originalBytes) {
		t.Fatalf("resume rewrote the original assistant message: %v", err)
	}
	if model.completeCalls.Load() != 0 || model.streamCalls.Load() != 7 {
		t.Fatalf("unexpected model path: streams=%d complete=%d", model.streamCalls.Load(), model.completeCalls.Load())
	}
	t.Logf("HTTP partial/resume verified: operation=%s generation_calls=%d original_asset=%s recovered_assets=%d", firstAssets[0].OperationID, generations.Load(), firstAssets[0].ID, len(resumedAssets))
}
