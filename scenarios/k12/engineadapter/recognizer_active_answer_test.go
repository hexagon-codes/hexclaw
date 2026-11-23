package engineadapter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

func TestAdjudicatedActiveWorkRequiresIndependentCompleteLines(t *testing.T) {
	const firstWork = "2y=16\ny=6\ny=8"
	const clean = "2y=16\ny=8"
	const marked = "\\(2y=16\\)\n\\(y=6\\) [crossed out]\n\\(y=8\\)"
	for _, tc := range []struct {
		name, first, review, adjudicated, final string
		expected                                bool
	}{
		{"same answer area and independently confirmed active lines", firstWork, clean, marked, "y = 8", true},
		{"whole equation in plain text", firstWork, clean, strings.Replace(marked, "\\(y=6\\)", "y=6", 1), "y=8", true},
		{"wrong active line retained independently", "2y=12\ny=6\ny=8", "2y=12\ny=8", strings.Replace(marked, "2y=16", "2y=12", 1), "y=8", true},
		{"mark attached to a part of the line", firstWork, clean, strings.Replace(marked, "\\(y=6\\) [crossed out]", "y=\\(6\\) [crossed out]", 1), "y=8", false},
		{"annotation refers to adjacent line", firstWork, clean, strings.Replace(marked, " [crossed out]", " [line above crossed out]", 1), "y=8", false},
		{"still active conflict in review", firstWork, "2y=16\ny=6\ny=8", marked, "y=8", false},
		{"original did not observe that line", "2y=16\ny=9\ny=8", clean, marked, "y=8", false},
		{"independently confirmed end belongs to other reading", firstWork, clean, marked, "y=4", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observation := func(ans string) usecase.RecognizedQuestion {
				return usecase.RecognizedQuestion{Subject: "数学", AnswerState: usecase.AnswerStatePresent, SourceNumberPath: []string{"5"}, AnswerRawTranscription: ans}
			}
			fields := map[string]any{"student_answer": tc.adjudicated, "question": "2y=16", "answer_bbox": []int{1, 2, 3, 4}}
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			original := bytes.Clone(raw)
			output := projectAdjudicatedActiveAnswer(raw, observation(tc.first), observation(tc.review), observation(tc.adjudicated), tc.final)
			if tc.expected {
				fields["student_answer"] = tc.review
			}
			want, _ := json.Marshal(fields)
			if !bytes.Equal(output, want) || !bytes.Equal(raw, original) {
				t.Fatalf("projected observation=%s, want %s", output, want)
			}
			other := observation(tc.review)
			other.SourceNumberPath = []string{"6"}
			if got := projectAdjudicatedActiveAnswer(raw, observation(tc.first), other, observation(tc.adjudicated), tc.final); !bytes.Equal(got, original) {
				t.Fatal("another target supplied the active answer")
			}
		})
	}
}
