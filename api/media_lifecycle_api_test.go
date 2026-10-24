package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/toolkit/blobstore"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	webadapter "github.com/hexagon-codes/hexclaw/adapter/web"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
	sqlitestore "github.com/hexagon-codes/hexclaw/storage/sqlite"
	"github.com/hexagon-codes/hexclaw/streamstate"
)

const lifecycleToken = "isolated-media-lifecycle-fixture"

type lifecycleApp struct {
	app    *httptest.Server
	engine *engine.ReActEngine
	flow   *mediaflow.Service
	store  *sqlitestore.Store
	web    *webadapter.WebAdapter
}

// 上游只控制有效 PNG 的返回时点，应用 HTTP、引擎、会话锁、数据库及文件使用真实实现。
func newLifecycleApp(t *testing.T, endpoint string, wrap func(storage.Store) storage.Store) *lifecycleApp {
	t.Helper()
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
	cfg.Server.APIToken = lifecycleToken
	cfg.Compaction.Enabled = false
	cfg.FileMemory.Enabled = false
	cfg.LLM.Cache.Enabled = false
	cfg.Skill.Builtin.MediaGen = false
	cfg.LLM.Default = "gateway"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"gateway": {APIKey: "fixture-key", Compatible: "openai", BaseURL: endpoint, ModelSpecsMode: config.LLMModelSpecsModeExplicit, ModelSpecs: []config.LLMProviderModelSpec{{ID: "drawing-model", Capabilities: []string{config.LLMModelCapabilityImageGeneration}}}}}
	router := mediaflow.NewRouter()
	router.Reload(cfg, nil)
	flow, err := mediaflow.NewService(t.Context(), router, store.MediaRepository(), blobs, filepath.Join(root, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(flow.Close)
	textRouter, err := llmrouter.New(cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	var effectiveStore storage.Store = store
	if wrap != nil {
		effectiveStore = wrap(store)
	}
	eng := engine.NewReActEngine(cfg, textRouter, effectiveStore, skill.NewRegistry())
	eng.SetMediaFlow(flow)
	if err := eng.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	wa := webadapter.New()
	if err := wa.Start(t.Context(), eng.Process); err != nil {
		t.Fatal(err)
	}
	wa.SetStreamHandler(eng.ProcessStream)
	t.Cleanup(func() { _ = wa.Stop(context.Background()) })
	srv := NewServer(cfg, eng, nil, effectiveStore)
	srv.SetGenStore(blobs)
	srv.SetMediaFlow(flow)
	srv.SetWebSocketHandler(wa.Handler())
	srv.SetStreamStateProvider(wa)
	app := httptest.NewServer(srv.routes())
	t.Cleanup(app.Close)
	return &lifecycleApp{app: app, engine: eng, flow: flow, store: store, web: wa}
}

func lifecyclePNG(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}
func lifecycleImage(w http.ResponseWriter, png64 string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"b64_json": png64}}})
}
func (f *lifecycleApp) call(ctx context.Context, method, path string, payload any, accept string) (int, []byte, error) {
	var data io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		data = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, f.app.URL+path, data)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+lifecycleToken)
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	r, err := f.app.Client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	return r.StatusCode, body, err
}
func lifecycleMessage(session, id, prompt string) map[string]any {
	return map[string]any{"session_id": session, "request_id": id, "message": prompt, "provider": "gateway", "model": "drawing-model", "metadata": map[string]string{"image_generation": "true"}}
}
func lifecycleWSMessage(session, id, prompt string) map[string]any {
	return map[string]any{"type": "message", "session_id": session, "request_id": id, "content": prompt, "provider": "gateway", "model": "drawing-model", "metadata": map[string]string{"image_generation": "true"}}
}
func (f *lifecycleApp) dial(ctx context.Context) (*websocket.Conn, error) {
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.app.URL, "http")+"/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"tauri://localhost"}, "Authorization": []string{"Bearer " + lifecycleToken}}})
	return c, err
}
func lifecycleEvidence(t *testing.T, name string, data map[string]any) {
	t.Helper()
	data["case"] = name
	data["observed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("MEDIA_LIFECYCLE_EVIDENCE_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("EVIDENCE %s", raw)
}
func lifecycleWait(t *testing.T, ctx context.Context, check func() bool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("observation condition not reached: " + ctx.Err().Error())
		case <-ticker.C:
		}
	}
}
func (f *lifecycleApp) operation(ctx context.Context, session, id string) (*mediaflow.Operation, error) {
	ops, err := f.flow.RequestOperations(ctx, "api-user", mediaflow.Origin{Kind: "conversation", SessionID: session, RequestID: id})
	if err != nil {
		return nil, err
	}
	if len(ops) != 1 {
		return nil, fmt.Errorf("operation count %d", len(ops))
	}
	return ops[0], nil
}

func TestMediaHTTPSessionLane(t *testing.T) {
	png64 := lifecyclePNG(t)
	firstEntered, release := make(chan struct{}), make(chan struct{})
	secondEntered := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var firstCalls, secondCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Prompt string `json:"prompt"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad", 400)
			return
		}
		if req.Prompt == "first held drawing" {
			if firstCalls.Add(1) == 1 {
				close(firstEntered)
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		} else {
			if secondCalls.Add(1) == 1 {
				close(secondEntered)
			}
		}
		lifecycleImage(w, png64)
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(unblock)
	f := newLifecycleApp(t, upstream.URL, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	type result struct {
		status int
		body   []byte
		err    error
	}
	firstDone := make(chan result, 1)
	secondDone := make(chan result, 1)
	go func() {
		s, b, e := f.call(ctx, "POST", "/api/v1/chat", lifecycleMessage("lane-session", "lane-first", "first held drawing"), "text/event-stream")
		firstDone <- result{s, b, e}
	}()
	select {
	case <-firstEntered:
	case <-ctx.Done():
		t.Fatal("first generation did not start")
	}
	go func() {
		s, b, e := f.call(ctx, "POST", "/api/v1/chat", lifecycleMessage("lane-session", "lane-second", "second drawing"), "text/event-stream")
		secondDone <- result{s, b, e}
	}()
	early := false
	select {
	case <-secondEntered:
		early = true
	case <-time.After(1200 * time.Millisecond):
	}
	var before []map[string]any
	rows, err := f.store.DB().QueryContext(ctx, "SELECT role,request_id FROM messages WHERE session_id=? ORDER BY created_at", "lane-session")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var role, id string
		if err := rows.Scan(&role, &id); err != nil {
			t.Fatal(err)
		}
		before = append(before, map[string]any{"role": role, "request_id": id})
	}
	_ = rows.Close()
	unblock()
	a, b := <-firstDone, <-secondDone
	status, history, herr := f.call(ctx, "GET", "/api/v1/sessions/lane-session/messages", nil, "")
	lifecycleEvidence(t, "finding-01-session-lane", map[string]any{"second_provider_entered_before_first_released": early, "first_calls": firstCalls.Load(), "second_calls": secondCalls.Load(), "messages_before_first_release": before, "first_http": a.status, "first_transport_error": fmt.Sprint(a.err), "second_http": b.status, "second_transport_error": fmt.Sprint(b.err), "history_http": status, "history_error": fmt.Sprint(herr), "history": json.RawMessage(history), "first_sse": string(a.body), "second_sse": string(b.body)})
	if a.err != nil || b.err != nil || status != 200 {
		t.Fatal("fixture transport failed")
	}
	if early {
		t.Fatal("same-session second generation reached provider before first turn completed")
	}
}

func TestMediaHTTPDisconnectRecovery(t *testing.T) {
	png64 := lifecyclePNG(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		lifecycleImage(w, png64)
	}))
	t.Cleanup(upstream.Close)
	t.Cleanup(unblock)
	f := newLifecycleApp(t, upstream.URL, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	defer unblock()
	c, err := f.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const id, session = "disconnect-request", "disconnect-session"
	if err := wsjson.Write(ctx, c, lifecycleWSMessage(session, id, "held disconnect drawing")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("provider not entered")
	}
	_ = c.Close(websocket.StatusNormalClosure, "connection lost")
	var snapshot streamstate.Snapshot
	// 必须跨过入口的断线宽限期，断线期间仍保留真实业务状态。
	grace := time.NewTimer(5100 * time.Millisecond)
	defer grace.Stop()
	select {
	case <-grace.C:
	case <-ctx.Done():
		t.Fatal("disconnect grace observation expired")
	}
	statusAfterGrace, rawAfterGrace, graceErr := f.call(ctx, "GET", "/api/v1/streams/"+id, nil, "")
	if graceErr != nil || statusAfterGrace != 200 || json.Unmarshal(rawAfterGrace, &snapshot) != nil {
		t.Fatal("stream snapshot unavailable")
	}
	op, err := f.operation(ctx, session, id)
	if err != nil {
		t.Fatal(err)
	}
	activeStatus, activeRaw, err := f.call(ctx, "GET", "/api/v1/streams/active", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	c, err = f.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(websocket.StatusNormalClosure, "") }()
	if err := wsjson.Write(ctx, c, map[string]any{"type": "resume", "session_id": session, "request_id": id}); err != nil {
		t.Fatal(err)
	}
	var resumed map[string]any
	if err := wsjson.Read(ctx, c, &resumed); err != nil {
		t.Fatal(err)
	}
	unblock()
	lifecycleWait(t, ctx, func() bool {
		var n int
		_ = f.store.DB().QueryRowContext(ctx, "SELECT count(*) FROM messages WHERE role='assistant' AND request_id=?", id).Scan(&n)
		return n == 1
	})
	finalOp, err := f.operation(ctx, session, id)
	if err != nil {
		t.Fatal(err)
	}
	finalStatus, finalRaw, err := f.call(ctx, "GET", "/api/v1/streams/"+id, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	historyStatus, history, err := f.call(ctx, "GET", "/api/v1/sessions/"+session+"/messages", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	lifecycleEvidence(t, "finding-02-disconnect-recovery", map[string]any{"snapshot_after_grace": snapshot, "operation_before_release": map[string]any{"generation_state": op.GenerationState, "save_state": op.SaveState, "cancel_requested": op.CancelRequested}, "active_http": activeStatus, "active_streams": json.RawMessage(activeRaw), "resume_ws_response": resumed, "final_operation": map[string]any{"generation_state": finalOp.GenerationState, "save_state": finalOp.SaveState, "cancel_requested": finalOp.CancelRequested, "assets": finalOp.Assets}, "final_snapshot_http": finalStatus, "final_snapshot": json.RawMessage(finalRaw), "history_http": historyStatus, "history": json.RawMessage(history), "generation_calls": calls.Load()})
	if snapshot.Done && !op.CancelRequested && op.SaveState != "ready" {
		t.Fatal("ordinary disconnect exposed a terminal snapshot while accepted media was still running")
	}
}

type lifecycleSaveGate struct {
	storage.Store
	requestID string
	arrived   chan context.Context
	release   chan struct{}
	finished  chan error
	once      sync.Once
}

func (g *lifecycleSaveGate) SaveMessage(ctx context.Context, msg *storage.MessageRecord) error {
	wait := false
	if msg.Role == "assistant" && msg.RequestID == g.requestID {
		g.once.Do(func() { wait = true; g.arrived <- ctx })
		if wait {
			<-g.release
		}
	}
	err := g.Store.SaveMessage(ctx, msg)
	if wait {
		g.finished <- err
	}
	return err
}

func TestMediaHTTPCancelAtCommit(t *testing.T) {
	png64 := lifecyclePNG(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); lifecycleImage(w, png64) }))
	t.Cleanup(upstream.Close)
	const id, session = "cancel-at-commit", "commit-session"
	gate := &lifecycleSaveGate{requestID: id, arrived: make(chan context.Context, 1), release: make(chan struct{}), finished: make(chan error, 1)}
	var once sync.Once
	unblock := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(unblock)
	f := newLifecycleApp(t, upstream.URL, func(s storage.Store) storage.Store { gate.Store = s; return gate })
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	defer unblock()
	c, err := f.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(websocket.StatusNormalClosure, "") }()
	if err := wsjson.Write(ctx, c, lifecycleWSMessage(session, id, "ready drawing")); err != nil {
		t.Fatal(err)
	}
	var saveContext context.Context
	select {
	case saveContext = <-gate.arrived:
	case <-ctx.Done():
		t.Fatal("assistant save barrier not reached")
	}
	ready, err := f.operation(ctx, session, id)
	if err != nil {
		t.Fatal(err)
	}
	if ready.SaveState != "ready" {
		t.Fatal("save barrier precedes media readiness")
	}
	if err := wsjson.Write(ctx, c, map[string]string{"type": "cancel", "session_id": session, "request_id": id}); err != nil {
		t.Fatal(err)
	}
	var cancelled *mediaflow.Operation
	lifecycleWait(t, ctx, func() bool {
		cancelled, err = f.operation(ctx, session, id)
		return err == nil && cancelled.CancelRequested
	})
	var before int
	if err := f.store.DB().QueryRowContext(ctx, "SELECT count(*) FROM messages WHERE role='assistant' AND request_id=?", id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	contextErr := fmt.Sprint(saveContext.Err())
	unblock()
	var commitError error
	select {
	case commitError = <-gate.finished:
	case <-ctx.Done():
		t.Fatal("save did not return after barrier release")
	}
	var after int
	_ = f.store.DB().QueryRowContext(ctx, "SELECT count(*) FROM messages WHERE role='assistant' AND request_id=?", id).Scan(&after)
	status, history, err := f.call(ctx, "GET", "/api/v1/sessions/"+session+"/messages", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	snapshotStatus, snapshotRaw, err := f.call(ctx, "GET", "/api/v1/streams/"+id, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	lifecycleEvidence(t, "finding-07-cancel-at-commit", map[string]any{"save_barrier_reached_after_ready": true, "cancel_persisted_before_release": cancelled.CancelRequested, "save_context_error_after_cancel": contextErr, "commit_error": fmt.Sprint(commitError), "assistant_before_release": before, "assistant_after_release": after, "generation_calls": calls.Load(), "asset_count": len(cancelled.Assets), "history_http": status, "history": json.RawMessage(history), "snapshot_http": snapshotStatus, "snapshot": json.RawMessage(snapshotRaw)})
	if cancelled.CancelRequested && before == 0 && after != 0 {
		t.Fatal("assistant completion committed after explicit cancellation was already durable")
	}
}
