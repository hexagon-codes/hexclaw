package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditedAlternativeKeepsEachCandidateWithItsOwnExecutionAndGuide(t *testing.T) {
	candidate := func(method, label, quantity, extra string) solverSolution {
		answer := "两段组成总长；" + method
		output := method + "\n答案：" + answer
		task := "solve using " + method
		report := `{"report_content":{"version":"teaching-report-content-v1","items":[{"problem_id":"current","answers":[{"label":"数量关系","text":"总长等于两段之和"},{"label":"` + label + `","text":"` + quantity + `"}` + extra + `]}]}}`
		return solverSolution{output: output, answer: answer, executionTask: task, executionReceipt: standaloneVerifierTestReceipt(task, "COMPUTED: "+answer),
			generation: &SolveGenerationAttachment{OutputVersion: solveWithTeachingReportV2, ParentGuideJSON: report}}
	}
	audit := func(s solverSolution, valid bool) parentGuideAudit {
		a := parentGuideAudit{SourceDigest: executionInputDigest(s.output), Scope: "IN_SCOPE", verifiedQuantity: "18 cm"}
		for _, field := range append(append([]string(nil), parentGuideFields[:]...), "report_content") {
			pass := field != "likely_mistakes" || valid
			a.Fields = append(a.Fields, guideFieldAudit{Field: field, Valid: &pass, Reason: "已独立核对"})
		}
		return a
	}
	for _, tc := range []struct{ name, label, quantity, extra string }{
		{"valid", "总长", "18 cm", ""}, {"different role", "周长", "18 cm", ""}, {"different unit", "总长", "18 m", ""},
		{"additional quantity", "总长", "18 cm", `,{"label":"宽度","text":"3 cm"}`},
		{"embedded quantity", "总长", "18 cm", `,{"label":"其余","text":"另有 3 cm 和 2 cm 两段"}`},
		{"unknown execution", "总长", "18 cm", ""}, {"different input", "总长", "18 cm", ""},
		{"conflicting source", "总长", "18 cm", ""}, {"primary valid", "总长", "18 cm", ""}, {"primary report rejected", "总长", "18 cm", ""},
		{"conflicting approved alternative", "总长", "18 cm", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := candidate("两段相加", "总长", "18 cm", ""), candidate("从总数分解", tc.label, tc.quantity, tc.extra)
			aa, bb := audit(a, tc.name == "primary valid"), audit(b, true)
			switch tc.name {
			case "unknown execution":
				b.executionReceipt = nil
			case "different input":
				b.executionReceipt.InputDigest = executionInputDigest("another problem")
			case "conflicting source":
				bb.SourceDigest = aa.SourceDigest
			case "primary report rejected":
				rejected := false
				aa.Fields[len(aa.Fields)-1].Valid = &rejected
			}
			candidates, audits := []solverSolution{a, b}, []parentGuideAudit{aa, bb}
			if tc.name == "conflicting approved alternative" {
				other := candidate("第三种解法", "总长", "18 m", "")
				candidates, audits = append(candidates, other), append(audits, audit(other, true))
			}
			got, ok := auditedReportAlternative(a, candidates, audits)
			if ok != (tc.name == "valid") || ok && (got.output != b.output || got.generation != b.generation) {
				t.Fatalf("selected=%q ok=%t", got.output, ok)
			}
		})
	}
}

func TestCoreTeachingRequiresBothFieldsFromOneCompleteAudit(t *testing.T) {
	source := jointGuideDigest("同题原解")
	fields := jointGuideAudit("同题原解", "IN_SCOPE")["fields"].([]map[string]any)
	fields = append(fields, map[string]any{"field": "report_content", "valid": false, "reason": "图表与原解不符"})
	raw, err := json.Marshal([]any{map[string]any{"source_digest": source, "scope": "IN_SCOPE", "fields": fields}})
	if err != nil {
		t.Fatal(err)
	}
	line := "PARENT_GUIDE_AUDITS: " + string(raw)
	if !TeachingReportCoreApproved(line, source) || TeachingReportGuideAuditVerdict(line, source) != "INVALID" {
		t.Fatal("separately accepted answer and steps should not change the rejected full guide")
	}
	for _, field := range []string{"answer", "full_solution_steps"} {
		for _, entry := range fields {
			entry["valid"] = entry["field"] != field && entry["field"] != "report_content"
		}
		invalid, _ := json.Marshal([]any{map[string]any{"source_digest": source, "scope": "IN_SCOPE", "fields": fields}})
		if TeachingReportCoreApproved("PARENT_GUIDE_AUDITS: "+string(invalid), source) {
			t.Fatalf("accepted a rejected %s", field)
		}
	}
	if TeachingReportCoreApproved(line, jointGuideDigest("另一题")) ||
		TeachingReportCoreApproved(strings.Replace(line, `"valid":true`, `"valid":false,"valid":true`, 1), source) {
		t.Fatal("an unrelated or internally conflicting audit allowed core teaching")
	}
}
