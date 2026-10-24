package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	hexapi "github.com/hexagon-codes/hexclaw/api"
	"github.com/hexagon-codes/hexclaw/channel"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/apihttp"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assembly"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/skilladapter"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
)

// 仅替代外部模型和渠道；引擎、HTTP、冻结产物、SQLite与原投递批次使用真实实现。
type printReplyAPIModel struct{ calls atomic.Int32 }

func (*printReplyAPIModel) Name() string { return "print-reply-model" }
func (*printReplyAPIModel) Models() []llm.ModelInfo {
	return []llm.ModelInfo{{ID: "fixture-chat"}}
}
func (*printReplyAPIModel) CountTokens([]llm.Message) (int, error) { return 1, nil }
func (p *printReplyAPIModel) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.calls.Add(1)
	for _, message := range req.Messages {
		if message.Role == llm.RoleTool {
			return &llm.CompletionResponse{Content: "报告已准备。"}, nil
		}
	}
	return &llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "frozen-pdf", Name: "print_reply_fixture", Arguments: "{}"}}}, nil
}
func (p *printReplyAPIModel) Stream(_ context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	p.calls.Add(1)
	complete := false
	for _, m := range req.Messages {
		if m.Role == llm.RoleTool {
			complete = true
		}
	}
	delta := map[string]any{"content": "报告已准备。"}
	finish := "stop"
	if !complete {
		delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "frozen-pdf", "type": "function", "function": map[string]string{"name": "print_reply_fixture", "arguments": "{}"}}}}
		finish = "tool_calls"
	}
	frame, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	if err != nil {
		return nil, err
	}
	return llm.NewStream(strings.NewReader("data: "+string(frame)+"\n\ndata: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
}

type printReplyAPIRenderer struct {
	calls atomic.Int32
	pdf   []byte
}

func (p *printReplyAPIRenderer) Render(context.Context, string, string) ([]byte, string, error) {
	p.calls.Add(1)
	return bytes.Clone(p.pdf), "application/pdf", nil
}

type printReplyAPITool struct {
	deps     k12usecase.Deps
	artifact string
	calls    atomic.Int32
}

func (*printReplyAPITool) Name() string        { return "print_reply_fixture" }
func (*printReplyAPITool) Description() string { return "Return an already frozen PDF" }
func (*printReplyAPITool) Match(string) bool   { return false }
func (*printReplyAPITool) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("print_reply_fixture", "Return the prepared report", &llm.Schema{Type: "object"})
}
func (p *printReplyAPITool) Execute(ctx context.Context, _ map[string]any) (*skill.Result, error) {
	p.calls.Add(1)
	return skilladapter.PrintableArtifactResult(ctx, p.deps, "child-a", p.artifact, "冻结报告已就绪")
}

func printReplyValidPDF() []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Contents 4 0 R >>",
		"<< /Length 0 >>\nstream\n\nendstream",
	}
	var offsets []int
	for i, object := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 5\n0000000000 65535 f \n")
	for _, offset := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return b.Bytes()
}

func TestK12PrintReplyAPIOriginalTurnAndRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "print-reply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO agents(name) VALUES('child-a'),('child-b')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"print-web-session", "print-im-session"} {
		if err := store.CreateSession(ctx, &storage.Session{ID: id, UserID: "desktop-user", Platform: "api", Title: "PDF reply"}); err != nil {
			t.Fatal(err)
		}
	}
	delivery, dispatcher, channels := newDelivererFixture(t)
	recorded := &recordChannel{name: "dingtalk", queryAck: channel.DeliveryAck{Status: channel.DeliveryAccepted}}
	channels.Register(recorded)
	bindRule(t, dispatcher, "dingtalk", "bot-1", "parent-chat", "child-a")
	delivery.MarkReady()
	renderer := &printReplyAPIRenderer{pdf: printReplyValidPDF()}
	runtime, err := assembly.Wire(store.DB(), &printReplyAPITool{}, assembly.WithRenderer(renderer), assembly.WithDeliveryTransport(delivery))
	if err != nil {
		t.Fatal(err)
	}
	frozen, replayed, err := runtime.Deps.PreparePrintableArtifact(ctx, k12usecase.PreparePrintableArtifactRequest{AgentName: "child-a", SourceKind: k12.PrintSourceTutoringTips, SourceRef: "print-api-proof", Title: "题目讲解", CanonicalMarkdown: "# 讲解\n\n1+1=2"})
	if err != nil || replayed {
		t.Fatalf("freeze failed: replay=%v err=%v", replayed, err)
	}
	wantDigest := sha256.Sum256(renderer.pdf)
	if frozen.Render.ByteDigest != hex.EncodeToString(wantDigest[:]) || !bytes.Equal(frozen.Render.Payload, renderer.pdf) {
		t.Fatal("freeze changed source PDF bytes")
	}
	tool := &printReplyAPITool{deps: runtime.Deps, artifact: frozen.Artifact.ArtifactID}
	registry := skill.NewRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
	model := &printReplyAPIModel{}
	cfg := config.DefaultConfig()
	cfg.Compaction.Enabled, cfg.FileMemory.Enabled, cfg.LLM.Cache.Enabled = false, false, false
	cfg.LLM.Default, cfg.LLM.Tools.Enabled = "fixture", "on"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"fixture": {Model: "fixture-chat"}}
	eng := engine.NewReActEngine(cfg, llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"fixture": model}), store, registry)
	eng.SetAgentRouter(dispatcher)
	eng.SetToolCollector(engine.NewToolCollector(registry, nil, 20))
	eng.SetToolExecutor(engine.NewToolExecutor(registry, nil))
	eng.SetReplyArtifactResolver(engineadapter.NewPrintReplyResolver(runtime.Deps))
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.Host, cfg.Server.Port = "127.0.0.1", listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	server := hexapi.NewServer(cfg, eng, nil, store)
	server.SetDesktopAPIToken("print-reply-test-token")
	server.Mount("/api/k12", apihttp.NewHandler(apihttp.Runtime{Views: runtime.Registry.Views, Records: runtime.Records, Deps: runtime.Deps, PrincipalMode: "local_loopback", OwnerScope: "desktop-user"}))
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- server.Start(ctx, func() { close(ready) }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("start API: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Stop(stop); err != nil {
			t.Error(err)
		}
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
	call := func(method, target string, body any) (int, []byte) {
		t.Helper()
		var raw []byte
		if body != nil {
			var err error
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer print-reply-test-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	webRequest := map[string]any{"request_id": "print-web-request", "session_id": "print-web-session", "message": "把已冻结的讲解报告发给我", "role": "child-a"}
	status, raw := call(http.MethodPost, base+"/api/v1/chat", webRequest)
	if status != http.StatusOK {
		t.Fatalf("normal chat status=%d body=%s", status, raw)
	}
	webReply, err := store.GetMessage(ctx, "print-web-request:assistant")
	if err != nil || !strings.Contains(webReply.Metadata, frozen.Artifact.ArtifactID) {
		t.Fatalf("normal chat missing frozen reference: reply=%+v err=%v", webReply, err)
	}
	status, raw = call(http.MethodGet, base+"/api/v1/sessions/print-web-session/messages", nil)
	if status != http.StatusOK || !bytes.Contains(raw, []byte(frozen.Artifact.ArtifactID)) || !bytes.Contains(raw, []byte(frozen.Render.ByteDigest)) {
		t.Errorf("normal history lost frozen PDF reference: status=%d body=%s", status, raw)
	}
	status, raw = call(http.MethodGet, base+"/api/k12/print-artifacts/"+frozen.Artifact.ArtifactID+"/content?agent=child-a", nil)
	if status != http.StatusOK || !bytes.Equal(raw, renderer.pdf) {
		t.Fatalf("normal chat download lost frozen bytes: status=%d bytes=%d", status, len(raw))
	}
	status, raw = call(http.MethodGet, base+"/api/k12/print-artifacts/"+frozen.Artifact.ArtifactID+"/content?agent=child-b", nil)
	if status != http.StatusNotFound {
		t.Fatalf("cross-agent PDF status=%d body=%s", status, raw)
	}
	webCalls := model.calls.Load()
	status, raw = call(http.MethodPost, base+"/api/v1/chat", webRequest)
	if status != http.StatusOK || model.calls.Load() != webCalls {
		t.Fatalf("normal chat replay regenerated: status=%d calls=%d/%d body=%s", status, model.calls.Load(), webCalls, raw)
	}
	// HTTP入口只替代外部IM回调；可信物理实例和源消息由入口注入，不由模型填写。
	inbound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		msg := &adapter.Message{ID: "print-platform-message", UserID: "desktop-user", SessionID: "print-im-session", Platform: adapter.PlatformDingtalk, InstanceID: "bot-1", ChatID: "parent-chat", Content: "把已冻结的讲解报告发送在当前会话", Metadata: map[string]string{"request_id": "print-im-request", "routed_agent": "child-a", "role": "child-a", "user_locale": "zh-CN"}}
		reply, err := eng.Process(adapter.WithPlatformInstanceIdentity(r.Context(), "bot-1", "dingtalk"), msg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(inbound.Close)
	status, raw = call(http.MethodPost, inbound.URL, nil)
	if status != http.StatusOK {
		t.Fatalf("IM callback status=%d body=%s", status, raw)
	}
	imReply, err := store.GetMessage(ctx, "print-im-request:assistant")
	if err != nil || !strings.Contains(imReply.Metadata, frozen.Artifact.ArtifactID) {
		t.Fatalf("IM canonical reply missing PDF: reply=%+v err=%v", imReply, err)
	}
	var eventCount, replyCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_events WHERE event_type=? AND aggregate_id=?`, k12.EventConversationPrintReply, imReply.ID).Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("IM reply must atomically enqueue one original Outbox event: count=%d err=%v", eventCount, err)
	}
	if err := runtime.Outbox.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	status, raw = call(http.MethodGet, base+"/api/k12/print-replies/"+imReply.ID+"?agent=child-a", nil)
	if status != http.StatusOK || !bytes.Contains(raw, []byte("batch")) || bytes.Contains(raw, []byte(`"state":"delivered"`)) {
		var outboxState, outboxError string
		if err := store.DB().QueryRowContext(ctx, `SELECT status,last_error FROM outbox_events WHERE event_id=?`, "print-reply:"+imReply.ID).Scan(&outboxState, &outboxError); err != nil {
			t.Logf("outbox diagnostic: %v", err)
		} else {
			t.Logf("outbox state=%s last_error=%s", outboxState, outboxError)
		}
		t.Fatalf("unknown receipt was claimed delivered or batch missing: status=%d body=%s", status, raw)
	}
	var savedView k12usecase.ConversationPrintReplyView
	if err := json.Unmarshal(raw, &savedView); err != nil || savedView.Source.AgentName != "child-a" || savedView.Source.SessionID != "print-im-session" || savedView.Source.InstanceID != "bot-1" || savedView.Source.ChatID != "parent-chat" || len(savedView.Source.Artifacts) != 1 || savedView.Source.Artifacts[0].ArtifactID != frozen.Artifact.ArtifactID || savedView.Source.Artifacts[0].ByteDigest != frozen.Render.ByteDigest || savedView.Batch == nil {
		t.Fatalf("original channel or frozen PDF identity changed: view=%+v err=%v", savedView, err)
	}
	for _, receipt := range savedView.Batch.Receipts {
		if receipt.AgentName != "child-a" || receipt.Target.Platform != "dingtalk" || receipt.Target.InstanceID != "bot-1" || receipt.Target.ChatID != "parent-chat" {
			t.Fatalf("original batch target changed: %+v", receipt)
		}
	}
	if len(recorded.sentParts) != 2 {
		t.Fatalf("original turn expected text and PDF parts, got %d", len(recorded.sentParts))
	}
	if recorded.sentParts[1].Attachment == nil || recorded.sentParts[1].Attachment.MIME != "application/pdf" || !bytes.Equal(recorded.sentParts[1].Attachment.Data, renderer.pdf) {
		t.Fatal("original channel did not receive the frozen PDF bytes")
	}
	for range 3 {
		if err := runtime.Outbox.ProcessPending(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var waitingState string
	var waitingAttempts int
	if err := store.DB().QueryRowContext(ctx, `SELECT status,attempts FROM outbox_events WHERE event_id=?`, "print-reply:"+imReply.ID).Scan(&waitingState, &waitingAttempts); err != nil || waitingState != "pending" || waitingAttempts != 0 {
		t.Fatalf("accepted receipt waiting must not consume failure attempts: state=%s attempts=%d err=%v", waitingState, waitingAttempts, err)
	}
	sent := len(recorded.sentParts)
	modelCalls, toolCalls := model.calls.Load(), tool.calls.Load()
	recorded.queryAck = channel.DeliveryAck{Status: channel.DeliveryOutcomeUnknown}
	status, raw = call(http.MethodPost, base+"/api/k12/print-replies/"+imReply.ID+"/resume?agent=child-a", nil)
	if status != http.StatusOK || !bytes.Contains(raw, []byte(`"status":"outcome_unknown"`)) || recorded.queryCalls == 0 || len(recorded.sentParts) != sent {
		t.Fatalf("unknown receipt must remain unknown without resend: status=%d query=%d sent=%d body=%s", status, recorded.queryCalls, len(recorded.sentParts), raw)
	}
	status, raw = call(http.MethodPost, inbound.URL, nil)
	if status != http.StatusOK || model.calls.Load() != modelCalls || tool.calls.Load() != toolCalls {
		t.Fatalf("IM replay invoked model/tool again: status=%d body=%s", status, raw)
	}
	recorded.queryAck = channel.DeliveryAck{Status: channel.DeliveryDelivered}
	status, raw = call(http.MethodPost, base+"/api/k12/print-replies/"+imReply.ID+"/resume?agent=child-a", nil)
	if status != http.StatusOK || !bytes.Contains(raw, []byte(`"status":"delivered"`)) {
		t.Fatalf("resume status=%d body=%s", status, raw)
	}
	if err := runtime.Outbox.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	var outboxCompleted string
	if err := store.DB().QueryRowContext(ctx, `SELECT status FROM outbox_events WHERE event_id=?`, "print-reply:"+imReply.ID).Scan(&outboxCompleted); err != nil || outboxCompleted != "delivered" {
		t.Fatalf("delivered batch did not complete original Outbox: state=%s err=%v", outboxCompleted, err)
	}
	if len(recorded.sentParts) != sent || renderer.calls.Load() != 1 || model.calls.Load() != modelCalls {
		t.Fatalf("resume repeated work: parts=%d/%d renders=%d model=%d/%d", len(recorded.sentParts), sent, renderer.calls.Load(), model.calls.Load(), modelCalls)
	}
	status, raw = call(http.MethodPost, base+"/api/k12/print-replies/"+imReply.ID+"/resume?agent=child-a", nil)
	if status != http.StatusOK || len(recorded.sentParts) != sent {
		t.Fatalf("completed replay resent a successful part: status=%d parts=%d body=%s", status, len(recorded.sentParts), raw)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE role='assistant' AND request_id=?`, "print-im-request").Scan(&replyCount); err != nil || replyCount != 1 {
		t.Fatalf("IM replay wrote another assistant: count=%d err=%v", replyCount, err)
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox_events WHERE event_type=? AND aggregate_id=?`, k12.EventConversationPrintReply, imReply.ID).Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("replay duplicated the original Outbox event: count=%d err=%v", eventCount, err)
	}
	var mediaOperations int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM media_operations`).Scan(&mediaOperations); err != nil || mediaOperations != 0 {
		t.Fatalf("PDF reply must not create a media operation: count=%d err=%v", mediaOperations, err)
	}
	ref, attachment, err := runtime.Deps.ResolvePrintReplyArtifact(ctx, "child-a", frozen.Artifact.ArtifactID)
	if err != nil || ref.ByteDigest != frozen.Render.ByteDigest || attachment.ContentDigest != "sha256:"+frozen.Render.ByteDigest {
		t.Fatalf("recovered attachment identity changed: ref=%+v err=%v", ref, err)
	}
	reader, err := attachment.Source.OpenReader(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bytesAfter, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(bytesAfter, renderer.pdf) {
		t.Fatalf("recovered attachment bytes changed: read=%v close=%v", readErr, closeErr)
	}
	t.Logf("real HTTP/Engine/SQLite/Outbox/K12 batch passed; external model/channel are controlled; frozen=%s digest=%s renders=%d assistant=%d parts=%d; no live SDK receipt claim", ref.ArtifactID, ref.ByteDigest, renderer.calls.Load(), replyCount, len(recorded.sentParts))
}
