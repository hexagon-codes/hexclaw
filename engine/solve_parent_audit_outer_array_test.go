package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTeachingReportRecoversOnlyCompleteSourceBoundAuditArray(t *testing.T) {
	first, second := jointGuideDigest("first"), jointGuideDigest("second")
	audit := func(digest string, valid bool) map[string]any {
		v := jointGuideAudit("", "IN_SCOPE")
		v["source_digest"] = digest
		v["fields"] = append(v["fields"].([]map[string]any), map[string]any{"field": "report_content", "valid": valid, "reason": "已复核报告内容"})
		return v
	}
	line := func(items ...map[string]any) string {
		raw, err := json.Marshal(items)
		if err != nil {
			t.Fatal(err)
		}
		return "PARENT_GUIDE_AUDITS: " + strings.TrimSuffix(string(raw), "]")
	}
	complete := line(audit(second, false), audit(first, true))
	want := []string{first, second}
	missing := audit(second, true)
	missing["fields"] = missing["fields"].([]map[string]any)[:7]
	outOfScope := map[string]any{"source_digest": second, "scope": "OUT_OF_SCOPE"}
	for _, tc := range []struct {
		name, input, digest, want string
		sources                   []string
	}{
		{"complete without closing bracket", complete, first, "VALID", want},
		{"complete JSON", complete + "]", first, "VALID", want},
		{"negative stays negative", complete, second, "INVALID", want},
		{"explicit scope veto", line(audit(first, true), outOfScope), second, "INVALID", want},
		{"missing other review", line(audit(first, true)), first, "NOT_PROVIDED", want},
		{"complete JSON missing other review", line(audit(first, true)) + "]", first, "NOT_PROVIDED", want},
		{"missing other field", line(audit(first, true), missing), first, "NOT_PROVIDED", want},
		{"different source", line(audit(first, true), audit("unexpected", true)), first, "NOT_PROVIDED", want},
		{"duplicate source", line(audit(first, true), audit(first, true)), first, "NOT_PROVIDED", want},
		{"ambiguous frozen sources", complete, first, "NOT_PROVIDED", []string{first, first}},
		{"conflicting JSON property", strings.Replace(complete, `"valid":true`, `"valid":false,"valid":true`, 1), first, "NOT_PROVIDED", want},
		{"unfinished review", strings.TrimSuffix(complete, "}"), first, "NOT_PROVIDED", want},
		{"following unconsumed text", complete + "\nmore", first, "NOT_PROVIDED", want},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TeachingReportGuideAuditVerdictForSources(tc.input, tc.digest, tc.sources); got != tc.want {
				t.Fatalf("verdict=%s want=%s", got, tc.want)
			}
		})
	}
}

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
