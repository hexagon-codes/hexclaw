package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/channel"
	"github.com/hexagon-codes/hexclaw/config"
	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/internal/documentflow"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/llmrouter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assembly"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/builtin"
	"github.com/hexagon-codes/hexclaw/storage"
)

type partialDocumentFailureModel struct{ printReplyAPIModel }

var errPartialDocumentUpstream = context.DeadlineExceeded

func (p *partialDocumentFailureModel) Complete(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.calls.Add(1)
	for _, message := range req.Messages {
		if message.Role == llm.RoleTool {
			return nil, errPartialDocumentUpstream
		}
	}
	return &llm.CompletionResponse{ToolCalls: []llm.ToolCall{{ID: "partial-document-export", Name: "export_document", Arguments: `{"content":"# Notes\n\nFrozen actual document.","format":"pdf","filename":"partial-notes.pdf"}`}}}, nil
}

func (p *partialDocumentFailureModel) Stream(ctx context.Context, req llm.CompletionRequest) (*llm.Stream, error) {
	response, err := p.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	calls := make([]any, 0, len(response.ToolCalls))
	for i, call := range response.ToolCalls {
		calls = append(calls, map[string]any{"index": i, "id": call.ID, "type": "function", "function": map[string]string{"name": call.Name, "arguments": call.Arguments}})
	}
	raw, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}})
	if err != nil {
		return nil, err
	}
	return llm.NewStream(strings.NewReader("data: "+string(raw)+"\n\ndata: [DONE]\n\n"), llm.StreamOpenAIFormat), nil
}

// 模型错误和平台网络受控；文档工具、文件登记及原助手消息使用正式实现。
func TestIMPartialDocumentAfterUpstreamFailureReachesDelivery(t *testing.T) {
	ctx := t.Context()
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "partial-document.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO agents(name) VALUES('child-a')`); err != nil {
		t.Fatal(err)
	}
	const sessionID, requestID = "partial-document-session", "partial-document-request"
	if err := store.CreateSession(ctx, &storage.Session{ID: sessionID, UserID: "document-owner", Platform: "dingtalk", InstanceID: "bot-1", ChatID: "document-owner"}); err != nil {
		t.Fatal(err)
	}
	delivery, dispatcher, channels := newDelivererFixture(t)
	recorded := &recordChannel{name: "dingtalk", queryAck: channel.DeliveryAck{Status: channel.DeliveryDelivered}}
	channels.Register(recorded)
	bindRule(t, dispatcher, "dingtalk", "bot-1", "document-owner", "child-a")
	delivery.MarkReady()
	runtime, err := assembly.Wire(store.DB(), &printReplyAPITool{}, assembly.WithDeliveryTransport(delivery))
	if err != nil {
		t.Fatal(err)
	}
	renderer := &combinedDocumentPDFRenderer{dir: t.TempDir(), pdf: append(printReplyValidPDF(), []byte("\n% actual partial artifact\n")...)}
	flow := documentflow.NewService(renderer, store.DocumentRepository())
	exporter := builtin.NewExportDocumentSkill(renderer)
	exporter.SetArtifactPublisher(flow)
	registry := skill.NewRegistry()
	if err := registry.Register(exporter); err != nil {
		t.Fatal(err)
	}
	model := &partialDocumentFailureModel{}
	cfg := config.DefaultConfig()
	cfg.Compaction.Enabled, cfg.FileMemory.Enabled, cfg.LLM.Cache.Enabled = false, false, false
	cfg.LLM.Default, cfg.LLM.Tools.Enabled = "fixture", "on"
	cfg.LLM.Providers = map[string]config.LLMProviderConfig{"fixture": {Model: "fixture-chat"}}
	eng := engine.NewReActEngine(cfg, llmrouter.NewWithProviders(cfg.LLM, map[string]hexagon.Provider{"fixture": model}), store, registry)
	eng.SetAgentRouter(dispatcher)
	eng.SetToolCollector(engine.NewToolCollector(registry, nil, 20))
	eng.SetToolExecutor(engine.NewToolExecutor(registry, nil))
	eng.AddReplyArtifactResolver(flow.ReplyResolver)
	if err := eng.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Stop(context.Background()) })
	msg := &adapter.Message{ID: "partial-platform-event", UserID: "document-owner", SessionID: sessionID, Platform: adapter.PlatformDingtalk, InstanceID: "bot-1", ChatID: "document-owner", Content: "Export these notes as PDF.", Metadata: map[string]string{"request_id": requestID, "routed_agent": "child-a", "role": "child-a", "user_locale": "zh-CN", "conversation_type": "1"}}
	reply, handlerErr := eng.Process(adapter.WithPlatformInstanceIdentity(ctx, "bot-1", "dingtalk"), msg)
	if handlerErr == nil || reply == nil || len(reply.Attachments) != 1 || reply.AssistantMessageID == "" {
		t.Fatalf("fixture did not retain a genuine partial attachment: reply=%+v err=%v", reply, handlerErr)
	}
	if !errors.Is(handlerErr, context.DeadlineExceeded) || ctx.Err() != nil {
		t.Fatal("fixture did not isolate a model timeout from the live IM context")
	}
	saved, err := store.GetMessage(ctx, reply.AssistantMessageID)
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := flow.ResolveMessageAttachments(ctx, saved)
	if err != nil || len(attachments) != 1 {
		t.Fatalf("actual document was not committed on original assistant: attachments=%d err=%v", len(attachments), err)
	}
	r, err := attachments[0].Source.OpenReader(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var actual bytes.Buffer
	if _, err := actual.ReadFrom(r); err != nil || !bytes.Equal(actual.Bytes(), renderer.pdf) {
		t.Fatal("registered document lost its frozen bytes")
	}
	var outboxCount int
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM outbox_events`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 0 {
		t.Fatalf("fixture already has an outbox owner: %d", outboxCount)
	}
	var received atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, readErr := io.ReadAll(r.Body)
		if readErr != nil || !bytes.Equal(payload, renderer.pdf) {
			t.Error("final delivery changed frozen PDF bytes")
		}
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	deps := imPartialDocumentDependencies{store: store, documents: flow, getPrintReply: runtime.Records.GetConversationPrintReply,
		prepareDelivery: func(context.Context, *adapter.Message) (bool, error) { return true, nil },
		resolveInstanceID: func(platform, instance string) (string, error) {
			if platform != "dingtalk" || instance != "bot-1" {
				return "", errors.New("wrong original instance")
			}
			return "bot-1", nil
		}, send: func(sendCtx context.Context, instance, chat string, outgoing *adapter.Reply) error {
			if instance != "bot-1" || chat != msg.ChatID || len(outgoing.Attachments) != 1 || outgoing.Content != "" {
				t.Fatal("supplemental delivery changed target or included unrelated reply content")
			}
			source, openErr := outgoing.Attachments[0].Source.OpenReader(sendCtx)
			if openErr != nil {
				return openErr
			}
			defer source.Close()
			request, makeErr := http.NewRequestWithContext(sendCtx, http.MethodPost, receiver.URL, source)
			if makeErr != nil {
				return makeErr
			}
			response, sendErr := http.DefaultClient.Do(request)
			if sendErr != nil {
				return sendErr
			}
			defer response.Body.Close()
			return nil
		}}
	// 已取消或准备失败不开始新的发送，也不把事件标为已派发。
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	expired, stopExpired := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer stopExpired()
	for _, boundary := range []struct {
		ctx context.Context
		err error
	}{{cancelled, handlerErr}, {ctx, context.Canceled}, {expired, context.DeadlineExceeded}} {
		copy := *reply
		got, gotErr := maybeDeliverIMPartialDocuments(boundary.ctx, msg, &copy, boundary.err, deps)
		if got != &copy || gotErr != boundary.err || copy.DeliveryAttempted(msg) || received.Load() != 0 {
			t.Fatal("cancellation started a new document delivery")
		}
	}
	missing := *reply
	missing.AssistantMessageID = "not-persisted-assistant"
	_, missingErr := maybeDeliverIMPartialDocuments(ctx, msg, &missing, handlerErr, deps)
	if missingErr != handlerErr || missing.DeliveryAttempted(msg) || received.Load() != 0 {
		t.Fatal("failed preparation claimed a sending attempt")
	}
	mediaMessage := *msg
	mediaMessage.ID = "media-only-event"
	mediaMessage.Metadata = map[string]string{"request_id": requestID + "-media"}
	mediaRecord := &storage.MessageRecord{ID: requestID + "-media:assistant", SessionID: sessionID, Role: "assistant", RequestID: requestID + "-media", Content: "Media is handled separately.", Metadata: `{"media_delivery_id":"existing-media-owner"}`}
	if err := store.SaveMessage(ctx, mediaRecord); err != nil {
		t.Fatal(err)
	}
	mediaReply := &adapter.Reply{AssistantMessageID: mediaRecord.ID, Metadata: map[string]string{"media_delivery_id": "existing-media-owner"}, Attachments: []adapter.Attachment{{ID: "native-media-only", Type: "image", Mime: "image/png"}}}
	_, mediaErr := maybeDeliverIMPartialDocuments(ctx, &mediaMessage, mediaReply, handlerErr, deps)
	if mediaErr != handlerErr || mediaReply.DeliveryAttempted(&mediaMessage) || received.Load() != 0 {
		t.Fatal("media-only reply was duplicated by generic document delivery")
	}
	for _, prepareErr := range []error{nil, errors.New("controlled original event write unavailable")} {
		blocked := *reply
		blockedDeps := deps
		blockedDeps.prepareDelivery = func(context.Context, *adapter.Message) (bool, error) { return false, prepareErr }
		_, blockedErr := maybeDeliverIMPartialDocuments(ctx, msg, &blocked, handlerErr, blockedDeps)
		if blockedErr != handlerErr || blocked.DeliveryAttempted(msg) || received.Load() != 0 {
			t.Fatal("unprepared or already claimed event began a new document send")
		}
	}
	// 另一项媒体由既有 worker 接管，不影响独立文档的首次交付。
	reply.Metadata["media_delivery_id"] = "existing-media-owner"
	reply.Attachments = append(reply.Attachments, adapter.Attachment{ID: "unrelated-media", Type: "image", Mime: "image/png"})
	reply.ToolCalls = append(reply.ToolCalls, adapter.ToolCall{Name: "unrelated_model_action", Detail: &adapter.ToolExecutionDetail{Outcome: "unknown"}})
	got, gotErr := maybeDeliverIMPartialDocuments(ctx, msg, reply, handlerErr, deps)
	if gotErr != handlerErr {
		t.Fatalf("original processing error changed: %v", gotErr)
	}
	if got != reply || !reply.DeliveryAttempted(msg) || received.Load() != 1 {
		t.Fatalf("necessary document did not reach one actual HTTP delivery: attempts=%d original_error=%v", received.Load(), gotErr)
	}
	modelCalls, renders := model.calls.Load(), renderer.calls.Load()
	_, replayErr := maybeDeliverIMPartialDocuments(ctx, msg, reply, handlerErr, deps)
	if replayErr != handlerErr || received.Load() != 1 || model.calls.Load() != modelCalls || renderer.calls.Load() != renders {
		t.Fatal("the same delivered reply regenerated or resent its document")
	}
	if err := store.DB().QueryRowContext(ctx, `SELECT count(*) FROM outbox_events`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 0 {
		t.Fatal("generic document unexpectedly created an outbox")
	}
}
