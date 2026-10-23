package k12_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 夹具表示已读回的四题，其中两题待核对、一题未填但有解法、一题已判正确。
func teachingReportFixture() k12.TeachingReport {
	report := k12.TeachingReport{
		SchemaVersion: k12.TeachingReportSchemaVersion, ContentVersion: k12.TeachingReportContentVersion,
		Source:      k12.TeachingReportSource{Kind: k12.TeachingReportSourceGrading, SourceID: "grading-1", Revision: 1, Digest: "source-digest"},
		Metadata:    k12.TeachingReportMetadata{Title: "作业批改结果", AgentDisplayName: "数学辅导", GradeTerm: "六年级上册", Subject: "数学", ReportDate: "2026-10-23", Timezone: "Asia/Shanghai"},
		SourcePages: []k12.TeachingReportSourcePage{{AssetID: "page-1", AssetDigest: "annotated-digest", SourceDigest: "original-digest", MIME: "image/png", Width: 1800, Height: 900}},
	}
	rows := []struct {
		id, raw string
		state   k12.TeachingReportAnswerState
		status  k12.GradingAssessmentStatus
	}{
		{"q1", "2/5÷2=1/5", k12.TeachingReportAnswerPresent, k12.GradingAssessmentUntrusted},
		{"q2", "1/5×2\n1", k12.TeachingReportAnswerPresent, k12.GradingAssessmentUntrusted},
		{"q3", "", k12.TeachingReportAnswerBlank, k12.GradingAssessmentBlankSolved},
		{"q4", "2+2=4", k12.TeachingReportAnswerPresent, k12.GradingAssessmentCorrect},
	}
	for _, row := range rows {
		origin := k12.TeachingReportContentOrigin{ProblemID: row.id, InputRevision: 1, InputDigest: "input-" + row.id, SolutionDigest: "solution-" + row.id}
		report.ProblemSources = append(report.ProblemSources, k12.TeachingReportProblemSource{
			ProblemID: row.id, ParentProblemID: "parent-1", AttemptID: "attempt-" + row.id,
			InputRevision: 1, InputDigest: "input-" + row.id, AssessmentDigest: "assessment-" + row.id,
			SolutionDigest: "solution-" + row.id, AssessmentStatus: row.status,
			AnswerState: row.state, AnswerRaw: row.raw, AnswerCanonical: row.raw,
		})
		report.Items = append(report.Items, k12.TeachingReportItem{
			ProblemID: row.id, DisplayLabel: row.id, ShortTitle: "求所需数量",
			Assessment:     k12.TeachingReportAssessment{Status: row.status},
			OriginalAnswer: k12.TeachingReportOriginalAnswer{State: row.state, RawText: row.raw, CanonicalText: row.raw},
			AnswerAnalysis: k12.TeachingReportAnswerAnalysis{
				State:    k12.TeachingReportReferenceAvailable,
				Answers:  []k12.TeachingReportAnswer{{ID: "answer", Text: "12 TB", Origin: origin, Conditions: []string{"已用空间是总容量的五分之二"}}},
				Steps:    []k12.TeachingReportCalculationStep{{ID: "calculate", LaTeX: `4.8\div\frac{2}{5}=12\,(\mathrm{TB})`, Origin: origin}},
				Evidence: k12.TeachingReportReferenceEvidence{Verdict: "agree", EvidenceType: "heuristic", SolutionDigest: "solution-" + row.id},
			},
		})
	}
	report.Items[3].AnswerAnalysis.Answers[0].Text = "4"
	report.Items[3].AnswerAnalysis.Answers[0].Conditions = nil
	report.Items[3].AnswerAnalysis.Steps[0].LaTeX = "2+2=4"
	report.ProblemSources[0].AnswerSource = &k12.ProblemAnswerSource{Kind: k12.ProblemAnswerModel, FactsDigest: "facts-q1", InvocationID: "solve-q1"}
	report.Items[1].OriginalAnswer.Slots = []k12.TeachingReportAnswerSlot{
		{ID: "left", Label: "左空", State: k12.TeachingReportAnswerPresent, Text: "1/5×2", EvidenceRef: "left-region"},
		{ID: "right", Label: "右空", State: k12.TeachingReportAnswerPresent, Text: "1", EvidenceRef: "right-region"},
		{ID: "calculation", Label: "列式", State: k12.TeachingReportAnswerBlank, EvidenceRef: "calculation-region"},
	}
	report.DeepSections = []k12.TeachingReportDeepSection{{
		ID: "fraction", Title: "比较份数与容量", ProblemIDs: []string{"q1", "q2", "q3"},
		Blocks: []k12.TeachingReportExplanationBlock{{ID: "equal-parts", Text: "先找到已用的两份，再求五份。", Origins: []k12.TeachingReportContentOrigin{report.Items[0].AnswerAnalysis.Answers[0].Origin}}},
		Diagrams: []k12.TeachingDiagramAsset{{
			AssetID: "diagram-1", AssetDigest: "diagram-digest", SpecDigest: "diagram-spec", RendererVersion: "diagram-v1",
			ProblemID: "q1", InputRevision: 1, ProblemInputDigest: "input-q1", SolutionDigest: "solution-q1",
			MIME: "image/svg+xml", Width: 800, Height: 200, AltText: "五份中已用两份",
		}},
		MethodNotes: []k12.TeachingReportMethodNote{{Name: "数形结合", Description: "用等分图对应数量。", BlockIDs: []string{"equal-parts"}, EvidenceRef: "teaching-receipt-1"}},
	}}
	return report
}

func TestTeachingReportPreservesAllProblemsAndAnswerStates(t *testing.T) {
	input := teachingReportFixture()
	report, err := k12.NewTeachingReport(input)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, item := range report.Items {
		ids = append(ids, item.ProblemID)
	}
	if !reflect.DeepEqual(ids, []string{"q1", "q2", "q3", "q4"}) {
		t.Fatalf("source problem coverage changed: %v", ids)
	}
	if report.Items[0].Assessment.Status != k12.GradingAssessmentUntrusted || report.Items[1].Assessment.Status != k12.GradingAssessmentUntrusted ||
		report.Items[2].Assessment.Status != k12.GradingAssessmentBlankSolved || report.Items[3].Assessment.Status != k12.GradingAssessmentCorrect {
		t.Fatal("report changed the source grading states")
	}
	if report.Items[1].OriginalAnswer.RawText != "1/5×2\n1" || report.Items[1].OriginalAnswer.Slots[1].Text != "1" ||
		report.Items[1].OriginalAnswer.Slots[2].State != k12.TeachingReportAnswerBlank {
		t.Fatal("multi-slot student answer was lost or reinterpreted")
	}
	if report.Items[2].OriginalAnswer.State != k12.TeachingReportAnswerBlank || report.Items[2].OriginalAnswer.RawText != "" {
		t.Fatal("blank student answer was filled with the reference solution")
	}
	for _, mutate := range []func(*k12.TeachingReport){
		func(r *k12.TeachingReport) { r.Items = r.Items[:3] },
		func(r *k12.TeachingReport) { r.Items[0].Assessment.Status = k12.GradingAssessmentWrong },
		func(r *k12.TeachingReport) { r.Items[1].OriginalAnswer.RawText = "12 TB" },
	} {
		changed := input.Clone()
		mutate(&changed)
		if _, err := k12.NewTeachingReport(changed); !errors.Is(err, k12.ErrTeachingReportInvalid) {
			t.Fatalf("source fact change was accepted: %v", err)
		}
	}
	t.Run("solve-only result preserves observed handwriting", func(t *testing.T) {
		solveOnly := teachingReportFixture()
		solveOnly.ProblemSources[2].AnswerState = k12.TeachingReportAnswerPresent
		solveOnly.ProblemSources[2].AnswerRaw = "5÷2"
		solveOnly.ProblemSources[2].AnswerCanonical = "5÷2"
		solveOnly.Items[2].OriginalAnswer = k12.TeachingReportOriginalAnswer{
			State: k12.TeachingReportAnswerPresent, RawText: "5÷2", CanonicalText: "5÷2",
		}
		result, err := k12.NewTeachingReport(solveOnly)
		if err != nil {
			t.Fatal(err)
		}
		if result.Items[2].Assessment.Status != k12.GradingAssessmentBlankSolved || result.Items[2].OriginalAnswer.RawText != "5÷2" {
			t.Fatal("solve-only result changed the observed handwriting or invented a grading verdict")
		}
	})

	t.Run("missing reference and missing student answer remain explicit", func(t *testing.T) {
		missing := teachingReportFixture()
		missing.Source.Kind = k12.TeachingReportSourceTextSolve
		missing.ProblemSources = missing.ProblemSources[:1]
		missing.Items = missing.Items[:1]
		missing.DeepSections = nil
		missing.SourcePages = nil
		missing.ProblemSources[0].AttemptID = ""
		missing.ProblemSources[0].AssessmentStatus = ""
		missing.ProblemSources[0].AssessmentDigest = ""
		missing.ProblemSources[0].AnswerState = k12.TeachingReportAnswerNotProvided
		missing.ProblemSources[0].AnswerRaw = ""
		missing.ProblemSources[0].AnswerCanonical = ""
		missing.ProblemSources[0].SolutionDigest = ""
		missing.ProblemSources[0].AnswerSource = nil
		missing.Items[0].Assessment = k12.TeachingReportAssessment{Reason: "No student answer was supplied."}
		missing.Items[0].OriginalAnswer = k12.TeachingReportOriginalAnswer{State: k12.TeachingReportAnswerNotProvided}
		missing.Items[0].AnswerAnalysis = k12.TeachingReportAnswerAnalysis{State: k12.TeachingReportReferenceUnavailable, UnavailableReason: "The required quantity is missing."}
		result, err := k12.NewTeachingReport(missing)
		if err != nil {
			t.Fatal(err)
		}
		if result.Items[0].Assessment.Status != "" || result.Items[0].OriginalAnswer.State != k12.TeachingReportAnswerNotProvided || len(result.Items[0].AnswerAnalysis.Answers) != 0 {
			t.Fatal("report fabricated an assessment or reference answer")
		}
	})
}

func TestTeachingReportRejectsCrossProblemAndRevisionContent(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*k12.TeachingReport)
	}{
		{"answer from another problem", func(r *k12.TeachingReport) {
			r.Items[0].AnswerAnalysis.Answers[0].Origin = r.Items[1].AnswerAnalysis.Answers[0].Origin
		}},
		{"answer from another solution", func(r *k12.TeachingReport) {
			r.Items[0].AnswerAnalysis.Answers[0].Origin.SolutionDigest = "other-solution"
		}},
		{"step from an old revision", func(r *k12.TeachingReport) { r.Items[0].AnswerAnalysis.Steps[0].Origin.InputRevision = 2 }},
		{"step from different input", func(r *k12.TeachingReport) { r.Items[0].AnswerAnalysis.Steps[0].Origin.InputDigest = "different-input" }},
		{"explanation outside its group", func(r *k12.TeachingReport) {
			r.DeepSections[0].Blocks[0].Origins[0] = r.Items[3].AnswerAnalysis.Answers[0].Origin
		}},
		{"explanation from an old revision", func(r *k12.TeachingReport) { r.DeepSections[0].Blocks[0].Origins[0].InputRevision = 2 }},
		{"diagram from an old revision", func(r *k12.TeachingReport) { r.DeepSections[0].Diagrams[0].InputRevision = 2 }},
		{"diagram from another solution", func(r *k12.TeachingReport) { r.DeepSections[0].Diagrams[0].SolutionDigest = "other-solution" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := teachingReportFixture()
			tc.mutate(&report)
			if _, err := k12.NewTeachingReport(report); !errors.Is(err, k12.ErrTeachingReportInvalid) {
				t.Fatalf("mismatched content was accepted: %v", err)
			}
		})
	}
}

func TestTeachingReportSnapshotDoesNotAliasInput(t *testing.T) {
	input := teachingReportFixture()
	input.DeepSections[0].Blocks = append(input.DeepSections[0].Blocks, k12.TeachingReportExplanationBlock{
		ID: "equivalent-fillings", Origins: []k12.TeachingReportContentOrigin{input.Items[1].AnswerAnalysis.Answers[0].Origin},
		Table: &k12.TeachingReportTable{
			Headers: []k12.TeachingReportTableCell{{Text: "比较标准"}, {Text: "左空"}, {Text: "右空"}},
			Rows:    [][]k12.TeachingReportTableCell{{{Text: "总容量看作一"}, {LaTeX: `\frac{2}{5}`}, {LaTeX: "1"}}},
		},
	})
	snapshot, err := k12.NewTeachingReport(input)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	input.ProblemSources[0].AnswerSource.InvocationID = "changed"
	input.ProblemSources[0].AnswerRaw = "changed"
	input.SourcePages[0].AssetID = "changed"
	input.Items[1].OriginalAnswer.Slots[1].Text = "changed"
	input.Items[0].AnswerAnalysis.Answers[0].Conditions[0] = "changed"
	input.Items[0].AnswerAnalysis.Steps[0].LaTeX = "changed"
	input.DeepSections[0].ProblemIDs[0] = "changed"
	input.DeepSections[0].Blocks[0].Origins[0].InputDigest = "changed"
	input.DeepSections[0].Diagrams[0].AssetID = "changed"
	input.DeepSections[0].MethodNotes[0].BlockIDs[0] = "changed"
	input.DeepSections[0].Blocks[1].Table.Headers[0].Text = "changed"
	input.DeepSections[0].Blocks[1].Table.Rows[0][1].LaTeX = "changed"
	after, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("input mutation changed the constructed report snapshot")
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("snapshot became invalid after input mutation: %v", err)
	}
}
