package k12storage_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// 替身只替代物理模型及解析器版本；回执与命令读写使用真实 SQLite。
type weeklyReceiptSolver struct {
	calls         *int
	fixed         bool
	unknown       bool
	knownFailure  bool
	legacy        bool
	changedPrompt bool
	afterVerify   func()
}

func (s weeklyReceiptSolver) UsesGradingPhysicalCalls() bool { return true }
func (s weeklyReceiptSolver) Solve(ctx context.Context, question, _, _ string) (usecase.SolveResult, error) {
	result := usecase.SolveResult{Solution: "## 答案\n4", Evidence: usecase.SolveEvidence{Verdict: usecase.VerdictAgree, EvidenceType: usecase.EvidenceHeuristic}}
	if s.legacy {
		return result, nil
	}
	for _, operation := range []k12.GradingItemOperation{k12.GradingItemOperationSolveGenerate, k12.GradingItemOperationSolveVerify} {
		task := question + ":" + string(operation)
		if s.changedPrompt {
			task += " changed"
		}
		sum := sha256.Sum256([]byte(task))
		response, err := usecase.ExecuteGradingPhysicalCall(ctx, usecase.GradingPhysicalCallSpec{Operation: operation, RequestDigest: hex.EncodeToString(sum[:])}, func(context.Context) (string, error) {
			*s.calls++
			if s.afterVerify != nil && operation == k12.GradingItemOperationSolveVerify {
				s.afterVerify()
			}
			if s.knownFailure && operation == k12.GradingItemOperationSolveVerify {
				return "", egress.ErrProviderNotSent
			}
			if s.unknown && operation == k12.GradingItemOperationSolveVerify {
				return "", context.DeadlineExceeded
			}
			return `{"Output":"VERDICT: AGREE\nPROCESS: VALID\nCOMPUTED: 4","SessionID":"original-session","execution_receipt":{"input_digest":"original-input","run_id":"original-run","status":"success","stdout":"COMPUTED: 4\n","stdout_bytes":12,"exit_code":0,"truncated":false}}`, nil
		})
		if err != nil {
			return result, err
		}
		var saved struct {
			SessionID string
			Receipt   struct {
				RunID  string `json:"run_id"`
				Stdout string `json:"stdout"`
			} `json:"execution_receipt"`
		}
		if err := json.Unmarshal([]byte(response.Payload), &saved); err != nil {
			return result, err
		}
		if saved.SessionID != "original-session" || saved.Receipt.RunID != "original-run" || saved.Receipt.Stdout != "COMPUTED: 4\n" {
			return result, fmt.Errorf("saved physical response incomplete")
		}
	}
	if s.fixed {
		result.Evidence.EvidenceType = usecase.EvidenceNumericExec
	}
	return result, nil
}

func TestWeeklyPhysicalReceipts_ReinterpretationAndReplay(t *testing.T) {
	for _, outcome := range []string{"success", "unknown", "recovery_success", "sent_restart", "legacy_without_receipts", "source_changed", "request_changed", "model_changed"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			d, plan := weeklyTextbookSourceFixture(t, weeklyLessonPage)
			path := filepath.Join(t.TempDir(), "weekly.db")
			if _, err := d.Records.DB().Exec(`VACUUM INTO ?`, path); err != nil {
				t.Fatal(err)
			}
			weeklyRecoveryReopen(t, &d, path)
			generated, calls := 0, 0
			d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(context.Context, string, string, string) (usecase.SolveResult, error) {
				generated++
				return usecase.SolveResult{Solution: "## 问题\n计算：1/5×20\n## 解答\n分子乘20，再约分。\n## 答案\n4"}, nil
			})
			d.Solver = weeklyReceiptSolver{calls: &calls, unknown: outcome == "unknown" || outcome == "recovery_success", legacy: outcome == "legacy_without_receipts"}
			if _, _, err := d.PrepareWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, plan.Revision, 1, "original"); err == nil {
				t.Fatal("unverified original accepted")
			}
			raw, original := weeklyRecoveryRead(t, &d, plan)
			oldSolve := original["solve"].(map[string]any)
			if outcome != "legacy_without_receipts" {
				receipts := oldSolve["physical_calls"].([]any)
				if len(receipts) != 2 || receipts[0].(map[string]any)["status"] != "succeeded" {
					t.Fatalf("physical success missing: %v", receipts)
				}
				if outcome == "unknown" || outcome == "recovery_success" {
					if oldSolve["status"] != "outcome_unknown" || receipts[1].(map[string]any)["status"] != "outcome_unknown" {
						t.Fatal("unknown child became successful aggregate")
					}
				} else if receipts[1].(map[string]any)["status"] != "succeeded" {
					t.Fatal("verifier success missing")
				}
			}
			if outcome == "source_changed" {
				if _, err := d.Records.DB().Exec(`UPDATE kb_semantic_document_bindings SET content_generation=2`); err != nil {
					t.Fatal(err)
				}
			}
			if outcome == "sent_restart" || outcome == "model_changed" {
				var body map[string]any
				if err := json.Unmarshal([]byte(raw), &body); err != nil {
					t.Fatal(err)
				}
				generation := body["generation"].(map[string]any)
				if outcome == "sent_restart" {
					generation["items"].([]any)[0].(map[string]any)["solve"].(map[string]any)["physical_calls"].([]any)[1].(map[string]any)["status"] = "sent"
				} else {
					generation["route"].(map[string]any)["model"] = "changed-model"
				}
				next, _ := json.Marshal(body)
				ref := usecase.WeeklyCandidateCheckpointRef{AgentName: "mingming", Kind: "refresh", PlanID: plan.PlanID, Revision: plan.Revision, IdempotencyKey: "original"}
				if err := d.Records.SaveWeeklyCandidateCheckpoint(ctx, ref, raw, string(next)); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.Records.DB().Close(); err != nil {
				t.Fatal(err)
			}
			weeklyRecoveryReopen(t, &d, path)
			raw, original = weeklyRecoveryRead(t, &d, plan)
			oldSolve = original["solve"].(map[string]any)
			beforeCalls := calls
			d.Solver = weeklyReceiptSolver{calls: &calls, fixed: true, changedPrompt: outcome == "request_changed"}
			sum := sha256.Sum256([]byte(raw))
			req := usecase.WeeklyTextbookReinterpretationRequest{SourceCommandKey: "original", PlanRevision: plan.Revision, CheckpointSHA256: hex.EncodeToString(sum[:]), ItemIndex: 0, IdempotencyKey: "parse-fixed"}
			if outcome == "recovery_success" {
				recovery := usecase.WeeklyTextbookRecoveryRequest{SourceCommandKey: req.SourceCommandKey, PlanRevision: req.PlanRevision, CheckpointSHA256: req.CheckpointSHA256, ItemIndex: 0, IdempotencyKey: "explicit-recovery", AcceptDuplicateExecution: true}
				result, replay, err := d.RecoverWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, recovery)
				if err != nil || replay || result.Tracks[1].Status != k12.WeeklyTrackReady || calls != beforeCalls+1 || generated != 1 {
					t.Fatalf("recovery did not reuse successful solver: %+v %v calls=%d", result, err, calls)
				}
				afterRaw, after := weeklyRecoveryRead(t, &d, plan)
				if !reflect.DeepEqual(original["solve"], after["solve"]) {
					t.Fatal("recovery rewrote original physical receipts")
				}
				newCall := after["solve_recovery_attempts"].([]any)[0].(map[string]any)["call"].(map[string]any)
				if len(newCall["physical_calls"].([]any)) != 2 || newCall["physical_calls"].([]any)[0].(map[string]any)["reuse_source_digest"] == nil {
					t.Fatal("recovery lost reused receipt provenance")
				}
				if err := d.Records.DB().Close(); err != nil {
					t.Fatal(err)
				}
				weeklyRecoveryReopen(t, &d, path)
				_, replay, err = d.RecoverWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, recovery)
				if err != nil || !replay || calls != beforeCalls+1 {
					t.Fatalf("recovery replay: %v %v calls=%d", replay, err, calls)
				}
				finalRaw, _ := weeklyRecoveryRead(t, &d, plan)
				if finalRaw != afterRaw {
					t.Fatal("recovery replay changed receipts")
				}
				return
			}
			result, replay, err := d.ReinterpretWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, req)
			if outcome == "success" {
				if err != nil || replay || result.Tracks[1].Status != k12.WeeklyTrackReady || len(result.Tracks[1].Items) != 1 {
					t.Fatalf("reinterpreted plan not ready: %+v %v", result, err)
				}
			} else if err == nil {
				t.Fatal("incomplete or changed receipts reinterpreted")
			}
			if calls != beforeCalls || generated != 1 {
				t.Fatalf("reinterpretation made new external calls: %d -> %d, generation %d", beforeCalls, calls, generated)
			}
			afterRaw, after := weeklyRecoveryRead(t, &d, plan)
			if outcome != "success" {
				if afterRaw != raw {
					t.Fatal("rejected reinterpretation modified original checkpoint")
				}
				if outcome == "unknown" {
					if _, _, err := d.PrepareWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, plan.Revision, 1, "original"); err == nil || calls != beforeCalls {
						t.Fatal("unknown replay resent")
					}
				}
				return
			}
			newSolve := after["solve"].(map[string]any)
			for _, key := range []string{"result", "physical_calls", "status", "input_digest"} {
				if !reflect.DeepEqual(oldSolve[key], newSolve[key]) {
					t.Fatalf("original %s rewritten", key)
				}
			}
			if !reflect.DeepEqual(original["generate"], after["generate"]) || len(newSolve["interpretations"].([]any)) != 1 {
				t.Fatal("original generation changed or interpretation missing")
			}
			if err := d.Records.DB().Close(); err != nil {
				t.Fatal(err)
			}
			weeklyRecoveryReopen(t, &d, path)
			_, replay, err = d.ReinterpretWeeklyTextbookTrack(ctx, "mingming", plan.PlanID, req)
			if err != nil || !replay || calls != beforeCalls {
				t.Fatalf("replay: %v %v calls=%d", replay, err, calls)
			}
			finalRaw, _ := weeklyRecoveryRead(t, &d, plan)
			if finalRaw != afterRaw {
				t.Fatal("same-key replay changed persisted receipts")
			}
		})
	}
}

func TestWeeklyPhysicalReceipts_KnownFailureRetryPreservesSuccess(t *testing.T) {
	s, d, asset, _ := practiceAssetFixture(t)
	ctx := context.Background()
	plan := weeklyAssetDeps(t, &d, "lele", false)
	if err := s.ArchiveProblemAsset(ctx, asset.OwnerID, asset.AssetID, asset.Revision); err != nil {
		t.Fatal(err)
	}
	generated, calls := 0, 0
	d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(context.Context, string, string, string) (usecase.SolveResult, error) {
		generated++
		return usecase.SolveResult{Solution: "## 问题\n计算：24÷6\n## 解答\n24除以6。\n## 答案\n4"}, nil
	})
	d.Solver = weeklyReceiptSolver{calls: &calls, knownFailure: true}
	batch, _, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "lele", plan.PlanID, plan.Revision, 1, "physical-known-failure")
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetWeeklyArithmeticBatch(ctx, "lele", batch.BatchID)
	if err != nil || before.State != k12.WeeklyArithmeticFailedRetryable || generated != 1 || calls != 2 {
		t.Fatalf("known failure state: %+v %v calls=%d/%d", before, err, generated, calls)
	}
	d.Solver = weeklyReceiptSolver{calls: &calls, fixed: true}
	if _, replay, err := d.RetryWeeklyArithmeticBatch(ctx, "lele", batch.BatchID, "physical-known-retry"); err != nil || replay {
		t.Fatalf("retry: %v %v", replay, err)
	}
	after, err := s.GetWeeklyArithmeticBatch(ctx, "lele", batch.BatchID)
	if err != nil || after.State != k12.WeeklyArithmeticReady || generated != 1 || calls != 3 {
		t.Fatalf("successful solver resent: %+v %v calls=%d/%d", after, err, generated, calls)
	}
	var body struct {
		Generation struct {
			Items []struct {
				Solve struct {
					PhysicalCalls []map[string]any `json:"physical_calls"`
					History       []map[string]any `json:"physical_history"`
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(after.GenerationCheckpoint), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Generation.Items) != 1 || len(body.Generation.Items[0].Solve.PhysicalCalls) != 2 || len(body.Generation.Items[0].Solve.History) != 1 || body.Generation.Items[0].Solve.History[0]["status"] != "failed" {
		t.Fatal("known failure receipt was lost")
	}
	if _, replay, err := d.RetryWeeklyArithmeticBatch(ctx, "lele", batch.BatchID, "physical-known-retry"); err != nil || !replay || calls != 3 {
		t.Fatalf("retry replay called provider: %v %v calls=%d", replay, err, calls)
	}
}
