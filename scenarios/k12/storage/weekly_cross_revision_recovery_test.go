package k12storage_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// 直接设置隔离 SQLite 的前置历史状态，恢复仍经过真实用例和提交事务。
func weeklyCrossRevisionStorePlan(t *testing.T, d *usecase.Deps, plan k12.WeeklyPracticePlan) {
	t.Helper()
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := json.Marshal(plan.AnswerKeys)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Records.DB().Exec(`UPDATE k12_weekly_practice_plans SET revision=?,status=?,plan_json=?,answer_keys_json=? WHERE agent_name=? AND plan_id=?`, plan.Revision, plan.Status, string(raw), string(keys), plan.AgentName, plan.PlanID); err != nil {
		t.Fatal(err)
	}
}

func weeklyCrossRevisionRawPlan(t *testing.T, d *usecase.Deps, planID string) (k12.WeeklyPracticePlan, string) {
	t.Helper()
	var raw, keys string
	if err := d.Records.DB().QueryRow(`SELECT plan_json,answer_keys_json FROM k12_weekly_practice_plans WHERE plan_id=?`, planID).Scan(&raw, &keys); err != nil {
		t.Fatal(err)
	}
	var plan k12.WeeklyPracticePlan
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(keys), &plan.AnswerKeys); err != nil {
		t.Fatal(err)
	}
	return plan, raw + keys
}

func TestWeeklyPhysicalReceipts_CrossRevisionRecovery(t *testing.T) {
	for _, outcome := range []string{"success", "reinterpretation", "missing_receipts", "unknown", "nonempty", "source_changed", "version_changed", "preparing", "orphan_answer", "version_during_solve", "target_during_solve", "source_during_solve"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			d, sourcePlan := weeklyTextbookSourceFixture(t, weeklyLessonPage)
			path := filepath.Join(t.TempDir(), "cross-revision.db")
			if _, err := d.Records.DB().Exec(`VACUUM INTO ?`, path); err != nil {
				t.Fatal(err)
			}
			weeklyRecoveryReopen(t, &d, path)
			sourcePlan.Revision = 3
			sourcePlan.Tracks[1].Status = k12.WeeklyTrackFailed
			weeklyCrossRevisionStorePlan(t, &d, sourcePlan)
			generated, calls := 0, 0
			d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(context.Context, string, string, string) (usecase.SolveResult, error) {
				generated++
				return usecase.SolveResult{Solution: "## 问题\n计算：1/5×20\n## 解答\n分子乘20，再约分。\n## 答案\n4"}, nil
			})
			reinterpret := outcome == "reinterpretation" || outcome == "missing_receipts"
			d.Solver = weeklyReceiptSolver{calls: &calls, unknown: !reinterpret, legacy: outcome == "missing_receipts"}
			if _, _, err := d.PrepareWeeklyTextbookTrack(ctx, "mingming", sourcePlan.PlanID, 3, 1, "original"); err == nil {
				t.Fatal("unverified source accepted")
			}
			raw, original := weeklyRecoveryRead(t, &d, sourcePlan)
			var sourceDigest, historyBefore string
			if err := d.Records.DB().QueryRow(`SELECT request_digest FROM k12_weekly_track_refresh_commands WHERE idempotency_key='original'`).Scan(&sourceDigest); err != nil {
				t.Fatal(err)
			}
			if err := d.Records.DB().QueryRow(`SELECT response_json FROM k12_weekly_practice_plan_commands WHERE idempotency_key='source-plan'`).Scan(&historyBefore); err != nil {
				t.Fatal(err)
			}
			current, _ := weeklyCrossRevisionRawPlan(t, &d, sourcePlan.PlanID)
			current.Revision = 4
			current.AnswerKeys = make(map[string]string)
			for i := 0; i < 5; i++ {
				id := fmt.Sprintf("due-%d", i)
				current.Tracks[0].Items = append(current.Tracks[0].Items, k12.WeeklyPracticeItem{ItemID: id, PlanSection: k12.WeeklySectionDueReview, PromptMarkdown: fmt.Sprintf("复习题 %d", i), SourceRef: id})
				current.AnswerKeys[id] = fmt.Sprintf("复习答案 %d", i)
			}
			current.Tracks[2].Status = k12.WeeklyTrackReady
			current.Tracks[2].Items = []k12.WeeklyPracticeItem{{ItemID: "other-arithmetic", PlanSection: k12.WeeklySectionArithmeticWarmup, PromptMarkdown: "3+4", SourceRef: "other-arithmetic"}}
			current.AnswerKeys["other-arithmetic"] = "7"
			switch outcome {
			case "nonempty":
				current.Tracks[1].Items = []k12.WeeklyPracticeItem{{ItemID: "new-textbook", PromptMarkdown: "新教材题"}}
			case "orphan_answer":
				current.AnswerKeys["old-textbook-answer"] = "保留答案"
			case "version_changed":
				current.Revision = 5
			case "source_changed":
				if _, err := d.Records.DB().Exec(`UPDATE kb_semantic_document_bindings SET content_generation=2`); err != nil {
					t.Fatal(err)
				}
			case "preparing":
				var pending map[string]any
				if err := json.Unmarshal([]byte(raw), &pending); err != nil {
					t.Fatal(err)
				}
				pending["checkpoint"].(map[string]any)["revision"] = 4
				pending["checkpoint"].(map[string]any)["idempotency_key"] = "new-preparing"
				body, _ := json.Marshal(map[string]any{"_weekly_pending": true, "_weekly_generation": pending})
				if _, err := d.Records.DB().Exec(`INSERT INTO k12_weekly_track_refresh_commands(agent_name,plan_id,idempotency_key,request_digest,response_json,created_revision,created_at) VALUES(?,?,'new-preparing',?,?,0,1)`, current.AgentName, current.PlanID, sourceDigest, string(body)); err != nil {
					t.Fatal(err)
				}
			}
			weeklyCrossRevisionStorePlan(t, &d, current)
			_, currentRaw := weeklyCrossRevisionRawPlan(t, &d, current.PlanID)
			beforeCalls := calls
			solver := weeklyReceiptSolver{calls: &calls, fixed: true, unknown: outcome == "unknown"}
			if outcome == "version_during_solve" || outcome == "target_during_solve" || outcome == "source_during_solve" {
				solver.afterVerify = func() {
					if outcome == "source_during_solve" {
						if _, err := d.Records.DB().Exec(`UPDATE kb_semantic_document_bindings SET content_generation=2`); err != nil {
							t.Fatal(err)
						}
						return
					}
					changed, _ := weeklyCrossRevisionRawPlan(t, &d, current.PlanID)
					if outcome == "version_during_solve" {
						changed.Revision++
					} else {
						changed.Tracks[1].Items = []k12.WeeklyPracticeItem{{ItemID: "new-textbook-during-solve"}}
					}
					weeklyCrossRevisionStorePlan(t, &d, changed)
					_, currentRaw = weeklyCrossRevisionRawPlan(t, &d, current.PlanID)
				}
			}
			d.Solver = solver
			sum := sha256.Sum256([]byte(raw))
			recovery := usecase.WeeklyTextbookRecoveryRequest{SourceCommandKey: "original", SourcePlanRevision: 3, ExpectedPlanRevision: 4, CheckpointSHA256: hex.EncodeToString(sum[:]), ItemIndex: 0, IdempotencyKey: "cross-recovery", AcceptDuplicateExecution: true}
			reparse := usecase.WeeklyTextbookReinterpretationRequest{SourceCommandKey: "original", SourcePlanRevision: 3, ExpectedPlanRevision: 4, CheckpointSHA256: recovery.CheckpointSHA256, ItemIndex: 0, IdempotencyKey: "cross-reparse"}
			invoke := func() (k12.WeeklyPracticePlan, bool, error) {
				if reinterpret {
					return d.ReinterpretWeeklyTextbookTrack(ctx, "mingming", sourcePlan.PlanID, reparse)
				}
				return d.RecoverWeeklyTextbookTrack(ctx, "mingming", sourcePlan.PlanID, recovery)
			}
			result, replay, err := invoke()
			success := outcome == "success" || outcome == "reinterpretation"
			if success {
				if err != nil || replay || result.Revision != 5 || result.Tracks[1].Status != k12.WeeklyTrackReady || len(result.Tracks[1].Items) != 1 {
					t.Fatalf("cross revision result: %+v replay=%v err=%v", result, replay, err)
				}
			} else if err == nil {
				t.Fatal("ineligible cross revision accepted")
			}
			wantCalls := beforeCalls
			if outcome == "success" || outcome == "unknown" || outcome == "version_during_solve" || outcome == "target_during_solve" || outcome == "source_during_solve" {
				wantCalls++
			}
			if calls != wantCalls || generated != 1 {
				t.Fatalf("unexpected external calls: calls %d want %d; generated %d", calls, wantCalls, generated)
			}
			afterRaw, after := weeklyRecoveryRead(t, &d, sourcePlan)
			if !reflect.DeepEqual(original["generate"], after["generate"]) {
				t.Fatal("original generation changed")
			}
			if !reinterpret && !reflect.DeepEqual(original["solve"], after["solve"]) {
				t.Fatal("original unknown or physical receipts changed")
			}
			stored, storedRaw := weeklyCrossRevisionRawPlan(t, &d, current.PlanID)
			if success {
				if !reflect.DeepEqual(stored.Tracks[0], current.Tracks[0]) || !reflect.DeepEqual(stored.Tracks[2], current.Tracks[2]) || len(stored.AnswerKeys) != len(current.AnswerKeys)+1 {
					t.Fatal("unrelated tracks or answer keys changed")
				}
				for id, answer := range current.AnswerKeys {
					if stored.AnswerKeys[id] != answer {
						t.Fatalf("answer lost: %s", id)
					}
				}
			} else if storedRaw != currentRaw {
				t.Fatal("failed recovery changed current plan")
			}
			if wantCalls == beforeCalls && !success && afterRaw != raw {
				t.Fatal("rejected authorization changed checkpoint")
			}
			if outcome == "version_during_solve" || outcome == "target_during_solve" || outcome == "source_during_solve" {
				call := after["solve_recovery_attempts"].([]any)[0].(map[string]any)["call"].(map[string]any)
				if call["status"] != "succeeded" || len(call["physical_calls"].([]any)) != 2 {
					t.Fatal("commit conflict discarded successful receipts")
				}
			}
			var finalDigest, historyAfter string
			if err := d.Records.DB().QueryRow(`SELECT request_digest FROM k12_weekly_track_refresh_commands WHERE idempotency_key='original'`).Scan(&finalDigest); err != nil || finalDigest != sourceDigest {
				t.Fatalf("source command digest changed: %v", err)
			}
			if err := d.Records.DB().QueryRow(`SELECT response_json FROM k12_weekly_practice_plan_commands WHERE idempotency_key='source-plan'`).Scan(&historyAfter); err != nil || historyAfter != historyBefore {
				t.Fatalf("historical response changed: %v", err)
			}
			if success || outcome == "unknown" || outcome == "version_during_solve" {
				if err := d.Records.DB().Close(); err != nil {
					t.Fatal(err)
				}
				weeklyRecoveryReopen(t, &d, path)
				_, replay, err = invoke()
				if success && (err != nil || !replay) {
					t.Fatalf("cold replay: %v %v", replay, err)
				}
				if !success && err == nil {
					t.Fatal("unknown or conflicting cold replay succeeded")
				}
				if calls != wantCalls || generated != 1 {
					t.Fatal("cold replay resent")
				}
				finalRaw, _ := weeklyRecoveryRead(t, &d, sourcePlan)
				if finalRaw != afterRaw {
					t.Fatal("cold replay changed receipts")
				}
				if success && !reinterpret {
					recovery.ExpectedPlanRevision++
					if _, _, err := invoke(); !errors.Is(err, records.ErrVersionConflict) || calls != wantCalls {
						t.Fatalf("same authorization accepted changed revision: %v", err)
					}
				}
			}
		})
	}
}
