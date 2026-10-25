package usecase

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type blankWorksheetSolver struct {
	evidenceType EvidenceType
}

func (s blankWorksheetSolver) Solve(_ context.Context, problem, _, _ string) (SolveResult, error) {
	evidenceType := s.evidenceType
	if evidenceType == "" {
		evidenceType = EvidenceNumericExec
	}
	return SolveResult{
		Solution: blankWorksheetVerifiedSolution(problem),
		Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: evidenceType},
	}, nil
}

func blankWorksheetAnswer(problem string) string {
	switch problem {
	case "4.5×2=", "36÷4=":
		return "9"
	case "1+1=":
		return "2"
	case "2+2=":
		return "4"
	case "3+3=":
		return "6"
	default:
		return "2"
	}
}

func blankWorksheetVerifiedSolution(problem string) string {
	answer := blankWorksheetAnswer(problem)
	return fmt.Sprintf(
		"## 完整方法\n先根据题意计算 %s，并逐步核对中间结果。\n## 答案\n**%s**",
		problem, answer,
	)
}

func blankWorksheetVerifiedSteps(problem string) []string {
	return []string{"先根据题意计算 " + problem + "，并逐步核对中间结果。"}
}

type parentTeachingGuideSpy struct {
	mu     sync.Mutex
	calls  []ParentTeachingGuideRequest
	answer string
}

func (s *parentTeachingGuideSpy) GenerateParentTeachingGuide(
	_ context.Context,
	req ParentTeachingGuideRequest,
) (ParentTeachingGuide, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()
	answer := s.answer
	if answer == "" {
		answer = blankWorksheetAnswer(req.Problem)
	}
	return ParentTeachingGuide{
		Answer:                 answer,
		FullSolutionSteps:      []string{"untrusted generator rewrite"},
		GradeLevelMethod:       "本年级方法：" + req.Problem,
		LikelyMistakes:         []string{"易错：" + req.Problem},
		ParentTeachingSequence: []string{"先讲题意：" + req.Problem, "再让孩子自己计算"},
		FollowUpQuestions:      []string{"为什么这样计算：" + req.Problem + "？"},
		CheckingMethod:         "代回原题：" + req.Problem,
	}, nil
}

func (s *parentTeachingGuideSpy) snapshot() []ParentTeachingGuideRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ParentTeachingGuideRequest(nil), s.calls...)
}

func TestParentTeachingAnswerLabelKeepsExactVerifiedContent(t *testing.T) {
	for _, tc := range []struct {
		name, answer, solution string
		want                   bool
	}{
		{"same Chinese answer with label", "答案：比喻；金水河弯曲、秀美的特点。", "先分析修辞。\n答案：比喻；金水河弯曲、秀美的特点。", true},
		{"same numeric answer with label", "答案：42 cm", "计算完成。\n答案：42 cm", true},
		{"different word", "答案：拟人；金水河弯曲、秀美的特点。", "先分析修辞。\n答案：比喻；金水河弯曲、秀美的特点。", false},
		{"different number", "答案：42 cm", "计算完成。\n答案：142 cm", false},
		{"empty label", "答案：", "计算完成。\n答案：42 cm", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := answerAnchoredInVerifiedSolution(tc.answer, tc.solution); got != tc.want {
				t.Fatalf("answer anchor = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGradeHomeworkPhotoBlankWorksheetBuildsCompleteProblemSpecificParentGuides(t *testing.T) {
	d, _ := newPipeline(t, blankWorksheetSolver{evidenceType: EvidenceHeuristic}, fakeGrader{}, nil)
	generator := &parentTeachingGuideSpy{}
	d.ParentTeachingGuide = generator
	d.Recognizer = photoRecognizerFake{questions: []RecognizedQuestion{
		{
			Question: "4.5×2=", Subject: "数学", AnswerState: AnswerStateBlank,
			KnowledgePoints: []string{"小数乘法"},
		},
		{Question: "36÷4=", Subject: "数学", AnswerState: AnswerStateBlank},
	}}

	got, err := d.GradeHomeworkPhoto(context.Background(), PhotoGradeRequest{
		AgentName: "mingming", Grade: "五年级上", Image: []byte("blank worksheet"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskIntent != PhotoTaskBlankWorksheet || got.ResultSurface != PhotoSurfaceParentTeachingGuide {
		t.Fatalf("blank worksheet semantics = %q/%q", got.TaskIntent, got.ResultSurface)
	}
	calls := generator.snapshot()
	if len(calls) != 2 {
		t.Fatalf("guide generator calls=%d, want one per question: %#v", len(calls), calls)
	}
	seenProblems := map[string]bool{}
	for _, call := range calls {
		seenProblems[call.Problem] = true
		if call.VerifiedSolution != blankWorksheetVerifiedSolution(call.Problem) {
			t.Fatalf("generator did not receive the verified solution for %q: %#v", call.Problem, call)
		}
	}
	for _, problem := range []string{"4.5×2=", "36÷4="} {
		if !seenProblems[problem] {
			t.Fatalf("question-specific guide request missing for %q: %#v", problem, calls)
		}
	}

	if len(got.Items) != 2 {
		t.Fatalf("items=%d, want 2", len(got.Items))
	}
	for i, item := range got.Items {
		if item.Status != PhotoBlankSolved || item.ResultKind != PhotoItemParentTeachingGuide || item.ParentGuide == nil {
			t.Fatalf("item %d lacks parent-guide result semantics: %#v", i, item)
		}
		guide := *item.ParentGuide
		wantAnswer := blankWorksheetAnswer(item.Recognized.Question)
		if guide.Answer != wantAnswer {
			t.Fatalf("item %d answer=%q, want solver-anchored final answer %q", i, guide.Answer, wantAnswer)
		}
		wantFullSolutionSteps := blankWorksheetVerifiedSteps(item.Recognized.Question)
		if !reflect.DeepEqual(guide.FullSolutionSteps, wantFullSolutionSteps) {
			t.Fatalf("item %d full_solution_steps=%#v, want deterministic verified steps %#v",
				i, guide.FullSolutionSteps, wantFullSolutionSteps)
		}
		if len(guide.FullSolutionSteps) == 1 && guide.Answer == guide.FullSolutionSteps[0] {
			t.Fatalf("item %d copied the entire solution into answer: %#v", i, guide)
		}
		if strings.TrimSpace(guide.GradeLevelMethod) == "" ||
			len(guide.LikelyMistakes) == 0 ||
			len(guide.ParentTeachingSequence) == 0 ||
			len(guide.FollowUpQuestions) == 0 ||
			strings.TrimSpace(guide.CheckingMethod) == "" {
			t.Fatalf("item %d guide is incomplete: %#v", i, guide)
		}
		if !strings.Contains(guide.LikelyMistakes[0], item.Recognized.Question) {
			t.Fatalf("item %d received repeated generic guidance: %#v", i, guide)
		}
	}

	for _, want := range []string{
		"家长辅导指南",
		"**答案：**", "**必要步骤：**", "**本年级方法：**", "**易错点：**",
		"**家长怎么讲：**", "**可以追问：**", "**怎么检查：**",
		"易错：4.5×2=", "易错：36÷4=",
	} {
		if !strings.Contains(got.Markdown, want) {
			t.Fatalf("parent-guide markdown missing %q:\n%s", want, got.Markdown)
		}
	}
	for _, stale := range []string{"**知识点：**", "**讲解步骤：**", "**批改标准：**", "**家长提问顺序：**", "**核对方法：**"} {
		if strings.Contains(got.Markdown, stale) {
			t.Fatalf("parent-guide markdown still exposes stale semantic item %q:\n%s", stale, got.Markdown)
		}
	}
}

func TestNumericAnswerDoesNotReplaceApplicationTeachingWithArithmeticTemplate(t *testing.T) {
	evidence := SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}
	application := ParentTeachingGuideRequest{Problem: "妈妈从家到学校要走10分钟，红红要走15分钟，同时相向而行，5分钟后相距200米，求全程。", Grade: "六年级上", VerifiedSolution: "全程的1/6对应200米，200÷(1/6)=1200米。\n\n## 答案\n\n1200米"}
	if _, local := deterministicParentTeachingGuideForEvidence(application, evidence); local {
		t.Fatal("a verified numeric answer cannot select a generic arithmetic teaching template for an application problem")
	}
	application.Problem, application.VerifiedSolution = "计算：4.5×2=？", "4.5×2=9。\n\n## 答案\n\n9"
	if guide, local := deterministicParentTeachingGuideForEvidence(application, evidence); !local || guide.Answer != "9" {
		t.Fatal("pure verified arithmetic must retain its local teaching path")
	}
}

func TestJointNumericApplicationWithoutCandidateUsesFrozenGuideGenerator(t *testing.T) {
	d, _ := newPipeline(t, blankWorksheetSolver{}, fakeGrader{}, nil)
	generator := &parentTeachingGuideSpy{}
	d.ParentTeachingGuide = generator
	req := GradeRequest{AgentName: "mingming", Subject: "数学", Grade: "二年级上",
		Problem:            "小明有1本书，又买了1本，一共有多少本？",
		SolveOutputVersion: SolveOutputWithParentGuideV1, ParentTeachingContract: "frozen teaching contract"}
	result, err := d.SolveBlankWorksheetProblem(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	calls := generator.snapshot()
	if len(calls) != 1 || calls[0].FrozenTeachingContract != req.ParentTeachingContract || result.Guide.Answer != "2" {
		t.Fatalf("missing candidate must use the frozen provider guide: calls=%#v result=%#v", calls, result.Guide)
	}
	if !reflect.DeepEqual(result.Guide.FullSolutionSteps, blankWorksheetVerifiedSteps(req.Problem)) {
		t.Fatal("guide generation replaced the verified solution steps")
	}
}

type incompleteParentTeachingGuide struct{}

func (incompleteParentTeachingGuide) GenerateParentTeachingGuide(
	context.Context,
	ParentTeachingGuideRequest,
) (ParentTeachingGuide, error) {
	return ParentTeachingGuide{Answer: "model answer"}, nil
}

func TestGradeHomeworkPhotoBlankWorksheetRejectsIncompleteGenericGuide(t *testing.T) {
	d, _ := newPipeline(t, blankWorksheetSolver{evidenceType: EvidenceHeuristic}, fakeGrader{}, nil)
	d.ParentTeachingGuide = incompleteParentTeachingGuide{}
	d.Recognizer = photoRecognizerFake{questions: []RecognizedQuestion{{
		Question: "2+2=", Subject: "数学", AnswerState: AnswerStateBlank,
	}}}

	got, err := d.GradeHomeworkPhoto(context.Background(), PhotoGradeRequest{
		AgentName: "mingming", Grade: "五年级上", Image: []byte("blank worksheet"),
	})
	if err == nil {
		t.Fatal("an incomplete generic guide must fail visibly")
	}
	if len(got.Items) != 1 || got.Items[0].Status != PhotoFailed || got.Items[0].ParentGuide != nil {
		t.Fatalf("incomplete guide was presented as complete: %#v, err=%v", got.Items, err)
	}
	if !strings.Contains(err.Error(), "parent teaching guide") {
		t.Fatalf("error must identify the failed contract, got %v", err)
	}
}

func TestGradeHomeworkPhotoCompletedHomeworkSolvesBlankItemsForParent(t *testing.T) {
	d, _ := newPipeline(t, blankWorksheetSolver{}, fakeGrader{outcome: GradeOutcome{Verdict: VerdictAgree}}, nil)
	generator := &parentTeachingGuideSpy{}
	d.ParentTeachingGuide = generator
	d.Recognizer = photoRecognizerFake{questions: []RecognizedQuestion{
		{Question: "1+1=", Subject: "数学", StudentAnswer: "2", AnswerState: AnswerStatePresent},
		{Question: "2+2=", Subject: "数学", AnswerState: AnswerStateBlank},
	}}

	got, err := d.GradeHomeworkPhoto(context.Background(), PhotoGradeRequest{
		AgentName: "mingming", Grade: "五年级上", Image: []byte("completed homework"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskIntent != PhotoTaskCompletedHomework || got.ResultSurface != PhotoSurfaceAnnotatedHomework {
		t.Fatalf("completed-homework semantics = %q/%q", got.TaskIntent, got.ResultSurface)
	}
	if len(got.Items) != 2 ||
		got.Items[0].ResultKind != PhotoItemAssessment ||
		got.Items[1].Status != PhotoBlankSolved ||
		got.Items[1].ResultKind != PhotoItemParentTeachingGuide ||
		got.Items[1].ParentGuide == nil ||
		got.Items[1].Solve.Solution == "" || got.Items[1].Grade.RecordCreated {
		t.Fatalf("mixed completed-homework must solve blanks without grading them: %#v", got.Items)
	}
	if calls := generator.snapshot(); len(calls) != 0 {
		t.Fatalf("verified arithmetic must use the local parent guide without another provider call: %#v", calls)
	}
}

func TestSolveHomeworkProblemLegacyClientDoesNotRequireParentGuideGenerator(t *testing.T) {
	d, _ := newPipeline(t, blankWorksheetSolver{}, fakeGrader{}, nil)
	res, err := d.SolveHomeworkProblem(context.Background(), GradeRequest{
		AgentName: "mingming", Subject: "数学", Grade: "五年级上", Problem: "3+3=",
	})
	if err != nil || res.Solution != blankWorksheetVerifiedSolution("3+3=") {
		t.Fatalf("legacy solve client changed: result=%#v err=%v", res, err)
	}
}

func TestBlankWorksheetGuidePassesOnlyNonEmptyRecognizedKnowledgePointsAsGenerationFacts(t *testing.T) {
	d, _ := newPipeline(t, blankWorksheetSolver{evidenceType: EvidenceHeuristic}, fakeGrader{}, nil)
	generator := &parentTeachingGuideSpy{}
	d.ParentTeachingGuide = generator
	result, err := d.SolveBlankWorksheetProblem(context.Background(), GradeRequest{
		AgentName: "mingming", Subject: "数学", Grade: "五年级上", Problem: "3+3=",
		KnowledgePoints: []string{" ", "整数加法", ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls := generator.snapshot(); len(calls) != 1 ||
		len(calls[0].KnowledgePoints) != 1 || calls[0].KnowledgePoints[0] != "整数加法" {
		t.Fatalf("guide generation facts did not normalize recognized knowledge points: %#v", calls)
	}
	if result.Guide.GradeLevelMethod != "本年级方法：3+3=" {
		t.Fatalf("grade-level method was not retained: %#v", result.Guide)
	}
}

func TestParentTeachingGuideValidationErrorNamesMissingField(t *testing.T) {
	err := validateParentTeachingGuide(ParentTeachingGuide{
		Answer:                 "2",
		FullSolutionSteps:      []string{"把 1 和 1 相加得到 2"},
		GradeLevelMethod:       "把两个加数相加",
		LikelyMistakes:         []string{"写错符号"},
		ParentTeachingSequence: []string{"先让孩子指出两个加数"},
		FollowUpQuestions:      []string{"交换两个加数后结果怎样？"},
	})
	if err == nil || !strings.Contains(err.Error(), "checking_method") {
		t.Fatalf("validation error=%v, want missing checking_method", err)
	}
}

type intermediateResultAnswerGuide struct{}

func (intermediateResultAnswerGuide) GenerateParentTeachingGuide(
	context.Context,
	ParentTeachingGuideRequest,
) (ParentTeachingGuide, error) {
	return ParentTeachingGuide{
		Answer:                 "90",
		FullSolutionSteps:      []string{"generator-controlled"},
		GradeLevelMethod:       "先按整数乘法算，再点回一位小数",
		LikelyMistakes:         []string{"忘记点回小数点"},
		ParentTeachingSequence: []string{"先算 45×2，再让孩子点小数点"},
		FollowUpQuestions:      []string{"积应该有几位小数？"},
		CheckingMethod:         "用除法反向验算",
	}, nil
}

func TestBlankWorksheetGuideRestoresVerifiedAnswerInsteadOfIntermediateResult(t *testing.T) {
	d, _ := newPipeline(t, blankWorksheetSolver{evidenceType: EvidenceHeuristic}, fakeGrader{}, nil)
	d.ParentTeachingGuide = intermediateResultAnswerGuide{}

	result, err := d.SolveBlankWorksheetProblem(context.Background(), GradeRequest{
		AgentName: "mingming", Subject: "数学", Grade: "五年级上", Problem: "4.5×2=",
	})
	if err != nil {
		t.Fatal(err)
	}
	if normalizeAnswerAnchorText(result.Guide.Answer) != "9" ||
		result.Solved.Solution != blankWorksheetVerifiedSolution("4.5×2=") ||
		!reflect.DeepEqual(result.Guide.FullSolutionSteps, blankWorksheetVerifiedSteps("4.5×2=")) {
		t.Fatalf("guide did not preserve the verified answer and solution: %#v", result)
	}
}

func TestVerifiedFullSolutionStepsPreserveOrderedParagraphsAndExcludeAnswerSection(t *testing.T) {
	solution := "## 方法\n\n1. 先列式 45×2=90。\n2. 再点回一位小数。\n\n补充核对数量级。\n\n## 答案\n**9**"
	got := verifiedFullSolutionSteps(solution)
	want := []string{
		"先列式 45×2=90。",
		"再点回一位小数。",
		"补充核对数量级。",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verified steps=%#v, want ordered method-only steps %#v", got, want)
	}
}

func TestVerifiedFullSolutionStepsFallsBackToOneVerifiedElement(t *testing.T) {
	const solution = "解：4.5×2=9"
	if got := verifiedFullSolutionSteps(solution); !reflect.DeepEqual(got, []string{solution}) {
		t.Fatalf("verified steps fallback=%#v, want exact single verified element", got)
	}
}

func TestVerifiedFullSolutionStepsDoNotPromoteHeadingsOrAnswerBlocks(t *testing.T) {
	for _, tc := range []struct {
		name, solution string
	}{
		{"received_empty_method", "## 解答\n\n## 答案\n\n**2**"},
		{"inline_answer", "## 解答\n\n答案：2"},
		{"headings_only", "## 解答\n\n### 计算方法\n\n## 答案"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if steps := verifiedFullSolutionSteps(tc.solution); len(steps) != 0 {
				t.Fatalf("presentation structure or final answer became a method: %#v", steps)
			}
			guide := ParentTeachingGuide{
				Answer: "2", FullSolutionSteps: []string{"generator supplied an unverified derivation"},
				GradeLevelMethod: "理解份数", LikelyMistakes: []string{"把时间看作份数"},
				ParentTeachingSequence: []string{"先说出每份表示的量"}, FollowUpQuestions: []string{"一份是多少？"},
				CheckingMethod: "回看原图中的份数",
			}
			if result, err := finalizeParentTeachingGuide(guide, tc.solution); err == nil || !strings.Contains(err.Error(), "full_solution_steps") {
				t.Fatalf("unverified generator method replaced missing solver steps: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestVerifiedFullSolutionStepsKeepExecutedEquationAndSevenGuideFields(t *testing.T) {
	const solution = "## 解答\n\n2 * 1/3 = 2/3\n\n## 答案\n\n**2**"
	guide := ParentTeachingGuide{
		Answer: "2", FullSolutionSteps: []string{"unverified replacement"},
		GradeLevelMethod: "分数的意义", LikelyMistakes: []string{"把时间当成份数"},
		ParentTeachingSequence: []string{"先指出每份表示的时间"}, FollowUpQuestions: []string{"两个1/3是多少？"},
		CheckingMethod: "将份数与每份时间相乘检查",
	}
	finalized, err := finalizeParentTeachingGuide(guide, solution)
	if err != nil || !reflect.DeepEqual(finalized.FullSolutionSteps, []string{"2 * 1/3 = 2/3"}) || finalized.Answer != "2" {
		t.Fatalf("verified execution equation was lost: %+v err=%v", finalized, err)
	}
	guide.FullSolutionSteps = finalized.FullSolutionSteps
	if !reflect.DeepEqual(finalized, guide) || validateParentTeachingGuide(finalized) != nil {
		t.Fatal("seven guide fields or answer anchor changed during method projection")
	}
}

func TestVerifiedFullSolutionStepsDoesNotCorruptLeadingDecimal(t *testing.T) {
	const solution = "4.5×2=9"
	if got := verifiedFullSolutionSteps(solution); !reflect.DeepEqual(got, []string{solution}) {
		t.Fatalf("leading decimal was mistaken for a list marker: %#v", got)
	}
}

func TestVerifiedFullSolutionStepsPreserveNumericEnumeration(t *testing.T) {
	const solution = "## 方法\n1、先列式\n\n5、14、23\n\n5、14、23、29\n\n5、12、23\n\n2. 核对各数之和\n## 答案\n29"
	want := []string{"先列式", "5、14、23", "5、14、23、29", "5、12、23", "核对各数之和"}
	if got := verifiedFullSolutionSteps(solution); !reflect.DeepEqual(got, want) {
		t.Fatalf("numeric enumeration or ordered steps changed: got %#v, want %#v", got, want)
	}
}

func TestAnswerAnchorRejectsEmptyExplicitAnswerSection(t *testing.T) {
	const solution = "## 方法\n45×2=90，再点回小数点得到9。\n## 答案\n"
	if answerAnchoredInVerifiedSolution("90", solution) {
		t.Fatal("an empty explicit answer section must not fall back to an intermediate result")
	}
}
