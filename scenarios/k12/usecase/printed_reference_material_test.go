package usecase

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestIndependentReferenceComparesVerifiedSharedLinesAndLocalSlots(t *testing.T) {
	const heading = "4. 用两种方法说明。"
	const material = "已用长度是总长度的2/5，正好是 4.8 km。"
	const graphic = "已用长度　剩余长度\n共？ km"
	const child = "根据数量关系：（　）＝（　）×2/5。"
	const method = "先求一份是多少，再求总长度是多少。"
	q := unclearHandwritingQuestion()
	q.SourceNumberPath, q.DisplayLabel = []string{"4", "2"}, "（2）"
	q.ImageSourceContext = &k12.RecognitionImageSourceContextV1{
		Version: k12.RecognitionImageScopeVersionV1, ScopeDigest: "frozen-scope",
		TargetIDs: []string{"target"}, Materials: []k12.RecognitionImageScopeDependencyV1{
			{Text: heading + "\n" + material, ForOrders: []int{1, 2}},
			{Text: graphic, ForOrders: []int{1, 2}},
		},
	}
	q.AnswerState = AnswerStatePresent
	q.OCRSignals = nil
	q.ConfirmationReasons = []OCRRiskReason{OCRRiskEvidenceConflict}
	compact := strings.ReplaceAll(strings.TrimPrefix(heading, "4. ")+"\n"+material+"\n"+graphic, " ", "")
	for _, tc := range []struct {
		name, raw, reading string
		eligible           bool
	}{
		{"numbered material with different typesetting", compact + "\n" + method, heading + "\n" + material + "\n" + graphic + "\n" + method, true},
		{"local slots with or without material", `根据数量关系：\(（\quad）=（\qquad）\times\frac{2}{5}\)。`, heading + "\n" + material + "\n" + graphic + "\n" + child, true},
		{"rewrapped shared paragraph", heading + material + "\n" + graphic + "\n" + method, method, true},
		{"rewrapped different quantity", heading + strings.Replace(material, "4.8", "4.9", 1) + "\n" + graphic + "\n" + method, method, false},
		{"rewrapped different unit", heading + strings.Replace(material, "km", "m", 1) + "\n" + graphic + "\n" + method, method, false},
		{"empty vs measured underline", child + "\n列式：________", child + "\n列式：" + `\(\underline{\hspace{3cm}}\)`, true},
		{"numeric content in underline", child + "\n列式：________", child + "\n列式：" + `\(\underline{3\,\mathrm{cm}}\)`, false},
		{"changed quantity", child, heading + "\n" + strings.Replace(material, "4.8", "4.9", 1) + "\n" + graphic + "\n" + child, false},
		{"changed unit", child, heading + "\n" + strings.Replace(material, "km", "m", 1) + "\n" + graphic + "\n" + child, false},
		{"different heading", child, strings.Replace(heading, "4.", "3.", 1) + "\n" + material + "\n" + graphic + "\n" + child, false},
		{"changed operator", child, heading + "\n" + material + "\n" + graphic + "\n" + strings.Replace(child, "×", "÷", 1), false},
		{"different blank expression", child, heading + "\n" + material + "\n" + graphic + "\n" + strings.Replace(child, "（　）", "（1）", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
			item.Question, item.RawTranscription, item.CanonicalMarkdown = tc.raw, tc.raw, tc.raw
			item.EvidenceTranscriptions = []string{tc.reading, tc.raw}
			before, _ := json.Marshal(item)
			if got := independentReferenceAnswerEligible(PhotoGradeRequest{InitialReadMode: k12.RecognitionLayoutManifestWithContentV1}, item); got != tc.eligible {
				t.Fatalf("reference eligibility = %v, want %v", got, tc.eligible)
			}
			after, _ := json.Marshal(item)
			if string(before) != string(after) {
				t.Fatal("reference comparison changed the original evidence")
			}
		})
	}
}

func TestSharedPrintAdjudicationKeepsHandwritingUnjudged(t *testing.T) {
	const shared = "订购了 4 箱，每箱装 8 册。"
	const child = "根据数量关系：（　）＝（　）×8。"
	const work = "\n列式：________"
	const observation = `{"verification":{"question_complete_and_legible":true,"target_ownership_confirmed":true,"active_answer_complete":false},"items":[{"target_id":"t1","kind":"question","recognition":{"question":"根据数量关系：（　）＝（　）×8。\n列式：\\(\\underline{\\hspace{3cm}}\\)","subject":"数学","answer_state":"present","student_answer":"4","recognition_confidence":0.99,"ocr_signals":["第二个括号内有部分被遮挡的笔迹，无法可靠辨认。"],"answer_bbox":null}}],"final_answer":null}`
	req := PhotoGradeRequest{InitialReadMode: k12.RecognitionLayoutManifestWithContentV1}
	for _, tc := range []struct {
		name, old, replacement string
		priorRisk              string
		want                   bool
	}{
		{name: "three matching printed observations", want: true},
		{name: "printed quantity changed", old: "×8", replacement: "×6"},
		{name: "printed operation changed", old: "×8", replacement: "÷8"},
		{name: "ownership denied", old: "target_ownership_confirmed\":true", replacement: "target_ownership_confirmed\":false"},
		{name: "print declared unreadable", old: "question_complete_and_legible\":true", replacement: "question_complete_and_legible\":false"},
		{name: "printed quantity still disputed", priorRisk: "笔迹影响印刷条件，无法分清是 8 册还是 6 册。"},
		{name: "adjudication still sees mixed risk", old: "第二个括号内有部分被遮挡的笔迹", replacement: "笔迹遮住印刷数值"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := unclearHandwritingQuestion()
			q.SourceNumberPath, q.DisplayLabel = []string{"7", "2"}, "（2）"
			q.SourceRegion = &k12.SourcePixelRegion{Width: 100, Height: 40}
			q.ImageSourceContext = &k12.RecognitionImageSourceContextV1{
				Version: k12.RecognitionImageScopeVersionV1, ScopeDigest: "same-scope", TargetIDs: []string{"current"},
				Materials: []k12.RecognitionImageScopeDependencyV1{{Text: shared, ForOrders: []int{1, 2}}},
			}
			q.ProblemKind, q.AnswerState = ProblemKindStandalone, AnswerStatePresent
			q.Question, q.RawTranscription, q.CanonicalMarkdown = child+work, child+work, child+work
			q.EvidenceTranscriptions = []string{shared + "\n" + child + work, child + work}
			q.OCRSignals = []string{"括号内有学生笔迹，上部被标记遮挡。"}
			if tc.priorRisk != "" {
				q.OCRSignals = append(q.OCRSignals, tc.priorRisk)
			}
			q.ConfirmationReasons = []OCRRiskReason{OCRRiskEvidenceConflict, OCRRiskLowConfidence}
			input, _ := json.Marshal(q)
			payload := observation
			if tc.old != "" {
				payload = strings.Replace(observation, tc.old, tc.replacement, 1)
				if payload == observation {
					t.Fatal("adjudication fixture was not changed")
				}
			}
			if _, ok := independentReferenceFromAdjudication(req, q, payload); ok != tc.want {
				t.Fatalf("independent reference eligibility = %v, want %v", ok, tc.want)
			}
			result, _ := json.Marshal(q)
			if string(result) != string(input) {
				t.Fatal("reference solving changed frozen grading observations")
			}
		})
	}
}

func TestFrozenSharedBlankReadsRequireEveryCompleteObservation(t *testing.T) {
	const condition = "每箱有 8 册。\n一共 4 箱。"
	const local = "求总册数。\n列式：____"
	target := k12.RecognitionLayoutTargetV2{TargetID: "item-b", ManifestOrder: 2, SourceNumberPath: []string{"6", "2"}}
	plan := k12.RecognitionLayoutPlanV2{AuthorizedPlanDigest: "frozen", Scope: &k12.RecognitionImageScopeV1{
		Dependencies: []k12.RecognitionImageScopeDependencyV1{{ForOrders: []int{1, 2}, Text: condition}},
	}}
	base := RecognizedQuestion{Subject: "数学", DisplayLabel: "（2）", SourceNumberPath: target.SourceNumberPath, AnswerState: AnswerStateBlank, RecognitionConfidence: float64Ptr(.99)}
	sources := [3]RecognizedQuestion{base, base, base}
	sources[0].RawTranscription = condition + "\n" + local
	sources[1].RawTranscription = strings.ReplaceAll(condition, "\n", "") + "\n" + local
	sources[2].RawTranscription = local
	if !FrozenSharedBlankReadingsAgree(plan, target, sources) {
		t.Fatal("three complete observations of the same blank were rejected")
	}
	for _, tc := range []struct {
		name   string
		change func(*RecognizedQuestion)
	}{
		{"different printed number", func(q *RecognizedQuestion) { q.SourceNumberPath = []string{"6", "3"} }},
		{"different quantity", func(q *RecognizedQuestion) { q.RawTranscription = strings.Replace(q.RawTranscription, "8", "6", 1) }},
		{"different unit", func(q *RecognizedQuestion) { q.RawTranscription = strings.Replace(q.RawTranscription, "册", "本", 1) }},
		{"obscured ink", func(q *RecognizedQuestion) { q.OCRSignals = []string{"有遮挡的笔迹"} }},
		{"observed handwriting", func(q *RecognizedQuestion) { q.StudentAnswer = "32" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := sources
			tc.change(&changed[1])
			if FrozenSharedBlankReadingsAgree(plan, target, changed) {
				t.Fatal("disputed observation was classified as a confirmed blank")
			}
		})
	}
}

func TestReferenceAdjudicationUsesOnlyItsAuthorizedTarget(t *testing.T) {
	const first, second = "question-a", "question-b"
	single, err := k12.RecognitionLayoutTargetExactSetDigestV2([]string{second})
	if err != nil {
		t.Fatal(err)
	}
	page, err := k12.RecognitionLayoutTargetExactSetDigestV2([]string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	proof := k12.ModelPhysicalInvocation{
		Stage: k12.GradingStageRecognizing, Status: k12.ModelInvocationSucceeded,
		PlanDigest: "sha256:" + strings.Repeat("a", 64), CandidateExactSetDigest: single,
		RequestDigest: "sha256:" + strings.Repeat("b", 64),
	}
	if !referenceAdjudicationMatchesTarget(&proof, proof.PlanDigest, second) ||
		referenceAdjudicationMatchesTarget(&proof, proof.PlanDigest, first) {
		t.Fatal("the adjudication must belong to precisely the observed target")
	}
	proof.CandidateExactSetDigest = page
	if referenceAdjudicationMatchesTarget(&proof, proof.PlanDigest, second) {
		t.Fatal("a whole-page digest cannot establish a single-target adjudication")
	}
}
