package usecase

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func unclearHandwritingQuestion() RecognizedQuestion {
	return RecognizedQuestion{
		Question: "4.5×2=", RawTranscription: "4.5×2=", CanonicalMarkdown: "4.5×2=",
		Subject: "数学", AnswerState: AnswerStateUnclear,
		AnswerRawTranscription: "[不清]", RecognitionConfidence: float64Ptr(.99),
		EvidenceTranscriptions:       []string{"4.5×2=", "4.5×2="},
		AnswerEvidenceTranscriptions: []string{"[不清]", "[不清]"},
		OCRSignals:                   []string{"unclear_handwriting"},
	}
}

func TestUnclearHandwritingStillProvidesIndependentAnswerWithoutJudgment(t *testing.T) {
	ctx := context.Background()
	solver := &itemResumeSolver{calls: map[string]int{}, solutions: map[string]string{"4.5×2=": "4.5×2=9\n答案：9"}}
	grader := &itemResumeGrader{calls: map[string]int{}}
	o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{unclearHandwritingQuestion()}, solver, grader)
	jobID := runItemResumeJobToAssessing(t, o, "unclear-independent-answer")
	run := o.lookup(jobID)
	job, err := o.deps.GetGradingJob(ctx, run.agentName, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = o.autoFreezeRecognition(ctx, run, job); err != nil {
		t.Fatal(err)
	}
	if _, err = o.RunGradingJob(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	waitForStage(t, o.deps, run.agentName, jobID, k12.GradingStageCompleted)
	items, err := o.deps.Records.ListGradingAssessmentItems(ctx, run.agentName, jobID)
	if err != nil || len(items) != 1 {
		t.Fatalf("receipts=%+v err=%v", items, err)
	}
	r := items[0]
	var item PhotoGradeItem
	if err = json.Unmarshal([]byte(r.ResultJSON), &item); err != nil {
		t.Fatal(err)
	}
	if r.Status != k12.GradingAssessmentAnswerUnclear || item.Status != PhotoAnswerUnclear ||
		item.ParentGuide == nil || item.ParentGuide.Answer != "9" || len(item.ParentGuide.FullSolutionSteps) == 0 {
		t.Fatalf("清楚题干必须提供独立答案和完整讲法，同时保留未判: %+v", item)
	}
	if r.SolveInvocationID == "" || r.ParentGuideInvocationID == "" || r.GradeInvocationID != "" ||
		r.ProjectionCreated || r.ProjectionRecordID != "" || grader.callCount("4.5×2=") != 0 || solver.callCount("4.5×2=") != 1 {
		t.Fatalf("未判题只能求解一次，禁止学生判分和学习副作用: %+v", r)
	}
	if err = r.ValidateTerminalParentGuideReference(); err != nil {
		t.Fatal(err)
	}
	bad := r
	bad.GradeInvocationID = "unexpected-grade"
	if err = bad.Validate(); err == nil {
		t.Fatal("未判题不得引用学生判分")
	}
	bad = r
	bad.ParentGuideInvocationID = ""
	if err = bad.ValidateTerminalParentGuideReference(); err == nil {
		t.Fatal("独立答案必须同时有讲题凭据")
	}
	// 历史未判回执没有新增求解/讲法，仍保持同题身份并可只读重放。
	old := r
	old.SolveInvocationID, old.ParentGuideInvocationID = "", ""
	item.ParentGuide, item.Solve = nil, SolveHomeworkResult{}
	raw, _ := json.Marshal(gradingAssessmentCanonicalResult(item))
	old.ResultJSON, old.ResultDigest = string(raw), modelInvocationDigest(raw)
	if _, err = replayGradingAssessmentItem(item.Recognized, old); err != nil {
		t.Fatalf("旧未判回执不应失效: %v", err)
	}
}

func TestUnclearReferenceAnswerDoesNotGuessPrintedFactsOrPracticeMatch(t *testing.T) {
	for _, scenario := range []string{"clear", "printed conflict", "low confidence", "missing confidence", "missing condition", "unmatched practice"} {
		t.Run(scenario, func(t *testing.T) {
			solver := &itemResumeSolver{calls: map[string]int{}, solutions: map[string]string{"4.5×2=": "4.5×2=9\n答案：9"}}
			grader := &itemResumeGrader{calls: map[string]int{}}
			o := newItemResumeOrchestrator(t, t.TempDir(), nil, solver, grader)
			q := unclearHandwritingQuestion()
			req := PhotoGradeRequest{AgentName: "mingming", Grade: "五年级上", SourceUncertaintyFinalized: true}
			switch scenario {
			case "printed conflict":
				q.EvidenceTranscriptions[1] = "4.5÷2="
			case "low confidence":
				q.RecognitionConfidence = float64Ptr(.60)
			case "missing confidence":
				q.RecognitionConfidence = nil
			case "missing condition":
				q.parentSourceUnclear = true
			case "unmatched practice":
				req.PracticeReferences = []PracticeGradingReference{{ItemID: "another-item"}}
			}
			item, err := o.deps.assessPhotoItem(context.Background(), req, PhotoModeGrade, q)
			if err != nil {
				t.Fatal(err)
			}
			wantAnswer := scenario == "clear"
			if item.Status != PhotoAnswerUnclear || (item.ParentGuide != nil) != wantAnswer ||
				grader.callCount("4.5×2=") != 0 || (solver.callCount("4.5×2=") > 0) != wantAnswer {
				t.Fatalf("题干与学生作答风险必须分离，不能猜条件或判学生答案: %+v", item)
			}
		})
	}
}
