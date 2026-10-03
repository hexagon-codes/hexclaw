package usecase

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func printedPracticeQuestions() []RecognizedQuestion {
	return []RecognizedQuestion{
		{
			ProblemKind:       ProblemKindStandalone,
			SourceSectionPath: []string{"二"}, SourceSectionLabel: "二 分数乘法",
			Question: "讨论一下：分数乘整数，怎样计算？",
			Subject:  "数学", AnswerState: AnswerStateBlank,
		},
		{
			ProblemKind:       ProblemKindStandalone,
			SourceSectionPath: []string{"二"}, SourceSectionLabel: "二 分数乘法\n做一做",
			Question: "一袋面包重3/10 kg，3袋重多少千克？\n□×□=□（kg）",
			Subject:  "数学", AnswerState: AnswerStatePresent, StudentAnswer: "9/10千克",
		},
	}
}

func TestNormalizePrintedPracticeHeadingPreservesFactsInEitherOrder(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "base_heading_first"
		if reverse {
			name = "practice_caption_first"
		}
		t.Run(name, func(t *testing.T) {
			input := printedPracticeQuestions()
			if reverse {
				input[0], input[1] = input[1], input[0]
			}
			before, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			got, err := NormalizeRecognizedProblems("printed-practice-page", input)
			if err != nil {
				t.Fatalf("same chapter with a practice caption must normalize: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("questions=%d, want 2", len(got))
			}
			for i, question := range got {
				if question.SourceSectionLabel != "二 分数乘法" ||
					!reflect.DeepEqual(question.SourceSectionPath, input[i].SourceSectionPath) {
					t.Fatalf("source heading changed beyond the practice caption: %#v", question)
				}
				if question.Question != input[i].Question ||
					question.StudentAnswer != input[i].StudentAnswer ||
					question.AnswerState != input[i].AnswerState ||
					!reflect.DeepEqual(question.SourceNumberPath, input[i].SourceNumberPath) ||
					question.DisplayLabel != input[i].DisplayLabel {
					t.Fatalf("normalization changed question/answer/source-number facts at %d: %#v", i, question)
				}
				if question.SystemSectionOrdinal != i+1 {
					t.Fatalf("system ordinal=%d at %d, want %d", question.SystemSectionOrdinal, i, i+1)
				}
			}
			after, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("new-input normalization must not mutate the original recognition facts")
			}
		})
	}
}

func TestNormalizePrintedPracticeHeadingRetainsRealConflicts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		label string
	}{
		{"different_numbered_heading", "二 分数除法\n做一做"},
		{"different_caption", "二 分数乘法\n练一练"},
		{"additional_heading", "二 分数乘法\n做一做\n三 分数除法"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := printedPracticeQuestions()
			input[1].SourceSectionLabel = tc.label
			if _, err := NormalizeRecognizedProblems("conflicting-printed-heading", input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("conflicting source headings must remain invalid, err=%v", err)
			}
		})
	}
}

func TestNormalizePrintedPracticeHeadingRequiresSamePathBaseEvidence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []RecognizedQuestion
	}{
		{
			name:  "no_base_heading",
			input: printedPracticeQuestions()[1:],
		},
		{
			name: "base_heading_has_different_path",
			input: func() []RecognizedQuestion {
				questions := printedPracticeQuestions()
				questions[1].SourceSectionPath = []string{"二", "一"}
				return questions
			}(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeRecognizedProblems("no-same-path-base", tc.input)
			if err != nil {
				t.Fatal(err)
			}
			last := got[len(got)-1]
			if last.SourceSectionLabel != "二 分数乘法\n做一做" {
				t.Fatalf("a caption without same-path base evidence must not be truncated: %#v", last)
			}
		})
	}
}

func TestPrintedPracticeHeadingDoesNotRewriteHistoricalSnapshot(t *testing.T) {
	normalized, err := NormalizeRecognizedProblems("historical-printed-heading", printedPracticeQuestions())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := RecognizedQuestionsProblemAttemptSnapshot(
		"printed-heading-agent", "historical-printed-heading", normalized, 100,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Problems[1].SourceSectionLabel = "二 分数乘法\n做一做"
	before, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RecognizedQuestionsFromProblemAttemptSnapshot(snapshot); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("historical conflicting headings must retain strict snapshot validation, err=%v", err)
	}
	after, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("snapshot restoration must not rewrite historical source facts")
	}
}
