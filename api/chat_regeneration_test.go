package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm/openai"
	webadapter "github.com/hexagon-codes/hexclaw/adapter/web"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/storage"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

func TestChatRegenerationPreservesOriginalOnFailureAndAtomicallyReplacesOnSuccess(t *testing.T) {
	const (
		sessionID  = "regeneration-fixture-session"
		sourceID   = "original-image-message"
		marker     = "Original image question"
		newAnswer  = "Verified replacement answer."
		imageData  = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jmFcAAAAASUVORK5CYII="
		imageStore = `{"attachments":[{"type":"image","name":"source.png","mime":"image/png","data":"` + imageData + `"}]}`
	)
	var physicalCalls atomic.Int32
	seen := make(chan string, 4)
	held := make(chan struct{})
	release := make(chan struct{})
	cancelHeld := make(chan struct{})
	cancelRelease := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil || r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		seen <- string(raw)
		call := physicalCalls.Add(1)
		if call == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"message":"Synthetic upstream response"}}`)
			return
		}
		if call == 3 {
			close(held)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if call == 4 {
			close(cancelHeld)
			select {
			case <-r.Context().Done():
			case <-cancelRelease:
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"replacement\",\"choices\":[{\"delta\":{\"content\":\""+newAnswer+"\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	ctx := t.Context()
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "chat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.CreateSession(ctx, &storage.Session{ID: sessionID, UserID: "api-user", Platform: "api", Status: 1}); err != nil {
		t.Fatal(err)
	}
	for _, seed := range []storage.MessageRecord{
		{ID: "prefix-user", Role: "user", Content: "Earlier prefix question"},
		{ID: "prefix-assistant", Role: "assistant", Content: "Earlier prefix answer"},
		{ID: sourceID, Role: "user", Content: marker, Attachments: imageStore},
		{ID: "old-assistant", Role: "assistant", Content: "Prior answer to regenerate", Metadata: `{"provider":"test","model":"mock-model"}`},
		{ID: "later-user", Role: "user", Content: "Later question not in replacement input"},
		{ID: "later-assistant", Role: "assistant", Content: "Later answer not in replacement input"},
	} {
		seed.SessionID = sessionID
		if err = store.SaveMessage(ctx, &seed); err != nil {
			t.Fatal(err)
		}
	}
	cfg := apiCanonicalHistoryConfig()
	provider := cfg.LLM.Providers["test"]
	provider.ModelSpecsMode = config.LLMModelSpecsModeExplicit
	provider.ModelSpecs = []config.LLMProviderModelSpec{{ID: "mock-model", Capabilities: []string{"text", "vision"}}}
	cfg.LLM.Providers["test"] = provider
	eng := newAPICanonicalHistoryEngine(t, cfg, store, openai.New("synthetic-fixture", openai.WithBaseURL(upstream.URL)))
	defer eng.Stop(context.Background())
	srv := NewServer(cfg, eng, nil, store)
	defer srv.attachmentStaging.Close()
	wa := webadapter.New()
	if err = wa.Start(ctx, eng.Process); err != nil {
		t.Fatal(err)
	}
	defer wa.Stop(context.Background())
	wa.SetStreamHandler(eng.ProcessStream)
	srv.SetWebSocketHandler(wa.Handler())
	srv.SetStreamStateProvider(wa)
	app := httptest.NewServer(srv.routes())
	defer app.Close()
	var version map[string]string
	apiCanonicalHistoryDoJSON(t, app.Client(), http.MethodGet, app.URL+"/api/v1/version", nil, &version)
	if version["chat_regenerate"] != "atomic_replace_v1" {
		t.Fatalf("capability missing: %v", version)
	}
	history := func() []storage.MessageRecord {
		t.Helper()
		var result struct{ Messages []storage.MessageRecord }
		apiCanonicalHistoryDoJSON(t, app.Client(), http.MethodGet, app.URL+"/api/v1/sessions/"+sessionID+"/messages", nil, &result)
		return result.Messages
	}
	before := history()
	if len(before) != 6 {
		t.Fatalf("wanted original 6 messages, got %d", len(before))
	}
	failed, err := http.NewRequestWithContext(ctx, http.MethodPost, app.URL+"/api/v1/chat", strings.NewReader(`{"session_id":"`+sessionID+`","request_id":"regen-fail","regenerate_from_message_id":"`+sourceID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	failed.Header.Set("Authorization", "Bearer "+cfg.Server.APIToken)
	failed.Header.Set("Content-Type", "application/json")
	response, err := app.Client().Do(failed)
	if err != nil {
		t.Fatal(err)
	}
	failureBody, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || !reflect.DeepEqual(history(), before) {
		t.Fatalf("failed regeneration changed original history or lost the upstream error: %d %s", response.StatusCode, failureBody)
	}

	wsCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(wsCtx, "ws"+strings.TrimPrefix(app.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{
		"Origin": {"tauri://localhost"}, "Authorization": {"Bearer " + cfg.Server.APIToken},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	payload := map[string]string{"type": "message", "session_id": sessionID, "request_id": "regen-ok", "regenerate_from_message_id": sourceID}
	if err = wsjson.Write(wsCtx, conn, payload); err != nil {
		t.Fatal(err)
	}
	var final struct {
		Type             string
		Content          string
		BackendMessageID string `json:"backend_message_id"`
		Done             bool
		Metadata         map[string]string
	}
	for !final.Done {
		if err = wsjson.Read(wsCtx, conn, &final); err != nil {
			t.Fatal(err)
		}
		if final.Type == "error" {
			t.Fatalf("regeneration failed: %+v", final)
		}
	}
	if final.BackendMessageID != "regen-ok:assistant" || final.Metadata["regenerate_from_message_id"] != sourceID {
		t.Fatalf("success lacks original turn and completed assistant identity: %+v", final)
	}
	after := history()
	if len(after) != 4 || !reflect.DeepEqual(after[:3], before[:3]) || after[3].ID != final.BackendMessageID || after[3].Content != newAnswer {
		t.Fatalf("replacement changed prefix/attachment or left the original tail: %+v", after)
	}
	var replay ChatResponse
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	apiCanonicalHistoryDoJSON(t, app.Client(), http.MethodPost, app.URL+"/api/v1/chat", body, &replay)
	if replay.BackendMessageID != final.BackendMessageID || replay.Reply != newAnswer || physicalCalls.Load() != 2 || !reflect.DeepEqual(history(), after) {
		t.Fatalf("same request did not reuse the original completed run: calls=%d replay=%+v", physicalCalls.Load(), replay)
	}
	for range 2 {
		upstreamBody := <-seen
		if !strings.Contains(upstreamBody, marker) || !strings.Contains(upstreamBody, "Earlier prefix answer") ||
			!strings.Contains(upstreamBody, imageData) || !strings.Contains(upstreamBody, `"model":"mock-model"`) ||
			strings.Contains(upstreamBody, "Prior answer to regenerate") || bytes.Contains([]byte(upstreamBody), []byte("not in replacement input")) {
			t.Fatalf("provider received content outside the frozen original turn: %s", upstreamBody)
		}
	}

	// 生成进行中，从现有消息 API 追加一条历史。提交必须发现前后快照不同。
	type outcome struct {
		status int
		err    error
	}
	casCtx, stop := context.WithTimeout(ctx, 8*time.Second)
	defer stop()
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	startRegeneration := func(requestCtx context.Context, requestID string) <-chan outcome {
		result := make(chan outcome, 1)
		go func() {
			request, reqErr := http.NewRequestWithContext(requestCtx, http.MethodPost, app.URL+"/api/v1/chat", strings.NewReader(`{"session_id":"`+sessionID+`","request_id":"`+requestID+`","regenerate_from_message_id":"`+sourceID+`"}`))
			if reqErr != nil {
				result <- outcome{err: reqErr}
				return
			}
			request.Header.Set("Authorization", "Bearer "+cfg.Server.APIToken)
			request.Header.Set("Content-Type", "application/json")
			response, reqErr := app.Client().Do(request)
			status := 0
			if reqErr == nil {
				status = response.StatusCode
				_ = response.Body.Close()
			}
			result <- outcome{status, reqErr}
		}()
		return result
	}
	result := startRegeneration(casCtx, "regen-conflict")
	select {
	case <-held:
	case <-casCtx.Done():
		t.Fatal("expected physical call for the in-flight regeneration")
	}
	var active struct {
		Streams []struct {
			RequestID        string `json:"request_id"`
			BackendMessageID string `json:"backend_message_id"`
			Metadata         map[string]string
		}
	}
	apiCanonicalHistoryDoJSON(t, app.Client(), http.MethodGet, app.URL+"/api/v1/streams/active", nil, &active)
	if len(active.Streams) != 1 || active.Streams[0].RequestID != "regen-conflict" || active.Streams[0].Metadata["regenerate_from_message_id"] != sourceID || active.Streams[0].BackendMessageID != "" {
		t.Fatalf("active recovery requires the original source without claiming a durable new reply: %+v", active.Streams)
	}
	appendCall, err := http.NewRequestWithContext(casCtx, http.MethodPost, app.URL+"/api/v1/sessions/"+sessionID+"/messages", strings.NewReader(`{"id":"late-message","role":"user","content":"User input committed during regeneration"}`))
	if err != nil {
		t.Fatal(err)
	}
	appendCall.Header.Set("Authorization", "Bearer "+cfg.Server.APIToken)
	appendCall.Header.Set("Content-Type", "application/json")
	appended, err := app.Client().Do(appendCall)
	if err != nil {
		t.Fatal(err)
	}
	appended.Body.Close()
	if appended.StatusCode != http.StatusCreated {
		t.Fatalf("could not append the concurrent user message: %d", appended.StatusCode)
	}
	unblock()
	if received := <-result; received.err != nil || received.status != http.StatusConflict {
		t.Fatalf("changed conversation was not reported as a conflict: %+v", received)
	}
	conflicted := history()
	if len(conflicted) != len(after)+1 || !reflect.DeepEqual(conflicted[:len(after)], after) || conflicted[len(after)].ID != "late-message" {
		t.Fatalf("regeneration conflict removed committed messages: %+v", conflicted)
	}

	cancelCtx, stopCancel := context.WithTimeout(ctx, 8*time.Second)
	defer stopCancel()
	defer close(cancelRelease)
	cancelResult := startRegeneration(cancelCtx, "regen-cancelled")
	select {
	case <-cancelHeld:
	case <-cancelCtx.Done():
		t.Fatal("expected an active request that can be explicitly cancelled")
	}
	stopCall, err := http.NewRequestWithContext(cancelCtx, http.MethodPost, app.URL+"/api/v1/streams/regen-cancelled/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	stopCall.Header.Set("Authorization", "Bearer "+cfg.Server.APIToken)
	stopped, err := app.Client().Do(stopCall)
	if err != nil {
		t.Fatal(err)
	}
	stopped.Body.Close()
	if stopped.StatusCode != http.StatusAccepted {
		t.Fatalf("cancel request was not accepted: %d", stopped.StatusCode)
	}
	if received := <-cancelResult; received.err != nil || received.status == http.StatusOK || !reflect.DeepEqual(history(), conflicted) {
		t.Fatalf("cancelled regeneration changed the original history: %+v", received)
	}
}
