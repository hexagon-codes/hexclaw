package k12

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
)

// RecognitionPrintedQuestionEvidenceV1 只证明印刷题干，不证明学生作答清晰或正确。
type RecognitionPrintedQuestionEvidenceV1 struct {
	RawTranscription     string   `json:"raw_transcription"`
	Confidence           float64  `json:"confidence"`
	MatchedQuestionPrior string   `json:"matched_question_prior"`
	OCRSignals           []string `json:"ocr_signals,omitempty"`
}

type RecognitionPrintedQuestionReceiptV1 struct {
	Evidence                   RecognitionPrintedQuestionEvidenceV1 `json:"evidence"`
	PlanDigest                 string                               `json:"plan_digest"`
	CandidateID                string                               `json:"candidate_id"`
	SourcePhysicalInvocationID string                               `json:"source_physical_invocation_id"`
	SourcePhysicalResultDigest string                               `json:"source_physical_result_digest"`
	AuthorizationID            string                               `json:"authorization_id"`
	SettlementDigest           string                               `json:"settlement_digest"`
}

var recognitionChinesePrintedBlankV1 = regexp.MustCompile(`[_＿]{2,}`)

// RecognitionChineseSolvePrintedComparisonV1 统一语文解题的空白排版，保留全部实际文字。
func RecognitionChineseSolvePrintedComparisonV1(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
	value = recognitionChinesePrintedBlankV1.ReplaceAllString(value, "□")
	value = strings.ReplaceAll(value, "□，我也", "□。我也")
	for _, suffix := range []string{"仿写一句话：□", "仿写一句话:□"} {
		if strings.HasSuffix(value, suffix) {
			return strings.TrimSuffix(value, "□")
		}
	}
	return value
}

func ValidateRecognitionPrintedQuestionEvidenceV1(e RecognitionPrintedQuestionEvidenceV1, plan RecognitionLayoutPlanV2, priors []string) error {
	if plan.ScopeInput == nil || plan.ScopeInput.Version != RecognitionImageScopeVersionV1 || plan.ScopeInput.Action != "solve" ||
		plan.ScopeInput.SourceReviewPolicy != RecognitionImageSourceUprightReviewPolicyV1 || plan.ScopeInput.SourceComparisonPolicy != RecognitionImageSourceComparisonPolicyV1 ||
		plan.InitialReadMode != RecognitionLayoutManifestWithContentV1 || !plan.SourceAdjudication || len(priors) != 2 ||
		strings.TrimSpace(e.RawTranscription) == "" || math.IsNaN(e.Confidence) || math.IsInf(e.Confidence, 0) || e.Confidence < .90 || e.Confidence > 1 {
		return fmt.Errorf("%w: printed question evidence is invalid", ErrRecognitionProtocolInvalid)
	}
	match := func(prior string) bool {
		return strings.TrimSpace(prior) != "" && RecognitionSourceQuestionsEqualV1(RecognitionChineseSolvePrintedComparisonV1(prior), RecognitionChineseSolvePrintedComparisonV1(e.RawTranscription))
	}
	p, r := match(priors[0]), match(priors[1])
	want := ""
	switch {
	case p && r:
		want = "both"
	case p:
		want = "primary"
	case r:
		want = "repair"
	}
	if want == "" || want != e.MatchedQuestionPrior {
		return fmt.Errorf("%w: printed question prior differs", ErrRecognitionProtocolInvalid)
	}
	return nil
}

// RecognitionPrintedQuestionEvidenceFromResponseV1 读取既有第三次裁决，不追加模型请求。
func RecognitionPrintedQuestionEvidenceFromResponseV1(raw string, plan RecognitionLayoutPlanV2, target RecognitionLayoutTargetV2, priors []string) (*RecognitionPrintedQuestionEvidenceV1, error) {
	if target.AdjudicationRegion == nil {
		return nil, nil
	}
	source, err := RecognitionLayoutAdjudicationSourceResponseV1(raw, plan, target)
	if err != nil {
		return nil, err
	}
	response, err := ParseRecognitionFinalAnswerResponseV1(source, *target.AdjudicationRegion)
	if err != nil {
		return nil, err
	}
	if !response.QuestionCompleteAndLegible || !response.TargetOwnershipConfirmed {
		return nil, nil
	}
	var envelope struct {
		Items []struct {
			Recognition struct {
				Subject    string   `json:"subject"`
				Confidence float64  `json:"recognition_confidence"`
				OCRSignals []string `json:"ocr_signals"`
			} `json:"recognition"`
		} `json:"items"`
	}
	if json.Unmarshal(response.LegacyEnvelope, &envelope) != nil || len(envelope.Items) != 1 || envelope.Items[0].Recognition.Subject != "语文" {
		return nil, nil
	}
	// 回执保留同次观察的风险；完整性声明和高分数不能抹去缺损或未决争议。
	e := RecognitionPrintedQuestionEvidenceV1{RawTranscription: response.QuestionRawTranscription, Confidence: envelope.Items[0].Recognition.Confidence, OCRSignals: envelope.Items[0].Recognition.OCRSignals}
	if len(e.OCRSignals) == 0 {
		e.OCRSignals = nil
	}
	for _, prior := range []string{"both", "primary", "repair"} {
		e.MatchedQuestionPrior = prior
		if ValidateRecognitionPrintedQuestionEvidenceV1(e, plan, priors) == nil {
			return &e, nil
		}
	}
	return nil, nil
}
