package knowledge

import (
	"database/sql"
	"testing"
	"time"
)

func TestSemanticIndexWorkerProgressPreservesRetryBudgetAcrossRestarts(t *testing.T) {
	h := newWorkerHarness(t, "one", "two", "three", "four", "five", "six", "seven", "eight", "nine")
	projection, err := h.service.GetPolicy(h.ctx, "owner-1", "default")
	if err != nil || projection.DesiredRevision == nil || projection.DesiredRevision.JobID == nil {
		t.Fatalf("staged policy=%+v err=%v", projection, err)
	}
	jobID := *projection.DesiredRevision.JobID
	revisionID := projection.DesiredRevision.RevisionID
	now := time.Unix(1_800_490_000, 0).UTC()
	var databaseSequence int
	var databaseName, databasePath string
	if err := h.db.QueryRowContext(h.ctx, `PRAGMA database_list`).Scan(&databaseSequence, &databaseName, &databasePath); err != nil {
		t.Fatal(err)
	}
	var delays []time.Duration
	for attempt := 1; attempt <= 9; attempt++ {
		// 每轮重新打开磁盘数据库并重建 Worker，预算只能由提交事实恢复。
		reopened, err := sql.Open("sqlite", databasePath+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		repo := NewSQLiteSemanticIndexRepository(reopened)
		config := workerConfig(&now, "progress-restart", 1)
		config.RetryDelay = 10 * time.Second
		config.MaxAttempts = 8
		worker := NewSemanticIndexWorker(repo, &workerExecutorRegistry{executors: map[string]ProfileEmbeddingExecutor{
			"profile-a": &scriptedWorkerExecutor{dimension: 3, failAt: 2},
		}}, config)
		processed, runErr := worker.RunOnce(h.ctx)
		if err := reopened.Close(); err != nil {
			t.Fatal(err)
		}
		job, getErr := h.service.GetJob(h.ctx, "owner-1", jobID)
		if !processed || getErr != nil || job.Attempt != attempt {
			t.Fatalf("attempt %d: processed=%v job=%+v runErr=%v getErr=%v", attempt, processed, job, runErr, getErr)
		}
		if attempt == 9 {
			if runErr != nil || job.State != KnowledgeJobSucceeded {
				t.Fatalf("final batch did not publish: job=%+v err=%v", job, runErr)
			}
			break
		}
		if runErr == nil || job.State != KnowledgeJobRetryWait || job.NextAttemptAt == nil {
			t.Fatalf("successful progress exhausted total budget at attempt %d: job=%+v err=%v", attempt, job, runErr)
		}
		if job.ChunksDone == nil || *job.ChunksDone != int64(attempt) {
			t.Fatalf("attempt %d lost committed progress: %+v", attempt, job)
		}
		delays = append(delays, job.NextAttemptAt.Sub(now))
		now = *job.NextAttemptAt
	}
	for i, delay := range delays {
		if delay != 10*time.Second {
			t.Fatalf("progress at attempt %d retained cumulative backoff %v, want 10s", i+1, delay)
		}
	}
	var vectors, succeededBatches, totalProviderAttempts int
	if err := h.db.QueryRowContext(h.ctx, `SELECT COUNT(*) FROM kb_revision_vectors WHERE revision_id=?`, revisionID).Scan(&vectors); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRowContext(h.ctx, `SELECT COUNT(*),SUM(attempts) FROM kb_embedding_batch_manifests
		WHERE job_id=? AND state='succeeded'`, jobID).Scan(&succeededBatches, &totalProviderAttempts); err != nil {
		t.Fatal(err)
	}
	if vectors != 9 || succeededBatches != 9 || totalProviderAttempts != 17 {
		t.Fatalf("persistent vectors/batches/physical attempts=%d/%d/%d, want 9/9/17", vectors, succeededBatches, totalProviderAttempts)
	}
	final, err := h.service.GetPolicy(h.ctx, "owner-1", "default")
	if err != nil || final.ActiveRevision == nil || final.ActiveRevision.RevisionID != revisionID || final.DesiredRevision != nil {
		t.Fatalf("incomplete publication: policy=%+v err=%v", final, err)
	}
}

func TestSemanticIndexWorkerProgressThenNoProgressRemainsBounded(t *testing.T) {
	h := newWorkerHarness(t, "committed", "remaining")
	projection, err := h.service.GetPolicy(h.ctx, "owner-1", "default")
	if err != nil || projection.DesiredRevision == nil || projection.DesiredRevision.JobID == nil {
		t.Fatalf("staged policy=%+v err=%v", projection, err)
	}
	jobID := *projection.DesiredRevision.JobID
	now := time.Unix(1_800_491_000, 0).UTC()
	for attempt := 1; attempt <= 3; attempt++ {
		config := workerConfig(&now, "bounded-after-progress", 1)
		config.RetryDelay = 10 * time.Second
		config.MaxRetryDelay = 15 * time.Second
		config.MaxAttempts = 3
		executor := &scriptedWorkerExecutor{dimension: 3, failAt: 2}
		if attempt > 1 {
			executor.failAll = true
		}
		worker := NewSemanticIndexWorker(NewSQLiteSemanticIndexRepository(h.db), &workerExecutorRegistry{executors: map[string]ProfileEmbeddingExecutor{
			"profile-a": executor,
		}}, config)
		if processed, err := worker.RunOnce(h.ctx); !processed || err == nil {
			t.Fatalf("attempt %d processed=%v err=%v", attempt, processed, err)
		}
		job, err := h.service.GetJob(h.ctx, "owner-1", jobID)
		if err != nil || job.Attempt != attempt || job.ChunksDone == nil || *job.ChunksDone != 1 {
			t.Fatalf("attempt %d job=%+v err=%v", attempt, job, err)
		}
		if attempt == 3 {
			if job.State != KnowledgeJobFailed || job.NextAttemptAt != nil {
				t.Fatalf("unproductive batch did not stop: %+v", job)
			}
			if processed, err := worker.RunOnce(h.ctx); processed || err != nil {
				t.Fatalf("failed batch restarted: processed=%v err=%v", processed, err)
			}
			break
		}
		wantDelay := 10 * time.Second
		if attempt == 2 {
			wantDelay = 15 * time.Second
		}
		if job.State != KnowledgeJobRetryWait || job.NextAttemptAt == nil || job.NextAttemptAt.Sub(now) != wantDelay {
			t.Fatalf("attempt %d lost bounded backoff: %+v", attempt, job)
		}
		now = *job.NextAttemptAt
	}
}
