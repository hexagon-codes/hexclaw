package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	imagegen "github.com/hexagon-codes/ai-core/media/image"
	"github.com/hexagon-codes/ai-core/media/voicechat"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/toolkit/blobstore"
)

func TestMediaHTTPImageReturnsDurableResultAndPreservesDirectContract(t *testing.T) {
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewNRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	foreignImage := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	foreignImage.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var foreignBytes bytes.Buffer
	if err := png.Encode(&foreignBytes, foreignImage); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/images/generations" {
			t.Errorf("unexpected provider path %s", r.URL.Path)
		}
		var request imagegen.Request
		_ = json.NewDecoder(r.Body).Decode(&request)
		data := pngBytes.Bytes()
		if request.Prompt == "a private drawing" {
			data = foreignBytes.Bytes()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(data)}}})
	}))
	defer upstream.Close()
	var otherCalls atomic.Int32
	var audioCalls atomic.Int32
	wav, err := os.ReadFile("../internal/mediaflow/testdata/short-audio.wav")
	if err != nil {
		t.Fatal(err)
	}
	otherUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-other-provider" {
			t.Error("selected provider credentials were replaced")
		}
		if r.URL.Path == "/chat/completions" {
			audioCalls.Add(1)
			var request struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Model != "gpt-4o-audio-preview" {
				t.Error("audio request selected the wrong model")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"audio": map[string]string{"data": base64.StdEncoding.EncodeToString(wav), "transcript": "hello"}}}}})
			return
		}
		otherCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(pngBytes.Bytes())}}})
	}))
	defer otherUpstream.Close()
	cfg := config.DefaultConfig()
	cfg.Server.APIToken = "media-api-fixture"
	cfg.Compaction.Enabled = false
	cfg.FileMemory.Enabled = false
	cfg.LLM.Cache.Enabled = false
	cfg.LLM.Default = "gateway"
	cfg.Skill.Builtin.MediaGen = false
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"gateway": {APIKey: "fixture-provider-secret", Compatible: "openai", BaseURL: upstream.URL, ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: []config.LLMProviderModelSpec{{ID: "drawing-model", Capabilities: []string{config.LLMModelCapabilityImageGeneration}}}}}
	const otherProviderID = "pvd_v1_00000000000000000000000000000002"
	cfg.LLM.Providers["other"] = config.LLMProviderConfig{ProviderInstanceID: otherProviderID, APIKey: "fixture-other-provider", Compatible: "openai", BaseURL: otherUpstream.URL, ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: []config.LLMProviderModelSpec{{ID: "drawing-model", Capabilities: []string{config.LLMModelCapabilityImageGeneration}}}}
	otherConfig := cfg.LLM.Providers["other"]
	otherConfig.ModelSpecs = append(otherConfig.ModelSpecs, config.LLMProviderModelSpec{ID: "gpt-4o-audio-preview", Capabilities: []string{config.LLMModelCapabilityAudio}})
	cfg.LLM.Providers["other"] = otherConfig
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
	router := mediaflow.NewRouter()
	router.Reload(cfg, nil)
	flow, err := mediaflow.NewService(t.Context(), router, store.MediaRepository(), blobs, filepath.Join(root, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	textRouter, err := llmrouter.New(cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.NewReActEngine(cfg, textRouter, store, skill.NewRegistry())
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
	call := func(method, path, body string) (int, http.Header, []byte) {
		t.Helper()
		request, err := http.NewRequest(method, app.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer media-api-fixture")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "same-image")
		response, err := app.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, response.Header, data
	}
	status, _, body := call("GET", "/api/v1/voice/status", "")
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"tts_enabled":false`)) {
		t.Fatalf("disabled voice status is unavailable: %d %s", status, body)
	}
	status, header, body := call("POST", "/api/v1/images/generate", `{"prompt":"a small red square","model":"drawing-model"}`)
	if status != 200 {
		t.Fatalf("generate status=%d body=%s", status, body)
	}
	var result imagegen.Result
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Images) != 1 || result.Images[0].FilePath == "" || result.Images[0].B64JSON != "" {
		t.Fatalf("old result shape lost persisted image: %+v", result)
	}
	id := header.Get("X-Media-Operation-ID")
	if id == "" {
		t.Fatal("missing operation association")
	}
	status, _, body = call("GET", "/api/v1/files/generated/"+result.Images[0].FilePath, "")
	if status != 200 {
		t.Fatalf("asset status=%d", status)
	}
	if _, err := png.Decode(bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	status, _, body = call("GET", "/api/v1/media/operations/"+id, "")
	if status != 200 || !bytes.Contains(body, []byte(`"ready"`)) {
		t.Fatalf("operation not ready: %d %s", status, body)
	}
	if bytes.Contains(body, []byte("fixture-provider-secret")) || bytes.Contains(body, []byte(upstream.URL)) {
		t.Fatal("internal route material exposed")
	}
	status, _, _ = call("POST", "/api/v1/images/generate", `{"prompt":"a small red square","model":"drawing-model"}`)
	if status != 200 || calls.Load() != 1 {
		t.Fatalf("HTTP replay regenerated: status=%d calls=%d", status, calls.Load())
	}
	status, _, _ = call("POST", "/api/v1/images/generate", `{"prompt":"different input","model":"drawing-model"}`)
	if status != 409 || calls.Load() != 1 {
		t.Fatalf("intent conflict sent upstream: status=%d calls=%d", status, calls.Load())
	}
	status, _, body = call("POST", "/api/v1/images/generate", `{"prompt":"select the second instance","provider":"`+otherProviderID+`","model":"drawing-model","idempotency_key":"other-instance-image"}`)
	var otherResult imagegen.Result
	if err := json.Unmarshal(body, &otherResult); err != nil {
		t.Fatal(err)
	}
	if status != 200 || otherResult.Provider != otherProviderID || otherCalls.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("same model drifted across provider instances: status=%d provider=%s first=%d second=%d", status, otherResult.Provider, calls.Load(), otherCalls.Load())
	}
	status, _, body = call("POST", "/api/v1/voicechat/chat", `{"provider":"`+otherProviderID+`","model":"gpt-4o-audio-preview","text":"hello","format":"wav"}`)
	var audioResult struct {
		voicechat.Result
		AudioFilePath string `json:"audio_file_path"`
	}
	if err := json.Unmarshal(body, &audioResult); err != nil {
		t.Fatal(err)
	}
	if status != 200 || audioResult.Transcript != "hello" || audioResult.AudioFilePath == "" || audioResult.ResponseB64 != "" || audioCalls.Load() != 1 {
		t.Fatalf("audio chat contract changed: %d %s", status, body)
	}
	status, audioHeaders, audioBody := call("GET", "/api/v1/files/generated/"+audioResult.AudioFilePath, "")
	if status != 200 || audioHeaders.Get("Content-Type") != "audio/wav" || !bytes.Equal(audioBody, wav) {
		t.Fatal("audio chat file does not match its response")
	}
	var messages, sessions int
	if err := store.DB().QueryRow(`SELECT count(*) FROM messages`).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT count(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || sessions != 0 {
		t.Fatalf("standalone API created chat: messages=%d sessions=%d", messages, sessions)
	}
	foreign, err := flow.Begin(t.Context(), mediaflow.Request{Origin: mediaflow.Origin{Owner: "another-user", Kind: "direct_api", RequestID: "foreign-image", Ordinal: "image:generate"}, Kind: "image", Model: "drawing-model", Image: &imagegen.Request{Prompt: "a private drawing"}})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err = flow.Wait(t.Context(), foreign.Owner, foreign.ID, false)
	if err != nil || len(foreign.Assets) != 1 {
		t.Fatalf("foreign fixture not saved: %v", err)
	}
	status, _, _ = call("GET", "/api/v1/media/operations/"+foreign.ID, "")
	if status != http.StatusNotFound {
		t.Fatalf("another user's operation is readable: %d", status)
	}
	status, _, _ = call("GET", "/api/v1/files/generated/"+foreign.Assets[0].Path, "")
	if status != http.StatusNotFound {
		t.Fatalf("another user's asset is readable: %d", status)
	}
	status, _, body = call("POST", "/api/v1/chat", `{"message":"Draw a square","provider":"gateway","model":"drawing-model","request_id":"chat-media-http","metadata":{"image_generation":"true"}}`)
	if status != http.StatusOK {
		t.Fatalf("explicit media chat rejected: %d %s", status, body)
	}
	var chat ChatResponse
	if err := json.Unmarshal(body, &chat); err != nil {
		t.Fatal(err)
	}
	if chat.AssistantMessageID == "" || chat.Metadata["artifacts"] == "" || chat.MessageContent == nil || len(chat.MessageContent.Attachments) != 1 {
		t.Fatalf("chat media final result incomplete: %s", body)
	}
	beforeReplay := calls.Load()
	status, _, body = call("POST", "/api/v1/chat", `{"message":"Draw a square","provider":"gateway","model":"drawing-model","request_id":"chat-media-http","session_id":"`+chat.SessionID+`","metadata":{"image_generation":"true"}}`)
	if status != http.StatusOK || calls.Load() != beforeReplay {
		t.Fatalf("HTTP chat replay failed or regenerated: %d %s", status, body)
	}
	var replay ChatResponse
	if err := json.Unmarshal(body, &replay); err != nil || replay.MessageContent == nil || len(replay.MessageContent.Attachments) != 1 {
		t.Fatalf("HTTP replay lost canonical assets: %v, %s", err, body)
	}
	status, _, body = call("GET", "/api/v1/sessions/"+chat.SessionID+"/messages", "")
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"asset_id":"`+chat.MessageContent.Attachments[0].AssetID+`"`)) {
		t.Fatalf("history lost canonical media references: %d %s", status, body)
	}
	sse, err := http.NewRequest(http.MethodPost, app.URL+"/api/v1/chat", strings.NewReader(`{"message":"Draw a square","provider":"gateway","model":"drawing-model","request_id":"chat-media-http","session_id":"`+chat.SessionID+`","metadata":{"image_generation":"true"}}`))
	if err != nil {
		t.Fatal(err)
	}
	sse.Header.Set("Authorization", "Bearer media-api-fixture")
	sse.Header.Set("Content-Type", "application/json")
	sse.Header.Set("Accept", "text/event-stream")
	response, err := app.Client().Do(sse)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Contains(stream, []byte(`"attachments":[`)) || calls.Load() != beforeReplay {
		t.Fatalf("SSE replay incomplete: %v status=%d %s", err, response.StatusCode, stream)
	}
}
