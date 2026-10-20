package k12storage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// 真实 SQLite 批次经原重试入口恢复；仅生成和验算边界可控。
func weeklyDuplicateBatch(t *testing.T, legacy, unknown bool) (usecase.Deps, string) {
	t.Helper()
	s, d, _, _ := practiceAssetFixture(t)
	plan := weeklyAssetDeps(t, &d, "lele", false)
	request := usecase.WeeklyPracticeCandidateRequest{AgentName: "lele", PlanSection: k12.WeeklySectionArithmeticWarmup,
		MaxItems: 2, ArithmeticMinutes: 1, GradeTerm: "五年级下", Textbook: "人教版", ProfileRevision: 1,
		Targets: []usecase.WeeklyPracticeTarget{{SourceRef: "assessment:division", Subject: "数学", KnowledgePoint: "整数除法", Question: "4×1＝"}}}
	raw, _ := json.Marshal(request)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	items := []any{}
	if legacy {
		for index := range 2 {
			generated := usecase.SolveResult{Solution: "## 问题\n8÷2＝\n## 答案\n4"}
			candidate := usecase.WeeklyPracticeCandidate{SourceKind: "assessment_variant", GenerationMethod: k12.WeeklyGenerationMethodAIVariant,
				SourceRef: fmt.Sprintf("assessment:division:variant:%d", index+1), Subject: "数学", KnowledgePoint: "整数除法", PromptMarkdown: "8÷2＝", ExpectedAnswer: "4", EstimatedSeconds: 30, EvidenceRefs: []string{"numeric_exec"}}
			items = append(items, map[string]any{"target": 0, "candidate": candidate,
				"generate": map[string]any{"status": "succeeded", "result": generated},
				"solve": map[string]any{"status": "succeeded", "result": usecase.SolveResult{Solution: "## 答案\n4"},
					"physical_calls": []any{map[string]any{"status": "succeeded", "request_digest": "old-receipt", "payload": `{"answer":4}`}}}})
		}
		if unknown {
			items[1].(map[string]any)["solve"].(map[string]any)["physical_calls"].([]any)[0].(map[string]any)["status"] = "outcome_unknown"
		}
	}
	body["generation"] = map[string]any{"route": k12.GradingModelSnapshot{Provider: "test", Model: "weekly-model", Route: "test/weekly-model", Capability: "text"}, "items": items}
	raw, _ = json.Marshal(body)
	batch, _, err := s.PrepareWeeklyArithmeticBatch(context.Background(), "lele", plan.PlanID, plan.Revision, "original-duplicate", strings.Repeat("a", 64), string(raw), d.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishWeeklyArithmeticGeneration(context.Background(), "lele", batch.BatchID, k12.WeeklyArithmeticFailedRetryable, nil, nil, "", "weekly candidate duplicates practiced problem", d.Now()); err != nil {
		t.Fatal(err)
	}
	return d, batch.BatchID
}

func TestWeeklyDuplicate_TargetedReplacementAndLimit(t *testing.T) {
	for _, limit := range []bool{false, true} {
		t.Run(map[bool]string{false: "replace only duplicate", true: "two replacement limit"}[limit], func(t *testing.T) {
			d, id := weeklyDuplicateBatch(t, false, false)
			d.WeeklyCandidates = usecase.NewWeeklyPracticeCandidateSource(&d)
			generated, solved, solvedQuestion := 0, 0, ""
			d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(_ context.Context, _, prompt, _ string) (usecase.SolveResult, error) {
				generated++
				if generated > 1 && !strings.Contains(prompt, "8÷2＝") {
					t.Fatal("selected question absent from generator input")
				}
				question := "8÷2＝"
				if !limit && generated == 3 {
					question = "12÷3＝"
				}
				return usecase.SolveResult{Solution: "## 问题\n" + question + "\n## 答案\n4"}, nil
			})
			d.Solver = weeklySavedResponseSolver{&solved, &solvedQuestion}
			_, _, err := d.RetryWeeklyArithmeticBatch(context.Background(), "lele", id, "retry-duplicate")
			if err != nil {
				t.Fatal(err)
			}
			batch, err := d.Records.GetWeeklyArithmeticBatch(context.Background(), "lele", id)
			if err != nil {
				t.Fatal(err)
			}
			if limit {
				if batch.State != k12.WeeklyArithmeticFailedRetryable || generated != 4 || solved != 1 || len(batch.Items) != 0 || !strings.Contains(batch.FailureMessage, "replacement limit") {
					t.Fatalf("unbounded or published duplicate: state=%s calls=%d/%d failure=%s", batch.State, generated, solved, batch.FailureMessage)
				}
			} else if batch.State != k12.WeeklyArithmeticReady || generated != 3 || solved != 2 || len(batch.Items) != 2 || batch.Items[0].PromptMarkdown != "8÷2＝" || batch.Items[1].PromptMarkdown != "12÷3＝" {
				t.Fatalf("targeted repair failed: %+v calls=%d/%d", batch, generated, solved)
			}
			before := batch.GenerationCheckpoint
			if _, replay, err := d.RetryWeeklyArithmeticBatch(context.Background(), "lele", id, "retry-duplicate"); err != nil || !replay {
				t.Fatalf("replay: %v %v", replay, err)
			}
			after, _ := d.Records.GetWeeklyArithmeticBatch(context.Background(), "lele", id)
			if after.GenerationCheckpoint != before {
				t.Fatal("replay changed receipts or replacement budget")
			}
		})
	}
}

func TestWeeklyDuplicate_LegacyCheckpointAndUnknownReceipt(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy successful duplicate", true: "unknown never resent"}[unknown], func(t *testing.T) {
			d, id := weeklyDuplicateBatch(t, true, unknown)
			d.WeeklyCandidates = usecase.NewWeeklyPracticeCandidateSource(&d)
			before, _ := d.Records.GetWeeklyArithmeticBatch(context.Background(), "lele", id)
			generated, solved, solvedQuestion := 0, 0, ""
			d.PracticeVariant = usecase.PracticeVariantGeneratorFunc(func(_ context.Context, _, prompt, _ string) (usecase.SolveResult, error) {
				generated++
				if !strings.Contains(prompt, "8÷2＝") {
					t.Fatal("legacy selection missing")
				}
				return usecase.SolveResult{Solution: "## 问题\n12÷3＝\n## 答案\n4"}, nil
			})
			d.Solver = weeklySavedResponseSolver{&solved, &solvedQuestion}
			if _, _, err := d.RetryWeeklyArithmeticBatch(context.Background(), "lele", id, "legacy-retry"); err != nil {
				t.Fatal(err)
			}
			after, _ := d.Records.GetWeeklyArithmeticBatch(context.Background(), "lele", id)
			if unknown {
				if generated != 0 || solved != 0 || after.State != k12.WeeklyArithmeticFailedTerminal || !weeklyDuplicateSameCalls(before.GenerationCheckpoint, after.GenerationCheckpoint) {
					t.Fatalf("unknown receipt changed or resent: state=%s calls=%d/%d", after.State, generated, solved)
				}
				return
			}
			if after.State != k12.WeeklyArithmeticReady || generated != 1 || solved != 1 || len(after.Items) != 2 {
				t.Fatalf("legacy recovery: state=%s calls=%d/%d failure=%s", after.State, generated, solved, after.FailureMessage)
			}
			var old, current struct {
				Generation struct{ Items []map[string]json.RawMessage }
			}
			if err := json.Unmarshal([]byte(before.GenerationCheckpoint), &old); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(after.GenerationCheckpoint), &current); err != nil {
				t.Fatal(err)
			}
			if !weeklyDuplicateSameJSON(old.Generation.Items[0]["generate"], current.Generation.Items[0]["generate"]) || !weeklyDuplicateSameJSON(old.Generation.Items[0]["solve"], current.Generation.Items[0]["solve"]) {
				t.Fatal("accepted calls changed")
			}
			var rejected []map[string]json.RawMessage
			if err := json.Unmarshal(current.Generation.Items[1]["rejected_attempts"], &rejected); err != nil || len(rejected) != 1 {
				t.Fatalf("rejected history missing: %v", err)
			}
			if !weeklyDuplicateSameJSON(old.Generation.Items[1]["generate"], rejected[0]["generate"]) || !weeklyDuplicateSameJSON(old.Generation.Items[1]["solve"], rejected[0]["solve"]) {
				t.Fatal("rejected call facts changed")
			}
		})
	}
}

func weeklyDuplicateSameCalls(before, after string) bool {
	var a, b map[string]any
	_ = json.Unmarshal([]byte(before), &a)
	_ = json.Unmarshal([]byte(after), &b)
	return reflect.DeepEqual(weeklyDuplicateNormalize(a["generation"]), weeklyDuplicateNormalize(b["generation"]))
}

// 结构默认空值不代表新增调用事实，比较保留下来的有效回执字段。
func weeklyDuplicateSameJSON(a, b json.RawMessage) bool {
	var x, y any
	_ = json.Unmarshal(a, &x)
	_ = json.Unmarshal(b, &y)
	return reflect.DeepEqual(weeklyDuplicateNormalize(x), weeklyDuplicateNormalize(y))
}
func weeklyDuplicateNormalize(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for k, item := range v {
			item = weeklyDuplicateNormalize(item)
			if item == nil || reflect.DeepEqual(item, "") || reflect.DeepEqual(item, float64(0)) {
				delete(v, k)
			} else {
				v[k] = item
			}
		}
	case []any:
		for i := range v {
			v[i] = weeklyDuplicateNormalize(v[i])
		}
	}
	return value
}
