package sqlite

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMaintenanceSkipsVacuumForLivePages(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "data.db")
	store, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	mustExec2(t, store.db, "CREATE TABLE retained(value TEXT)")
	content := strings.Repeat("retained content ", 8192)
	mustExec2(t, store.db, "INSERT INTO retained VALUES(?)", content)
	mustExec2(t, store.db, "PRAGMA wal_checkpoint(TRUNCATE)")
	var freePages, schemaBefore int64
	if err := store.db.QueryRow("PRAGMA freelist_count").Scan(&freePages); err != nil {
		t.Fatal(err)
	}
	if freePages != 0 {
		t.Fatalf("fixture unexpectedly contains reclaimable pages: %d", freePages)
	}
	if err := store.db.QueryRow("PRAGMA schema_version").Scan(&schemaBefore); err != nil {
		t.Fatal(err)
	}
	threshold := vacuumThreshold
	t.Cleanup(func() { vacuumThreshold = threshold })
	vacuumThreshold = 1
	store.runMaintenance(dbPath)
	var schemaAfter int64
	if err := store.db.QueryRow("PRAGMA schema_version").Scan(&schemaAfter); err != nil {
		t.Fatal(err)
	}
	// 完整VACUUM会更新SQLite的schema版本；只有有效数据时维护不应重建整库。
	if schemaAfter != schemaBefore {
		t.Fatalf("maintenance rebuilt a database with no reclaimable pages: schema %d -> %d", schemaBefore, schemaAfter)
	}
	var actual string
	if err := store.db.QueryRow("SELECT value FROM retained").Scan(&actual); err != nil {
		t.Fatal(err)
	}
	if actual != content {
		t.Fatal("maintenance changed retained content")
	}
}
