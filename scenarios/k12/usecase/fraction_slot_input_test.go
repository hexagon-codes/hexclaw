package usecase

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
)

func TestFractionSlotPhotoInputProjectsOnlyCurrentPrintedTask(t *testing.T) {
	const parent = "小明2/3小时走了2千米，照这样的速度，他每小时行走多少千米？\n\n线段图：1小时？km；1/3小时？km；2/3小时2km。"
	for _, current := range []string{"也就是2÷2＝2×□/□；", "也就是2×□/□×3。"} {
		q := RecognizedQuestion{ProblemKind: ProblemKindSubproblem, ParentProblemID: "parent", SubproblemNo: "求路程",
			Question: parent + "\n\n" + current, RawTranscription: current, StudentAnswer: "1/2", Subject: "数学", AnswerState: AnswerStatePresent}
		before, _ := json.Marshal(q)
		req := photoItemGradeRequest(PhotoGradeRequest{AgentName: "child", Grade: "六年级上"}, q)
		shared, target, ok := elementarymath.ParseFractionSlotProblem(req.Problem)
		if !ok || shared != q.Question || target != current || req.StudentAnswer != "1/2" {
			t.Fatalf("printed target lost or replaced by subproblem label: %+v", req)
		}
		after, _ := json.Marshal(q)
		if string(before) != string(after) {
			t.Fatal("input projection changed frozen recognition facts")
		}
		q.SubproblemNo = ""
		if unnamed := photoItemGradeRequest(PhotoGradeRequest{AgentName: "child", Grade: "六年级上"}, q); unnamed.Problem != req.Problem {
			t.Fatal("display label changed the printed answer target")
		}
	}
	for _, q := range []RecognizedQuestion{
		{ProblemKind: ProblemKindSubproblem, SubproblemNo: "求一份", Question: "已知（ ）份是2千米。", RawTranscription: "已知（ ）份是2千米。"},
		{Question: "2×□/□", RawTranscription: "2×□/□"},
		{ProblemKind: ProblemKindSubproblem, SubproblemNo: "求一份", Question: parent, RawTranscription: "求一份走多少千米？"},
		{ProblemKind: ProblemKindSubproblem, SubproblemNo: "求一份", Question: "2×□/□", RawTranscription: "2×□/□", Subject: "语文"},
	} {
		req := photoItemGradeRequest(PhotoGradeRequest{AgentName: "child", Grade: "六年级上"}, q)
		if _, _, ok := elementarymath.ParseFractionSlotProblem(req.Problem); ok {
			t.Fatal("unrelated question entered fraction slot scope")
		}
		if !strings.HasPrefix(req.Problem, q.Question) {
			t.Fatal("unrelated question's existing input changed")
		}
	}
}

func TestFractionSlotPhantomSourcesProjectCurrentCanonical(t *testing.T) {
	const parent = `1. 小明 $\frac{2}{3}$ 小时走了 $2$ 千米，照这样的速度，他每小时行走多少千米？

线段图标注：
- $1$ 小时走了？$\mathrm{km}$
- $\frac{1}{3}$ 小时走了？$\mathrm{km}$
- $\frac{2}{3}$ 小时走了 $2\,\mathrm{km}$`
	for _, tc := range []struct{ id, raw, canonical, digest string }{
		{"c", "2÷2＝2×　/　；", `$2\div 2=2\times\frac{\phantom{1}}{\phantom{2}}$；`, "0c0216aa3f6a2ba31bb31876d9f1fdf4e588974814e08a9f4648185b2bdf3429"},
		{"e", "也就是2×　/　×3。", `也就是 $2\times\frac{\phantom{1}}{\phantom{2}}\times3$。`, "5770856a83328f13ee18fd14249900865739f8634c7e1baf7f9ab8e2cd61b4ca"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			questions := []RecognizedQuestion{
				{ProblemID: "parent", ProblemKind: ProblemKindCompoundParent, Question: parent, CanonicalMarkdown: parent, Subject: "数学"},
				{ProblemID: tc.id, ProblemKind: ProblemKindSubproblem, ParentProblemID: "parent", SubproblemNo: tc.id,
					Question: tc.canonical, CanonicalMarkdown: tc.canonical, RawTranscription: tc.raw,
					CanonicalVersion: 1, ConfirmedVersion: 1, InputDigest: tc.digest,
					Subject: "数学", AnswerState: AnswerStatePresent, StudentAnswer: "1/2", AnswerRawTranscription: "1/2"},
			}
			before, _ := json.Marshal(questions)
			assessed := RecognizedQuestionsForAssessment(questions)
			if len(assessed) != 1 {
				t.Fatal("frozen source did not produce one selected subproblem")
			}
			req := photoItemGradeRequest(PhotoGradeRequest{AgentName: "child", Grade: "六年级上"}, assessed[0])
			shared, current, ok := elementarymath.ParseFractionSlotProblem(req.Problem)
			if !ok || current != tc.raw || !strings.Contains(shared, "千米") || !strings.Contains(req.Problem, `\phantom`) {
				t.Fatalf("same-source phantom fraction lost current answer scope: projected=%t current=%q", ok, current)
			}
			after, _ := json.Marshal(questions)
			if string(before) != string(after) {
				t.Fatal("assessment changed the frozen raw/canonical sources")
			}
			var restored []RecognizedQuestion
			if err := json.Unmarshal(before, &restored); err != nil {
				t.Fatal(err)
			}
			restoredAssessment := RecognizedQuestionsForAssessment(restored)
			if restoredReq := photoItemGradeRequest(PhotoGradeRequest{AgentName: "child", Grade: "六年级上"}, restoredAssessment[0]); restoredReq.Problem != req.Problem {
				t.Fatal("reloading the same frozen sources changed scope identity")
			}
			invalidSource := append([]RecognizedQuestion(nil), questions...)
			invalidSource[1].CanonicalMarkdown = `\(` + tc.canonical
			if CanonicalMarkdownValid(invalidSource[1].CanonicalMarkdown) {
				t.Fatal("unclosed canonical fixture was unexpectedly valid")
			}
			invalidAssessment := RecognizedQuestionsForAssessment(invalidSource)
			invalidReq := photoItemGradeRequest(PhotoGradeRequest{AgentName: "child", Grade: "六年级上"}, invalidAssessment[0])
			if _, _, ok := elementarymath.ParseFractionSlotProblem(invalidReq.Problem); ok || currentAssessmentCanonical(invalidAssessment[0]) != "" {
				t.Fatal("invalid current canonical became fraction slot proof")
			}
			if invalidSource[1].RawTranscription != tc.raw || invalidSource[1].CanonicalMarkdown != `\(`+tc.canonical {
				t.Fatal("rejecting invalid canonical changed original source facts")
			}
			for _, drift := range []string{"input digest", "problem identity", "confirmed revision", "canonical revision", "raw"} {
				changed := assessed[0]
				switch drift {
				case "input digest":
					changed.InputDigest = "other-input"
				case "problem identity":
					changed.ProblemID = "other-problem"
				case "confirmed revision":
					changed.ConfirmedVersion++
				case "canonical revision":
					changed.CanonicalVersion++
				case "raw":
					changed.RawTranscription += " "
				}
				changedReq := photoItemGradeRequest(PhotoGradeRequest{AgentName: "child", Grade: "六年级上"}, changed)
				if _, _, ok := elementarymath.ParseFractionSlotProblem(changedReq.Problem); ok {
					t.Fatalf("old canonical proof survived changed %s", drift)
				}
			}
		})
	}
}
