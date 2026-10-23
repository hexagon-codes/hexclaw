package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	mockllm "github.com/hexagon-codes/hexagon/testing/mock"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
	sqlitestore "github.com/hexagon-codes/hexclaw/storage/sqlite"
	"github.com/hexagon-codes/toolkit/blobstore"
)

func mediaEngineFixture(t *testing.T, endpoint string, provider *mockllm.LLMProvider, wrapStore ...func(mediaflow.BlobStore) mediaflow.BlobStore) (*ReActEngine, *sqlitestore.Store) {
	t.Helper()
	root := t.TempDir()
	store, err := sqlitefixture.New(filepath.Join(root, "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.DefaultConfig()
	cfg.Compaction.Enabled = false
	cfg.LLM.Cache.Enabled = false
	cfg.FileMemory.Enabled = false
	cfg.LLM.Tools.Enabled = "on"
	cfg.LLM.Default = "test"
	cfg.Skill.Builtin.MediaGen = true
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"test": {Model: "mock-model"}, "image-fixture": {APIKey: "fixture-key", Compatible: "openai", BaseURL: endpoint, ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: []config.LLMProviderModelSpec{{ID: "fixture-image", Capabilities: []string{config.LLMModelCapabilityImageGeneration}}}}}
	blobs, err := blobstore.NewStore(filepath.Join(root, "generated"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blobs.Close() })
	mediaRouter := mediaflow.NewRouter()
	mediaRouter.Reload(cfg, nil)
	var mediaBlobs mediaflow.BlobStore = blobs
	if len(wrapStore) > 0 {
		mediaBlobs = wrapStore[0](mediaBlobs)
	}
	flow, err := mediaflow.NewService(t.Context(), mediaRouter, store.MediaRepository(), mediaBlobs, filepath.Join(root, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(flow.Close)
	registry := skill.NewRegistry()
	if err := registry.Register(builtin.NewMediaFlowGenerateSkill(flow)); err != nil {
		t.Fatal(err)
	}
	router := llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"test": provider})
	eng := NewReActEngine(cfg, router, store, registry)
	eng.SetToolCollector(NewToolCollector(registry, nil, 40))
	eng.SetToolExecutor(NewToolExecutor(registry, nil))
	eng.SetMediaFlow(flow)
	if err := eng.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	return eng, store
}

func mediaPNG(t *testing.T) string {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(data.Bytes())
}

func TestMediaToolTurnAggregatesAndReplaysExistingReply(t *testing.T) {
	pngB64 := mediaPNG(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": pngB64}}})
	}))
	defer upstream.Close()
	model := mockllm.NewLLMProvider("test").WithResponseFn(func(req llm.CompletionRequest) (*llm.CompletionResponse, error) {
		toolCount := 0
		for _, message := range req.Messages {
			if message.Role == llm.RoleTool {
				toolCount++
			}
		}
		if toolCount < 2 {
			return &llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "reused-call-id", Name: "media_generate", Arguments: []string{`{"kind":"image","prompt":"first drawing"}`, `{"kind":"image","prompt":"second drawing"}`}[toolCount]}}}, nil
		}
		return &llm.CompletionResponse{Content: "Both drawings are ready."}, nil
	})
	eng, store := mediaEngineFixture(t, upstream.URL, model)
	newMessage := func() *adapter.Message {
		return &adapter.Message{ID: "media-turn", UserID: "user", SessionID: "media-session", Platform: adapter.PlatformAPI, Content: "Generate two drawings", Metadata: map[string]string{"request_id": "media-turn", "tools": "on"}}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	reply, err := eng.Process(ctx, newMessage())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(reply.Attachments) != 2 || reply.MessageContent == nil || len(reply.MessageContent.Attachments) != 2 {
		t.Fatalf("turn outputs incomplete: calls=%d attachments=%d reply=%+v", calls.Load(), len(reply.Attachments), reply)
	}
	replayed, err := eng.Process(ctx, newMessage())
	if err != nil {
		t.Fatal(err)
	}
	if replayed.AssistantMessageID != reply.AssistantMessageID || replayed.Content != reply.Content || calls.Load() != 2 {
		t.Fatal("existing completed turn was regenerated or rewritten")
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE role='assistant' AND request_id='media-turn'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("assistant messages=%d", count)
	}
	history, err := eng.sessions.BuildContext(ctx, "media-session")
	if err != nil {
		t.Fatal(err)
	}
	var refs []mediaflow.Asset
	if err := json.Unmarshal([]byte(reply.Metadata["artifacts"]), &refs); err != nil {
		t.Fatal(err)
	}
	hasReference := false
	for _, message := range history {
		if message.Role == llm.RoleAssistant && strings.Contains(message.Content, refs[0].OperationID) && strings.Contains(message.Content, refs[1].OperationID) {
			hasReference = true
		}
	}
	if !hasReference {
		t.Fatal("follow-up context lost durable operation references")
	}
}

func TestMediaDirectStreamDisconnectPersistsCanonicalAsset(t *testing.T) {
	pngB64 := mediaPNG(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": pngB64}}})
	}))
	defer upstream.Close()
	defer unblock()
	eng, store := mediaEngineFixture(t, upstream.URL, mockllm.NewLLMProvider("test"))
	ctx, cancel := context.WithCancel(t.Context())
	chunks, err := eng.ProcessStream(ctx, &adapter.Message{ID: "media-stream", UserID: "user", SessionID: "stream-session", Platform: adapter.PlatformAPI, Content: "Draw a square", Metadata: map[string]string{"request_id": "media-stream", "image_generation": "true", "provider": "image-fixture", "model": "fixture-image"}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("generation did not start")
	}
	cancel()
	unblock()
	done := make(chan struct{})
	go func() {
		for range chunks {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retained media turn did not finish")
	}
	message, err := store.GetMessage(t.Context(), "media-stream:assistant")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(message.Content, "data:image") || !strings.Contains(message.Content, "/api/v1/files/generated/") {
		t.Fatalf("invalid durable media content: %s", message.Content)
	}
	var persisted struct {
		Attachments []adapter.Attachment `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(message.Attachments), &persisted); err != nil {
		t.Fatal(err)
	}
	attachments := persisted.Attachments
	if len(attachments) != 1 || attachments[0].Name == "" {
		t.Fatalf("durable attachment lost: %+v", attachments)
	}
}

type interruptedMediaSave struct {
	mediaflow.BlobStore
	saves   atomic.Int32
	blocked atomic.Bool
}

func (s *interruptedMediaSave) SaveStream(ctx context.Context, reader io.Reader, extension string) (string, error) {
	if s.saves.Add(1) > 1 && s.blocked.Load() {
		return "", errors.New("fixture asset storage interrupted")
	}
	return s.BlobStore.SaveStream(ctx, reader, extension)
}

func TestMediaPartialFailureAndResumeKeepAssetsWithoutRegeneration(t *testing.T) {
	for _, pipeline := range []bool{false, true} {
		name := "direct"
		if pipeline {
			name = "pipeline"
		}
		t.Run(name, func(t *testing.T) {
			first := mediaPNG(t)
			var second bytes.Buffer
			if err := png.Encode(&second, image.NewNRGBA(image.Rect(0, 0, 3, 2))); err != nil {
				t.Fatal(err)
			}
			var generations atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				generations.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": first}, {"b64_json": base64.StdEncoding.EncodeToString(second.Bytes())}}})
			}))
			defer upstream.Close()
			resumeID := ""
			model := mockllm.NewLLMProvider("test").WithResponseFn(func(req llm.CompletionRequest) (*llm.CompletionResponse, error) {
				tools := 0
				for _, message := range req.Messages {
					if message.Role == llm.RoleTool {
						tools++
					}
				}
				if resumeID != "" {
					if tools == 0 {
						args, _ := json.Marshal(map[string]string{"action": "resume", "operation_id": resumeID})
						return &llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "resume-call", Name: "media_generate", Arguments: string(args)}}}, nil
					}
					return &llm.CompletionResponse{Content: "Both original outputs are now saved."}, nil
				}
				if tools < 2 {
					args := []string{`{"kind":"image","prompt":"two fixture outputs","n":2}`, `{"n":2,"prompt":"two fixture outputs","kind":"image"}`}
					return &llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "generation-call", Name: "media_generate", Arguments: args[tools]}}}, nil
				}
				return &llm.CompletionResponse{Content: "The first image is saved. The second image still needs storage recovery."}, nil
			})
			var interrupted *interruptedMediaSave
			eng, _ := mediaEngineFixture(t, upstream.URL, model, func(store mediaflow.BlobStore) mediaflow.BlobStore {
				interrupted = &interruptedMediaSave{BlobStore: store}
				interrupted.blocked.Store(true)
				return interrupted
			})
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			if pipeline {
				ctx = withFlagOn(ctx, skill.FlagSkillPipelineV1)
			}
			message := &adapter.Message{ID: "partial-source", UserID: "user", SessionID: "partial-session", Platform: adapter.PlatformAPI, Content: "Generate two fixture images", Metadata: map[string]string{"request_id": "partial-source", "tools": "on"}}
			reply, err := eng.Process(ctx, message)
			if err != nil {
				t.Fatal(err)
			}
			if generations.Load() != 1 || len(reply.Attachments) != 1 || reply.MessageContent == nil || len(reply.MessageContent.Attachments) != 1 {
				t.Fatalf("partial outputs were lost or regenerated: calls=%d reply=%+v", generations.Load(), reply)
			}
			if len(reply.ToolCalls) != 2 || reply.ToolCalls[0].Status == "success" || reply.ToolCalls[1].Status == "success" {
				t.Fatalf("partial failure was hidden: %+v", reply.ToolCalls)
			}
			var refs []mediaflow.Asset
			if err := json.Unmarshal([]byte(reply.Metadata["artifacts"]), &refs); err != nil || len(refs) != 1 {
				t.Fatalf("partial asset references: %v %+v", err, refs)
			}
			resumeID = refs[0].OperationID
			interrupted.blocked.Store(false)
			recovered, err := eng.Process(ctx, &adapter.Message{ID: "partial-resume", UserID: "user", SessionID: "partial-session", Platform: adapter.PlatformAPI, Content: "Resume the saved media operation", Metadata: map[string]string{"request_id": "partial-resume", "tools": "on"}})
			if err != nil {
				t.Fatal(err)
			}
			if generations.Load() != 1 || len(recovered.Attachments) != 2 || recovered.MessageContent == nil || len(recovered.MessageContent.Attachments) != 2 {
				t.Fatalf("resume did not deliver original assets: calls=%d reply=%+v", generations.Load(), recovered)
			}
			if recovered.Attachments[0].ID != reply.Attachments[0].ID {
				t.Fatal("resume replaced the previously saved asset")
			}
			statusOnly, err := eng.Process(ctx, &adapter.Message{ID: "delivery-resume", UserID: "user", SessionID: "partial-session", Platform: adapter.PlatformAPI, Content: "Resume only pending delivery", Metadata: map[string]string{"request_id": "delivery-resume", "tools": "on"}})
			if err != nil {
				t.Fatal(err)
			}
			if generations.Load() != 1 || len(statusOnly.Attachments) != 0 {
				t.Fatal("delivery recovery created another media reply or generation")
			}
		})
	}
}
