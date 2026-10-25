package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestPrintedFractionFramesKeepIndependentReferenceWithoutGradingInk(t *testing.T) {
	stem := `小明\(\frac{2}{3}\)小时走了2千米，照这样的速度，他每小时行走多少千米？` + "\n"
	graph := `图：1小时走了？km；\(\frac{1}{3}\)小时走了？km；\(\frac{2}{3}\)小时走了2km。` + "\n"
	body := `从上图看出：已知（ ）份是2千米，可以先求出这样的1份是多少，也就是\(2\div2=2\times\frac{\square}{\square}\)；再求这样的（ ）份是多少，也就是\(2\times\frac{\square}{\square}\times3\)。由此推算出：` + "\n" + `\(2\div\frac{2}{3}=2\div2\times3=2\times\frac{1}{2}\times3=2\times（\frac{1}{2}\times3）=2\times\frac{3}{2}=3\)（km）。推算过程中“\(2\times\frac{1}{2}\times3=2\times（\frac{1}{2}\times3）\)”的依据是（ ）。`
	raw := stem + graph + body
	other := strings.NewReplacer("图：", "图中标注：", `\div`, "÷", `\times`, "×", `\square`, "□").Replace(raw)
	confidence := 0.97
	q := RecognizedQuestion{Subject: "数学", RawTranscription: raw, CanonicalMarkdown: adapter.NormalizeMathText(raw), Question: adapter.NormalizeMathText(raw), RecognitionConfidence: &confidence,
		AnswerState: AnswerStatePresent, StudentAnswer: "2/3\n1/2\n3\n1/2", AnswerRawTranscription: "2/3\n1/2\n3\n1/2", AnswerCanonicalMarkdown: "2/3\n1/2\n3\n1/2",
		EvidenceTranscriptions: []string{other, raw}, AnswerEvidenceTranscriptions: []string{"2/3\n1/2\n3\n1/2", "2/3\n1/2\n3\n1/2"},
		OCRSignals:          []string{"末尾“依据是”后的填写区域被不透明遮挡物覆盖，无法确认其下是否有手写答案或转录其内容。"},
		ConfirmationReasons: []OCRRiskReason{OCRRiskEvidenceConflict, OCRRiskLowConfidence, OCRRiskUnclearHandwriting},
		ImageSourceContext:  &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1, Action: "grade", ScopeDigest: "frozen-scope", TargetIDs: []string{"current"}, Materials: []k12.RecognitionImageScopeDependencyV1{{Text: `1小时走了？km；\(\frac{1}{3}\)小时走了？km；\(\frac{2}{3}\)小时走了2km。`}}},
	}
	if err := json.Unmarshal([]byte(`{"recoveries":[{"gap":{"order":1,"kind":"handwriting","before":"的依据是（","after":"）。"},"state":"no_verified_bound_textbook"}]}`), q.ImageSourceContext); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(q)
	wideBlank := strings.ReplaceAll(other, `\frac{□}{□}`, `\frac{　}{　}`)
	escapedBlank := strings.ReplaceAll(other, `\frac{□}{□}`, `\frac{\ }{\ }`)
	escapedBlank = strings.ReplaceAll(escapedBlank, "推算过程中", "计算过程中")
	coveredAnswer := strings.Replace(raw, "的依据是（ ）。", "的依据是【遮挡】。", 1)
	for _, tc := range []struct {
		name       string
		reading    string
		raw        string
		printedGap bool
		eligible   bool
	}{
		{"same printed question", other, "", false, true},
		{"fullwidth empty fraction frame", wideBlank, "", false, true},
		{"escaped empty fraction frame and calculation caption", escapedBlank, "", false, true},
		{"covered handwriting is not a missing printed condition", wideBlank, coveredAnswer, false, true},
		{"a missing printed condition remains unresolved", wideBlank, coveredAnswer, true, false},
		{"different distance", strings.Replace(other, "走了2千米", "走了3千米", 1), "", false, false},
		{"different unit", strings.Replace(other, "走了2千米", "走了2米", 1), "", false, false},
		{"different operator", strings.Replace(other, "2÷2", "2×2", 1), "", false, false},
		{"different negation", strings.Replace(other, "可以先求出", "不可以先求出", 1), "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := q
			if tc.raw != "" {
				current.RawTranscription = tc.raw
				current.CanonicalMarkdown, current.Question = adapter.NormalizeMathText(tc.raw), adapter.NormalizeMathText(tc.raw)
			}
			if tc.printedGap {
				copy := *q.ImageSourceContext
				copy.Recoveries = append([]k12.RecognitionImageSourceRecoveryV1(nil), copy.Recoveries...)
				copy.Recoveries[0].Gap.Kind = "condition"
				current.ImageSourceContext = &copy
			}
			current.EvidenceTranscriptions = []string{tc.reading, current.RawTranscription}
			if got := independentReferenceAnswerEligible(PhotoGradeRequest{InitialReadMode: k12.RecognitionLayoutManifestWithContentV1}, current); got != tc.eligible {
				t.Fatalf("reference eligibility=%t, want %t; printed conflict=%t", got, tc.eligible, questionEvidenceTranscriptionsConflict(current))
			}
		})
	}
	after, _ := json.Marshal(q)
	if string(after) != string(before) {
		t.Fatal("reference evaluation changed the observed work")
	}
}

func TestCompleteSequentialWorkReadingsPreserveConditions(t *testing.T) {
	raw := "一项工程，甲单独20天完成，乙单独12天完成。这项工程先由甲做了若干天，然后由乙单独完成余下工作，从头至尾共用14天完成，那么甲乙各工作几天？"
	other := strings.NewReplacer("余下", "剩下", "共用", "只用", "那么", "求").Replace(raw)
	for _, tc := range []struct {
		name, reading string
		equal         bool
	}{
		{"equivalent wording", other, true}, {"different duration", strings.Replace(other, "14天", "15天", 1), false},
		{"different unit", strings.Replace(other, "14天", "14小时", 1), false}, {"different sequence", strings.Replace(other, "先由甲", "先由乙", 1), false},
		{"extra condition", other + "甲不能工作5天。", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prior := RecognizedQuestion{Subject: "数学", RawTranscription: raw, CanonicalMarkdown: raw, AnswerState: AnswerStateBlank, EvidenceTranscriptions: []string{raw, raw}}
			review := prior
			review.RawTranscription, review.CanonicalMarkdown = tc.reading, tc.reading
			review.EvidenceTranscriptions = []string{raw, tc.reading}
			got, _ := RecognitionSourceReadingsMatchForInitialReadMode(prior, review, k12.RecognitionLayoutManifestWithContentV1)
			if got != tc.equal {
				t.Fatalf("source equality=%t, want %t", got, tc.equal)
			}
		})
	}
}

func TestReportDraftAcceptsEmptyOptionalFieldsButRejectsTwoValues(t *testing.T) {
	draft := &TeachingReportContentDraft{Version: TeachingReportContentContractVersion, Items: []TeachingReportDraftItem{{ProblemID: "current", ShortTitle: "总容量", Answers: []TeachingReportDraftAnswer{{ID: "answer", LaTeX: `12\mathrm{TB}`}}}}, Sections: []TeachingReportDraftSection{{ID: "section", Title: "份数与容量", ProblemIDs: []string{"current"}, Blocks: []TeachingReportDraftBlock{{ID: "relation", ProblemIDs: []string{"current"}, LaTeX: `4.8\div\frac25=12`}}}}}
	raw, err := json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	var restored TeachingReportContentDraft
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err = ValidateSingleProblemReportDraft(&restored); err != nil {
		t.Fatalf("host serialization rejected a valid mathematical value: %v", err)
	}
	restored.Items[0].Answers[0].Text = "a second answer"
	if ValidateSingleProblemReportDraft(&restored) == nil {
		t.Fatal("two nonempty representations were accepted")
	}
}

func TestRejectedReportAuditReusesCandidateAndPreservesOriginalReceipt(t *testing.T) {
	o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{{Question: "1+1=", Subject: "数学", StudentAnswer: "3", AnswerState: AnswerStatePresent}}, &itemResumeSolver{calls: map[string]int{}}, &itemResumeGrader{calls: map[string]int{}})
	jobID := runItemResumeJobToAssessing(t, o, "report-audit-replay")
	run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	ctx := withGradingPhysicalCallExecutor(context.Background(), newDurableGradingPhysicalCallExecutor(o, job, run.questions[0]))
	generated, audits := 0, 0
	candidate := GradingPhysicalCallSpec{Operation: k12.GradingItemOperationParentGuide, RequestDigest: modelInvocationDigest([]byte("same-candidate"))}
	audit := GradingPhysicalCallSpec{Operation: k12.GradingItemOperationParentGuide, RequestDigest: modelInvocationDigest([]byte("same-audit")), ReplayResultFailure: func(raw string) error {
		var a ParentTeachingGuideAudit
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			return err
		}
		if a.Verdict != "VALID" {
			return fmt.Errorf("audit rejected")
		}
		return nil
	}}
	generate := func(context.Context) (string, error) { generated++; return `{"candidate":"unchanged"}`, nil }
	review := func(context.Context) (string, error) {
		audits++
		if audits == 1 {
			return `{"verdict":"INVALID","output":"format rejected"}`, nil
		}
		return `{"verdict":"VALID","output":"accepted"}`, nil
	}
	first, err := ExecuteGradingPhysicalCall(ctx, candidate, generate)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := ExecuteGradingPhysicalCall(ctx, audit, review)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ExecuteGradingPhysicalCall(ctx, audit, review); err == nil || audits != 1 {
		t.Fatal("same generation retried a rejected audit")
	}
	job.Fields.AttemptCount++
	retry := withGradingPhysicalCallExecutor(context.Background(), newDurableGradingPhysicalCallExecutor(o, job, run.questions[0]))
	reused, err := ExecuteGradingPhysicalCall(retry, candidate, generate)
	if err != nil || reused.Payload != first.Payload || generated != 1 {
		t.Fatalf("candidate was regenerated: count=%d err=%v", generated, err)
	}
	accepted, err := ExecuteGradingPhysicalCall(retry, audit, review)
	if err != nil || audits != 2 || accepted.InvocationID == rejected.InvocationID {
		t.Fatalf("new retry did not re-audit: count=%d err=%v", audits, err)
	}
	old, err := o.deps.Records.GetGradingItemInvocation(context.Background(), job.Record.AgentName, rejected.InvocationID)
	if err != nil || old.Status != k12.ModelInvocationSucceeded || old.ResultJSON != rejected.Payload {
		t.Fatalf("original physical success and rejected audit changed: err=%v", err)
	}
}
