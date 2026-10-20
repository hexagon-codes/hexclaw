package usecase

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestSharedSourceContextRestoresOnlyExactFrozenQuestion(t *testing.T) {
	o := &GradingOrchestrator{runDir: t.TempDir()}
	job := GradingJobView{Record: &records.AgentRecord{RecordID: "source-context-job", AgentName: "source-context-agent"}}
	q := RecognizedQuestion{ProblemID: "child-one", AttemptID: "answer-one", ProblemKind: ProblemKindStandalone, RawTranscription: "先求一份，再求总容量。", Question: "先求一份，再求总容量。", CanonicalMarkdown: "先求一份，再求总容量。", CanonicalVersion: 1, ConfirmedVersion: 1, Subject: "数学", AnswerState: AnswerStateBlank,
		ImageSourceContext: &k12.RecognitionImageSourceContextV1{ScopeDigest: "frozen-plan", TargetIDs: []string{"child-one"}, Materials: []k12.RecognitionImageScopeDependencyV1{{ForOrders: []int{1}, Text: "已用空间是总容量的2/5，正好用了4.8 TB。", Region: k12.SourcePixelRegion{Width: 80, Height: 100}}}},
	}
	q = FreezeRecognizedQuestionInputDigests([]RecognizedQuestion{q})[0]
	receipt := gradingRecognitionReceiptFile{JobID: job.Record.RecordID, AgentName: job.Record.AgentName, InvocationID: "original-first-read", Questions: []RecognizedQuestion{q}, CanonicalDigest: CanonicalRecognizedQuestionsDigest([]RecognizedQuestion{q})}
	data, _ := json.Marshal(receipt)
	path := o.recognitionReceiptPath(job.Record.RecordID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"exact", "another question", "changed input", "guardian correction"} {
		t.Run(scenario, func(t *testing.T) {
			current := q
			current.ImageSourceContext = nil
			switch scenario {
			case "another question":
				current.ProblemID = "child-two"
			case "changed input":
				current.InputDigest = "different-input"
			case "guardian correction":
				current.ConfirmedVersion = 2
				current.CanonicalMarkdown = "已修改的题目"
			}
			questions := []RecognizedQuestion{current}
			o.restoreRecognitionSourceContexts(job, questions)
			if (questions[0].ImageSourceContext != nil) != (scenario == "exact") {
				t.Fatal("restored materials to a different source")
			}
			if scenario == "exact" {
				req := photoItemGradeRequest(PhotoGradeRequest{AgentName: job.Record.AgentName}, questions[0])
				if !strings.Contains(req.Problem, "4.8 TB") || !strings.Contains(req.Problem, "2/5") {
					t.Fatal("solver did not receive both shared conditions")
				}
			}
		})
	}
}
