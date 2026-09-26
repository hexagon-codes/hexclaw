package k12storage_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenario"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type weeklyRecoverySolver struct {
	calls   *int
	unknown bool
	after   func()
	result  *usecase.SolveResult
}

func (s weeklyRecoverySolver) Solve(_ context.Context, question, _, _ string) (usecase.SolveResult, error) {
	*s.calls++
	if s.after != nil {
		s.after()
	}
	if s.unknown {
		return usecase.SolveResult{}, context.DeadlineExceeded
	}
	if s.result != nil {
		return *s.result, nil
	}
	return usecase.SolveResult{Solution: "## 答案\n4", Evidence: usecase.SolveEvidence{Verdict: usecase.VerdictAgree, EvidenceType: usecase.EvidenceNumericExec}}, nil
}

func weeklyRecoveryReopen(t *testing.T, d *usecase.Deps, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	reg := scenario.NewRegistry()
	if err := reg.Assemble(k12.Pack(k12.NewCurriculumStub())); err != nil {
		t.Fatal(err)
	}
	d.Records = k12storage.NewStore(db, reg.Records)
	d.WeeklyCandidates = usecase.NewWeeklyPracticeCandidateSource(d)
}

func weeklyRecoveryRead(t *testing.T, d *usecase.Deps, plan k12.WeeklyPracticePlan) (string, map[string]any) {
	t.Helper()
	raw, err := d.Records.GetWeeklyCandidateCheckpoint(context.Background(), usecase.WeeklyCandidateCheckpointRef{AgentName: "mingming", Kind: "refresh", PlanID: plan.PlanID, Revision: plan.Revision, IdempotencyKey: "original"})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	item := body["generation"].(map[string]any)["items"].([]any)[0].(map[string]any)
	return raw, item
}

func TestWeeklyRecovery_PreservedReceiptsReplayAndUnknownRestart(t *testing.T) {
	for _, outcome := range []string{"success", "unknown", "sent_restart", "source_changed", "source_changed_during_solve"} {
		t.Run(outcome, func(t *testing.T) {
			d, plan := weeklyTextbookSourceFixture(t, weeklyLessonPage)
			path := filepath.Join(t.TempDir(), "weekly.db")
			if _, err := d.Records.DB().Exec(`VACUUM INTO ?`, path); err != nil {
				t.Fatal(err)
			}
			weeklyRecoveryReopen(t, &d, path)
			generated, solved := 0, 0
			d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(context.Context, string, string, string) (usecase.SolveResult, error) {
				generated++
				return usecase.SolveResult{Solution: "## 问题\n计算：1/5×20\n## 解答\n分子乘20，再约分。\n## 答案\n4"}, nil
			})
			d.Solver = weeklyRecoverySolver{calls: &solved, unknown: true}
			if _, _, err := d.PrepareWeeklyTextbookTrack(context.Background(), "mingming", plan.PlanID, plan.Revision, 1, "original"); err == nil {
				t.Fatal("unknown accepted")
			}
			raw, original := weeklyRecoveryRead(t, &d, plan)
			if original["solve"].(map[string]any)["status"] != "outcome_unknown" || generated != 1 || solved != 1 {
				t.Fatalf("original receipts: %v %d/%d", original, generated, solved)
			}
			sum := sha256.Sum256([]byte(raw))
			req := usecase.WeeklyTextbookRecoveryRequest{SourceCommandKey: "original", PlanRevision: plan.Revision, CheckpointSHA256: hex.EncodeToString(sum[:]), ItemIndex: 0, IdempotencyKey: "recover-once", AcceptDuplicateExecution: true}
			d.Solver = weeklyRecoverySolver{calls: &solved, unknown: outcome == "unknown"}
			if outcome == "source_changed" {
				if _, err := d.Records.DB().Exec(`UPDATE kb_semantic_document_bindings SET content_generation=2`); err != nil {
					t.Fatal(err)
				}
			}
			if outcome == "sent_restart" {
				// 模拟发送后进程丢失结果，外发前原子持久化的 sent 必须阻止冷重放。
				d.Solver = weeklyRecoverySolver{calls: &solved, unknown: true}
			}
			if outcome == "source_changed_during_solve" {
				d.Solver = weeklyRecoverySolver{calls: &solved, after: func() {
					if _, err := d.Records.DB().Exec(`UPDATE kb_semantic_document_bindings SET content_generation=2`); err != nil {
						t.Fatal(err)
					}
				}}
			}

			result, replay, err := d.RecoverWeeklyTextbookTrack(context.Background(), "mingming", plan.PlanID, req)
			expectedCalls := 2
			if outcome == "source_changed" {
				expectedCalls = 1
			}
			if outcome == "success" {
				if err != nil || replay || result.Tracks[1].Status != k12.WeeklyTrackReady || len(result.Tracks[1].Items) != 1 || result.Tracks[1].Items[0].PromptMarkdown != "计算：1/5×20" {
					t.Fatalf("original question not ready: %+v %v", result, err)
				}
			} else if err == nil {
				t.Fatal("unverified recovery accepted")
			}
			if generated != 1 || solved != expectedCalls {
				t.Fatalf("unexpected calls %d/%d", generated, solved)
			}
			afterRaw, after := weeklyRecoveryRead(t, &d, plan)
			if !reflect.DeepEqual(original["generate"], after["generate"]) || !reflect.DeepEqual(original["solve"], after["solve"]) {
				t.Fatal("old receipts overwritten")
			}
			if outcome == "source_changed" {
				return
			}
			if outcome == "sent_restart" {
				var body map[string]any
				if err := json.Unmarshal([]byte(afterRaw), &body); err != nil {
					t.Fatal(err)
				}
				item := body["generation"].(map[string]any)["items"].([]any)[0].(map[string]any)
				item["solve_recovery_attempts"].([]any)[0].(map[string]any)["call"] = map[string]any{"status": "sent"}
				next, _ := json.Marshal(body)
				ref := usecase.WeeklyCandidateCheckpointRef{AgentName: "mingming", Kind: "refresh", PlanID: plan.PlanID, Revision: plan.Revision, IdempotencyKey: "original"}
				if err := d.Records.SaveWeeklyCandidateCheckpoint(context.Background(), ref, afterRaw, string(next)); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.Records.DB().Close(); err != nil {
				t.Fatal(err)
			}
			weeklyRecoveryReopen(t, &d, path)
			beforeReplay, _ := weeklyRecoveryRead(t, &d, plan)
			_, replay, err = d.RecoverWeeklyTextbookTrack(context.Background(), "mingming", plan.PlanID, req)
			if !replay || (outcome == "success" && err != nil) || (outcome != "success" && err == nil) || generated != 1 || solved != 2 {
				t.Fatalf("cold replay: %v %v calls %d/%d", replay, err, generated, solved)
			}
			afterReplay, _ := weeklyRecoveryRead(t, &d, plan)
			if beforeReplay != afterReplay {
				t.Fatal("replay changed receipts")
			}
			req.ItemIndex = 1
			if _, _, err := d.RecoverWeeklyTextbookTrack(context.Background(), "mingming", plan.PlanID, req); err == nil {
				t.Fatal("altered authorization accepted")
			}
		})
	}
}

func TestWeeklyRecovery_InsufficientEvidenceEligibilityAndReplay(t *testing.T) {
	for _, outcome := range []string{"insufficient", "missing_response", "empty_response", "verified", "out_of_scope"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			d, plan := weeklyTextbookSourceFixture(t, weeklyLessonPage)
			path := filepath.Join(t.TempDir(), "weekly.db")
			if _, err := d.Records.DB().Exec(`VACUUM INTO ?`, path); err != nil {
				t.Fatal(err)
			}
			weeklyRecoveryReopen(t, &d, path)
			generated, solved := 0, 0
			d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(context.Context, string, string, string) (usecase.SolveResult, error) {
				generated++
				return usecase.SolveResult{Solution: "## 问题\n计算：1/5×20\n## 解答\n分子乘20，再约分。\n## 答案\n4"}, nil
			})
			d.Solver = weeklyRecoverySolver{calls: &solved, unknown: true}
			if _, _, err := d.PrepareWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, plan.Revision, 1, "original"); err == nil {
				t.Fatal("unknown accepted")
			}
			raw, original := weeklyRecoveryRead(t, &d, plan)
			sum := sha256.Sum256([]byte(raw))
			req := usecase.WeeklyTextbookRecoveryRequest{SourceCommandKey: "original", PlanRevision: plan.Revision, CheckpointSHA256: hex.EncodeToString(sum[:]), ItemIndex: 0, IdempotencyKey: "recover-insufficient", AcceptDuplicateExecution: true}
			d.Solver = weeklyRecoverySolver{calls: &solved, result: &usecase.SolveResult{Solution: "## 答案\n4", Evidence: usecase.SolveEvidence{Verdict: usecase.VerdictAgree, EvidenceType: usecase.EvidenceHeuristic}}}
			if _, _, err := d.RecoverWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, req); err == nil {
				t.Fatal("insufficient result accepted")
			}
			raw, prior := weeklyRecoveryRead(t, &d, plan)
			// 无法从正常成功链形成的损坏／已验证快照用于核对恢复资格。
			if outcome != "insufficient" {
				var body map[string]any
				if err := json.Unmarshal([]byte(raw), &body); err != nil {
					t.Fatal(err)
				}
				item := body["generation"].(map[string]any)["items"].([]any)[0].(map[string]any)
				call := item["solve_recovery_attempts"].([]any)[0].(map[string]any)["call"].(map[string]any)
				switch outcome {
				case "missing_response":
					delete(call, "result")
				case "empty_response":
					call["result"] = usecase.SolveResult{}
				case "verified":
					call["result"] = usecase.SolveResult{Solution: "## 答案\n4", Evidence: usecase.SolveEvidence{Verdict: usecase.VerdictAgree, EvidenceType: usecase.EvidenceNumericExec}}
				case "out_of_scope":
					call["result"] = usecase.SolveResult{Solution: "## 答案\n4", Evidence: usecase.SolveEvidence{Verdict: usecase.VerdictOutOfScope}}
				}
				next, _ := json.Marshal(body)
				ref := usecase.WeeklyCandidateCheckpointRef{AgentName: "mingming", Kind: "refresh", PlanID: plan.PlanID, Revision: plan.Revision, IdempotencyKey: "original"}
				if err := d.Records.SaveWeeklyCandidateCheckpoint(ctx, ref, raw, string(next)); err != nil {
					t.Fatal(err)
				}
				raw, prior = weeklyRecoveryRead(t, &d, plan)
			}
			sum = sha256.Sum256([]byte(raw))
			req.CheckpointSHA256, req.IdempotencyKey = hex.EncodeToString(sum[:]), "recover-evidence-once"
			d.Solver = weeklyRecoverySolver{calls: &solved}
			result, replay, err := d.RecoverWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, req)
			if outcome != "insufficient" {
				after, _ := weeklyRecoveryRead(t, &d, plan)
				if err == nil || solved != 2 || generated != 1 || raw != after {
					t.Fatalf("ineligible recovery sent or mutated: %v calls %d/%d", err, generated, solved)
				}
				return
			}
			if err != nil || replay || result.Tracks[1].Status != k12.WeeklyTrackReady || len(result.Tracks[1].Items) != 1 {
				t.Fatalf("recovery not ready: %+v %v", result, err)
			}
			_, after := weeklyRecoveryRead(t, &d, plan)
			attempts := after["solve_recovery_attempts"].([]any)
			if !reflect.DeepEqual(original["generate"], after["generate"]) || !reflect.DeepEqual(original["solve"], after["solve"]) || len(attempts) != 2 || !reflect.DeepEqual(prior["solve_recovery_attempts"].([]any)[0], attempts[0]) {
				t.Fatal("prior receipts changed")
			}
			if err := d.Records.DB().Close(); err != nil {
				t.Fatal(err)
			}
			weeklyRecoveryReopen(t, &d, path)
			before, _ := weeklyRecoveryRead(t, &d, plan)
			_, replay, err = d.RecoverWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, req)
			afterRaw, _ := weeklyRecoveryRead(t, &d, plan)
			if err != nil || !replay || generated != 1 || solved != 3 || before != afterRaw {
				t.Fatalf("cold replay changed calls or receipts: %v %v %d/%d", replay, err, generated, solved)
			}
		})
	}
}
