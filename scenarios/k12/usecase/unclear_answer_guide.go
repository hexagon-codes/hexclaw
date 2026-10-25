package usecase

import (
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

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
	removeHandwritingGapMarkersForReference(&printed)
	printedConflict := questionEvidenceTranscriptionsConflictForInitialReadMode(printed, req.InitialReadMode)
	consistentPrintedReadings := len(printed.EvidenceTranscriptions) >= 2 && !printedConflict
	onlyHandwritingSignals := len(q.OCRSignals) > 0 && !printedConflict
	for _, signal := range q.OCRSignals {
		onlyHandwritingSignals = onlyHandwritingSignals && handwritingOnlySourceSignal(signal)
	}
	for _, reason := range q.ConfirmationReasons {
		if reason == OCRRiskEvidenceConflict && !consistentPrintedReadings ||
			reason == OCRRiskLowConfidence && !consistentPrintedReadings && !onlyHandwritingSignals {
			return false
		}
	}
	printed.OCRSignals = nil
	for _, signal := range q.OCRSignals {
		if (consistentPrintedReadings || onlyHandwritingSignals) && handwritingOnlySourceSignal(signal) {
			continue
		}
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

// 只在参考解的副本统一已定位手写区的未知占位，不替换任何已识别的字词、数值或条件。
func removeHandwritingGapMarkersForReference(q *RecognizedQuestion) {
	source := q.ImageSourceContext
	if source == nil || source.Version != k12.RecognitionImageScopeVersionV1 || source.ScopeDigest == "" || len(source.TargetIDs) == 0 || len(q.OCRSignals) == 0 {
		return
	}
	for _, signal := range q.OCRSignals {
		if !handwritingOnlySourceSignal(signal) {
			return
		}
	}
	normalize := func(value string) string {
		for _, recovery := range source.Recoveries {
			if recovery.Gap.Kind != "handwriting" {
				continue
			}
			before := strings.TrimRight(strings.TrimSpace(recovery.Gap.Before), "（(")
			after := strings.TrimLeft(strings.TrimSpace(recovery.Gap.After), "）)")
			if before == "" || after == "" || strings.Count(value, before) != 1 {
				continue
			}
			start := strings.Index(value, before) + len(before)
			end := strings.Index(value[start:], after)
			if end < 0 {
				continue
			}
			end += start
			middle := strings.Trim(value[start:end], "（）() \t\r\n　_＿□▢")
			if middle != "" && middle != "【遮挡】" && middle != "[遮挡]" && middle != "【无法辨认】" && middle != "【不清楚】" {
				continue
			}
			value = value[:start] + "（）" + value[end:]
		}
		return value
	}
	q.RawTranscription, q.CanonicalMarkdown, q.Question = normalize(q.RawTranscription), normalize(q.CanonicalMarkdown), normalize(q.Question)
	for i, reading := range q.EvidenceTranscriptions {
		q.EvidenceTranscriptions[i] = normalize(reading)
	}
}

// 自然语言观察明确只描述笔迹时，不把它作为独立题干求解的缺损；混合来源仍保留。
func handwritingOnlySourceSignal(signal string) bool {
	signal = strings.ToLower(strings.TrimSpace(signal))
	answerContent := false
	for _, marker := range []string{"手写", "笔迹", "填空中", "填空内", "作答区", "答案栏", "答案框", "填写的", "handwriting", "handwritten", "filled answer", "answer box"} {
		answerContent = answerContent || strings.Contains(signal, marker)
	}
	if !answerContent {
		return false
	}
	for _, printed := range []string{"题干", "印刷", "条件", "printed", "question", "stem"} {
		if strings.Contains(signal, printed) {
			return false
		}
	}
	return true
}

// 类型化恢复未复制观察明细时，只读取同任务同输入的冻结审计用于风险分离。
func (o *GradingOrchestrator) independentReferenceAnswerEligible(job GradingJobView, req PhotoGradeRequest, q RecognizedQuestion) bool {
	if len(q.EvidenceTranscriptions) < 2 {
		for _, prior := range o.frozenRecognitionSourceQuestions(job) {
			if prior.ProblemID == q.ProblemID && prior.AttemptID == q.AttemptID && prior.InputDigest != "" && prior.InputDigest == q.InputDigest &&
				prior.ConfirmedVersion == q.ConfirmedVersion && prior.RawTranscription == q.RawTranscription && prior.CanonicalMarkdown == q.CanonicalMarkdown &&
				prior.AnswerRawTranscription == q.AnswerRawTranscription && prior.AnswerCanonicalMarkdown == q.AnswerCanonicalMarkdown {
				if len(prior.EvidenceTranscriptions) > len(q.EvidenceTranscriptions) {
					q.EvidenceTranscriptions = append([]string(nil), prior.EvidenceTranscriptions...)
					q.AnswerEvidenceTranscriptions = append([]string(nil), prior.AnswerEvidenceTranscriptions...)
				}
				if len(q.OCRSignals) == 0 {
					q.OCRSignals = append([]string(nil), prior.OCRSignals...)
				}
			}
		}
	}
	return independentReferenceAnswerEligible(req, q)
}
