package usecase

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
)

func TestMathSubproblemCapturedSourcesKeepEachActualAnswerTarget(t *testing.T) {
	raw, err := os.ReadFile("../testdata/d20_current_tasks.json")
	if err != nil {
		t.Fatal(err)
	}
	var questions []RecognizedQuestion
	if err := json.Unmarshal(raw, &questions); err != nil {
		t.Fatal(err)
	}
	for i := range questions {
		questions[i].RawTranscription = questions[i].Question
		questions[i].CanonicalVersion, questions[i].ConfirmedVersion = 1, 1
		questions[i].InputDigest = "captured-" + questions[i].ProblemID
	}
	before, _ := json.Marshal(questions)
	assessed := RecognizedQuestionsForAssessment(questions)
	if len(assessed) != 7 {
		t.Fatalf("captured source lost a child target: %d", len(assessed))
	}
	for i, q := range assessed {
		request := photoItemGradeRequest(PhotoGradeRequest{AgentName: "child", Grade: "六年级上"}, q)
		switch i {
		case 0, 1, 6:
			shared, current, canonical, ok := elementarymath.ParseMathSubproblem(request.Problem)
			if !ok || shared != q.Question || current != questions[i+1].RawTranscription || canonical != questions[i+1].CanonicalMarkdown {
				t.Fatalf("child %d was addressed by system ordinal or another target: %q", i+1, request.Problem)
			}
		case 2, 4:
			_, current, _, ok := elementarymath.ParsePartCountSlotProblemSources(request.Problem)
			if !ok || current != questions[i+1].RawTranscription {
				t.Fatalf("child %d lost its sole count blank", i+1)
			}
		case 3, 5:
			_, current, ok := elementarymath.ParseFractionSlotProblem(request.Problem)
			if !ok || current != questions[i+1].RawTranscription {
				t.Fatalf("child %d lost its sole fraction blank", i+1)
			}
		}
		q.SubproblemNo = "different-system-order"
		if changed := photoItemGradeRequest(PhotoGradeRequest{AgentName: "child", Grade: "六年级上"}, q); changed.Problem != request.Problem {
			t.Fatal("system ordering redefined the actual question")
		}
	}
	after, _ := json.Marshal(questions)
	if string(before) != string(after) {
		t.Fatal("assessment rewrote original captured source")
	}
}
