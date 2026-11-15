package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
)

type guideAuditPhysicalUnknown struct{}

func (guideAuditPhysicalUnknown) Error() string                { return "controlled physical outcome unknown" }
func (guideAuditPhysicalUnknown) Unwrap() error                { return context.DeadlineExceeded }
func (guideAuditPhysicalUnknown) SubAgentRetryable() bool      { return false }
func (guideAuditPhysicalUnknown) SolveExecutionMustStop() bool { return true }

func TestVerificationProgramPromptPreservesDeclaredGuideAuditFields(t *testing.T) {
	for _, report := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy_seven", true: "report_eight"}[report], func(t *testing.T) {
			candidate := guideAuditCandidate{SourceDigest: jointGuideDigest("6×7=42张"), Solution: "6×7=42张", Guide: json.RawMessage(`{"answer":"42张"}`)}
			contract := ParentGuideAuditContract
			if report {
				candidate.RequiredFields = append(append([]string(nil), parentGuideFields[:]...), "report_content")
				contract = TeachingReportGuideAuditInstructions()
			}
			prompt := verificationProgramPrompt(&verificationInput{Problem: "七盒卡片，每盒六张，共多少张？", Candidate: "42张", ParentGuides: []guideAuditCandidate{candidate}})
			if !strings.Contains(prompt, contract) {
				t.Fatal("standalone verification omitted the declared guide audit contract")
			}
		})
	}
}

func TestExplicitSevenFieldGuideAuditDoesNotAcquireReportContract(t *testing.T) {
	const solution = "6×7=42张。\n答案：42张"
	candidate := guideAuditCandidate{SourceDigest: jointGuideDigest(solution), Solution: solution,
		Guide: json.RawMessage(`{"answer":"42张"}`), RequiredFields: append([]string(nil), parentGuideFields[:]...)}
	input := &verificationInput{Problem: "每盒有6张卡片，7盒一共有多少张？", Candidate: "42张", ParentGuides: []guideAuditCandidate{candidate}}
	for _, prompt := range []string{verificationProgramPrompt(input), withParentGuideVerification(SubAgentSpec{verification: input}, input.ParentGuides).Task} {
		if !strings.Contains(prompt, ParentGuideAuditContract) || strings.Contains(prompt, "report_content") {
			t.Fatal("explicit legacy fields must not acquire the report-content contract")
		}
	}
	calls := 0
	execute := func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
		calls++
		jointGuideExecution(ctx, spec.Task)
		return SubAgentResult{Output: jointGuideJudgment(t, "AGREE", "VALID", jointGuideAudit(solution, "IN_SCOPE"))}, nil
	}
	_, _, _, _, _, err := NewSolveSkill(execute, nil).verifySolutionWithGuides(WithCallerOwnedExecution(t.Context()), input.Problem, solution, input.Candidate, "", input.ParentGuides)
	if err != nil || calls != 1 {
		t.Fatalf("valid explicit seven-field audit must not add a call: calls=%d err=%v", calls, err)
	}
}

func TestSolveGuideAuditFormatRecoveryKeepsKnownAndUnknownBoundaries(t *testing.T) {
	const solution = "6×7=42张。\n答案：42张"
	for _, tc := range []struct {
		name, first, second string
		report, unknown     bool
		wantCalls           int
		wantFailure         bool
	}{
		{"legacy_seven", "seven", "", false, false, 1, false},
		{"report_eight", "eight", "", true, false, 1, false},
		{"known_missing_then_eight", "seven", "eight", true, false, 2, false},
		{"missing_stays_known_failure", "seven", "seven", true, false, 2, true},
		{"illegal_field_stays_known_failure", "illegal", "illegal", true, false, 2, true},
		{"negative_content_is_not_missing", "negative", "", true, false, 1, false},
		{"unknown_does_not_retry", "", "", true, true, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := guideAuditCandidate{SourceDigest: jointGuideDigest(solution), Solution: solution, Guide: json.RawMessage(`{"answer":"42张"}`)}
			if tc.report {
				candidate.RequiredFields = append(append([]string(nil), parentGuideFields[:]...), "report_content")
			}
			var tasks []string
			execute := func(ctx context.Context, spec SubAgentSpec) (SubAgentResult, error) {
				if spec.Agent != verifierAgentName {
					t.Fatalf("format recovery dispatched another generation: %s", spec.Agent)
				}
				tasks = append(tasks, spec.Task)
				if tc.unknown {
					return SubAgentResult{}, guideAuditPhysicalUnknown{}
				}
				kind := tc.first
				if len(tasks) == 2 {
					kind = tc.second
				}
				audit := jointGuideAudit(solution, "IN_SCOPE")
				fields := audit["fields"].([]map[string]any)
				if kind != "seven" {
					field, valid := "report_content", true
					if kind == "illegal" {
						field = "undeclared_report_field"
					}
					if kind == "negative" {
						valid = false
					}
					fields = append(fields, map[string]any{"field": field, "valid": valid, "reason": "controlled report-content audit"})
				}
				audit["fields"] = fields
				jointGuideExecution(ctx, spec.Task)
				return SubAgentResult{Output: jointGuideJudgment(t, "AGREE", "VALID", audit)}, nil
			}
			solver := NewSolveSkill(execute, nil)
			_, _, _, _, audits, err := solver.verifySolutionWithGuides(WithCallerOwnedExecution(t.Context()), "每盒有6张卡片，7盒一共有多少张？", solution, "42张", "", []guideAuditCandidate{candidate})
			if len(tasks) != tc.wantCalls || (err != nil) != tc.wantFailure {
				t.Fatalf("calls=%d want=%d err=%v", len(tasks), tc.wantCalls, err)
			}
			if len(tasks) == 2 && (tasks[0] == tasks[1] || !strings.Contains(tasks[1], strictFormatReminder)) {
				t.Fatal("the known format repair must use its distinct existing strict request identity")
			}
			if tc.unknown && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("unknown provider outcome was changed")
			}
			if tc.wantFailure && !tc.unknown && !errors.Is(err, egress.ErrProviderResponseProcessed) {
				t.Fatal("incomplete returned audit must remain a known response failure")
			}
			if tc.first == "negative" && parentGuideAuditVerdictForFields(audits, candidate.SourceDigest, candidate.RequiredFields) != "INVALID" {
				t.Fatal("a genuine content rejection must not become a valid or missing audit")
			}
		})
	}
}
