package k12

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGradingAssessmentItemValidateKnownContradictionRequiresSolveOnly(t *testing.T) {
	const result = `{"Solve":{"problem_issue":"inconsistent_gcd_lcm","Evidence":{"Verdict":"unverifiable","EvidenceType":"numeric_exec"}}}`
	for _, scenario := range []string{"solve only", "missing solve", "grade", "guide", "invented guide", "ordinary untrusted", "unsupported issue", "unverified issue", "agreed answer"} {
		t.Run(scenario, func(t *testing.T) {
			item := GradingAssessmentItem{AgentName: "agent", JobID: "job", ProblemID: "problem", AttemptID: "attempt", ConfirmedVersion: 1, InputDigest: "sha256:input", Status: GradingAssessmentUntrusted, ResultJSON: result, ResultDigest: "sha256:result", ProjectionStatus: GradingProjectionCommitted, SolveInvocationID: "solve"}
			switch scenario {
			case "missing solve":
				item.SolveInvocationID = ""
			case "grade":
				item.GradeInvocationID = "grade"
			case "guide":
				item.ParentGuideInvocationID = "guide"
			case "invented guide":
				var fields map[string]json.RawMessage
				_ = json.Unmarshal([]byte(result), &fields)
				fields["ParentGuide"] = json.RawMessage(`{"Answer":"24"}`)
				raw, _ := json.Marshal(fields)
				item.ResultJSON = string(raw)
			case "ordinary untrusted":
				item.ResultJSON = `{"Solve":{"Evidence":{"Verdict":"unverifiable","EvidenceType":"none"}}}`
			case "unsupported issue":
				item.ResultJSON = strings.ReplaceAll(result, "inconsistent_gcd_lcm", "unknown")
			case "unverified issue":
				item.ResultJSON = strings.ReplaceAll(result, "numeric_exec", "none")
			case "agreed answer":
				item.ResultJSON = strings.ReplaceAll(result, "unverifiable", "agree")
			}
			err := item.Validate()
			if scenario == "solve only" && err != nil {
				t.Fatalf("concrete contradiction solve receipt rejected: %v", err)
			}
			if scenario != "solve only" && scenario != "grade" && err == nil {
				t.Fatal("missing proof or invented operation accepted")
			}
			// 历史已批改的 untrusted 仍沿用原来的求解与批改操作集。
			if scenario == "grade" && err != nil {
				t.Fatalf("existing untrusted solve-plus-grade receipt rejected: %v", err)
			}
		})
	}
}

func TestGradingAssessmentItemValidateEnforcesStatusOperationExactSet(t *testing.T) {
	statuses := []struct {
		name          string
		status        GradingAssessmentStatus
		wantSolve     bool
		wantGrade     bool
		allowZeroCall bool
	}{
		{name: "correct", status: GradingAssessmentCorrect, wantSolve: true, wantGrade: true},
		{name: "process issue", status: GradingAssessmentProcessIssue, wantSolve: true, wantGrade: true},
		{name: "wrong", status: GradingAssessmentWrong, wantSolve: true, wantGrade: true},
		{name: "untrusted", status: GradingAssessmentUntrusted, wantSolve: true, wantGrade: true},
		{name: "blank solved", status: GradingAssessmentBlankSolved, wantSolve: true},
		{name: "out of scope", status: GradingAssessmentOutOfScope, wantSolve: true, allowZeroCall: true},
		{name: "unanswered", status: GradingAssessmentUnanswered},
		{name: "answer unclear", status: GradingAssessmentAnswerUnclear},
	}
	for _, status := range statuses {
		status := status
		for _, solve := range []bool{false, true} {
			for _, grade := range []bool{false, true} {
				name := status.name
				if solve {
					name += "/solve"
				} else {
					name += "/no-solve"
				}
				if grade {
					name += "/grade"
				} else {
					name += "/no-grade"
				}
				t.Run(name, func(t *testing.T) {
					item := GradingAssessmentItem{
						AgentName: "agent", JobID: "job", ProblemID: "problem", AttemptID: "attempt",
						ConfirmedVersion: 1, InputDigest: "sha256:input", Status: status.status,
						ResultJSON: `{"status":"terminal"}`, ResultDigest: "sha256:result",
						ProjectionStatus: GradingProjectionCommitted,
					}
					if solve {
						item.SolveInvocationID = "solve-invocation"
					}
					if grade {
						item.GradeInvocationID = "grade-invocation"
					}
					err := item.Validate()
					wantValid := solve == status.wantSolve && grade == status.wantGrade
					if status.allowZeroCall && !solve && !grade {
						wantValid = true
					}
					if wantValid && err != nil {
						t.Fatalf("exact operation set rejected: %v", err)
					}
					if !wantValid && err == nil {
						t.Fatal("missing or extra operation was accepted")
					}
				})
			}
		}
	}
}

func TestGradingAssessmentItemValidateRejectsOutOfScopeParentGuide(t *testing.T) {
	for _, solveInvocationID := range []string{"", "solve-invocation"} {
		name := "zero-call"
		if solveInvocationID != "" {
			name = "solve-only"
		}
		t.Run(name, func(t *testing.T) {
			item := GradingAssessmentItem{
				AgentName: "agent", JobID: "job", ProblemID: "problem", AttemptID: "attempt",
				ConfirmedVersion: 1, InputDigest: "sha256:input", Status: GradingAssessmentOutOfScope,
				ResultJSON: `{"status":"out_of_scope"}`, ResultDigest: "sha256:result",
				SolveInvocationID: solveInvocationID, ParentGuideInvocationID: "parent-guide-invocation",
				ProjectionStatus: GradingProjectionCommitted,
			}
			if err := item.Validate(); err == nil || !strings.Contains(err.Error(), "parent guide invocation") {
				t.Fatalf("out_of_scope parent guide rejection error=%v", err)
			}
		})
	}
}
