package usecase

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestImageScopeSolveIgnoresOnlyAnswerConflict(t *testing.T) {
	confidence := 0.99
	first := "河上有5座桥。这句话运用了________的修辞手法，写出了________，我也仿写一句话：\n________________"
	second := "河上有5座桥。这句话运用了______的修辞手法，写出了______。我也仿写一句话：______"
	q := NormalizeRecognizedQuestionForInitialReadMode(RecognizedQuestion{Question: second, RawTranscription: second, CanonicalMarkdown: second, Subject: "语文", SourceNumberPath: []string{"1"}, AnswerState: AnswerStatePresent, StudentAnswer: "比喻\n金水河", AnswerRawTranscription: "比喻\n金水河", AnswerCanonicalMarkdown: "比喻\n金水河", RecognitionConfidence: &confidence, EvidenceTranscriptions: []string{first, second}, AnswerEvidenceTranscriptions: []string{"比喻\n轻巧", "比喻\n金水河"}, ImageSourceContext: &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1, Action: "solve", ScopeDigest: "frozen", TargetIDs: []string{"question-1"}}}, k12.RecognitionLayoutManifestWithContentV1)
	before := cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
	if recognizedQuestionRequiresGuardianConfirmationForInitialReadMode(q, PhotoTaskBlankWorksheet, k12.RecognitionLayoutManifestWithContentV1) {
		t.Fatal("unrelated handwriting blocked explicit solving")
	}
	if !recognizedQuestionRequiresGuardianConfirmationForInitialReadMode(q, PhotoTaskCompletedHomework, k12.RecognitionLayoutManifestWithContentV1) {
		t.Fatal("answer conflict was ignored during grading")
	}
	if !reflect.DeepEqual(q, before) {
		t.Fatal("assessment view changed source observations")
	}
	q.EvidenceTranscriptions = []string{second, "河上有6座桥。这句话运用了______的修辞手法，写出了______。我也仿写一句话：______"}
	if !recognizedQuestionRequiresGuardianConfirmationForInitialReadMode(q, PhotoTaskBlankWorksheet, k12.RecognitionLayoutManifestWithContentV1) {
		t.Fatal("printed number conflict was discarded")
	}
	q.EvidenceTranscriptions = []string{first, second}
	q.ImageSourceContext.Recoveries = []k12.RecognitionImageSourceRecoveryV1{{Gap: k12.RecognitionImageSourceGapV1{Order: 1, Kind: "instruction", Before: "visible"}, State: "unresolved"}}
	if !recognizedQuestionRequiresGuardianConfirmationForInitialReadMode(q, PhotoTaskBlankWorksheet, k12.RecognitionLayoutManifestWithContentV1) {
		t.Fatal("unresolved exercise instruction was treated as complete")
	}
}

func TestImageScopeSourceContextRestoresFromConfirmedRun(t *testing.T) {
	o := &GradingOrchestrator{runDir: t.TempDir()}
	q := RecognizedQuestion{ProblemID: "question-1", AttemptID: "attempt-1", ProblemKind: ProblemKindStandalone, RawTranscription: "完整题干", Question: "完整题干", CanonicalMarkdown: "完整题干", CanonicalVersion: 1, ConfirmedVersion: 1, Subject: "语文", AnswerState: AnswerStateBlank, ImageSourceContext: &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1, Action: "solve", ScopeDigest: "scope", TargetIDs: []string{"target-1"}}}
	q = FreezeRecognizedQuestionInputDigests([]RecognizedQuestion{q}, "六年级上")[0]
	run := &gradingRun{agentName: "test-agent", req: PhotoGradeRequest{AgentName: "test-agent", Image: []byte("source"), Grade: "六年级上"}, questions: []RecognizedQuestion{q}}
	if err := o.persistRun("test-job", run); err != nil {
		t.Fatal(err)
	}
	current := q
	current.ImageSourceContext = nil
	questions := []RecognizedQuestion{current}
	o.restoreRecognitionSourceContexts(GradingJobView{Record: &records.AgentRecord{RecordID: "test-job", AgentName: "test-agent"}}, questions)
	if !reflect.DeepEqual(questions[0].ImageSourceContext, q.ImageSourceContext) {
		t.Fatal("confirmed run source context was lost")
	}
}

func TestImageScopeVerifiedQuotationDoesNotRewriteOCR(t *testing.T) {
	confidence := 0.5
	before, after := "弯弯的内金水河像", "横贯东西，河上是五座精美的汉白玉石桥。"
	raw := before + "[missing]" + after + "这句话使用了什么修辞？"
	q := RecognizedQuestion{Question: raw, RawTranscription: raw, CanonicalMarkdown: raw, CanonicalVersion: 1, Subject: "语文", AnswerState: AnswerStateBlank, RecognitionConfidence: &confidence, OCRSignals: []string{"source_gap_quotation"}, ImageSourceContext: &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1, Action: "solve", ScopeDigest: "frozen", TargetIDs: []string{"target-1"}, Recoveries: []k12.RecognitionImageSourceRecoveryV1{{Gap: k12.RecognitionImageSourceGapV1{Order: 1, Kind: "quotation", Before: before, After: after}, State: "verified", AdoptedText: "一条玉带", DocumentID: "textbook-document", DocumentGeneration: 2, SourceDigest: "source-digest", CitationDigest: "page-digest", TextbookBindingID: "binding", TextbookManifestID: "manifest"}}}}
	result := NormalizeRecognizedQuestionForInitialReadMode(q, k12.RecognitionLayoutManifestWithContentV1)
	if result.ConfirmationRequired || result.RawTranscription != raw || *result.RecognitionConfidence != 0.5 {
		t.Fatal("verified processing material did not preserve separate OCR facts")
	}
	request := photoItemGradeRequest(PhotoGradeRequest{}, result)
	if !strings.Contains(request.Problem, before+"一条玉带"+after) {
		t.Fatal("verified source was not supplied to the solver")
	}
	q.ImageSourceContext.Recoveries[0].State = "candidate_requires_source_verification"
	if !NormalizeRecognizedQuestionForInitialReadMode(q, k12.RecognitionLayoutManifestWithContentV1).ConfirmationRequired {
		t.Fatal("unverified source cleared source risk")
	}
	q.ImageSourceContext.Recoveries[0].State = "verified"
	q.ImageSourceContext.Recoveries[0].Gap.Kind = "instruction"
	if !NormalizeRecognizedQuestionForInitialReadMode(q, k12.RecognitionLayoutManifestWithContentV1).ConfirmationRequired {
		t.Fatal("textbook quotation was used to complete an exercise instruction")
	}
}
