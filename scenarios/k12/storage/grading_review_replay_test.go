package k12storage_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// 使用真实 SQLite 证明来源更新、判定回执与冷恢复共享同一事务，不依赖模型。
func TestGradingReviewKeepsOneMistakeAndReplaysOnce(t *testing.T) {
	for _, correct := range []bool{false, true} {
		name := "wrong_stays_due"
		if correct {
			name = "correct_adds_one_review"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, db := setup(t)
			job, attempt := seedItemLedgerFacts(t, store, "review-replay")
			solveID, gradeID := successfulAssessmentInvocations(t, store, job.RecordID, attempt)
			receipt := assessmentReceipt(job.RecordID, attempt, solveID, gradeID)
			if correct {
				receipt.Status = k12.GradingAssessmentCorrect
				receipt.ResultJSON = `{"status":"correct"}`
				receipt.ResultDigest = "sha256:review-correct"
			}
			source := newMistake(t, "mingming", "prior-session", "2+2=?")
			fields, err := k12.ParseMistakeFields(source.Fields)
			if err != nil {
				t.Fatal(err)
			}
			fields.ReviewStage, fields.LastRetriedAt = 2, 100
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			source.Fields, source.Status = string(raw), k12.StatusRetried
			if _, err := store.Put(ctx, source); err != nil {
				t.Fatal(err)
			}
			wantStage, wantRetriedAt := 0, int64(100)
			if correct {
				wantStage, wantRetriedAt = 3, 200
			}
			fields.ReviewStage, fields.LastRetriedAt = wantStage, wantRetriedAt
			due := int64(800)
			effects := k12storage.GradingAssessmentEffects{Review: &k12storage.GradingReviewEffect{
				RecordID: source.RecordID, ExpectedVersion: source.Version,
				NewStatus: k12.StatusRetried, Fields: fields, DueAt: &due,
			}}
			stored, created, err := store.CommitGradingAssessmentItem(ctx, receipt, effects)
			if err != nil || !created || stored.ProjectionCreated || stored.ProjectionRecordID != source.RecordID {
				t.Fatalf("review must retain the existing source: receipt=%+v created=%v err=%v", stored, created, err)
			}
			// 新建仓库实例模拟冷恢复；过期 CAS 仍应先命中不可变回执，不再执行副作用。
			restarted := k12storage.NewStore(db, nil)
			replayed, created, err := restarted.CommitGradingAssessmentItem(ctx, receipt, effects)
			if err != nil || created || replayed.ProjectionRecordID != source.RecordID {
				t.Fatalf("replay changed source identity: receipt=%+v created=%v err=%v", replayed, created, err)
			}
			readback, err := restarted.Get(ctx, source.RecordID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := k12.ParseMistakeFields(readback.Fields)
			if err != nil {
				t.Fatal(err)
			}
			if readback.Status != k12.StatusRetried || readback.Version != 1 || got.ReviewStage != wantStage ||
				got.LastRetriedAt != wantRetriedAt || readback.DueAt == nil || *readback.DueAt != due {
				t.Fatalf("review replay advanced or lost evidence: record=%+v fields=%+v", readback, got)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM k12_mistakes WHERE agent_name='mingming'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("review created a second mistake: count=%d err=%v", count, err)
			}
		})
	}
}
