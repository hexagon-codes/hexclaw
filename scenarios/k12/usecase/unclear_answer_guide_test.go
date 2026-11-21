package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/internal/mathtext"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestWorksheetReferenceRequiresTwoIndependentCompletePrintedReadings(t *testing.T) {
	const source = "小明2/3小时走了2千米，照这样的速度，他每小时行走多少千米？图示文字：1小时走了？km；1/3小时走了？km；2/3小时走了2km。" +
		"从上图看出：已知（ ）份是2千米，可以先求出这样的1份是多少，也就是2÷2=2×（ ）；再求这样的（ ）份是多少，也就是2×（ ）×3。" +
		"由此推算出：2÷(2/3)=2÷2×3=2×1/2×3=2×（1/2×3）=2×3/2=3（km）。推算过程中“2×1/2×3=2×（1/2×3）”的依据是（ ）。"
	reading := func(question, signal string) map[string]any {
		return map[string]any{"kind": "question", "recognition": map[string]any{"subject": "数学", "question": question, "ocr_signals": []string{signal}}}
	}
	initial, _ := json.Marshal(map[string]any{"targets": []any{map[string]any{"source_number_path": []string{"1"}, "initial_read": reading(source, "手写答案不清楚")}}})
	target := k12.RecognitionLayoutTargetV2{TargetID: "target", ManifestOrder: 1, DisplayLabel: "1.", SourceNumberPath: []string{"1"}}
	expected, ok := elementarymath.ParseTravelWorksheet(source)
	if !ok {
		t.Fatal("fixture did not identify every printed relationship")
	}
	// 只变更演算中的运算符，原始主问与用时、路程仍可独立核实。
	changedWork := reading(strings.Replace(source, "2÷2=2", "2×2=2", 1), "末尾笔迹不清")
	changedWork["target_id"] = "t1"
	work, _ := json.Marshal(map[string]any{"items": []any{changedWork}})
	check := func(review []byte, gap bool) (printedReference, bool) {
		readings, found := worksheetTargetReadings(string(initial), string(review), target, "t1", gap)
		if !found {
			return printedReference{}, false
		}
		return corroboratedPrintedReference(source, target, readings, true)
	}
	short, ok := check(work, true)
	main, _, _ := elementarymath.TravelQuestionPrefix(source)
	if !ok || short.question != main || short.worksheet != nil {
		t.Fatal("a disputed worked step changed the independently confirmed rate question")
	}
	prefix, _, _ := strings.Cut(source, "；再求")
	ref, ok := corroboratedPrintedReference(source, target, []string{source, prefix + "；再求"}, true)
	if !ok || ref.worksheet == nil || ref.worksheet.BlankCount != 2 || ref.question != prefix {
		t.Fatal("a matching unfinished next prompt hid the two complete blanks")
	}
	ref, ok = corroboratedPrintedReference(prefix+"；再求", target, []string{source, prefix + "；再求"}, false)
	if !ok || ref.worksheet == nil || ref.worksheet.BlankCount != 2 || ref.question != prefix {
		t.Fatal("selecting the truncated reread as the canonical source hid the complete blanks")
	}
	ref, ok = corroboratedPrintedReference(source, target, []string{source, prefix + "；另外增加6份"}, true)
	if !ok || ref.worksheet != nil || ref.question != main {
		t.Fatal("unexpected added conditions were trimmed as a matching prefix")
	}
	for _, note := range []string{"末尾“依据是”后的填空被图像下边缘截断，无法可靠辨认。", "目标下缘处的印刷内容及最后填空被图像裁切；该处可能还有主动手写内容。"} {
		before, safe := worksheetBeforeObscuredLastBlank(source, []string{note})
		if !safe || strings.Contains(before, "推算过程中") {
			t.Fatal("a cropped final prompt obscured the independent earlier blanks")
		}
		ref, ok = corroboratedPrintedReference(source, target, []string{source, before}, false)
		if !ok || ref.worksheet == nil || ref.worksheet.BlankCount != 4 || ref.worksheet.ReasonRequested {
			t.Fatal("incorrect extent for an obscured last prompt")
		}
		if _, safe = worksheetBeforeObscuredLastBlank(source, []string{note, "图上的路程也不清楚。"}); safe {
			t.Fatal("unreadable distance accepted because another risk concerned the last blank")
		}
	}
	before, safe := worksheetBeforeObscuredLastBlank(source, []string{"末尾依据处被遮挡，无法确定是否有填写内容。", "推算式末尾的数字及其是否为手写填入不清楚。"})
	if !safe || strings.Contains(before, "由此推算出") || !strings.Contains(before, "再求") {
		t.Fatal("a late annotation about the printed calculation should only restrict that calculation and its justification")
	}
	for _, tc := range []struct {
		name, question, signal, ref string
		ok                          bool
	}{
		{"printed dimensions affirmative", source, "题干、图示和单位均清晰可辨，最后一格的笔迹模糊", "t1", true},
		{"printed affirmative", source, "印刷题干清晰，只有最后一空的手写答案无法辨认。", "t1", true},
		{"mixed source", source, "题干清晰但主要条件看不清", "t1", false},
		{"handwriting only", "1. " + source, "未发现可辨认的填字", "t1", true},
		{"printed answer frame", source, "个别手写数字与印刷方框重叠", "t1", true},
		{"printed operand", source, "印刷数值受手写数字遮挡，无法确认", "t1", false},
		{"writing hides distance", source, "手写笔迹盖住了路程数字，无法确定是2千米还是7千米。", "t1", false},
		{"distance hidden by writing", source, "路程数字被手写笔迹遮住，无法确定是2千米还是7千米。", "t1", false},
		{"two unattributed values", source, "笔迹较乱，无法确定路程是2千米还是7千米。", "t1", false},
		{"Chinese quantity alternatives", source, "笔迹较乱，无法确定路程是两千米还是七千米。", "t1", false},
		{"different object after student observation", source, "手写答案模糊，无法确定路程是两千米还是7千米。", "t1", false},
		{"different object in one clause", source, "手写答案模糊且无法确定路程是两千米还是7千米。", "t1", false},
		{"distance as student's answer", source, "学生写的路程是两千米还是七千米，手写答案无法确认。", "t1", true},
		{"two handwritten Chinese values", source, "手写答案模糊，无法确定是两千米还是七千米。", "t1", true},
		{"quoted printed blank at crop edge", source, "底部“依据是（ ）。”处在裁切边缘，填空内容无法确认。", "t1", true},
		{"quoted blank and disputed distance", source, "底部“依据是（ ）。”处在裁切边缘，填空内容无法确认，无法确定路程是两或七千米。", "t1", false},
		{"quoted blank absent from the source", source, "底部“面积是（ ）。”处在裁切边缘，填空内容无法确认。", "t1", false},
		{"equivalent empty box", strings.Replace(source, "依据是（ ）", "依据是□", 1), "底部“依据是（ ）。”处在裁切边缘，填空内容无法确认。", "t1", true},
		{"two student values", source, "手写答案模糊，无法确定是2千米还是7千米。", "t1", true},
		{"printed risk", source, "题干和手写答案都缺损", "t1", false},
		{"different quantity", strings.Replace(source, "走了2千米", "走了4千米", 1), "手写答案模糊", "t1", false},
		{"other numbered question", "2. " + source, "笔迹不清", "t1", false},
		{"other target", source, "笔迹不清", "t2", false},
	} {
		read := reading(tc.question, tc.signal)
		read["target_id"] = tc.ref
		review, _ := json.Marshal(map[string]any{"items": []any{read}})
		actual, valid := check(review, true)
		if (valid && actual.worksheet != nil && *actual.worksheet == expected) != tc.ok {
			t.Errorf("%s: unexpected reference eligibility", tc.name)
		}
	}
	generic := reading(source, "最后一格边缘不够清楚")
	generic["target_id"] = "t1"
	uncertain, _ := json.Marshal(map[string]any{"items": []any{generic}})
	_, without := check(uncertain, false)
	_, with := check(uncertain, true)
	if without || !with {
		t.Fatal("generic observation lost its frozen handwriting scope")
	}
}

func TestPrintedReferenceTargetScope(t *testing.T) {
	const original = "一盒有24支铅笔，平均分给6人，每人几支？"
	target := k12.RecognitionLayoutTargetV2{TargetID: "second", ManifestOrder: 2, SourceNumberPath: []string{"3"}, DisplayLabel: "3."}
	entry := map[string]any{"kind": "question", "recognition": map[string]any{"subject": "数学", "question": original}}
	initial, _ := json.Marshal(map[string]any{"targets": []any{map[string]any{"source_number_path": []string{}}, map[string]any{"source_number_path": []string{"3"}, "initial_read": entry}}})
	entry["target_id"] = "t2"
	review, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"kind": "non_question", "target_id": "t1"}, entry}})
	reads, ok := worksheetTargetReadings(string(initial), string(review), target, "t2", false)
	if !ok {
		t.Fatal("excluded title blocked target")
	}
	ref, ok := corroboratedPrintedReference(original, target, reads, true)
	if !ok || ref.question != original || ref.worksheet != nil {
		t.Fatal("source proof should admit a general math question")
	}
	if _, ok = corroboratedPrintedReference(original, target, append(reads, strings.Replace(original, "24", "25", 1)), true); ok {
		t.Fatal("disputed condition entered general solver")
	}
	const named = "王思远2/3小时走了2千米，照这样的速度，他每小时行走多少千米？"
	source := named + "图示文字：1小时走了？km。从上图看出：求这样的1份。"
	formatted := named + "\n1小时走了？km\n从上图看出：求这样的一1份。"
	ref, ok = corroboratedPrintedReference(source, target, []string{source, formatted}, true)
	if !ok || ref.question != source || ref.worksheet != nil {
		t.Fatal("named subject blocked a corroborated source from general solving")
	}
	if _, ok := corroboratedPrintedReference(source, target, []string{source, strings.ReplaceAll(formatted, "2千米", "3千米")}, true); ok {
		t.Fatal("conflicting printed values entered general solving")
	}
	for _, tc := range []struct {
		source string
		path   []string
		ok     bool
	}{
		{"2. 小明", []string{"1"}, false}, {"2、题目", []string{"2"}, true}, {"（2）将两圆放置", []string{"1", "（1）"}, false},
		{"（2）将两圆放置", []string{"1", "（2）"}, true}, {"1.（2）将两圆放置", []string{"1", "2"}, true},
		{"2.5千米", []string{"1"}, true}, {"(2)÷3", []string{"1"}, true},
	} {
		if printedNumberMatches(tc.source, tc.path) != tc.ok {
			t.Errorf("number: %q %v", tc.source, tc.path)
		}
	}
	for _, note := range []string{"末尾“依据是”后的填空区域受遮挡或不清晰，无法确认是否有可辨认的手写答案。", "末尾“依据是（ ）”处是否有可辨认的填字不确定；未将模糊痕迹转写。"} {
		if !worksheetSignalsAllowReference([]string{note}, false) {
			t.Errorf("answer-slot clauses lost their scope: %s", note)
		}
	}
	if worksheetSignalsAllowReference([]string{"笔迹不清，整页左侧缺损"}, true) {
		t.Fatal("page defect interpreted as handwriting")
	}
}

func TestTravelReferencePreservesAllCorroboratedWorksheetQuestions(t *testing.T) {
	const main = "小宁3/5小时走了6千米，按相同的速度，她每小时走多少千米？"
	const diagram = "1小时走了？km；1/5小时走了？km；3/5小时走了6km。"
	const blanks = "从上图看出：已知（ ）份是6千米，可以先求出这样的1份是多少，也就是6÷3；再求这样的（ ）份是多少。"
	const chain = "由此推算出：6÷3×5=6×1/3×5=6×(1/3×5)"
	const reason = "（km）。推算过程中把后两个因数先乘的依据是（ ）。"
	full := main + diagram + blanks + chain + "=10" + reason
	short := main + diagram + blanks + chain + reason
	confirmedDiagram := main + "\n" + diagram
	confirmedBlanks := confirmedDiagram + "\n" + blanks
	if contract := travelReportTargets(full); !strings.Contains(contract, "2 fill-in values") || !strings.Contains(contract, "justification") {
		t.Fatalf("missing printed worksheet targets: %q", contract)
	}
	withFraction := strings.Replace(full, "也就是6÷3；", `也就是6÷3=6×\frac{\square}{3}；`, 1)
	if contract := travelReportTargets(withFraction); !strings.Contains(contract, "3 fill-in values") {
		t.Fatalf("an empty numerator is one additional target: %q", contract)
	}
	if travelReportTargets(main+diagram) != "" || strings.Contains(travelReportTargets(confirmedBlanks), "justification") {
		t.Fatal("instructions included targets absent from the verified source")
	}
	const quote = "推算过程中“6×1/3×5＝6×(1/3×5)”的依据是（ ）。"
	quotedA := diagram + blanks + chain + "（km）。" + quote
	quotedB := strings.Replace(quotedA, "份是6千米", "份是8千米", 1)
	travel, _ := elementarymath.ParseUniformTravelProblem(main)
	preserved := corroboratedTravelSource(main, []string{main + quotedA, main + quotedB}, []string{quotedA, quotedB}, travel)
	if preserved != confirmedDiagram+"\n"+quote {
		t.Fatalf("independent printed equation lost or conflicting condition accepted: %s", preserved)
	}
	for _, tc := range []struct{ name, read, want string }{
		{"equivalent final calculation", full, full},
		{"wrapped heading", strings.Replace(short, "由此推算出", "由此推\n算出", 1), short},
		{"different operator", strings.Replace(full, "6÷3；", "6×3；", 1), confirmedDiagram},
		{"different condition", strings.Replace(full, "份是6千米", "份是8千米", 1), confirmedDiagram},
		{"different unit", strings.Replace(full, "（km）", "（m）", 1), confirmedBlanks},
		{"incorrect additional calculation", strings.Replace(full, "=10", "=12", 1), confirmedBlanks},
		{"missing reason", strings.Split(short, "推算过程中")[0], confirmedBlanks + "\n" + chain + "（km）。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := RecognizedQuestion{RawTranscription: short, EvidenceTranscriptions: []string{short, tc.read}}
			got, ok := verifiedLeadingTravelQuestion(q)
			if !ok || got != tc.want {
				t.Fatalf("reference = %q, want %q", got, tc.want)
			}
		})
	}
}

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
	for _, scenario := range []string{
		"latest clear question", "question denied", "ownership denied", "changed distance", "printed risk", "missing handwriting scope",
		"answer frame occluded", "typed handwriting gap", "frame belongs to another slot", "frame and printed damage", "frame and mixed handwriting note", "frame and changed unit",
	} {
		t.Run(scenario, func(t *testing.T) {
			q := cloneRecognizedQuestions([]RecognizedQuestion{fixture.Question})[0]
			before, _ := json.Marshal(q)
			var envelope map[string]any
			if decodeErr := json.Unmarshal([]byte(fixture.Physical.Content), &envelope); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			verification := envelope["verification"].(map[string]any)
			observed := envelope["items"].([]any)[0].(map[string]any)["recognition"].(map[string]any)
			if strings.Contains(scenario, "frame") || scenario == "typed handwriting gap" {
				verification["question_complete_and_legible"] = false
				observed["ocr_signals"] = []string{"末尾“的依据是”后的印刷填空左边界部分被遮挡，空内手写笔画无法辨认。"}
			}
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
			case "typed handwriting gap":
				observed["ocr_signals"] = []string{"source_gap_handwriting"}
			case "frame belongs to another slot":
				observed["ocr_signals"] = []string{"末尾“应写的单位”后的填空框被遮挡。"}
			case "frame and printed damage":
				observed["ocr_signals"] = append(observed["ocr_signals"].([]string), "起始数量条件缺失。")
			case "frame and mixed handwriting note":
				observed["ocr_signals"] = []string{"末尾“的依据是”后的印刷填空左边界部分被遮挡，空内手写笔画和题面原有乘号无法辨认。"}
			case "frame and changed unit":
				prior := observed["question"].(string)
				observed["question"] = strings.Replace(prior, "走了2千米", "走了2米", 1)
				if observed["question"] == prior {
					t.Fatal("unit fixture did not change")
				}
			}
			payload, encodeErr := json.Marshal(envelope)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			want := scenario == "latest clear question" || scenario == "answer frame occluded" || scenario == "typed handwriting gap"
			if question, got := independentReferenceFromAdjudication(request, q, string(payload)); got != want || question != "" {
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

func TestVerifiedLeadingTravelQuestionKeepsIndependentConditions(t *testing.T) {
	const primary = "小宁3/4小时步行了6千米，按相同的速度，她每小时步行多少千米？"
	const tail = "图示：1小时步行多少千米？先求一份，再求整段。"
	for _, tc := range []struct {
		name, latest string
		want         bool
	}{
		{"different worked example", "4." + primary + "\n1小时……后半部裁切", true},
		{"changed printed quantity", strings.Replace(primary, "6千米", "5千米", 1) + tail, false},
		{"changed requested unit", strings.Replace(primary, "多少千米", "多少米", 1) + tail, false},
		{"changed instruction", strings.Replace(primary, "按相同的速度", "速度改变后", 1) + tail, false},
		{"no independent primary", "根据下图回答，步行多少千米？" + tail, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := RecognizedQuestion{RawTranscription: primary + tail, SourceNumberPath: []string{"4"}, DisplayLabel: "4.",
				EvidenceTranscriptions: []string{primary + tail, "4. " + primary + tail, tc.latest}}
			question, ok := verifiedLeadingTravelQuestion(q)
			if ok != tc.want || ok && question != primary {
				t.Fatalf("independent source=%q, eligible=%t", question, ok)
			}
		})
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
			wantAnswer := scenario == "clear" || scenario == "cropped handwriting" || scenario == "restored handwriting-only risk" || scenario == "unmatched practice"
			if item.Status != PhotoAnswerUnclear || (item.ParentGuide != nil) != wantAnswer ||
				grader.callCount("4.5×2=") != 0 || (solver.callCount("4.5×2=") > 0) != wantAnswer {
				t.Fatalf("题干与学生作答风险必须分离，不能猜条件或判学生答案: %+v", item)
			}
		})
	}
}
