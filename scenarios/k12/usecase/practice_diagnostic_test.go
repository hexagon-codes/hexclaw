package usecase

import (
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestPracticeDiagnosticRequiresObservedAnswerAndFreezesHypothesis(t *testing.T) {
	source := &records.AgentRecord{RecordID: "source-disk", Version: 7}
	fields := k12.MistakeFields{Question: "已用空间是总容量的2/5，正好用了4.8 TB，求总容量。", CanonicalAnswer: "12 TB", ObservedStudentAnswer: "2/5÷2=1/5"}
	diagnostic := singlePracticeDiagnostic(source, fields)
	if diagnostic == nil || diagnostic.SourceID != "source-disk" || diagnostic.SourceVersion != 7 || diagnostic.Hypothesis != "may_confuse_ratio_and_quantity" || diagnostic.Observation != fields.ObservedStudentAnswer {
		t.Fatalf("diagnostic lost observation or uncertain hypothesis: %+v", diagnostic)
	}
	prompt := singlePracticePrompt(singlePracticeRequestSnapshot{Difficulty: "same", Diagnostic: diagnostic})
	if !strings.Contains(prompt, "exactly one discriminating question") || !strings.Contains(prompt, "reference answer") || !strings.Contains(prompt, "not an established learner trait") {
		t.Fatal("diagnostic prompt omits stop, answer or evidence boundary")
	}
	fields.ObservedStudentAnswer = ""
	fields.WrongProcess = "单位错误"
	if singlePracticeDiagnostic(source, fields) != nil {
		t.Fatal("summary or unreadable work became observed evidence")
	}
	for _, state := range []AnswerState{AnswerStateUnclear, AnswerStateBlank} {
		if clearObservedStudentAnswer(GradeRequest{ObservedAnswerState: state, StudentAnswer: "12 TB"}) != "" {
			t.Fatal("reference answer became a student observation")
		}
	}
}
