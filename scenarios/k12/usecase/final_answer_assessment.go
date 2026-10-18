package usecase

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

const finalAnswerOnlyAssessmentWarning = "Only the final answer was judged. Intermediate working remains uncertain."

func finalAnswerAssessmentEligible(req PhotoGradeRequest, q RecognizedQuestion) bool {
	if !k12.RecognitionFinalAnswerExecutionPolicy(req.RecognitionExecutionPolicy) || !req.SourceUncertaintyFinalized ||
		q.FinalAnswerReceipt == nil || q.ConfirmedVersion != 1 || q.ProblemKind != ProblemKindStandalone || q.parentSourceUnclear ||
		q.Subject != "数学" || q.AnswerState != AnswerStatePresent || req.TaskIntent == PhotoTaskBlankWorksheet ||
		req.practiceReference != nil || len(req.PracticeReferences) != 0 {
		return false
	}
	if !k12.RecognitionFinalAnswerSourcesTrustedV1(finalAnswerQuestionSourceV1(q)) {
		return false
	}
	checked := EvaluateOCRConfirmationRiskForInitialReadMode(q, req.InitialReadMode)
	for _, reason := range checked.ConfirmationReasons {
		if reason != OCRRiskEvidenceConflict {
			return false
		}
	}
	return len(checked.ConfirmationReasons) != 0 || k12.RecognitionSourceObservationUnresolvedV1(q.OCRSignals)
}

func finalAnswerQuestionSourceV1(q RecognizedQuestion) k12.RecognitionFinalAnswerSourceV1 {
	return k12.RecognitionFinalAnswerSourceV1{Question: q.RawTranscription, Subject: q.Subject, AnswerState: string(q.AnswerState), Confidence: q.RecognitionConfidence, OCRSignals: q.OCRSignals}
}

func finalAnswerObservationUnresolved(req PhotoGradeRequest, q RecognizedQuestion) bool {
	return k12.RecognitionFinalAnswerExecutionPolicy(req.RecognitionExecutionPolicy) &&
		k12.RecognitionSourceObservationUnresolvedV1(q.OCRSignals)
}

// 识题原文与当前计算投影必须仍相同，不能将旧最终答案证据套到人工修改后的题目或作答。
func finalAnswerCandidateMatchesQuestion(raw json.RawMessage, q RecognizedQuestion) bool {
	var candidate struct {
		Question      string      `json:"question"`
		StudentAnswer string      `json:"student_answer"`
		Subject       string      `json:"subject"`
		AnswerState   AnswerState `json:"answer_state"`
	}
	if json.Unmarshal(raw, &candidate) != nil || candidate.Subject != q.Subject || candidate.AnswerState != q.AnswerState ||
		strings.TrimSpace(candidate.Question) == "" || strings.TrimSpace(candidate.StudentAnswer) == "" ||
		strings.TrimSpace(candidate.Question) != strings.TrimSpace(q.RawTranscription) ||
		strings.TrimSpace(candidate.StudentAnswer) != strings.TrimSpace(q.AnswerRawTranscription) {
		return false
	}
	return k12.RecognitionSourceQuestionsEqualV1(candidate.Question, q.Question) && k12.RecognitionSourceQuestionsEqualV1(candidate.Question, q.CanonicalMarkdown) &&
		k12.RecognitionSourceQuestionsEqualV1(candidate.StudentAnswer, q.StudentAnswer) && k12.RecognitionSourceQuestionsEqualV1(candidate.StudentAnswer, q.AnswerCanonicalMarkdown)
}

func (o *GradingOrchestrator) verifiedFinalAnswerSolution(ctx context.Context, deps Deps, job GradingJobView, req PhotoGradeRequest, q RecognizedQuestion) (string, bool, error) {
	if !finalAnswerAssessmentEligible(req, q) {
		return "", false, nil
	}
	if deps.Records == nil {
		return "", false, fmt.Errorf("final answer receipt store is unavailable")
	}
	if recomputed := FreezeRecognizedQuestionInputDigests([]RecognizedQuestion{q}, req.Grade); len(recomputed) != 1 || recomputed[0].InputDigest != q.InputDigest {
		return "", false, nil
	}
	raw, err := deps.Records.ValidateRecognitionFinalAnswerReceiptV1(ctx, job.Record.AgentName, job.Record.RecordID, *q.FinalAnswerReceipt)
	if err != nil {
		if errors.Is(err, k12storage.ErrModelPhysicalInvocationConflict) || errors.Is(err, records.ErrNotFound) || errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	if !finalAnswerCandidateMatchesQuestion(raw, q) {
		return "", false, nil
	}
	var verifier ArithmeticFinalAnswerVerifier
	if deps.VerifiedGrader != nil {
		verifier, _ = deps.VerifiedGrader.(ArithmeticFinalAnswerVerifier)
	} else {
		verifier, _ = deps.Grader.(ArithmeticFinalAnswerVerifier)
	}
	if verifier == nil {
		return "", false, nil
	}
	solution, wrong := verifier.VerifyIncorrectArithmeticFinalAnswer(q.Question, q.FinalAnswerReceipt.Evidence.RawTranscription)
	return solution, wrong && strings.TrimSpace(solution) != "", nil
}

// 类型化档案不持久扩展识题字段；恢复只从同任务回执寻找并由真实最终化账本复核。
func (o *GradingOrchestrator) restoreFinalAnswerReceipts(ctx context.Context, job GradingJobView, questions []RecognizedQuestion) {
	var sources []RecognizedQuestion
	if receipt, ok := o.readRecognitionReceipt(job.Record.RecordID); ok && receipt.AgentName == job.Record.AgentName {
		sources = receipt.Questions
	}
	if len(sources) == 0 {
		if audit, ok := o.readRecognitionAudit(job.Record.RecordID); ok && audit.AgentName == job.Record.AgentName {
			sources = audit.Questions
		}
	}
	for i := range questions {
		q := &questions[i]
		q.FinalAnswerReceipt = nil
		if q.ConfirmedVersion != 1 || q.ProblemKind != ProblemKindStandalone {
			continue
		}
		for _, source := range sources {
			if source.FinalAnswerReceipt == nil || source.ConfirmedVersion > 1 || source.ProblemID != q.ProblemID || source.AttemptID != q.AttemptID ||
				source.RawTranscription != q.RawTranscription || source.AnswerRawTranscription != q.AnswerRawTranscription {
				continue
			}
			raw, err := o.deps.Records.ValidateRecognitionFinalAnswerReceiptV1(ctx, job.Record.AgentName, job.Record.RecordID, *source.FinalAnswerReceipt)
			if err != nil || !finalAnswerCandidateMatchesQuestion(raw, *q) {
				continue
			}
			receipt := *source.FinalAnswerReceipt
			q.FinalAnswerReceipt = &receipt
			// 类型化投影不含观察数组；只从同一已核验候选恢复原始风险，不能凭回执消除过程歧义。
			var observations struct {
				Signals   []string `json:"ocr_signals"`
				Questions []string `json:"evidence_transcriptions"`
				Answers   []string `json:"answer_evidence_transcriptions"`
			}
			if json.Unmarshal(raw, &observations) == nil {
				q.OCRSignals = append([]string(nil), observations.Signals...)
				q.EvidenceTranscriptions = append([]string(nil), observations.Questions...)
				q.AnswerEvidenceTranscriptions = append([]string(nil), observations.Answers...)
			}
			box := receipt.Evidence.BBox
			q.ObservedAnswerRegion = &box
			break
		}
	}
}
