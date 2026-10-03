package migrate

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestKnowledgeEmbeddingProgressMigrationPreservesExistingAttemptsAndReceipts(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "progress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE kb_knowledge_jobs (
		job_id TEXT PRIMARY KEY,attempt INTEGER NOT NULL,state TEXT NOT NULL);
		CREATE TABLE kb_embedding_batch_manifests (batch_id TEXT PRIMARY KEY,job_id TEXT,payload TEXT);
		INSERT INTO kb_knowledge_jobs VALUES('old',7,'retry_wait');
		INSERT INTO kb_embedding_batch_manifests VALUES('receipt','old','original provider receipt');`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Run(ctx, db, []Migration{KnowledgeEmbeddingProgressV116}); err != nil {
			t.Fatal(err)
		}
	}
	var attempt, progress int
	var state, payload string
	if err := db.QueryRowContext(ctx, `SELECT attempt,last_progress_attempt,state FROM kb_knowledge_jobs WHERE job_id='old'`).Scan(&attempt, &progress, &state); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT payload FROM kb_embedding_batch_manifests WHERE batch_id='receipt'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if attempt != 7 || progress != 0 || state != "retry_wait" || payload != "original provider receipt" {
		t.Fatalf("migration changed old facts: attempt=%d progress=%d state=%s receipt=%s", attempt, progress, state, payload)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO kb_knowledge_jobs(job_id,attempt,state) VALUES('new',0,'queued')`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT last_progress_attempt FROM kb_knowledge_jobs WHERE job_id='new'`).Scan(&progress); err != nil || progress != 0 {
		t.Fatalf("new job progress=%d err=%v", progress, err)
	}
	t.Run("empty database full migrations", func(t *testing.T) {
		fresh, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fresh.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer fresh.Close()
		for i := 0; i < 2; i++ {
			if err := Run(ctx, fresh, All); err != nil {
				t.Fatalf("full migration %d: %v", i, err)
			}
		}
		rows, err := fresh.QueryContext(ctx, `SELECT last_progress_attempt FROM kb_knowledge_jobs LIMIT 0`)
		if err != nil {
			t.Fatalf("fresh database missing retry progress: %v", err)
		}
		rows.Close()
	})
}
