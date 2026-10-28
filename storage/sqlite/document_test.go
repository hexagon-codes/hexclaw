package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/documentflow"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
)

// 文本渲染替身只隔离外部编译器，来源、事务、文件字节与重开均使用真实依赖。
type documentTextRenderer struct {
	dir   string
	calls int
	fail  bool
}

func (r *documentTextRenderer) Render(_ context.Context, content string, format render.Format, _ render.RenderOptions) (*render.RenderResult, error) {
	r.calls++
	if r.fail {
		return nil, fmt.Errorf("renderer unavailable")
	}
	p := filepath.Join(r.dir, "rendered.txt")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		return nil, err
	}
	return &render.RenderResult{Path: p, Size: int64(len(content)), ContentType: format.MIME()}, nil
}

func TestDocumentFlowFailedRenderResumesFrozenContentAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resume.db")
	store, err := newMigratedTestStore(t, path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, _ := documentSourceFixture(t, store)
	failing := &documentTextRenderer{dir: t.TempDir(), fail: true}
	flow := documentflow.NewService(failing, store.DocumentRepository())
	const body = "Original meeting notes, preserved before rendering."
	failed, err := flow.ExportDocument(ctx, body, render.FormatTXT, render.RenderOptions{}, "original.txt")
	if err == nil || failed.ID == "" || failed.State != "failed" {
		t.Fatalf("failed render was not retained: %+v %v", failed, err)
	}
	prepared, err := store.DocumentRepository().Get(ctx, "document-owner", failed.ID)
	if err != nil || prepared.Markdown != body || prepared.State != "failed" || prepared.Error == "" {
		t.Fatal("original render input is missing")
	}
	if _, err := flow.Get(ctx, "document-owner", failed.ID); !errors.Is(err, documentflow.ErrNotReady) {
		t.Fatalf("failed artifact was exposed as a file: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	working := &documentTextRenderer{dir: t.TempDir()}
	flow = documentflow.NewService(working, reopened.DocumentRepository())
	ready, err := flow.Resume(ctx, "document-owner", failed.ID)
	if err != nil || ready.State != "ready" || ready.ID != failed.ID || string(ready.Payload) != body {
		t.Fatalf("resume changed the original document: %+v %v", ready, err)
	}
	if _, err := flow.Resume(ctx, "document-owner", failed.ID); err != nil {
		t.Fatal(err)
	}
	if working.calls != 1 {
		t.Fatal("ready artifact was rendered again")
	}
}

func documentSourceFixture(t *testing.T, store *Store) (context.Context, *adapter.Message) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO platform_instances(id,provider,name,created_at,updated_at) VALUES('physical-instance','dingtalk','friendly-instance',?,?)`, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(ctx, &storage.Session{ID: "document-session", UserID: "document-owner", Platform: "dingtalk", InstanceID: "friendly-instance", ChatID: "document-owner", Status: 1, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(ctx, &storage.MessageRecord{ID: "document-user-message", SessionID: "document-session", Role: "user", Content: "Export the meeting notes", RequestID: "document-request"}); err != nil {
		t.Fatal(err)
	}
	inv := skill.InvocationContext{AuthenticatedUserID: "document-owner", SessionID: "document-session", RequestID: "document-request", ToolCallID: "document-call", RoutedAgentName: "default", Platform: "dingtalk", PlatformInstanceID: "physical-instance", ChatID: "document-owner"}
	return skill.WithInvocationContext(ctx, inv), &adapter.Message{ID: "document-request", UserID: inv.AuthenticatedUserID, SessionID: inv.SessionID, Platform: adapter.PlatformDingtalk, InstanceID: "friendly-instance", ChatID: inv.ChatID, Metadata: map[string]string{"request_id": inv.RequestID}}
}

func TestDocumentFlowPersistsOriginalBytesAcrossReopenAndReplyReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.db")
	store, err := newMigratedTestStore(t, path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, msg := documentSourceFixture(t, store)
	renderer := &documentTextRenderer{dir: t.TempDir()}
	flow := documentflow.NewService(renderer, store.DocumentRepository())
	const body = "Meeting notes: the release review starts at 10:00."
	a, err := flow.ExportDocument(ctx, body, render.FormatTXT, render.RenderOptions{}, "meeting-notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	ref := documentflow.ReferenceFor(a)
	encoded, _ := json.Marshal([]documentflow.Reference{ref})
	metadata := map[string]string{"artifacts": string(encoded)}
	completionCtx, err := flow.ReplyResolver(ctx, msg, metadata)
	if err != nil {
		t.Fatal(err)
	}
	completion, ok := storage.AssistantCompletionFromContext(completionCtx)
	if !ok || len(completion.Attachments) != 1 || completion.OwnsDelivery {
		t.Fatal("generic document must join the original reply")
	}
	raw, _ := json.Marshal(metadata)
	assistant := &storage.MessageRecord{ID: "document-assistant", SessionID: msg.SessionID, Role: "assistant", RequestID: msg.ID, Content: "The meeting notes are attached.", Metadata: string(raw)}
	attachmentJSON, _ := json.Marshal(map[string]any{"attachments": completion.Attachments})
	assistant.Attachments = string(attachmentJSON)
	if err := store.SaveMessage(completionCtx, assistant); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(completionCtx, assistant); err != nil {
		t.Fatalf("same completion must replay: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	flow = documentflow.NewService(renderer, reopened.DocumentRepository())
	replay, err := flow.ExportDocument(ctx, body, render.FormatTXT, render.RenderOptions{}, "meeting-notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	if renderer.calls != 1 || replay.ID != a.ID || string(replay.Payload) != body {
		t.Fatal("reopen must reuse original bytes without rendering")
	}
	persisted, err := reopened.GetMessage(ctx, assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := flow.ResolveMessageAttachments(ctx, persisted)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].Name != "meeting-notes.txt" || attachments[0].Source == nil {
		t.Fatal("persisted original reply must resolve a file")
	}
	reader, err := attachments[0].Source.OpenReader(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, _ := io.ReadAll(reader)
	if string(got) != body {
		t.Fatal("delivered attachment bytes differ")
	}
	if _, err := flow.Get(ctx, "another-owner", a.ID); !errors.Is(err, documentflow.ErrNotFound) {
		t.Fatalf("wrong owner: %v", err)
	}
	if _, err := flow.ExportDocument(ctx, "changed content", render.FormatTXT, render.RenderOptions{}, "meeting-notes.txt"); !errors.Is(err, documentflow.ErrConflict) {
		t.Fatalf("same invocation changed its content: %v", err)
	}
}

func TestDocumentFlowRejectsSourceAndAssistantMismatches(t *testing.T) {
	store := newTestStore(t)
	ctx, msg := documentSourceFixture(t, store)
	flow := documentflow.NewService(&documentTextRenderer{dir: t.TempDir()}, store.DocumentRepository())
	a, err := flow.ExportDocument(ctx, "A plain report", render.FormatTXT, render.RenderOptions{}, "report")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal([]documentflow.Reference{documentflow.ReferenceFor(a)})
	for _, tc := range []struct {
		name   string
		change func(*adapter.Message)
	}{
		{"sender", func(m *adapter.Message) { m.UserID = "other" }},
		{"instance", func(m *adapter.Message) { m.InstanceID = "other" }},
		{"session", func(m *adapter.Message) { m.SessionID = "other" }},
		{"request", func(m *adapter.Message) { m.Metadata = map[string]string{"request_id": "other"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := *msg
			tc.change(&copy)
			if _, _, err := flow.ResolveAttachments(ctx, &copy, string(raw)); err == nil {
				t.Fatal("mismatched source was accepted")
			}
		})
	}
	completionCtx, err := flow.ReplyResolver(ctx, msg, map[string]string{"artifacts": string(raw)})
	if err != nil {
		t.Fatal(err)
	}
	assistant := &storage.MessageRecord{ID: "document-assistant", SessionID: msg.SessionID, Role: "assistant", RequestID: msg.ID, Content: "Report attached", Metadata: `{"artifacts":` + string(raw) + `}`}
	if err := store.SaveMessage(completionCtx, assistant); err != nil {
		t.Fatal(err)
	}
	changed := *assistant
	changed.Content = "A different response"
	if _, err := flow.ResolveMessageAttachments(ctx, &changed); !errors.Is(err, documentflow.ErrConflict) {
		t.Fatalf("changed assistant: %v", err)
	}
	if err := store.SaveMessage(completionCtx, &changed); !errors.Is(err, documentflow.ErrConflict) {
		t.Fatalf("changed replay: %v", err)
	}
}
