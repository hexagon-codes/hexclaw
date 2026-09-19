package migrate

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestBackendIdentityPersistsAcrossReopen(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := Run(t.Context(), db, []Migration{BackendIdentityV104}); err != nil {
			t.Fatal(err)
		}
		var id string
		if err := db.QueryRow("SELECT value FROM backend_metadata WHERE key = 'backend_id'").Scan(&id); err != nil {
			t.Fatal(err)
		}
		if len(id) != 32 {
			t.Fatalf("invalid identity length: %d", len(id))
		}
		return id
	}
	path := filepath.Join(t.TempDir(), "data.db")
	first := read(path)
	if read(path) != first {
		t.Fatal("backend identity changed on reopen")
	}
	if read(filepath.Join(t.TempDir(), "data.db")) == first {
		t.Fatal("different databases share identity")
	}
}
