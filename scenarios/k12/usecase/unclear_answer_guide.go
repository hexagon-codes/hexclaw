package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
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
	for _, marker := range []string{"手写", "笔迹", "填空中", "填空内", "作答", "答案栏", "答案框", "填写的", "handwriting", "handwritten", "filled answer", "answer box"} {
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
func (o *GradingOrchestrator) independentReferenceAnswerEligible(ctx context.Context, job GradingJobView, req PhotoGradeRequest, q RecognizedQuestion) (bool, error) {
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
	if independentReferenceAnswerEligible(req, q) {
		return true, nil
	}
	if o.deps.Records == nil || q.Subject != "数学" || q.SourceRegion == nil {
		return false, nil
	}
	receipt, ok := o.readRecognitionReceipt(job.Record.RecordID)
	if !ok || receipt.AgentName != job.Record.AgentName || len(receipt.Questions) != 1 {
		return false, nil
	}
	prior := receipt.Questions[0]
	// 布局页标识与绑定后的资产 URI 分属不同投影，像素归属由原计划和目标核对。
	if prior.ProblemID != q.ProblemID || prior.AttemptID != q.AttemptID ||
		!slices.Equal(prior.SourceNumberPath, q.SourceNumberPath) || prior.ImageSourceContext == nil || q.ImageSourceContext == nil ||
		prior.ImageSourceContext.ScopeDigest != q.ImageSourceContext.ScopeDigest || !slices.Equal(prior.ImageSourceContext.TargetIDs, q.ImageSourceContext.TargetIDs) || prior.RawTranscription != q.RawTranscription ||
		prior.CanonicalMarkdown != q.CanonicalMarkdown || prior.AnswerRawTranscription != q.AnswerRawTranscription || prior.AnswerCanonicalMarkdown != q.AnswerCanonicalMarkdown {
		return false, nil
	}
	runtime, err := o.deps.Records.LoadRecognitionLayoutPlanRuntimeV2(ctx, job.Record.AgentName, receipt.InvocationID)
	if errors.Is(err, records.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	plan := runtime.AuthorizedPlan
	if plan == nil || len(plan.Targets) != 1 || runtime.Header.JobID != job.Record.RecordID ||
		q.ImageSourceContext == nil || len(q.ImageSourceContext.TargetIDs) != 1 || plan.Targets[0].TargetID != q.ImageSourceContext.TargetIDs[0] ||
		!slices.Equal(plan.Targets[0].SourceNumberPath, q.SourceNumberPath) {
		return false, nil
	}
	physicalCalls, err := o.deps.Records.ListModelPhysicalInvocations(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil {
		return false, err
	}
	var proof *k12.ModelPhysicalInvocation
	for i := range physicalCalls {
		physical := &physicalCalls[i]
		if physical.ParentInvocationID == receipt.InvocationID && strings.HasPrefix(string(physical.PhysicalUnit), "layout_adjudicate_") {
			if proof != nil {
				return false, nil
			}
			proof = physical
		}
	}
	if proof == nil || proof.Stage != k12.GradingStageRecognizing || proof.Status != k12.ModelInvocationSucceeded ||
		proof.PlanDigest != plan.AuthorizedPlanDigest || proof.CandidateExactSetDigest != runtime.CandidateExactSetDigest || !validModelInvocationDigest(proof.RequestDigest) {
		return false, nil
	}
	payload, err := o.deps.Records.LoadSucceededModelPhysicalInvocationResultContent(ctx, job.Record.AgentName, proof.PhysicalInvocationID, proof.ResultDigest)
	if err != nil {
		return false, err
	}
	return independentReferenceFromAdjudication(req, q, payload), nil
}

// 单题参考解只采用同范围最新裁决的印刷观察，不结算学生作答。
func independentReferenceFromAdjudication(req PhotoGradeRequest, q RecognizedQuestion, payload string) bool {
	source := q.ImageSourceContext
	if q.Subject != "数学" || q.SourceRegion == nil || source == nil || source.Version != k12.RecognitionImageScopeVersionV1 ||
		source.ScopeDigest == "" || len(source.TargetIDs) != 1 || len(source.Recoveries) == 0 || len(q.EvidenceTranscriptions) < 2 {
		return false
	}
	for _, recovery := range source.Recoveries {
		if recovery.Gap.Kind != "handwriting" {
			return false
		}
	}
	observed, err := k12.ParseRecognitionFinalAnswerResponseV1(payload, *q.SourceRegion)
	if err != nil || !observed.QuestionCompleteAndLegible || !observed.TargetOwnershipConfirmed || observed.ActiveAnswerComplete {
		return false
	}
	var envelope struct {
		Items []struct {
			Recognition struct {
				Subject    string   `json:"subject"`
				Confidence *float64 `json:"recognition_confidence"`
				OCRSignals []string `json:"ocr_signals"`
			} `json:"recognition"`
		} `json:"items"`
	}
	if json.Unmarshal(observed.LegacyEnvelope, &envelope) != nil || len(envelope.Items) != 1 || envelope.Items[0].Recognition.Subject != q.Subject {
		return false
	}
	printed := cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
	printed.EvidenceTranscriptions = append(printed.EvidenceTranscriptions, observed.QuestionRawTranscription)
	if questionEvidenceTranscriptionsConflictForInitialReadMode(printed, req.InitialReadMode) {
		return false
	}
	printed.RecognitionConfidence = envelope.Items[0].Recognition.Confidence
	printed.OCRSignals = envelope.Items[0].Recognition.OCRSignals
	return independentReferenceAnswerEligible(req, printed)
}
