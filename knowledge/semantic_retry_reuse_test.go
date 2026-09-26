package knowledge

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestSemanticIndexExplicitRestageReusesOnlyMatchingCommittedVectors(t *testing.T) {
	for _, tt := range []struct {
		name       string
		change     string
		wantReused int
		wantTexts  []string
	}{
		{"same profile and content", "", 2, []string{"gamma"}},
		{"new document generation", "generation", 1, []string{"alpha", "gamma"}},
		{"changed chunk content", "content", 1, []string{"alpha changed", "gamma"}},
		{"changed profile configuration", "profile", 0, []string{"alpha", "beta", "gamma"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newSemanticHarness(t)
			boot, err := h.service.EnsureDefaultPolicy(h.ctx, "owner-1", "default")
			if err != nil || boot.ActiveRevisionID == nil {
				t.Fatalf("bootstrap: result=%+v err=%v", boot, err)
			}
			for _, doc := range []string{"doc-a", "doc-b"} {
				if _, err := h.db.ExecContext(h.ctx, `INSERT INTO kb_documents(id,title,content,deleted)
					VALUES(?,?,?,0)`, doc, doc, "body"); err != nil {
					t.Fatal(err)
				}
			}
			for i, content := range []string{"alpha", "beta", "gamma"} {
				docID := "doc-b"
				if i == 0 {
					docID = "doc-a"
				}
				if _, err := h.db.ExecContext(h.ctx, `INSERT INTO kb_chunks(id,doc_id,content,chunk_index)
					VALUES(?,?,?,?)`, content, docID, content, i); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := h.repo.BindLegacyDefaultCorpus(h.ctx, "owner-1", "default"); err != nil {
				t.Fatal(err)
			}
			// 先取消旧模型的增量任务，使本例只执行显式重建任务。
			rows, err := h.db.QueryContext(h.ctx, `SELECT job_id FROM kb_knowledge_jobs WHERE state='queued'`)
			if err != nil {
				t.Fatal(err)
			}
			var catchupJobs []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				catchupJobs = append(catchupJobs, id)
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
			for _, id := range catchupJobs {
				if _, err := h.service.CancelJob(h.ctx, "owner-1", id); err != nil {
					t.Fatal(err)
				}
			}
			selection := EmbeddingSelection{Kind: EmbeddingSelectionProfile, ProfileID: "profile-b"}
			staged, err := h.service.ApplyPolicy(h.ctx, "owner-1", "default", boot.PolicyVersion, selection)
			if err != nil || staged.JobID == nil || staged.DesiredRevisionID == nil {
				t.Fatalf("stage: result=%+v err=%v", staged, err)
			}
			now := time.Unix(1_800_490_000, 0).UTC()
			config := workerConfig(&now, "partial-build", 2)
			config.MaxAttempts = 1
			firstExecutor := &scriptedWorkerExecutor{dimension: 4, failAt: 2}
			worker := NewSemanticIndexWorker(h.repo, &workerExecutorRegistry{executors: map[string]ProfileEmbeddingExecutor{
				"profile-b": firstExecutor,
			}}, config)
			if processed, err := worker.RunOnce(h.ctx); !processed || err == nil {
				t.Fatalf("partial build should fail after first batch: processed=%v err=%v", processed, err)
			}
			failed, err := h.service.GetPolicy(h.ctx, "owner-1", "default")
			if err != nil || failed.DesiredRevision == nil || failed.DesiredRevision.State != VectorIndexFailed {
				t.Fatalf("failed projection=%+v err=%v", failed, err)
			}
			oldReceipts := semanticRetryReceiptSnapshot(t, h, *staged.JobID)
			switch tt.change {
			case "generation":
				if _, err := h.db.ExecContext(h.ctx, `INSERT INTO kb_semantic_document_generations
					(owner_id,corpus_uid,document_id,content_generation,created_at)
					SELECT owner_id,corpus_uid,document_id,2,created_at
					FROM kb_semantic_document_bindings WHERE document_id='doc-a'`); err != nil {
					t.Fatal(err)
				}
				if _, err := h.db.ExecContext(h.ctx, `UPDATE kb_semantic_document_bindings
					SET content_generation=2 WHERE document_id='doc-a'`); err != nil {
					t.Fatal(err)
				}
			case "content":
				if _, err := h.db.ExecContext(h.ctx, `UPDATE kb_chunks SET content='alpha changed' WHERE id='alpha'`); err != nil {
					t.Fatal(err)
				}
			case "profile":
				profile := h.resolver.profiles["profile-b"]
				profile.ProfileConfigHash = "hash-b-new"
				h.resolver.profiles["profile-b"] = profile
			}
			if tt.change == "generation" || tt.change == "content" {
				if _, err := h.repo.RecordCorpusContentChange(h.ctx, "owner-1", "default"); err != nil {
					t.Fatal(err)
				}
			}
			retried, err := h.service.ApplyPolicy(h.ctx, "owner-1", "default", failed.PolicyVersion, selection)
			if err != nil || retried.DesiredRevisionID == nil || retried.JobID == nil {
				t.Fatalf("restage: result=%+v err=%v", retried, err)
			}
			var vectorCount, boundSnapshots, revisionCount, documentCount int
			if err := h.db.QueryRowContext(h.ctx, `SELECT COUNT(*),COALESCE(SUM(v.profile_snapshot_id=r.profile_snapshot_id),0)
				FROM kb_revision_vectors v JOIN kb_index_revisions r ON r.revision_id=v.revision_id
				WHERE v.revision_id=?`, *retried.DesiredRevisionID).Scan(&vectorCount, &boundSnapshots); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRowContext(h.ctx, `SELECT embedded_chunks FROM kb_index_revisions WHERE revision_id=?`,
				*retried.DesiredRevisionID).Scan(&revisionCount); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRowContext(h.ctx, `SELECT SUM(embedded_chunks) FROM kb_revision_documents WHERE revision_id=?`,
				*retried.DesiredRevisionID).Scan(&documentCount); err != nil {
				t.Fatal(err)
			}
			if vectorCount != tt.wantReused || boundSnapshots != tt.wantReused ||
				revisionCount != tt.wantReused || documentCount != tt.wantReused {
				t.Fatalf("reused vectors/snapshots/revision/documents=%d/%d/%d/%d want %d",
					vectorCount, boundSnapshots, revisionCount, documentCount, tt.wantReused)
			}
			if tt.change == "" {
				var readyState, partialState string
				if err := h.db.QueryRowContext(h.ctx, `SELECT vector_state FROM kb_revision_documents
					WHERE revision_id=? AND document_id='doc-a'`, *retried.DesiredRevisionID).Scan(&readyState); err != nil {
					t.Fatal(err)
				}
				if err := h.db.QueryRowContext(h.ctx, `SELECT vector_state FROM kb_revision_documents
					WHERE revision_id=? AND document_id='doc-b'`, *retried.DesiredRevisionID).Scan(&partialState); err != nil {
					t.Fatal(err)
				}
				if readyState != "ready" || partialState != "building" {
					t.Fatalf("reused document states=%s/%s want ready/building", readyState, partialState)
				}
			}
			before, err := h.service.GetPolicy(h.ctx, "owner-1", "default")
			if err != nil || before.ActiveRevision == nil || before.ActiveRevision.RevisionID != *boot.ActiveRevisionID {
				t.Fatalf("restage changed serving revision: projection=%+v err=%v", before, err)
			}
			var newManifests int
			if err := h.db.QueryRowContext(h.ctx, `SELECT COUNT(*) FROM kb_embedding_batch_manifests WHERE job_id=?`,
				*retried.JobID).Scan(&newManifests); err != nil || newManifests != 0 {
				t.Fatalf("restage invented provider manifests=%d err=%v", newManifests, err)
			}
			secondExecutor := &scriptedWorkerExecutor{dimension: 4}
			worker = NewSemanticIndexWorker(h.repo, &workerExecutorRegistry{executors: map[string]ProfileEmbeddingExecutor{
				"profile-b": secondExecutor,
			}}, workerConfig(&now, "remaining-build", 8))
			if processed, err := worker.RunOnce(h.ctx); !processed || err != nil {
				t.Fatalf("remaining build: processed=%v err=%v", processed, err)
			}
			if !reflect.DeepEqual(secondExecutor.batches, [][]string{tt.wantTexts}) {
				t.Fatalf("provider received=%v want only remaining=%v", secondExecutor.batches, tt.wantTexts)
			}
			after, err := h.service.GetPolicy(h.ctx, "owner-1", "default")
			if err != nil || after.ActiveRevision == nil || after.ActiveRevision.RevisionID != *retried.DesiredRevisionID ||
				after.DesiredRevision != nil || after.ActiveRevision.ChunksDone == nil || *after.ActiveRevision.ChunksDone != 3 {
				t.Fatalf("complete revision not published: projection=%+v err=%v", after, err)
			}
			if got := semanticRetryReceiptSnapshot(t, h, *staged.JobID); got != oldReceipts {
				t.Fatal("restage or publication rewrote old failed job/batch receipts")
			}
		})
	}
}

// 整行快照验证旧回执的持久事实保持，不仅比较状态字段。
func semanticRetryReceiptSnapshot(t *testing.T, h *semanticHarness, jobID string) string {
	t.Helper()
	var records [][][]any
	for _, query := range []string{
		`SELECT * FROM kb_knowledge_jobs WHERE job_id=?`,
		`SELECT * FROM kb_embedding_batch_manifests WHERE job_id=? ORDER BY batch_id`,
		`SELECT bc.* FROM kb_embedding_batch_chunks bc JOIN kb_embedding_batch_manifests bm
		 ON bm.batch_id=bc.batch_id WHERE bm.job_id=? ORDER BY bc.batch_id,bc.ordinal`,
	} {
		rows, err := h.db.QueryContext(h.ctx, query, jobID)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var table [][]any
		for rows.Next() {
			values, destinations := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				t.Fatal(err)
			}
			table = append(table, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		records = append(records, table)
	}
	encoded, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
