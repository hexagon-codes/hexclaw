package migrate

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRunRejectsMissingEarlierMigrationAndPreservesHistory(t *testing.T) {
	db, openErr := sql.Open("sqlite", ":memory:")
	if openErr != nil {
		t.Fatal(openErr)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := t.Context()
	migrations := []Migration{
		{Version: 1, SQL: "CREATE TABLE first(version INTEGER)"},
		{Version: 2, SQL: "CREATE TABLE missing(version INTEGER)"},
		{Version: 3, SQL: "INSERT INTO first VALUES(3)"},
	}
	if err := Run(ctx, db, []Migration{migrations[0], migrations[2]}); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, db, migrations); err == nil || !strings.Contains(err.Error(), "缺少 v2") {
		t.Fatalf("missing v2 was ignored: %v", err)
	}
	if err := Run(ctx, db, []Migration{migrations[2], migrations[1]}); err == nil ||
		!strings.Contains(err.Error(), "严格递增") {
		t.Fatalf("out-of-order declaration was accepted: %v", err)
	}
	var records int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&records); err != nil || records != 2 {
		t.Fatalf("applied migration history changed: %d, %v", records, err)
	}
}
