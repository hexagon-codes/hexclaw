package engineadapter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
)

func TestSkillContextReceiptsHomeworkGuideRecordsSuccessfulActualLoad(t *testing.T) {
	for _, state := range []string{"success", "failed", "forged-model-field", "forged-empty-field"} {
		t.Run(state, func(t *testing.T) {
			calls := 0
			adapter := NewSolveAdapter(nil, WithParentTeachingGuideGen(func(_ context.Context, _ string, prompt string, _ string) (string, error) {
				calls++
				if !strings.Contains(prompt, "波利亚四步") || !strings.Contains(prompt, "责任逐渐释放") {
					t.Fatal("snapshot not tied to actual prompt")
				}
				if state == "failed" {
					return "", errors.New("provider unavailable")
				}
				guide := usecase.ParentTeachingGuide{Answer: "2", FullSolutionSteps: []string{"1+1=2"}, GradeLevelMethod: "加法", LikelyMistakes: []string{"漏算一个数"}, ParentTeachingSequence: []string{"摆出两个单位"}, FollowUpQuestions: []string{"一共有几个？"}, CheckingMethod: "再数一遍"}
				if state == "forged-model-field" {
					guide.SkillContextReceipts = []skill.ContextReceipt{{Name: "forged", Version: "1", Status: "loaded"}}
				}
				raw, _ := json.Marshal(guide)
				if state == "forged-empty-field" {
					raw = append(raw[:len(raw)-1], []byte(`,"skill_context_receipts":[]}`)...)
				}
				return string(raw), nil
			}))
			guide, err := adapter.GenerateParentTeachingGuide(t.Context(), usecase.ParentTeachingGuideRequest{Subject: "数学", Grade: "六年级上", Problem: "1+1=", VerifiedSolution: "2"})
			if calls != 1 {
				t.Fatalf("unexpected physical generation count: %d", calls)
			}
			if state != "success" {
				if err == nil || len(guide.SkillContextReceipts) > 0 {
					t.Fatal("failed or forged model result created trusted evidence")
				}
				return
			}
			if err != nil || len(guide.SkillContextReceipts) != 2 || guide.SkillContextReceipts[0].Name != "k12-pedagogy" || guide.SkillContextReceipts[1].Name != "math-tutor" {
				t.Fatalf("missing trusted sources: %+v err=%v", guide.SkillContextReceipts, err)
			}
			for _, receipt := range guide.SkillContextReceipts {
				if receipt.Status != "loaded" {
					t.Fatal("successful actual context injection not recorded")
				}
			}
		})
	}
}

func TestSkillContextReceiptsPreparedMethodologyIsNotExecution(t *testing.T) {
	adapter := NewSolveAdapter(nil)
	contract, snapshot, err := adapter.FreezeSolveTeachingContext(t.Context(), "数学", "六年级上")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 2 || snapshot[0].Name != "k12-pedagogy" || snapshot[1].Name != "math-tutor" {
		t.Fatalf("not actual bundled sources: %+v", snapshot)
	}
	for _, receipt := range snapshot {
		if receipt.Status != "prepared" || !strings.Contains(contract, "### Skill: "+receipt.Name) || receipt.ContentDigest == "" {
			t.Fatalf("prepared source mismatched: %+v", receipt)
		}
	}
	if snapshot[0].Methods[0].Name != "责任逐渐释放" || snapshot[1].Methods[0].Name != "波利亚四步" {
		t.Fatal("declared teaching basis was not preserved")
	}
	// 旧字符串合同保持逐字相同，快照不混入模型的教学正文。
	legacy, err := adapter.FreezeSolveTeachingContract(t.Context(), "数学", "六年级上")
	if err != nil || legacy != contract {
		t.Fatal("display snapshot changed teaching prompt")
	}
}

func TestSkillContextReceiptsOnlyCandidateThatConsumedFrozenContractIsLoaded(t *testing.T) {
	for _, state := range []string{"candidate", "script", "failed", "old"} {
		t.Run(state, func(t *testing.T) {
			guide := usecase.ParentTeachingGuide{Answer: "2", FullSolutionSteps: []string{"1+1=2"}, GradeLevelMethod: "加法", LikelyMistakes: []string{"漏算一个数"}, ParentTeachingSequence: []string{"摆出两个单位"}, FollowUpQuestions: []string{"一共有几个？"}, CheckingMethod: "再数一遍"}
			encoded, _ := json.Marshal(guide)
			meta := map[string]string{"solve_parent_guide_json": string(encoded), "solve_parent_guide_audit": "VALID"}
			if state == "script" {
				meta = map[string]string{"solve_evidence": "numeric_exec", "solve_mode": "deterministic_arithmetic"}
			}
			exec := &fakeExec{solveResult: &skill.Result{Content: "2", Metadata: meta}}
			if state == "failed" {
				exec.err = errors.New("known failure")
			}
			adapter := NewSolveAdapter(exec)
			contract, snapshot, err := adapter.FreezeSolveTeachingContext(t.Context(), "数学", "六年级上")
			if err != nil {
				t.Fatal(err)
			}
			request := usecase.GradeRequest{AgentName: "actual-child", Subject: "数学", Grade: "六年级上", Problem: "1+1=", SolveOutputVersion: usecase.SolveOutputWithParentGuideV1, ParentTeachingContract: contract, ParentTeachingSkillSnapshot: snapshot}
			if state == "old" {
				request.ParentTeachingSkillSnapshot = nil
			}
			result, err := (usecase.Deps{Solver: adapter}).SolveHomeworkProblem(t.Context(), request)
			if state == "failed" {
				if err == nil || result.Generation != nil {
					t.Fatal("failed call created teaching evidence")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if state == "script" {
				if result.Generation != nil {
					t.Fatal("script-only computation falsely claimed model skill injection")
				}
				return
			}
			if state == "old" {
				if len(result.Generation.SkillContextReceipts) != 0 {
					t.Fatal("old frozen task gained current metadata")
				}
				return
			}
			if exec.lastArgs["parent_teaching_contract"] != contract || len(result.Generation.SkillContextReceipts) != 2 {
				t.Fatal("execution receipt not tied to actual supplied contract")
			}
			for _, receipt := range result.Generation.SkillContextReceipts {
				if receipt.Status != "loaded" {
					t.Fatal("candidate did not record actual context load")
				}
			}
			if snapshot[0].Status != "prepared" {
				t.Fatal("result projection mutated the frozen preparation snapshot")
			}
		})
	}
}
