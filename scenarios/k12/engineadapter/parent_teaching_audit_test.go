package engineadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// 同一夹具也供真实模型审核使用；固定回执只验证候选传递和审核结果消费，不证明模型判断。
func TestParentTeachingGuideAuditPreservesCandidateAndSubstantiveVerdict(t *testing.T) {
	raw, err := os.ReadFile("testdata/parent_guide_road_work_audit.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Subject                string                      `json:"subject"`
		Grade                  string                      `json:"grade"`
		Problem                string                      `json:"problem"`
		VerifiedSolution       string                      `json:"verified_solution"`
		FrozenTeachingContract string                      `json:"frozen_teaching_contract"`
		Guide                  usecase.ParentTeachingGuide `json:"guide"`
		Cases                  []struct {
			Name                 string `json:"name"`
			ReplacementStepIndex int    `json:"replacement_step_index"`
			ReplacementStep      string `json:"replacement_step"`
			ExpectedVerdict      string `json:"expected_verdict"`
			InvalidField         string `json:"invalid_field"`
			Reason               string `json:"reason"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if err := usecase.ValidateSingleProblemReportDraft(fixture.Guide.ReportContent); err != nil {
		t.Fatalf("report fixture is incomplete: %v", err)
	}
	if len(fixture.Cases) != 2 || len(fixture.Guide.FullSolutionSteps) != 11 ||
		strings.Count(fixture.Guide.FullSolutionSteps[2], "。") < 2 ||
		strings.Count(fixture.Guide.FullSolutionSteps[10], "。") < 2 {
		t.Fatal("fixture must retain both the multi-sentence reasoning and substantive-error cases")
	}
	digest := sha256.Sum256([]byte(fixture.VerifiedSolution))
	sourceDigest := hex.EncodeToString(digest[:])
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			guide := fixture.Guide
			guide.FullSolutionSteps = append([]string(nil), guide.FullSolutionSteps...)
			if tc.ReplacementStep != "" {
				guide.FullSolutionSteps[tc.ReplacementStepIndex] = tc.ReplacementStep
			}
			before, err := json.Marshal(guide)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			a := NewSolveAdapter(nil)
			a.SetParentTeachingGuideAudit(func(_ context.Context, subject, prompt, gradeName string) (string, error) {
				calls++
				if subject != fixture.Subject || gradeName != fixture.Grade {
					t.Fatalf("audit changed subject or curriculum: %q/%q", subject, gradeName)
				}
				start := strings.Index(prompt, `{"problem":`)
				if start < 0 {
					t.Fatal("audit omitted the exact candidate facts")
				}
				var sent struct {
					Problem   string `json:"problem"`
					Candidate struct {
						SourceDigest string                      `json:"source_digest"`
						Solution     string                      `json:"solution"`
						Guide        usecase.ParentTeachingGuide `json:"parent_guide"`
					} `json:"candidate"`
				}
				if err := json.NewDecoder(strings.NewReader(prompt[start:])).Decode(&sent); err != nil {
					t.Fatal(err)
				}
				if sent.Problem != fixture.Problem || sent.Candidate.SourceDigest != sourceDigest ||
					sent.Candidate.Solution != fixture.VerifiedSolution || !reflect.DeepEqual(sent.Candidate.Guide, guide) {
					t.Fatal("audit changed the problem, source or candidate instead of evaluating it")
				}
				var fields []map[string]any
				for _, field := range []string{"answer", "full_solution_steps", "grade_level_method", "likely_mistakes", "parent_teaching_sequence", "follow_up_questions", "checking_method", "report_content"} {
					fields = append(fields, map[string]any{"field": field, "valid": field != tc.InvalidField, "reason": tc.Reason})
				}
				audits, err := json.Marshal([]map[string]any{{"source_digest": sourceDigest, "scope": "IN_SCOPE", "fields": fields}})
				if err != nil {
					t.Fatal(err)
				}
				return "PARENT_GUIDE_AUDITS: " + string(audits), nil
			})
			result, err := a.AuditParentTeachingGuide(t.Context(), usecase.ParentTeachingGuideAuditRequest{
				Request: usecase.ParentTeachingGuideRequest{
					Subject: fixture.Subject, Grade: fixture.Grade, Problem: fixture.Problem,
					VerifiedSolution: fixture.VerifiedSolution, FrozenTeachingContract: fixture.FrozenTeachingContract,
					ReportRequired: true,
				},
				Guide: guide, SourceSolutionDigest: sourceDigest,
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || result.Verdict != tc.ExpectedVerdict || result.SourceSolutionDigest != sourceDigest {
				t.Fatalf("audit result changed or resent: calls=%d result=%+v", calls, result)
			}
			after, err := json.Marshal(guide)
			if err != nil || string(after) != string(before) {
				t.Fatal("audit mutated the candidate")
			}
		})
	}
}
