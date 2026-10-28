package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/channel"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/documentflow"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assembly"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
	"github.com/hexagon-codes/hexclaw/storage"
)

type combinedDocumentReplyModel struct{ printReplyAPIModel }

func (p *combinedDocumentReplyModel) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.calls.Add(1)
	for _, message := range req.Messages {
		if message.Role == llm.RoleTool {
			return &llm.CompletionResponse{Content: "讲解报告和会议记录均已准备。"}, nil
		}
	}
	return &llm.CompletionResponse{ToolCalls: []llm.ToolCall{
		{ID: "combined-native-report", Name: "print_reply_fixture", Arguments: "{}"},
		{ID: "combined-generic-document", Name: "export_document", Arguments: `{"content":"# Meeting notes\n\nThe review starts at 10:00.","format":"pdf","filename":"meeting-notes.pdf"}`},
	}}, nil
}

func (p *combinedDocumentReplyModel) Stream(ctx context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	response, err := p.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	delta := map[string]any{"content": response.Content}
	finish := "stop"
	if len(response.ToolCalls) > 0 {
		calls := make([]any, 0, len(response.ToolCalls))
		for i, call := range response.ToolCalls {
			calls = append(calls, map[string]any{"index": i, "id": call.ID, "type": "function", "function": map[string]string{"name": call.Name, "arguments": call.Arguments}})
		}
		delta, finish = map[string]any{"tool_calls": calls}, "tool_calls"
	}
	raw, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	if err != nil {
		return nil, err
	}
	return llm.NewStream(strings.NewReader("data: "+string(raw)+"\n\ndata: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
}

// 仅替代文件编译边界，实际工具、产物登记和冻结字节由应用执行。
type combinedDocumentPDFRenderer struct {
	dir   string
	pdf   []byte
	calls atomic.Int32
}

func (r *combinedDocumentPDFRenderer) Render(_ context.Context, _ string, format render.Format, _ render.RenderOptions) (*render.RenderResult, error) {
	r.calls.Add(1)
	f, err := os.CreateTemp(r.dir, "combined-document-*.pdf")
	if err != nil {
		return nil, err
	}
	_, writeErr := f.Write(r.pdf)
	closeErr := f.Close()
	if writeErr != nil {
		return nil, writeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return &render.RenderResult{Path: f.Name(), Size: int64(len(r.pdf)), ContentType: format.MIME()}, nil
}

func TestCombinedNativeAndGenericDocumentsUseOnePersistentDeliveryBatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "combined-reply.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO agents(name) VALUES('child-a'),('child-b')`); err != nil {
		t.Fatal(err)
	}
	const sessionID, requestID = "combined-im-session", "combined-im-request"
	if err := store.CreateSession(ctx, &storage.Session{ID: sessionID, UserID: "combined-owner", Platform: "dingtalk", InstanceID: "bot-1", ChatID: "parent-chat", Title: "Combined documents"}); err != nil {
		t.Fatal(err)
	}
	delivery, dispatcher, channels := newDelivererFixture(t)
	recorded := &recordChannel{name: "dingtalk", queryAck: channel.DeliveryAck{Status: channel.DeliveryDelivered}}
	channels.Register(recorded)
	bindRule(t, dispatcher, "dingtalk", "bot-1", "parent-chat", "child-a")
	delivery.MarkReady()
	nativeRenderer := &printReplyAPIRenderer{pdf: printReplyValidPDF()}
	genericRenderer := &combinedDocumentPDFRenderer{dir: t.TempDir(), pdf: append(printReplyValidPDF(), []byte("\n% meeting-notes artifact\n")...)}
	flow := documentflow.NewService(genericRenderer, store.DocumentRepository())
	var additionalCalls, additionalFiles atomic.Int32
	runtime, err := assembly.Wire(store.DB(), &printReplyAPITool{}, assembly.WithRenderer(nativeRenderer), assembly.WithDeliveryTransport(delivery), func(d *k12usecase.Deps) {
		d.ResolveAdditionalReplyAttachments = func(ctx context.Context, message *storage.MessageRecord) ([]k12usecase.FrozenReplyAttachment, error) {
			additionalCalls.Add(1)
			attachments, err := flow.ResolveMessageAttachments(ctx, message)
			if err != nil {
				return nil, err
			}
			additionalFiles.Add(int32(len(attachments)))
			result := make([]k12usecase.FrozenReplyAttachment, 0, len(attachments))
			for _, attachment := range attachments {
				result = append(result, k12usecase.FrozenReplyAttachment{DeliveryAttachmentIdentity: k12usecase.DeliveryAttachmentIdentity{Name: attachment.Name, MIME: attachment.Mime, ContentDigest: attachment.ContentDigest}, Size: attachment.Size, OpenReader: attachment.Source.OpenReader})
			}
			return result, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, _, err := runtime.Deps.PreparePrintableArtifact(ctx, k12usecase.PreparePrintableArtifactRequest{AgentName: "child-a", SourceKind: k12.PrintSourceTutoringTips, SourceRef: "combined-native-proof", Title: "题目讲解", CanonicalMarkdown: "# 讲解\n\n1+1=2"})
	if err != nil {
		t.Fatal(err)
	}
	nativeTool := &printReplyAPITool{deps: runtime.Deps, artifact: frozen.Artifact.ArtifactID}
	exporter := builtin.NewExportDocumentSkill(genericRenderer)
	exporter.SetArtifactPublisher(flow)
	registry := skill.NewRegistry()
	for _, tool := range []skill.Skill{nativeTool, exporter} {
		if err := registry.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	model := &combinedDocumentReplyModel{}
	cfg := config.DefaultConfig()
	cfg.Compaction.Enabled, cfg.FileMemory.Enabled, cfg.LLM.Cache.Enabled = false, false, false
	cfg.LLM.Default, cfg.LLM.Tools.Enabled = "fixture", "on"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"fixture": {Model: "fixture-chat"}}
	eng := engine.NewReActEngine(cfg, llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"fixture": model}), store, registry)
	eng.SetAgentRouter(dispatcher)
	eng.SetToolCollector(engine.NewToolCollector(registry, nil, 20))
	eng.SetToolExecutor(engine.NewToolExecutor(registry, nil))
	eng.SetReplyArtifactResolver(engineadapter.NewPrintReplyResolver(runtime.Deps))
	eng.AddReplyArtifactResolver(flow.ReplyResolver)
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	process := func() *adapter.Reply {
		t.Helper()
		msg := &adapter.Message{ID: "combined-platform-message", UserID: "combined-owner", SessionID: sessionID, Platform: adapter.PlatformDingtalk, InstanceID: "bot-1", ChatID: "parent-chat", Content: "返回原讲解报告，并将会议记录导出 PDF。", Metadata: map[string]string{"request_id": requestID, "routed_agent": "child-a", "role": "child-a", "user_locale": "zh-CN"}}
		reply, err := eng.Process(adapter.WithPlatformInstanceIdentity(ctx, "bot-1", "dingtalk"), msg)
		if err != nil {
			t.Fatal(err)
		}
		return reply
	}
	process()
	saved, err := store.GetMessage(ctx, requestID+":assistant")
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal([]byte(saved.Metadata), &metadata); err != nil {
		t.Fatal(err)
	}
	var documentRefs []documentflow.Reference
	if err := json.Unmarshal(metadata["document_artifacts"], &documentRefs); err != nil || len(documentRefs) != 1 {
		t.Fatalf("ordinary document was not committed with the original reply: refs=%v err=%v metadata=%s", documentRefs, err, saved.Metadata)
	}
	document, err := flow.Get(ctx, "combined-owner", documentRefs[0].ID)
	if err != nil || !bytes.Equal(document.Payload, genericRenderer.pdf) || document.Origin.RequestID != requestID || document.Origin.SessionID != sessionID {
		t.Fatalf("persisted ordinary document lost its bytes or source: err=%v artifact=%+v", err, document)
	}
	native, err := runtime.Deps.GetPrintableArtifactPDF(ctx, "child-a", frozen.Artifact.ArtifactID)
	if err != nil || !bytes.Equal(native.Render.Payload, nativeRenderer.pdf) {
		t.Fatalf("persisted teaching report changed: %v", err)
	}
	if err := runtime.Outbox.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	source, err := runtime.Records.GetConversationPrintReply(ctx, "child-a", saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 首次接纳后沿原批次查询可核验回执，不凭发送请求成功声明送达。
	if _, err := runtime.Deps.SendConversationPrintReply(ctx, source); err != nil {
		t.Fatal(err)
	}
	view, err := runtime.Deps.GetConversationPrintReply(ctx, source)
	if err != nil || view.Batch == nil || view.State != "delivered" {
		t.Fatalf("original native delivery did not complete: state=%s has_batch=%v err=%v", view.State, view.Batch != nil, err)
	}
	if len(recorded.sentParts) != 3 {
		t.Fatalf("one text and two document parts required, got %d; additional resolver calls=%d files=%d artifacts=%s", len(recorded.sentParts), additionalCalls.Load(), additionalFiles.Load(), metadata["artifacts"])
	}
	wantPayloads := map[string][]byte{frozen.Render.ByteDigest: nativeRenderer.pdf, document.ByteDigest: genericRenderer.pdf}
	files := 0
	for _, part := range recorded.sentParts {
		if part.Attachment == nil {
			continue
		}
		found := false
		for digest, payload := range wantPayloads {
			if bytes.Equal(part.Attachment.Data, payload) {
				delete(wantPayloads, digest)
				found = true
				break
			}
		}
		if !found || part.Attachment.MIME != "application/pdf" {
			t.Fatalf("delivery contains unexpected or duplicate document bytes: %+v", part)
		}
		files++
	}
	if files != 2 || len(wantPayloads) != 0 {
		t.Fatal("the persistent batch omitted a registered document")
	}
	modelCalls, toolCalls := model.calls.Load(), nativeTool.calls.Load()
	nativeCalls, genericCalls, sends := nativeRenderer.calls.Load(), genericRenderer.calls.Load(), len(recorded.sentParts)
	if modelCalls == 0 || toolCalls != 1 || nativeCalls != 1 || genericCalls != 1 {
		t.Fatalf("original engine/tool/render path was not exercised once: model=%d tool=%d native=%d generic=%d", modelCalls, toolCalls, nativeCalls, genericCalls)
	}
	process()
	if err := runtime.Outbox.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Deps.SendConversationPrintReply(ctx, source); err != nil {
		t.Fatal(err)
	}
	if model.calls.Load() != modelCalls || nativeTool.calls.Load() != toolCalls || nativeRenderer.calls.Load() != nativeCalls || genericRenderer.calls.Load() != genericCalls || len(recorded.sentParts) != sends {
		t.Fatalf("combined replay repeated work: model=%d/%d tool=%d/%d renders=%d/%d,%d/%d sends=%d/%d", model.calls.Load(), modelCalls, nativeTool.calls.Load(), toolCalls, nativeRenderer.calls.Load(), nativeCalls, genericRenderer.calls.Load(), genericCalls, len(recorded.sentParts), sends)
	}
	for _, check := range []struct {
		name, query string
		args        []any
	}{
		{"assistant", `SELECT COUNT(*) FROM messages WHERE role='assistant' AND request_id=?`, []any{requestID}},
		{"original outbox", `SELECT COUNT(*) FROM outbox_events WHERE event_type=? AND aggregate_id=?`, []any{k12.EventConversationPrintReply, saved.ID}},
		{"native batch", `SELECT COUNT(*) FROM k12_delivery_batches`, nil},
		{"generic artifact", `SELECT COUNT(*) FROM document_artifacts`, nil},
		{"native artifact bytes", `SELECT COUNT(*) FROM k12_print_artifact_renders`, nil},
	} {
		var count int
		if err := store.DB().QueryRowContext(ctx, check.query, check.args...).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s must remain unique after replay: count=%d err=%v", check.name, count, err)
		}
	}
	var receiptBatch string
	if err := store.DB().QueryRowContext(ctx, `SELECT batch_id FROM k12_delivery_receipts WHERE agent_name='child-a'`).Scan(&receiptBatch); err != nil || receiptBatch != view.Batch.BatchID {
		t.Fatalf("documents escaped the original persistent batch: batch=%s want=%s err=%v", receiptBatch, view.Batch.BatchID, err)
	}
	t.Logf("real Engine/SQLite/Outbox batch=%s sent two distinct frozen documents; replay added no model, render, tool or send calls", view.Batch.BatchID)
}
