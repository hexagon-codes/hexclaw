package instances

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/documentflow"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
)

type partialReplyHTTPAdapter struct {
	stubAdapter
	url string
}

// 独立 HTTP 出口仅验证通用实例发送端口，不声明真实平台的附件协议能力。
func (a *partialReplyHTTPAdapter) Send(ctx context.Context, _ string, reply *adapter.Reply) error {
	if len(reply.Attachments) != 1 || reply.Attachments[0].Source == nil {
		return errors.New("expected one frozen document")
	}
	reader, err := reply.Attachments[0].Source.OpenReader(ctx)
	if err != nil {
		return err
	}
	defer reader.Close()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, reader)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	return nil
}

func TestPartialReplyAttemptKeepsOriginalErrorAndEventDeduplication(t *testing.T) {
	m, cleanup := newTestManager(t)
	defer cleanup()
	const pdf = "%PDF-1.4\nfrozen necessary document\n%%EOF"
	var received atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(payload, []byte(pdf)) {
			t.Error("generic sender changed original PDF bytes")
		}
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	inst := &Instance{ID: "stable-partial", Name: "partial-robot", Provider: "slack", Enabled: true}
	if err := m.Upsert(t.Context(), inst); err != nil {
		t.Fatal(err)
	}
	m.running[inst.Name] = &partialReplyHTTPAdapter{url: receiver.URL}
	m.metadata[inst.Name] = inst
	processingErr := errors.New("original upstream error")
	attempts := 0
	handler := m.wrapHandler(inst, func(ctx context.Context, msg *adapter.Message) (*adapter.Reply, error) {
		attempts++
		reply := &adapter.Reply{AssistantMessageID: "assistant-" + msg.ID, Attachments: []adapter.Attachment{{Type: "file", Mime: "application/pdf", Source: &adapter.AttachmentSource{OpenReader: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte(pdf))), nil
		}}}}}
		if prepared, err := m.PrepareReplyDelivery(ctx, msg); err != nil || !prepared {
			t.Fatalf("original event was not prepared: %v", err)
		}
		reply.MarkDeliveryAttempted(msg)
		if err := m.Send(ctx, inst.ID, msg.ChatID, reply); err != nil {
			t.Fatal(err)
		}
		return reply, processingErr
	})
	msg := &adapter.Message{ID: "event-one", Platform: adapter.PlatformSlack, InstanceID: inst.ID, UserID: "owner", ChatID: "chat-one"}
	if _, err := handler(t.Context(), msg); err != processingErr {
		t.Fatalf("original error changed: %v", err)
	}
	status, err := m.Get(t.Context(), inst.Name)
	if err != nil || status.Status != StatusError {
		t.Fatalf("processing failure was hidden: %+v %v", status, err)
	}
	if reply, err := handler(t.Context(), msg); reply != nil || err != nil || attempts != 1 || received.Load() != 1 {
		t.Fatalf("same event reran its handler or delivery: attempts=%d received=%d reply=%v err=%v", attempts, received.Load(), reply, err)
	}
	other := *msg
	other.ID = "event-two"
	if _, err := handler(t.Context(), &other); err != processingErr || attempts != 2 || received.Load() != 2 {
		t.Fatalf("a different event was blocked by the earlier attempt: attempts=%d received=%d err=%v", attempts, received.Load(), err)
	}
}

func TestUnknownPartialDeliveryAttemptDoesNotReleaseEvent(t *testing.T) {
	m, cleanup := newTestManager(t)
	defer cleanup()
	inst := &Instance{ID: "stable-unknown", Name: "unknown-robot", Provider: "slack"}
	if err := m.Upsert(t.Context(), inst); err != nil {
		t.Fatal(err)
	}
	var received atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		received.Add(1)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	defer receiver.Close()
	m.running[inst.Name] = &partialReplyHTTPAdapter{url: receiver.URL}
	m.metadata[inst.Name] = inst
	processingErr := errors.New("original model connection lost")
	attempts := 0
	handler := m.wrapHandler(inst, func(ctx context.Context, msg *adapter.Message) (*adapter.Reply, error) {
		attempts++
		reply := &adapter.Reply{AssistantMessageID: "assistant-original", Attachments: []adapter.Attachment{{Source: &adapter.AttachmentSource{OpenReader: func(context.Context) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte("%PDF-1.4\noriginal\n%%EOF"))), nil
		}}}}}
		if prepared, err := m.PrepareReplyDelivery(ctx, msg); err != nil || !prepared {
			t.Fatalf("original event was not prepared: %v", err)
		}
		reply.MarkDeliveryAttempted(msg)
		if err := m.Send(ctx, inst.ID, msg.ChatID, reply); err == nil {
			t.Fatal("lost provider response was treated as confirmed delivery")
		}
		return reply, processingErr
	})
	msg := &adapter.Message{ID: "unknown-event", Platform: adapter.PlatformSlack, InstanceID: inst.ID, UserID: "owner", ChatID: "chat"}
	if _, err := handler(t.Context(), msg); err != processingErr {
		t.Fatal("original error was replaced")
	}
	if _, err := handler(t.Context(), msg); err != nil || attempts != 1 || received.Load() != 1 {
		t.Fatal("unconfirmed delivery was blindly replayed")
	}
}

type replyEventClaimPDFRenderer struct{ dir string }

func (r replyEventClaimPDFRenderer) Render(context.Context, string, render.Format, render.RenderOptions) (*render.RenderResult, error) {
	const content = "%PDF-1.4\nfrozen original document\n%%EOF"
	path := filepath.Join(r.dir, "original.pdf")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return nil, err
	}
	return &render.RenderResult{Path: path, Size: int64(len(content)), ContentType: "application/pdf"}, nil
}

func TestPartialReplyDeliveryRecoversInitialEventWriteFailure(t *testing.T) {
	ctx := t.Context()
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "event-claim.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	m := NewManager(store.DB())
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	inst := &Instance{ID: "stable-claim", Name: "claim-robot", Provider: "slack"}
	if err := m.Upsert(ctx, inst); err != nil {
		t.Fatal(err)
	}
	requestID := storage.IMRequestIDForEvent("slack", inst.ID, "original-event")
	msg := &adapter.Message{ID: "original-event", Platform: adapter.PlatformSlack, InstanceID: inst.ID, UserID: "owner", ChatID: "original-chat", SessionID: "original-session", Metadata: map[string]string{"request_id": requestID}}
	if err := store.CreateSession(ctx, &storage.Session{ID: msg.SessionID, UserID: msg.UserID, Platform: "slack", InstanceID: inst.ID, ChatID: msg.ChatID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(ctx, &storage.MessageRecord{ID: requestID + ":user", SessionID: msg.SessionID, Role: "user", RequestID: requestID, Content: "Export the original document"}); err != nil {
		t.Fatal(err)
	}
	flow := documentflow.NewService(replyEventClaimPDFRenderer{t.TempDir()}, store.DocumentRepository())
	inv := skill.InvocationContext{AuthenticatedUserID: msg.UserID, SessionID: msg.SessionID, RequestID: requestID, ToolCallID: "original-export", RoutedAgentName: "default", Platform: "slack", PlatformInstanceID: inst.ID, ChatID: msg.ChatID}
	document, err := flow.ExportDocument(skill.WithInvocationContext(ctx, inv), "Frozen original content", render.FormatPDF, render.RenderOptions{}, "original.pdf")
	if err != nil {
		t.Fatal(err)
	}
	refs, _ := json.Marshal([]documentflow.Reference{documentflow.ReferenceFor(document)})
	completionCtx, err := flow.ReplyResolver(ctx, msg, map[string]string{"artifacts": string(refs)})
	if err != nil {
		t.Fatal(err)
	}
	assistant := &storage.MessageRecord{ID: requestID + ":assistant", SessionID: msg.SessionID, Role: "assistant", RequestID: requestID, Content: "", Metadata: "{}"}
	if err := store.SaveMessage(completionCtx, assistant); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.ExecContext(ctx, `CREATE TRIGGER fail_original_event_claim BEFORE INSERT ON platform_events BEGIN SELECT RAISE(FAIL,'controlled event claim unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	var received atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(payload, document.Payload) {
			t.Error("original frozen PDF bytes changed")
		}
		received.Add(1)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	defer receiver.Close()
	m.running[inst.Name] = &partialReplyHTTPAdapter{url: receiver.URL}
	m.metadata[inst.Name] = inst
	processingErr := errors.New("original later model failure")
	handlers := 0
	handler := m.wrapHandler(inst, func(callCtx context.Context, original *adapter.Message) (*adapter.Reply, error) {
		handlers++
		if _, err := m.db.ExecContext(callCtx, `DROP TRIGGER IF EXISTS fail_original_event_claim`); err != nil {
			t.Fatal(err)
		}
		saved, err := store.GetMessage(callCtx, assistant.ID)
		if err != nil {
			t.Fatal(err)
		}
		files, err := flow.ResolveMessageAttachments(callCtx, saved)
		if err != nil || len(files) != 1 {
			t.Fatalf("original registered document is unavailable: %v", err)
		}
		reply := &adapter.Reply{AssistantMessageID: assistant.ID, Attachments: files}
		prepared, prepareErr := m.PrepareReplyDelivery(callCtx, original)
		if prepareErr != nil || !prepared {
			return reply, processingErr
		}
		reply.MarkDeliveryAttempted(original)
		if err := m.Send(callCtx, inst.ID, original.ChatID, reply); err == nil {
			t.Fatal("missing provider response was treated as confirmed")
		}
		return reply, processingErr
	})
	if _, err := handler(ctx, msg); err != processingErr {
		t.Fatalf("original handler error changed: %v", err)
	}
	var firstMarkers int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM platform_events WHERE instance_name=? AND event_id=?`, inst.Name, msg.ID).Scan(&firstMarkers); err != nil {
		t.Fatal(err)
	}
	_, secondErr := handler(ctx, msg)
	if handlers != 1 || received.Load() != 1 || secondErr != nil {
		t.Fatalf("unknown document replayed after initial event write failed: handler_calls=%d receiver_posts=%d marker_after_first=%d second_error=%v", handlers, received.Load(), firstMarkers, secondErr)
	}
}

func TestPartialReplyDeliveryPreparationFailureAllowsRetry(t *testing.T) {
	m, cleanup := newTestManager(t)
	defer cleanup()
	ctx := t.Context()
	inst := &Instance{ID: "retry-claim", Name: "retry-robot", Provider: "slack"}
	if _, err := m.db.ExecContext(ctx, `CREATE TRIGGER fail_reply_claim BEFORE INSERT ON platform_events BEGIN SELECT RAISE(FAIL,'controlled event claim unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	processingErr := errors.New("original model failure")
	handlers, sends := 0, 0
	handler := m.wrapHandler(inst, func(callCtx context.Context, msg *adapter.Message) (*adapter.Reply, error) {
		handlers++
		reply := &adapter.Reply{AssistantMessageID: "retry-assistant"}
		prepared, err := m.PrepareReplyDelivery(callCtx, msg)
		if err == nil && prepared {
			sends++
			reply.MarkDeliveryAttempted(msg)
		}
		return reply, processingErr
	})
	msg := &adapter.Message{ID: "retry-event", Platform: adapter.PlatformSlack, InstanceID: inst.ID, UserID: "owner", ChatID: "chat"}
	if _, err := handler(ctx, msg); err != processingErr || handlers != 1 || sends != 0 {
		t.Fatalf("failed claim blocked ordinary handler or began a send: handlers=%d sends=%d err=%v", handlers, sends, err)
	}
	if _, err := m.db.ExecContext(ctx, `DROP TRIGGER fail_reply_claim`); err != nil {
		t.Fatal(err)
	}
	if _, err := handler(ctx, msg); err != processingErr || handlers != 2 || sends != 1 {
		t.Fatalf("unprepared original event could not recover: handlers=%d sends=%d err=%v", handlers, sends, err)
	}
}

func TestPartialReplyDeliveryDoesNotDeleteAnotherCallbackClaim(t *testing.T) {
	m, cleanup := newTestManager(t)
	defer cleanup()
	ctx := t.Context()
	inst := &Instance{ID: "other-claim", Name: "other-robot", Provider: "slack"}
	if _, err := m.db.ExecContext(ctx, `CREATE TRIGGER fail_reply_claim BEFORE INSERT ON platform_events BEGIN SELECT RAISE(FAIL,'controlled event claim unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	processingErr := errors.New("original model failure")
	handlers := 0
	handler := m.wrapHandler(inst, func(callCtx context.Context, msg *adapter.Message) (*adapter.Reply, error) {
		handlers++
		if _, err := m.db.ExecContext(callCtx, `DROP TRIGGER fail_reply_claim`); err != nil {
			t.Fatal(err)
		}
		if _, err := m.recordEvent(callCtx, inst.Name, msg.ID); err != nil {
			t.Fatal(err)
		}
		prepared, err := m.PrepareReplyDelivery(callCtx, msg)
		if err != nil || prepared {
			t.Fatal("another callback's durable claim authorized a new send")
		}
		return &adapter.Reply{AssistantMessageID: "other-assistant"}, processingErr
	})
	msg := &adapter.Message{ID: "other-event", Platform: adapter.PlatformSlack, InstanceID: inst.ID, UserID: "owner", ChatID: "chat"}
	if _, err := handler(ctx, msg); err != processingErr {
		t.Fatal("original error changed")
	}
	if _, err := handler(ctx, msg); err != nil || handlers != 1 {
		t.Fatal("unowned callback removed another callback's claim")
	}
}
