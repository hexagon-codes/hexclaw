package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/documentflow"
	"github.com/hexagon-codes/hexclaw/internal/testutil/sqlitefixture"
	"github.com/hexagon-codes/hexclaw/storage"
)

func TestIMPhotoReplayRestoresOriginalScopedSessionAndDocument(t *testing.T) {
	store, err := sqlitefixture.New(filepath.Join(t.TempDir(), "replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := adapter.WithPlatformInstanceIdentity(t.Context(), "physical", "dingtalk")
	request := storage.IMRequestIDForEvent("dingtalk", "physical", "event")
	msg := adapter.Message{ID: "event", Platform: adapter.PlatformDingtalk, InstanceID: "original-name",
		UserID: "parent", ChatID: "parent", Metadata: map[string]string{"request_id": request}}
	old := storage.Session{ID: "archived", UserID: msg.UserID, Platform: string(msg.Platform), InstanceID: msg.InstanceID, ChatID: msg.ChatID}
	if err := store.CreateSession(ctx, &old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO platform_instances(id,provider,name,created_at,updated_at) VALUES (?,?,?,?,?)`, "physical", "dingtalk", msg.InstanceID, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(ctx, &storage.MessageRecord{ID: request, RequestID: request, SessionID: old.ID, Role: "user", Content: "说明见附件"}); err != nil {
		t.Fatal(err)
	}
	data := []byte("原照片的说明")
	digest := sha256.Sum256(data)
	document, err := store.DocumentRepository().Save(ctx, documentflow.Artifact{
		ID: "document", Name: "说明.txt", Format: "txt", MIME: "text/plain", ByteDigest: hex.EncodeToString(digest[:]),
		Size: int64(len(data)), Payload: data, State: "ready", CreatedAt: time.Now(), InputDigest: "original-input",
		Origin: documentflow.Origin{Owner: msg.UserID, SessionID: old.ID, RequestID: request, ToolCallID: "call", AgentName: "original-agent",
			Platform: old.Platform, InstanceID: "physical", ChatID: old.ChatID},
	})
	if err != nil {
		t.Fatal(err)
	}
	flow := documentflow.NewService(nil, store.DocumentRepository())
	encoded, _ := json.Marshal([]documentflow.Reference{documentflow.ReferenceFor(document)})
	msg.SessionID = old.ID
	completionCtx, err := flow.ReplyResolver(ctx, &msg, map[string]string{"artifacts": string(encoded)})
	if err != nil {
		t.Fatal(err)
	}
	completion, _ := storage.AssistantCompletionFromContext(completionCtx)
	attachments, _ := json.Marshal(map[string]any{"attachments": completion.Attachments})
	const body = "这是原来已经写好的说明。"
	if err := store.SaveMessage(completionCtx, &storage.MessageRecord{ID: canonicalAssistantMessageID(&msg), SessionID: old.ID,
		Role: "assistant", RequestID: request, Content: body, Metadata: `{"agent_name":"original-agent","delivery_attempted":"true"}`, Attachments: string(attachments)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, "UPDATE sessions SET status=0 WHERE id=?", old.ID); err != nil {
		t.Fatal(err)
	}
	newer := old
	newer.ID = "current"
	if err := store.CreateSession(ctx, &newer); err != nil {
		t.Fatal(err)
	}
	msg.SessionID = ""
	eng := &ReActEngine{store: store}
	eng.SetIMPhotoDocumentReader(flow.ResolveMessageAttachments)
	eng.SetReplyArtifactResolver(func(ctx context.Context, _ *adapter.Message, _ map[string]string) (context.Context, error) {
		t.Fatal("read-only recovery called the original writer")
		return ctx, nil
	})
	reply, err := eng.ReplayCompletedIMPhotoReply(ctx, &msg)
	if err != nil {
		t.Fatal(err)
	}
	if reply == nil || reply.Content != body || reply.AssistantMessageID != request+":assistant" || msg.SessionID != old.ID ||
		msg.Metadata["routed_agent"] != "original-agent" || reply.DeliveryAttempted(&msg) || reply.MessageContent == nil || len(reply.Attachments) != 1 {
		t.Fatalf("original scoped reply was not restored: %+v", reply)
	}
	attachment := reply.Attachments[0]
	if attachment.Source == nil || attachment.ID != document.ID || attachment.ContentDigest != "sha256:"+document.ByteDigest {
		t.Fatal("original document source or digest missing")
	}
	file, err := attachment.Source.OpenReader(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil || string(got) != string(data) {
		t.Fatalf("original attachment mismatch: %v", err)
	}
	for _, change := range []func(*adapter.Message){
		func(m *adapter.Message) { m.SessionID = newer.ID },
		func(m *adapter.Message) { m.InstanceID = "other-name" },
		func(m *adapter.Message) { m.UserID, m.ChatID = "other", "other" },
	} {
		other := msg
		change(&other)
		if reply, err := eng.ReplayCompletedIMPhotoReply(ctx, &other); err != nil || reply != nil {
			t.Fatalf("another scope recovered the event: reply=%v error=%v", reply, err)
		}
	}
	wrongPhysical := adapter.WithPlatformInstanceIdentity(ctx, "other-physical", "dingtalk")
	if reply, err := eng.ReplayCompletedIMPhotoReply(wrongPhysical, &msg); reply != nil || err == nil {
		t.Fatal("request bound to a different physical instance was admitted")
	}
	if _, err := store.DB().ExecContext(ctx, "UPDATE document_artifacts SET payload=? WHERE id=?", []byte("changed"), document.ID); err != nil {
		t.Fatal(err)
	}
	if reply, err := eng.ReplayCompletedIMPhotoReply(ctx, &msg); reply != nil || err == nil {
		t.Fatal("a changed document was silently dropped or delivered")
	}
}
