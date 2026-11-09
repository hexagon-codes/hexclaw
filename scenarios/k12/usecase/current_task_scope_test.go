package usecase

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
)

func TestPhotoComputationPreservesUnrelatedFrozenNumberOrDecimal(t *testing.T) {
	for _, tc := range []struct{ name, question, label string }{
		{"different question number", "13. x-1/4x=18", "12."},
		{"decimal coefficient", "12.5x-1/4x=18", "12."},
		{"missing frozen label", "12. x-1/4x=18", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := RecognizedQuestion{Subject: "数学", Question: tc.question, RawTranscription: tc.question,
				SourceNumberPath: []string{"12"}, DisplayLabel: tc.label}
			before, _ := json.Marshal(q)
			problem, source := photoQuestionProblemTexts(q)
			if problem != tc.question || source != tc.question {
				t.Fatalf("unrelated prefix was removed: problem=%q source=%q", problem, source)
			}
			after, _ := json.Marshal(q)
			if string(before) != string(after) {
				t.Fatal("computation rewrote frozen source")
			}
		})
	}
}

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
