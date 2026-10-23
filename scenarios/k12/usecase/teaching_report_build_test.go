package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 模型替身只补充其返回解法的内容摘要，回执仍经正式任务及存储提交生成。
type teachingReportFixtureSolver struct {
	delegate *itemResumeSolver
}

func (s teachingReportFixtureSolver) Solve(ctx context.Context, problem, grade, constraint string) (SolveResult, error) {
	result, err := s.delegate.Solve(ctx, problem, grade, constraint)
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256([]byte(result.Solution))
	result.Evidence.SolverOutputDigest = hex.EncodeToString(digest[:])
	return result, nil
}

// 夹具走真实SQLite、识题确认、逐题执行及回执冻结；外部识题/模型使用既有替身。
func teachingReportBuildFixture(t *testing.T) GradingTeachingReportInput {
	t.Helper()
	solver := &itemResumeSolver{calls: map[string]int{}}
	grader := &itemResumeGrader{
		calls: map[string]int{},
		outcomes: map[string]GradeOutcome{
			"3-1=": {Verdict: VerdictUnverifiable},
		},
	}
	o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{
		{Question: "1+1=", Subject: "数学", StudentAnswer: "2", AnswerState: AnswerStatePresent},
		{Question: "3-1=", Subject: "数学", StudentAnswer: "3-1=1", AnswerState: AnswerStatePresent},
		{Question: "4-2=", Subject: "数学", AnswerState: AnswerStateBlank},
	}, solver, grader)
	o.deps.Solver = teachingReportFixtureSolver{delegate: solver}
	jobID := runItemResumeJobToAssessing(t, o, "teaching-report-source")
	completed, err := o.ConfirmAndRun(context.Background(), jobID, nil)
	if err != nil || completed.Record.Status != k12.GradingStageCompleted {
		t.Fatalf("freeze source grading task: stage=%s err=%v", completed.Record.Status, err)
	}
	run, err := o.ensureRun(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	assessments, err := o.deps.Records.ListEffectiveGradingAssessments(context.Background(), "mingming", jobID)
	if err != nil {
		t.Fatal(err)
	}
	input := GradingTeachingReportInput{
		AgentName: "mingming",
		Source: k12.TeachingReportSource{
			Kind: k12.TeachingReportSourceGrading, SourceID: jobID,
			Revision: completed.Record.Version, Digest: CanonicalRecognizedQuestionsDigest(run.questions),
		},
		Metadata:  k12.TeachingReportMetadata{Title: "作业批改结果", AgentDisplayName: "数学辅导", GradeTerm: "五年级上", Subject: "数学", ReportDate: "2026-10-23", Timezone: "Asia/Shanghai"},
		Questions: cloneRecognizedQuestions(run.questions), Assessments: assessments,
	}
	for _, question := range input.Questions {
		if question.ProblemKind == ProblemKindCompoundParent {
			continue
		}
		// 本组替身均返回数字2，独立的报告内容只引用已冻结的对应解法摘要。
		digest := sha256.Sum256([]byte("2"))
		origin := k12.TeachingReportContentOrigin{
			ProblemID: question.ProblemID, InputRevision: question.ConfirmedVersion,
			InputDigest: question.InputDigest, SolutionDigest: hex.EncodeToString(digest[:]),
		}
		input.Content = append(input.Content, TeachingReportQuestionContent{
			ProblemID: question.ProblemID, ShortTitle: "计算结果",
			AnswerAnalysis: k12.TeachingReportAnswerAnalysis{
				State:   k12.TeachingReportReferenceAvailable,
				Answers: []k12.TeachingReportAnswer{{ID: "result", Text: "2", Origin: origin}},
				Steps:   []k12.TeachingReportCalculationStep{{ID: "calculation", LaTeX: question.Question + "2", Origin: origin}},
				// 教学内容提供的证据必须被实际回执证据覆盖，不能成为信任来源。
				Evidence: k12.TeachingReportReferenceEvidence{Verdict: "disagree", EvidenceType: "none", ReceiptRef: "untrusted-content-reference", SolutionDigest: "untrusted-content-digest"},
			},
		})
	}
	input.DeepSections = []k12.TeachingReportDeepSection{{
		ID: "check", Title: "检查计算结果", ProblemIDs: []string{input.Questions[0].ProblemID},
		Blocks: []k12.TeachingReportExplanationBlock{{
			ID: "check-first", Text: "用两个一相加检查结果。",
			Origins: []k12.TeachingReportContentOrigin{input.Content[0].AnswerAnalysis.Answers[0].Origin},
		}},
	}}
	return input
}

func TestBuildGradingTeachingReportPreservesFrozenFacts(t *testing.T) {
	input := teachingReportBuildFixture(t)
	report, err := BuildGradingTeachingReport(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Items) != 3 {
		t.Fatalf("report lost a source question: %d", len(report.Items))
	}
	wantStates := []k12.GradingAssessmentStatus{k12.GradingAssessmentCorrect, k12.GradingAssessmentUntrusted, k12.GradingAssessmentBlankSolved}
	wantRaw := []string{"2", "3-1=1", ""}
	for index, item := range report.Items {
		if item.ProblemID != input.Questions[index].ProblemID || item.Assessment.Status != wantStates[index] || item.OriginalAnswer.RawText != wantRaw[index] {
			t.Fatalf("source fact changed for question %d: %+v", index, item)
		}
		var receipt k12.GradingAssessmentItem
		for _, candidate := range input.Assessments {
			if candidate.ProblemID == item.ProblemID {
				receipt = candidate
				break
			}
		}
		if item.AnswerAnalysis.Evidence.Verdict != "agree" || item.AnswerAnalysis.Evidence.EvidenceType != "numeric_exec" ||
			item.AnswerAnalysis.Evidence.ReceiptRef != receipt.SolveInvocationID ||
			report.ProblemSources[index].AssessmentDigest != receipt.ResultDigest {
			t.Fatal("report did not copy the frozen solve and assessment evidence")
		}
	}
	if report.Items[2].OriginalAnswer.State != k12.TeachingReportAnswerBlank {
		t.Fatal("reference solution was mistaken for a student answer")
	}
	if !reflect.DeepEqual(input.Content[0].AnswerAnalysis.Evidence, k12.TeachingReportReferenceEvidence{
		Verdict: "disagree", EvidenceType: "none", ReceiptRef: "untrusted-content-reference", SolutionDigest: "untrusted-content-digest",
	}) {
		t.Fatal("builder mutated the supplied teaching content")
	}
	missing := input
	missing.Assessments = append([]k12.GradingAssessmentItem(nil), input.Assessments[1:]...)
	if _, err := BuildGradingTeachingReport(missing); !errors.Is(err, ErrTeachingReportContentRequired) {
		t.Fatalf("missing assessment was silently omitted: %v", err)
	}
}

func TestBuildGradingTeachingReportRejectsMismatchedSources(t *testing.T) {
	input := teachingReportBuildFixture(t)
	cases := []struct {
		name   string
		mutate func(*GradingTeachingReportInput)
	}{
		{"another agent", func(in *GradingTeachingReportInput) { in.Assessments[0].AgentName = "other-agent" }},
		{"another job", func(in *GradingTeachingReportInput) { in.Assessments[0].JobID = "other-job" }},
		{"old input revision", func(in *GradingTeachingReportInput) { in.Assessments[0].InputRevision++ }},
		{"another attempt", func(in *GradingTeachingReportInput) { in.Assessments[0].AttemptID = "other-attempt" }},
		{"changed payload", func(in *GradingTeachingReportInput) { in.Assessments[0].ResultJSON += " " }},
		{"superseded assessment", func(in *GradingTeachingReportInput) {
			in.Assessments[0].CurrentDisposition = k12.GradingAssessmentDispositionSuperseded
		}},
		{"answer from another problem", func(in *GradingTeachingReportInput) {
			in.Content[0].AnswerAnalysis.Answers[0].Origin = in.Content[1].AnswerAnalysis.Answers[0].Origin
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := input
			changed.Assessments = append([]k12.GradingAssessmentItem(nil), input.Assessments...)
			changed.Content = append([]TeachingReportQuestionContent(nil), input.Content...)
			changed.Content[0].AnswerAnalysis.Answers = append([]k12.TeachingReportAnswer(nil), input.Content[0].AnswerAnalysis.Answers...)
			tc.mutate(&changed)
			if _, err := BuildGradingTeachingReport(changed); !errors.Is(err, k12.ErrTeachingReportInvalid) {
				t.Fatalf("mismatched source was accepted: %v", err)
			}
		})
	}
}

func TestBuildGradingTeachingReportSnapshotDoesNotAliasInput(t *testing.T) {
	input := teachingReportBuildFixture(t)
	report, err := BuildGradingTeachingReport(input)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	input.Questions[0].StudentAnswer = "changed"
	input.Assessments[0].ResultJSON = "changed"
	input.Content[0].AnswerAnalysis.Answers[0].Text = "changed"
	input.Content[0].AnswerAnalysis.Steps[0].LaTeX = "changed"
	input.DeepSections[0].ProblemIDs[0] = "changed"
	input.DeepSections[0].Blocks[0].Origins[0].InputDigest = "changed"
	after, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("input mutation changed the constructed report")
	}
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
}
