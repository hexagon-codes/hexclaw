package sqlite

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/hexagon-codes/hexclaw/storage"
)

func TestSupersededChatRequestSurvivesReopen(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSession(ctx, &storage.Session{ID: "conversation", UserID: "owner", Platform: "web"}); err != nil {
		t.Fatal(err)
	}
	for _, message := range []*storage.MessageRecord{
		{ID: "source", SessionID: "conversation", Role: "user", Content: "Question"},
		{ID: "older:assistant", SessionID: "conversation", Role: "assistant", RequestID: "older", Content: "Older answer"},
	} {
		if err := store.SaveMessage(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := store.FreezeChatRegeneration(ctx, "owner", "conversation", "source", 8)
	if err != nil {
		t.Fatal(err)
	}
	commit := storage.WithAssistantCompletion(ctx, storage.AssistantCompletion{BeforeInsert: snapshot.Replace})
	if err := store.SaveMessageIfSessionActive(commit, &storage.MessageRecord{
		ID: "newer:assistant", SessionID: "conversation", Role: "assistant", RequestID: "newer", Content: "Newer answer",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if session, err := reopened.FindSupersededChatRequest(ctx, "owner", "older"); err != nil || session != "conversation" {
		t.Fatalf("superseded receipt after reopen: session=%q err=%v", session, err)
	}
	if _, err := reopened.FindSupersededChatRequest(ctx, "another-owner", "older"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("foreign receipt must be absent: %v", err)
	}
	if _, err := reopened.GetMessage(ctx, "older:assistant"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("old reply must be absent from history: %v", err)
	}
	if reply, err := reopened.GetMessage(ctx, "newer:assistant"); err != nil || reply.Content != "Newer answer" {
		t.Fatalf("new reply after reopen: %+v err=%v", reply, err)
	}
}
