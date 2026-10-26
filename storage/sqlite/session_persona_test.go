package sqlite

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hexagon-codes/hexclaw/storage"
)

func TestSessionPersonaPersistsAndPreservesMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persona.db")
	store, err := newMigratedTestStore(t, path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := store.CreateSession(ctx, &storage.Session{ID: "one", UserID: "owner", Title: "first", Meta: `{"project":"retained","nested":{"x":2}}`}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSessionPersona(ctx, "one", "role-one"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	assertMeta := func(want string) {
		t.Helper()
		session, err := store.GetSession(ctx, "one")
		if err != nil {
			t.Fatal(err)
		}
		var meta map[string]any
		if err := json.Unmarshal([]byte(session.Meta), &meta); err != nil {
			t.Fatal(err)
		}
		got, _ := meta["persona_skill"].(string)
		if got != want || meta["project"] != "retained" || meta["nested"].(map[string]any)["x"] != float64(2) {
			t.Fatalf("metadata=%v, want persona %q and original project/nested", meta, want)
		}
	}
	assertMeta("role-one")
	stale, err := store.GetSession(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSessionPersona(ctx, "one", "role-two"); err != nil {
		t.Fatal(err)
	}
	stale.Title = "renamed"
	if err := store.UpdateSession(ctx, stale); err != nil {
		t.Fatal(err)
	}
	assertMeta("role-two")
	if err := store.SetSessionPersona(ctx, "one", ""); err != nil {
		t.Fatal(err)
	}
	assertMeta("")
	if err := store.SetSessionPersona(ctx, "missing", "role"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing session: %v", err)
	}
}
