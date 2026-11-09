package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/hexagon-codes/hexclaw/storage"
)

// 原子运行写入保护删除会话，同时保持归档会话、完成器事务及普通历史导入原合同。
func TestActiveSessionMessageWriteKeepsCompletionAndLegacyContracts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.CreateSession(ctx, &storage.Session{ID: "active-write", UserID: "owner", Platform: "web", Title: "original"}); err != nil {
		t.Fatal(err)
	}
	commits := 0
	completion := storage.WithAssistantCompletion(ctx, storage.AssistantCompletion{Commit: func(inner context.Context, tx *sql.Tx, _ *storage.MessageRecord) (bool, error) {
		commits++
		_, err := tx.ExecContext(inner, `UPDATE sessions SET title='completed' WHERE id='active-write'`)
		return false, err
	}})
	if err := store.SaveMessageIfSessionActive(completion, &storage.MessageRecord{ID: "active-result", SessionID: "active-write", Role: "assistant", Content: "observed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE sessions SET status=0 WHERE id='active-write'`); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessageIfSessionActive(completion, &storage.MessageRecord{ID: "archived-result", SessionID: "active-write", Role: "assistant", Content: "archived"}); err != nil {
		t.Fatalf("visible archived session was blocked: %v", err)
	}
	if err := store.DeleteSession(ctx, "active-write"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessageIfSessionActive(completion, &storage.MessageRecord{ID: "deleted-result", SessionID: "active-write", Role: "assistant", Content: "late"}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("deleted write=%v", err)
	}
	if commits != 2 {
		t.Fatalf("completion ran for unavailable session: %d", commits)
	}
	if _, err := store.GetMessage(ctx, "deleted-result"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("deleted result persisted: %v", err)
	}
	if err := store.SaveMessage(ctx, &storage.MessageRecord{ID: "legacy-import", SessionID: "active-write", Role: "assistant", Content: "historical"}); err != nil {
		t.Fatalf("ordinary import contract changed: %v", err)
	}
	if err := store.UpdateMessageMetadataIfSessionActive(ctx, "legacy-import", `{"late":true}`); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("deleted metadata write=%v", err)
	}
	if err := store.UpdateMessageMetadata(ctx, "legacy-import", `{"legacy":true}`); err != nil {
		t.Fatalf("ordinary metadata import contract changed: %v", err)
	}
	legacy, err := store.GetMessage(ctx, "legacy-import")
	if err != nil || legacy.Metadata != `{"legacy":true}` {
		t.Fatalf("legacy metadata unavailable: %#v %v", legacy, err)
	}
	if err := store.CreateSession(ctx, &storage.Session{ID: "rollback-write", UserID: "owner", Platform: "web", Title: "original"}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("completion failed")
	rollback := storage.WithAssistantCompletion(ctx, storage.AssistantCompletion{Commit: func(inner context.Context, tx *sql.Tx, _ *storage.MessageRecord) (bool, error) {
		if _, err := tx.ExecContext(inner, `UPDATE sessions SET title='must-rollback' WHERE id='rollback-write'`); err != nil {
			return false, err
		}
		return false, failure
	}})
	if err := store.SaveMessageIfSessionActive(rollback, &storage.MessageRecord{ID: "rollback-result", SessionID: "rollback-write", Role: "assistant", Content: "late"}); !errors.Is(err, failure) {
		t.Fatalf("completion error lost: %v", err)
	}
	sess, err := store.GetSession(ctx, "rollback-write")
	if err != nil || sess.Title != "original" {
		t.Fatalf("completion side effect escaped rollback: %#v %v", sess, err)
	}
	if _, err := store.GetMessage(ctx, "rollback-result"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("message escaped rollback: %v", err)
	}
	if err := store.SaveMessage(ctx, &storage.MessageRecord{ID: "metadata-visible", SessionID: "rollback-write", Role: "assistant", Content: "original"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateMessageMetadataIfSessionActive(ctx, "metadata-visible", `{"snapshot":true}`); err != nil {
		t.Fatal(err)
	}
	visible, err := store.GetMessage(ctx, "metadata-visible")
	if err != nil || visible.Content != "original" || visible.Metadata != `{"snapshot":true}` {
		t.Fatalf("visible metadata update changed message: %#v %v", visible, err)
	}
}

// A soft-deleted session (status = -1) must be invisible to reads and forks,
// matching ListSessions which already filters status >= 0. Otherwise a deleted
// session stays fully readable via GetSession and can be resurrected via
// ForkSession.
func TestBug_DeletedSessionNotReadableOrForkable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.CreateSession(ctx, &storage.Session{
		ID: "s-del", UserID: "u1", Platform: "web", Title: "t",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.SaveMessage(ctx, &storage.MessageRecord{
		ID: "m1", SessionID: "s-del", Role: "user", Content: "hi", Metadata: "{}",
	}); err != nil {
		t.Fatalf("save message: %v", err)
	}
	if err := store.DeleteSession(ctx, "s-del"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := store.GetSession(ctx, "s-del"); err != storage.ErrNotFound {
		t.Fatalf("GetSession on a deleted session must return ErrNotFound, got %v", err)
	}
	if _, err := store.ForkSession(ctx, "s-del", "m1", "u1"); err == nil {
		t.Fatalf("ForkSession on a deleted session must fail, got nil (resurrection)")
	}
}
