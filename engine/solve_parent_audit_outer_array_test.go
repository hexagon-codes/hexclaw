package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSolveParentGuideCompleteAuditOnlyMissingOuterArrayEnd(t *testing.T) {
	for _, scenario := range []string{"complete object", "missing field", "wrong source", "invalid audit", "unfinished object", "multiple candidates"} {
		t.Run(scenario, func(t *testing.T) {
			raw, _ := fractionRuleGuideJSON(t)
			audit := fractionRuleAudit()
			switch scenario {
			case "missing field":
				audit["fields"] = audit["fields"].([]map[string]any)[:6]
			case "wrong source":
				audit["source_digest"] = jointGuideDigest("another solution")
			case "invalid audit":
				audit["fields"].([]map[string]any)[0]["valid"] = false
			}
			output := strings.TrimSuffix(fractionRuleJudgment(t, audit), "]")
			if scenario == "unfinished object" {
				output = strings.TrimSuffix(output, "}")
			}
			candidates := []guideAuditCandidate{{SourceDigest: jointGuideDigest(fractionRuleSolution)}}
			if scenario == "multiple candidates" {
				candidates = append(candidates, guideAuditCandidate{SourceDigest: jointGuideDigest("other")})
			}
			verdict := parentGuideAuditVerdict(parentGuideAuditsForCandidates(output, candidates), candidates[0].SourceDigest)
			want := "NOT_PROVIDED"
			if scenario == "complete object" {
				want = "VALID"
			}
			if scenario == "invalid audit" {
				want = "INVALID"
			}
			if verdict != want {
				t.Fatalf("audit=%s want=%s", verdict, want)
			}
			if scenario != "complete object" {
				return
			}
			exec := &solveExec{solverOuts: []string{raw}, verifierOut: output}
			result, err := NewSolveSkill(exec.fn, nil).Execute(t.Context(), fractionRuleArgs(fractionRuleQuestion))
			if err != nil || result.Metadata["solve_parent_guide_audit"] != "VALID" || exec.solverCalls() != 1 || exec.verifierCalls() != 1 {
				t.Fatalf("audit recovery changed calls or evidence: err=%v result=%+v agents=%v", err, result, exec.agents())
			}
			var guide map[string]any
			if json.Unmarshal([]byte(result.Metadata["solve_parent_guide_json"]), &guide) != nil || guide["answer"] != fractionRuleAnswer {
				t.Fatal("audit recovery rewrote the candidate")
			}
		})
	}
}
