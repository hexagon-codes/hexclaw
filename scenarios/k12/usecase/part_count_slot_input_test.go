package usecase

import (
	"encoding/json"
	"strings"
	"testing"
)

const partCountSlotRawFixture = "从上图看出：已知（ ）份是2千米，可以先求出这样的1份是多少，也就是"
const partCountSlotCanonicalFixture = "从上图看出：已知（　）份是 $2$ 千米，可以先求出这样的 $1$ 份是多少，也就是"
const partCountSlotSquareRawFixture = "从上图看出：已知（　）份是2千米，可以先求出这样的1份是多少，也就是"
const partCountSlotSquareCanonicalFixture = `从上图看出：已知（$\square$）份是 $2$ 千米，可以先求出这样的 $1$ 份是多少，也就是……`

func TestPartCountSlotPhotoInputKeepsCurrentPrintedTarget(t *testing.T) {
	for _, source := range []struct{ name, raw, canonical, digest string }{
		{"empty parentheses", partCountSlotRawFixture, partCountSlotCanonicalFixture, "f91b5b1ef3fae3795391019ab350de443d93a0cfc09dd4c28a4fb90c17ffd0fb"},
		{"square parentheses", partCountSlotSquareRawFixture, partCountSlotSquareCanonicalFixture, "f77bd112d603cc62c6455ae2ffe342cadf1be9ed84c42c63b1e14c4f4bae5de6"},
	} {
		t.Run(source.name, func(t *testing.T) {
			partCountSlotPhotoInputKeepsCurrentPrintedTarget(t, source.raw, source.canonical, source.digest)
		})
	}
}

func partCountSlotPhotoInputKeepsCurrentPrintedTarget(t *testing.T, raw, canonical, digest string) {
	t.Helper()
	questions := []RecognizedQuestion{
		{ProblemID: "parent", ProblemKind: ProblemKindCompoundParent, Question: fractionSlotSharedFixture, RawTranscription: fractionSlotSharedFixture, CanonicalMarkdown: fractionSlotSharedFixture, Subject: "数学"},
		{ProblemID: "part-count", ProblemKind: ProblemKindSubproblem, ParentProblemID: "parent", SubproblemNo: "b",
			Question: canonical, RawTranscription: raw, CanonicalMarkdown: canonical,
			CanonicalVersion: 1, ConfirmedVersion: 1, InputDigest: digest,
			Subject: "数学", StudentAnswer: "2/3", AnswerState: AnswerStatePresent},
	}
	before, _ := json.Marshal(questions)
	assessed := RecognizedQuestionsForAssessment(questions)
	req := photoItemGradeRequest(PhotoGradeRequest{AgentName: "child", Grade: "六年级上"}, assessed[0])
	if !strings.HasPrefix(req.Problem, "k12-part-count-slot-v1\n") || !strings.Contains(req.Problem, raw) || req.StudentAnswer != "2/3" {
		t.Fatalf("part-count target lost or replaced by parent distance: %+v", req)
	}
	after, _ := json.Marshal(questions)
	if string(before) != string(after) {
		t.Fatal("part-count projection rewrote original sources")
	}
	var restored []RecognizedQuestion
	if err := json.Unmarshal(before, &restored); err != nil {
		t.Fatal(err)
	}
	if replay := photoItemGradeRequest(PhotoGradeRequest{AgentName: "child", Grade: "六年级上"}, RecognizedQuestionsForAssessment(restored)[0]); replay.Problem != req.Problem {
		t.Fatal("restored source changed the part-count target identity")
	}
}
