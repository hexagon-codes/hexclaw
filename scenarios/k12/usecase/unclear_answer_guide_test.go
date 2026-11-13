package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/mathtext"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestIndependentReferenceLatestAnswerOnlyObservation(t *testing.T) {
	raw, err := os.ReadFile("testdata/scoped-reference-adjudication.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Question        RecognizedQuestion `json:"question"`
		InitialReadMode string             `json:"initial_read_mode"`
		Physical        struct {
			Content string `json:"result_content"`
		} `json:"physical"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Items []struct {
			Recognition struct {
				Question   string   `json:"question"`
				OCRSignals []string `json:"ocr_signals"`
			} `json:"recognition"`
		} `json:"items"`
	}
	if err = json.Unmarshal([]byte(fixture.Physical.Content), &response); err != nil || len(response.Items) != 1 {
		t.Fatalf("invalid observed response: %v", err)
	}
	request := PhotoGradeRequest{InitialReadMode: fixture.InitialReadMode}
	if independentReferenceAnswerEligible(request, fixture.Question) {
		t.Fatal("older mixed printed risk unexpectedly passed")
	}
	current := cloneRecognizedQuestions([]RecognizedQuestion{fixture.Question})[0]
	current.OCRSignals = response.Items[0].Recognition.OCRSignals
	current.EvidenceTranscriptions = append(current.EvidenceTranscriptions, response.Items[0].Recognition.Question)
	if !independentReferenceAnswerEligible(request, current) {
		t.Fatalf("latest answer-only observation blocked reference: printed_conflict=%t signals=%q", questionEvidenceTranscriptionsConflictForInitialReadMode(current, request.InitialReadMode), current.OCRSignals)
	}
	for _, scenario := range []string{"latest clear question", "question denied", "ownership denied", "changed distance", "printed risk", "missing handwriting scope"} {
		t.Run(scenario, func(t *testing.T) {
			q := cloneRecognizedQuestions([]RecognizedQuestion{fixture.Question})[0]
			before, _ := json.Marshal(q)
			var envelope map[string]any
			if decodeErr := json.Unmarshal([]byte(fixture.Physical.Content), &envelope); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			verification := envelope["verification"].(map[string]any)
			observed := envelope["items"].([]any)[0].(map[string]any)["recognition"].(map[string]any)
			switch scenario {
			case "question denied":
				verification["question_complete_and_legible"] = false
			case "ownership denied":
				verification["target_ownership_confirmed"] = false
			case "changed distance":
				prior := observed["question"].(string)
				observed["question"] = strings.Replace(prior, "走了2千米", "走了3千米", 1)
				if observed["question"] == prior {
					t.Fatal("distance fixture did not change")
				}
			case "printed risk":
				observed["ocr_signals"] = []string{"印刷题干条件被遮挡，不能核实距离。"}
			case "missing handwriting scope":
				q.ImageSourceContext.Recoveries = nil
				before, _ = json.Marshal(q)
			}
			payload, encodeErr := json.Marshal(envelope)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			if got := independentReferenceFromAdjudication(request, q, string(payload)); got != (scenario == "latest clear question") {
				t.Fatalf("reference eligibility=%t", got)
			}
			after, _ := json.Marshal(q)
			if !bytes.Equal(before, after) {
				t.Fatal("frozen recognition observation changed")
			}
		})
	}
}

func TestParentGuidePreservesUnclearAnswerObservation(t *testing.T) {
	question := RecognizedQuestion{
		Question: "已用总容量的2/5，对应4.8 TB，求总容量。", Subject: "数学",
		AnswerState: AnswerStateUnclear, StudentAnswer: "?/5×2 1",
		AnswerRawTranscription: "[截断不清]/5×2\n1",
	}
	request := photoItemGradeRequest(PhotoGradeRequest{AgentName: "tutor", Grade: "五年级上"}, question)
	request.StudentAnswer = ""
	guide := (Deps{}).parentTeachingGuideRequest(t.Context(), request, SolveHomeworkResult{Solution: "答案：12 TB"}, GradeOutcome{})
	if guide.ObservedAnswerState != AnswerStateUnclear || guide.ObservedStudentAnswer != question.AnswerRawTranscription || guide.StudentAnswer != "" {
		t.Fatalf("independent solve lost original observation: %+v", guide)
	}
	// 新提示事实仍由冻结原题重建，不改变旧操作请求的序列化身份。
	before, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	request.ObservedAnswerState, request.ObservedStudentAnswer = "", ""
	after, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("observation projection changed durable request identity")
	}
}

func TestOwnDiagramLayoutKeepsIndependentReferenceEligible(t *testing.T) {
	data, err := os.ReadFile("testdata/scoped-reference-own-diagram.json")
	if err != nil {
		t.Fatal(err)
	}
	var original RecognizedQuestion
	if err := json.Unmarshal(data, &original); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"same quantities", "changed distance", "changed unit", "changed time", "changed operation", "split number", "split decimal", "missing scope"} {
		t.Run(scenario, func(t *testing.T) {
			q := cloneRecognizedQuestions([]RecognizedQuestion{original})[0]
			switch scenario {
			case "changed distance":
				q.EvidenceTranscriptions[0] = strings.Replace(q.EvidenceTranscriptions[0], "小时走了2 km", "小时走了3 km", 1)
			case "changed unit":
				q.EvidenceTranscriptions[0] = strings.Replace(q.EvidenceTranscriptions[0], "小时走了2 km", "小时走了2 m", 1)
			case "changed time":
				q.EvidenceTranscriptions[0] = strings.Replace(q.EvidenceTranscriptions[0], "1小时走了？km", "2小时走了？km", 1)
			case "changed operation":
				q.EvidenceTranscriptions[0] = strings.Replace(q.EvidenceTranscriptions[0], `2\div2`, `2\times2`, 1)
			case "split number":
				for _, view := range []*string{&q.Question, &q.RawTranscription, &q.CanonicalMarkdown, &q.EvidenceTranscriptions[1]} {
					*view = strings.Replace(*view, "1小时走了？km", "12小时走了？km", 1)
				}
				q.EvidenceTranscriptions[0] = strings.Replace(q.EvidenceTranscriptions[0], "1小时走了？km", "1 2小时走了？km", 1)
			case "split decimal":
				for _, view := range []*string{&q.Question, &q.RawTranscription, &q.CanonicalMarkdown, &q.EvidenceTranscriptions[1]} {
					*view = strings.Replace(*view, "1小时走了？km", "1.2小时走了？km", 1)
				}
				q.EvidenceTranscriptions[0] = strings.Replace(q.EvidenceTranscriptions[0], "1小时走了？km", "1 . 2小时走了？km", 1)
			case "missing scope":
				q.ImageSourceContext = nil
			}
			before, _ := json.Marshal(q)
			got := independentReferenceAnswerEligible(PhotoGradeRequest{InitialReadMode: k12.RecognitionLayoutManifestWithContentV1}, q)
			if want := scenario == "same quantities"; got != want {
				t.Fatalf("printed diagram reference eligibility = %t, want %t", got, want)
			}
			after, _ := json.Marshal(q)
			if string(before) != string(after) {
				t.Fatal("reference eligibility changed the frozen source observation")
			}
		})
	}
}

func TestOwnDiagramRowBoundaryPreservesDifferentNumbers(t *testing.T) {
	for _, tc := range []struct{ name, initial, review string }{
		{"integer boundary", "图示：\n1\n2米\n3米\n求总长。", "图示：\n12米\n3米\n求总长。"},
		{"decimal boundary", "图示：\n1.\n2米\n3米\n求总长。", "图示：\n1.2米\n3米\n求总长。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := RecognizedQuestion{Subject: "数学", Question: tc.review, RawTranscription: tc.review,
				CanonicalMarkdown: tc.review, AnswerState: AnswerStateBlank, RecognitionConfidence: float64Ptr(.99),
				EvidenceTranscriptions: []string{tc.initial, tc.review},
				ImageSourceContext: &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1,
					Action: "solve", ScopeDigest: "frozen-page", TargetIDs: []string{"target-1"}}}
			before, _ := json.Marshal(q)
			normalized := EvaluateOCRConfirmationRiskForInitialReadMode(q, k12.RecognitionLayoutManifestWithContentV1)
			if !normalized.ConfirmationRequired || !slices.Contains(normalized.ConfirmationReasons, OCRRiskEvidenceConflict) {
				t.Fatal("diagram projection erased a numeric token boundary")
			}
			if independentReferenceAnswerEligible(PhotoGradeRequest{InitialReadMode: k12.RecognitionLayoutManifestWithContentV1}, q) {
				t.Fatal("different printed numbers became eligible for an independent reference")
			}
			after, _ := json.Marshal(q)
			if !bytes.Equal(before, after) {
				t.Fatal("source comparison changed the frozen observation")
			}
		})
	}
}

func TestScopedPrintedNumberDifferenceKeepsIndependentReferenceEligible(t *testing.T) {
	observedJSON, err := os.ReadFile("testdata/scoped-reference-printed-q1.json")
	if err != nil {
		t.Fatal(err)
	}
	var observed RecognizedQuestion
	if err := json.Unmarshal(observedJSON, &observed); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"", k12.RecognitionLayoutManifestWithContentV1} {
		if !independentReferenceAnswerEligible(PhotoGradeRequest{InitialReadMode: mode}, observed) {
			t.Fatalf("actual printed source lost its reference answer: mode=%q canonical=%t confidence=%v conflict=%t normalized=%v", mode, CanonicalMarkdownValid(observed.CanonicalMarkdown), observed.RecognitionConfidence, questionEvidenceTranscriptionsConflictForInitialReadMode(observed, mode), NormalizeRecognizedQuestionForInitialReadMode(observed, mode).ConfirmationReasons)
		}
	}
	shared := "一个硬盘，已用空间是总容量的2/5，正好用了4.8 TB。"
	q := unclearHandwritingQuestion()
	q.Question, q.RawTranscription, q.CanonicalMarkdown = "（1）先求一份是多少，再求总容量是多少。", "（1）先求一份是多少，再求总容量是多少。", "（1）先求一份是多少，再求总容量是多少。"
	q.SourceNumberPath = []string{"3", "(1)"}
	q.EvidenceTranscriptions = []string{shared + "\n先求一份是多少，再求总容量是多少。", q.RawTranscription}
	q.AnswerEvidenceTranscriptions = []string{"1/5", "2/5"}
	q.ConfirmationReasons = []OCRRiskReason{OCRRiskEvidenceConflict}
	q.ImageSourceContext = &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1, Action: "grade", ScopeDigest: "sha256:" + strings.Repeat("a", 64), TargetIDs: []string{"target-1"}, Materials: []k12.RecognitionImageScopeDependencyV1{{ForOrders: []int{1}, Region: k12.SourcePixelRegion{Width: 100, Height: 100}, Text: shared}}}
	before, _ := json.Marshal(q)
	if !independentReferenceAnswerEligible(PhotoGradeRequest{}, q) {
		t.Fatal("a paired printed label difference blocked a reference answer")
	}
	request := photoItemGradeRequest(PhotoGradeRequest{}, q)
	if !strings.Contains(request.Problem, shared) || !strings.Contains(request.Problem, "ALL required reference answers") || strings.Contains(request.SourceProblem, "Current question and answer requirements") || !strings.Contains(request.SourceProblem, shared) {
		t.Fatal("current answer requirements or plain source projection were lost")
	}
	after, _ := json.Marshal(q)
	if string(before) != string(after) {
		t.Fatal("source comparison changed observations")
	}
	other := q
	other.SourceNumberPath = []string{"3", "(2)"}
	if independentReferenceAnswerEligible(PhotoGradeRequest{}, other) {
		t.Fatal("another printed number borrowed the reference eligibility")
	}
	q.RawTranscription, q.CanonicalMarkdown = "(1)+2=", "(1)+2="
	q.EvidenceTranscriptions = []string{shared + "\n+2=", q.RawTranscription}
	if independentReferenceAnswerEligible(PhotoGradeRequest{}, q) {
		t.Fatal("a mathematical operand was removed as a printed number")
	}
	q.SourceNumberPath = []string{"3", "(2)"}
	q.Question = "根据数量关系：( )=( )×2/5。"
	q.RawTranscription = `根据数量关系：\\((\\quad)= (\\quad)\\times\\frac{2}{5}\\)。`
	q.CanonicalMarkdown = q.Question
	q.EvidenceTranscriptions = []string{shared + "\n根据数量关系：（ ）=（ ）×2/5。列式：", q.RawTranscription}
	if !independentReferenceAnswerEligible(PhotoGradeRequest{}, q) {
		t.Fatalf("equivalent empty slots and a printed calculation label blocked the reference: current=%q initial=%q", mathtext.PlainMarkdownText(mathEmptyPrintedSlot.ReplaceAllString(q.RawTranscription, "()")), mathtext.PlainMarkdownText(q.EvidenceTranscriptions[0]))
	}
	q.EvidenceTranscriptions[0] = shared + "\n根据数量关系：（ ）=（ ）×3/5。列式："
	if independentReferenceAnswerEligible(PhotoGradeRequest{}, q) {
		t.Fatal("a changed fraction was treated as an empty-slot formatting difference")
	}
	for _, annotation := range []string{"图示：已用空间，剩余空间，共？TB。", "图中：已用空间，剩余空间，共？TB。", "图示：已用空间，剩余空间，共？GB。", "图中：已用空间，剩余空间，共？GB。", "图示：剩余空间大于已用空间。", "图示：已用空间4.8 TB，剩余空间7.2 TB。"} {
		diagram := unclearHandwritingQuestion()
		diagram.ImageSourceContext = q.ImageSourceContext
		diagram.Question = shared + "\n先求一份是多少，再求总容量是多少。"
		diagram.RawTranscription = shared + "\n" + annotation + "\n先求一份是多少，再求总容量是多少。"
		diagram.CanonicalMarkdown = diagram.RawTranscription
		diagram.EvidenceTranscriptions = []string{diagram.Question, diagram.RawTranscription}
		diagram.ConfirmationReasons = []OCRRiskReason{OCRRiskEvidenceConflict}
		want := annotation == "图示：已用空间，剩余空间，共？TB。" || annotation == "图中：已用空间，剩余空间，共？TB。"
		if got := independentReferenceAnswerEligible(PhotoGradeRequest{}, diagram); got != want {
			t.Fatalf("diagram labels must retain unit and condition differences: annotation=%q eligible=%t want=%t", annotation, got, want)
		}
	}
	for _, separator := range []string{"\n", ""} {
		diagram := unclearHandwritingQuestion()
		contextCopy := *q.ImageSourceContext
		contextCopy.Materials = []k12.RecognitionImageScopeDependencyV1{{Text: "用不同的知识解决问题，想一想这些方法之间有什么联系。\n" + shared}}
		diagram.ImageSourceContext = &contextCopy
		diagram.Question = "先求一份是多少，再求总容量是多少。"
		diagram.RawTranscription = shared + separator + "图示：已用空间，剩余空间，共？TB。" + separator + diagram.Question
		diagram.CanonicalMarkdown = diagram.RawTranscription
		diagram.EvidenceTranscriptions = []string{contextCopy.Materials[0].Text + "\n" + diagram.Question, diagram.RawTranscription}
		diagram.ConfirmationReasons = []OCRRiskReason{OCRRiskEvidenceConflict}
		if !independentReferenceAnswerEligible(PhotoGradeRequest{}, diagram) {
			t.Fatalf("a complete frozen material suffix and its labels must remain equivalent: separator=%q", separator)
		}
		diagram.EvidenceTranscriptions[0] = strings.Replace(diagram.EvidenceTranscriptions[0], "4.8 TB", "4.9 TB", 1)
		if independentReferenceAnswerEligible(PhotoGradeRequest{}, diagram) {
			t.Fatal("a different shared quantity borrowed the frozen material")
		}
	}
}

func TestUnclearQuantitySourceRetainsReferenceEligibilityAfterRestore(t *testing.T) {
	for _, name := range []string{"unclear_quantity_source.json", "unclear_quantity_source_with_readings.json", "unclear_work_line_source.json"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/" + name)
			if err != nil {
				t.Fatal(err)
			}
			var q RecognizedQuestion
			if err = json.Unmarshal(raw, &q); err != nil {
				t.Fatal(err)
			}
			before, _ := json.Marshal(q)
			if !independentReferenceAnswerEligible(PhotoGradeRequest{}, q) {
				t.Fatal("high-confidence printed quantity relation with handwriting-only damage must retain its independent reference answer")
			}
			after, _ := json.Marshal(q)
			if string(before) != string(after) {
				t.Fatal("reference eligibility changed recognized source facts")
			}
		})
	}
}

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
	for _, source := range []string{"independent readings", "restored handwriting-only risk"} {
		t.Run(source, func(t *testing.T) {
			ctx := context.Background()
			solver := &itemResumeSolver{calls: map[string]int{}, solutions: map[string]string{"4.5×2=": "4.5×2=9\n答案：9"}}
			grader := &itemResumeGrader{calls: map[string]int{}}
			q := unclearHandwritingQuestion()
			if source == "restored handwriting-only risk" {
				q.AnswerState = AnswerStatePresent
				q.StudentAnswer, q.AnswerRawTranscription, q.AnswerCanonicalMarkdown = "?/5×2\n1", "?/5×2\n1", "?/5×2\n1"
				q.EvidenceTranscriptions, q.AnswerEvidenceTranscriptions = nil, nil
				q.OCRSignals = []string{"第一个括号内手写分数的分子被裁切，无法可靠辨认，以 ? 保留；其余手写内容可辨。"}
				q.ConfirmationReasons = []OCRRiskReason{OCRRiskLowConfidence}
			}
			o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{q}, solver, grader)
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
		})
	}
}

func TestUnclearReferenceAnswerDoesNotGuessPrintedFactsOrPracticeMatch(t *testing.T) {
	for _, scenario := range []string{"clear", "cropped handwriting", "restored handwriting-only risk", "mixed printed damage", "printed conflict", "low confidence", "missing confidence", "missing condition", "unmatched practice"} {
		t.Run(scenario, func(t *testing.T) {
			solver := &itemResumeSolver{calls: map[string]int{}, solutions: map[string]string{"4.5×2=": "4.5×2=9\n答案：9"}}
			grader := &itemResumeGrader{calls: map[string]int{}}
			o := newItemResumeOrchestrator(t, t.TempDir(), nil, solver, grader)
			q := unclearHandwritingQuestion()
			req := PhotoGradeRequest{AgentName: "mingming", Grade: "五年级上", SourceUncertaintyFinalized: true}
			switch scenario {
			case "cropped handwriting":
				q.OCRSignals = []string{"第一个括号内手写分数的分子被裁切，无法可靠辨认；分母5及其后的×2可读。", "evidence_conflict"}
				q.ConfirmationReasons = []OCRRiskReason{OCRRiskLowConfidence, OCRRiskEvidenceConflict, OCRRiskUnclearHandwriting}
			case "restored handwriting-only risk":
				q.AnswerState = AnswerStatePresent
				q.StudentAnswer, q.AnswerRawTranscription, q.AnswerCanonicalMarkdown = "?/5×2\n1", "?/5×2\n1", "?/5×2\n1"
				q.EvidenceTranscriptions, q.AnswerEvidenceTranscriptions = nil, nil
				q.OCRSignals = []string{"第一个括号内手写分数的分子被裁切，无法可靠辨认，以 ? 保留；其余手写内容可辨。"}
				q.ConfirmationReasons = []OCRRiskReason{OCRRiskLowConfidence}
			case "mixed printed damage":
				q.OCRSignals = []string{"手写笔迹和印刷题干的分子均被裁切。"}
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
			wantAnswer := scenario == "clear" || scenario == "cropped handwriting" || scenario == "restored handwriting-only risk"
			if item.Status != PhotoAnswerUnclear || (item.ParentGuide != nil) != wantAnswer ||
				grader.callCount("4.5×2=") != 0 || (solver.callCount("4.5×2=") > 0) != wantAnswer {
				t.Fatalf("题干与学生作答风险必须分离，不能猜条件或判学生答案: %+v", item)
			}
		})
	}
}
