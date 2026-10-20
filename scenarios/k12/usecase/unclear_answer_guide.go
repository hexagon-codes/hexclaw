package usecase

import "strings"

const unclearHandwritingReferenceWarning = "The handwriting could not be reliably read. The reference answer does not judge the student's work."

// 只在求解评估副本中移除学生作答风险；题干、父材料与缺损来源仍须独立可靠。
func independentReferenceAnswerEligible(req PhotoGradeRequest, q RecognizedQuestion) bool {
	if q.parentSourceUnclear || q.RecognitionConfidence == nil || *q.RecognitionConfidence < ocrConfidenceConfirmationThreshold ||
		strings.TrimSpace(q.RawTranscription) == "" || !CanonicalMarkdownValid(q.CanonicalMarkdown) ||
		len(req.PracticeReferences) > 0 && req.practiceReference == nil {
		return false
	}
	printed := cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
	printed.AnswerState = AnswerStateBlank
	printed.StudentAnswer, printed.AnswerRawTranscription, printed.AnswerCanonicalMarkdown = "", "", ""
	printed.AnswerEvidenceTranscriptions = nil
	printed.ConfirmationRequired, printed.ConfirmationReasons = false, nil
	consistentPrintedReadings := len(printed.EvidenceTranscriptions) >= 2 && !questionEvidenceTranscriptionsConflictForInitialReadMode(printed, req.InitialReadMode)
	for _, reason := range q.ConfirmationReasons {
		if (reason == OCRRiskLowConfidence || reason == OCRRiskEvidenceConflict) && !consistentPrintedReadings {
			return false
		}
	}
	printed.OCRSignals = nil
	for _, signal := range q.OCRSignals {
		switch strings.ToLower(strings.TrimSpace(signal)) {
		case "erasure", "erasure_detected", "unclear", "unclear_handwriting":
			continue
		case "conflict", "evidence_conflict":
			if consistentPrintedReadings {
				continue
			}
		}
		printed.OCRSignals = append(printed.OCRSignals, signal)
	}
	if printed.ImageSourceContext != nil {
		printed.ImageSourceContext.Action = "solve"
	}
	return !NormalizeRecognizedQuestionForInitialReadMode(printed, req.InitialReadMode).ConfirmationRequired
}

// 类型化恢复未复制观察明细时，只读取同任务同输入的冻结审计用于风险分离。
func (o *GradingOrchestrator) independentReferenceAnswerEligible(job GradingJobView, req PhotoGradeRequest, q RecognizedQuestion) bool {
	if len(q.EvidenceTranscriptions) < 2 {
		if audit, ok := o.readRecognitionAudit(job.Record.RecordID); ok && audit.AgentName == job.Record.AgentName {
			for _, prior := range audit.Questions {
				if prior.ProblemID == q.ProblemID && prior.AttemptID == q.AttemptID && prior.InputDigest != "" && prior.InputDigest == q.InputDigest &&
					prior.ConfirmedVersion == q.ConfirmedVersion && prior.RawTranscription == q.RawTranscription && prior.CanonicalMarkdown == q.CanonicalMarkdown &&
					prior.AnswerRawTranscription == q.AnswerRawTranscription && prior.AnswerCanonicalMarkdown == q.AnswerCanonicalMarkdown {
					q.EvidenceTranscriptions, q.AnswerEvidenceTranscriptions = prior.EvidenceTranscriptions, prior.AnswerEvidenceTranscriptions
					q.OCRSignals = prior.OCRSignals
					break
				}
			}
		}
	}
	return independentReferenceAnswerEligible(req, q)
}
