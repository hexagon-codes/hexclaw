package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

const unclearHandwritingReferenceWarning = "The handwriting could not be reliably read. The reference answer does not judge the student's work."

// 只在求解评估副本中移除学生作答风险；题干、父材料与缺损来源仍须独立可靠。
func independentReferenceAnswerEligible(req PhotoGradeRequest, q RecognizedQuestion) bool {
	if q.Subject == "语文" && q.ImageSourceContext != nil && q.ImageSourceContext.PrintedQuestionReceipt != nil {
		// 批改中的参考解复用已持久核验的印刷证明，原作答与批改风险保持。
		printed := cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
		printed.ImageSourceContext.Action = "solve"
		return !recognizedQuestionRequiresGuardianConfirmationForInitialReadMode(printed, PhotoTaskBlankWorksheet, req.InitialReadMode)
	}
	if q.parentSourceUnclear || q.RecognitionConfidence == nil || *q.RecognitionConfidence < ocrConfidenceConfirmationThreshold ||
		strings.TrimSpace(q.RawTranscription) == "" || !CanonicalMarkdownValid(q.CanonicalMarkdown) {
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
			if start, end, ok := referenceHandwritingGap(value, recovery.Gap); ok {
				value = value[:start] + "（）" + value[end:]
			}
		}
		return value
	}
	q.RawTranscription, q.CanonicalMarkdown, q.Question = normalize(q.RawTranscription), normalize(q.CanonicalMarkdown), normalize(q.Question)
	for i, reading := range q.EvidenceTranscriptions {
		q.EvidenceTranscriptions[i] = normalize(reading)
	}
}

// 只识别同一印刷前后锚点之间的空位或显式未知标记，数字、运算符及词语必须保留。
func referenceHandwritingGap(value string, gap k12.RecognitionImageSourceGapV1) (int, int, bool) {
	before := strings.TrimRight(strings.TrimSpace(gap.Before), "（(")
	after := strings.TrimLeft(strings.TrimSpace(gap.After), "）)")
	if before == "" || after == "" || strings.Count(value, before) != 1 {
		return 0, 0, false
	}
	start := strings.Index(value, before) + len(before)
	offset := strings.Index(value[start:], after)
	if offset < 0 {
		return 0, 0, false
	}
	end := start + offset
	switch strings.Trim(value[start:end], "（）() \t\r\n　_＿□▢") {
	case "", "【遮挡】", "[遮挡]", "【无法辨认】", "【不清楚】":
		return start, end, true
	default:
		return 0, 0, false
	}
}

// 旧裁决可能把印在纸上的空框边缘当成题干。只有观察明确指向已冻结的作答空位才接纳。
var printedAnswerFrameObservation = regexp.MustCompile(`^(?:末尾|题末|最后)?[“"]([^”"]+)[”"](?:之)?后的?(?:印刷的?)?(?:填空|答题框|答案栏|答案框)(?:区域|框|空位)?(?:的?[左右上下]?边[界缘框])?(?:部分)?(?:被|有)?(?:遮挡|遮住|覆盖|裁切|截断)了?$`)
var answerFrameHandwritingObservation = regexp.MustCompile(`^(?:(?:空|框|填空)(?:内|里)(?:可见的?)?(?:手写(?:笔画|笔迹|字形|字迹|内容|作答|答案)?|笔迹)(?:无法|不能|难以)(?:可靠)?(?:辨认|转写|确认)|(?:无法|不能)(?:可靠)?确认(?:是否)?有(?:完整的?)?(?:有效的?)?(?:手写答案|作答|填答))$`)

func onlyAnchoredAnswerFrameOccluded(q RecognizedQuestion, signals []string) bool {
	if len(signals) == 0 {
		return false
	}
	for _, signal := range signals {
		clauses := strings.FieldsFunc(strings.TrimSpace(signal), func(r rune) bool {
			return r == '，' || r == ',' || r == '。' || r == '；' || r == ';'
		})
		if len(clauses) == 0 {
			return false
		}
		note := strings.TrimSpace(clauses[0])
		match := printedAnswerFrameObservation.FindStringSubmatch(note)
		if note == "source_gap_handwriting" {
			if len(q.ImageSourceContext.Recoveries) != 1 {
				return false
			}
		} else if match == nil || strings.Count(q.RawTranscription, match[1]) != 1 {
			return false
		}
		located := false
		for _, recovery := range q.ImageSourceContext.Recoveries {
			gap := recovery.Gap
			label := strings.TrimRight(strings.TrimSpace(gap.Before), "（(")
			if match != nil && !strings.HasSuffix(label, match[1]) {
				continue
			}
			readableSlot := true
			for _, reading := range append([]string{q.RawTranscription}, q.EvidenceTranscriptions...) {
				start, end, ok := referenceHandwritingGap(reading, gap)
				readableSlot = readableSlot && ok && strings.TrimSpace(reading[start:end]) != ""
			}
			if readableSlot {
				located = true
				break
			}
		}
		if !located {
			return false
		}
		for _, clause := range clauses[1:] {
			if !answerFrameHandwritingObservation.MatchString(strings.TrimSpace(clause)) {
				return false
			}
		}
	}
	return true
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
func (o *GradingOrchestrator) independentReferenceQuestion(ctx context.Context, job GradingJobView, req PhotoGradeRequest, q RecognizedQuestion) (string, bool, error) {
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
		return "", true, nil
	}
	if o.deps.Records == nil || q.Subject != "数学" || q.SourceRegion == nil {
		return "", false, nil
	}
	receipt, ok := o.readRecognitionReceipt(job.Record.RecordID)
	if !ok || receipt.AgentName != job.Record.AgentName {
		return "", false, nil
	}
	questionIndex := slices.IndexFunc(receipt.Questions, func(prior RecognizedQuestion) bool {
		return prior.ProblemID == q.ProblemID && prior.AttemptID == q.AttemptID
	})
	if questionIndex < 0 || q.ImageSourceContext == nil || len(q.ImageSourceContext.TargetIDs) != 1 {
		return "", false, nil
	}
	prior := receipt.Questions[questionIndex]
	// 布局页标识与绑定后的资产 URI 分属不同投影，像素归属由原计划和目标核对。
	if !slices.Equal(prior.SourceNumberPath, q.SourceNumberPath) || prior.ImageSourceContext == nil ||
		prior.ImageSourceContext.ScopeDigest != q.ImageSourceContext.ScopeDigest || !slices.Equal(prior.ImageSourceContext.TargetIDs, q.ImageSourceContext.TargetIDs) || prior.RawTranscription != q.RawTranscription ||
		prior.CanonicalMarkdown != q.CanonicalMarkdown || prior.AnswerRawTranscription != q.AnswerRawTranscription || prior.AnswerCanonicalMarkdown != q.AnswerCanonicalMarkdown {
		return "", false, nil
	}
	runtime, err := o.deps.Records.LoadRecognitionLayoutPlanRuntimeV2(ctx, job.Record.AgentName, receipt.InvocationID)
	if errors.Is(err, records.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	plan := runtime.AuthorizedPlan
	if plan == nil || !plan.SourceAdjudication || plan.AuthorizedPlanDigest != q.ImageSourceContext.ScopeDigest ||
		runtime.Header.JobID != job.Record.RecordID {
		return "", false, nil
	}
	targetIndex := slices.IndexFunc(plan.Targets, func(target k12.RecognitionLayoutTargetV2) bool {
		return target.TargetID == q.ImageSourceContext.TargetIDs[0] && slices.Equal(target.SourceNumberPath, q.SourceNumberPath)
	})
	if targetIndex < 0 {
		return "", false, nil
	}
	unit, err := k12.RecognitionLayoutAdjudicationUnitV2(targetIndex + 1)
	if err != nil {
		return "", false, err
	}
	physicalCalls, err := o.deps.Records.ListModelPhysicalInvocations(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil {
		return "", false, err
	}
	var proof *k12.ModelPhysicalInvocation
	for i := range physicalCalls {
		physical := &physicalCalls[i]
		if physical.ParentInvocationID == receipt.InvocationID && physical.PhysicalUnit == unit {
			if proof != nil {
				return "", false, nil
			}
			proof = physical
		}
	}
	if proof == nil || proof.Stage != k12.GradingStageRecognizing || proof.Status != k12.ModelInvocationSucceeded ||
		proof.PlanDigest != plan.AuthorizedPlanDigest || proof.CandidateExactSetDigest != runtime.CandidateExactSetDigest || !validModelInvocationDigest(proof.RequestDigest) {
		return "", false, nil
	}
	payload, err := o.deps.Records.LoadSucceededModelPhysicalInvocationResultContent(ctx, job.Record.AgentName, proof.PhysicalInvocationID, proof.ResultDigest)
	if err != nil {
		return "", false, err
	}
	question, eligible := independentReferenceFromAdjudication(req, q, payload)
	return question, eligible, nil
}

// 参考解只采用同目标裁决的印刷观察，不结算学生作答。
func independentReferenceFromAdjudication(req PhotoGradeRequest, q RecognizedQuestion, payload string) (string, bool) {
	source := q.ImageSourceContext
	if q.Subject != "数学" || q.SourceRegion == nil || source == nil || source.Version != k12.RecognitionImageScopeVersionV1 ||
		source.ScopeDigest == "" || len(source.TargetIDs) != 1 || len(q.EvidenceTranscriptions) < 2 {
		return "", false
	}
	for _, recovery := range source.Recoveries {
		if recovery.Gap.Kind != "handwriting" {
			return "", false
		}
	}
	observed, err := k12.ParseRecognitionFinalAnswerResponseV1(payload, *q.SourceRegion)
	if err != nil || !observed.TargetOwnershipConfirmed || observed.ActiveAnswerComplete {
		return "", false
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
		return "", false
	}
	printed := cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
	printed.EvidenceTranscriptions = append(printed.EvidenceTranscriptions, observed.QuestionRawTranscription)
	printed.RecognitionConfidence = envelope.Items[0].Recognition.Confidence
	printed.OCRSignals = envelope.Items[0].Recognition.OCRSignals
	if len(source.Recoveries) > 0 {
		if questionEvidenceTranscriptionsConflictForInitialReadMode(printed, req.InitialReadMode) {
			return "", false
		}
		if observed.QuestionCompleteAndLegible {
			return "", independentReferenceAnswerEligible(req, printed)
		}
		if onlyAnchoredAnswerFrameOccluded(printed, printed.OCRSignals) {
			// 已定位的作答框风险仅在参考解副本中作为笔迹风险处理。
			printed.OCRSignals = []string{"source_gap_handwriting"}
			return "", independentReferenceAnswerEligible(req, printed)
		}
		return "", false
	}
	if observed.QuestionCompleteAndLegible || req.practiceReference != nil || q.parentSourceUnclear || q.ProblemKind != ProblemKindStandalone ||
		q.RecognitionConfidence == nil || *q.RecognitionConfidence < ocrConfidenceConfirmationThreshold ||
		printed.RecognitionConfidence == nil || *printed.RecognitionConfidence < ocrConfidenceConfirmationThreshold ||
		!hasOnlyTrailingSourceRisk(printed.OCRSignals) {
		return "", false
	}
	return verifiedLeadingTravelQuestion(printed)
}

func hasOnlyTrailingSourceRisk(signals []string) bool {
	localized := false
	for _, signal := range signals {
		if handwritingOnlySourceSignal(signal) {
			continue
		}
		if !recognitionSignalIndicatesSourceOcclusion(strings.ReplaceAll(signal, "截断", "裁切")) ||
			(!strings.Contains(signal, "下缘") && !strings.Contains(signal, "末尾") && !strings.Contains(signal, "底部")) ||
			strings.Contains(signal, "主问") || strings.Contains(signal, "首行") || strings.Contains(signal, "主要条件") {
			return false
		}
		localized = true
	}
	return localized
}

// 参考解采用各次读取共同证明的最大题面；页尾无法核实的内容不进入求解或判分。
func verifiedLeadingTravelQuestion(q RecognizedQuestion) (string, bool) {
	leading := func(value string) (string, string, bool) {
		value = k12.RecognitionQuestionSourceTextV1(value, q.SourceNumberPath, q.DisplayLabel)
		if len(q.SourceNumberPath) == 1 && q.DisplayLabel == q.SourceNumberPath[0]+"." {
			if rest, found := strings.CutPrefix(value, q.DisplayLabel); found {
				first, _ := utf8.DecodeRuneInString(rest)
				if unicode.Is(unicode.Han, first) {
					value = rest
				}
			}
		}
		end := strings.IndexAny(value, "？?")
		if end < 0 {
			return "", "", false
		}
		// 主问之后还须有独立的示意或演算。普通截断题不按这个补全路径求解。
		_, size := utf8.DecodeRuneInString(value[end:])
		tail := strings.TrimSpace(value[end+size:])
		if !strings.HasPrefix(tail, "图示") && !strings.HasPrefix(tail, "1小时") && !strings.HasPrefix(tail, `\(1\)小时`) {
			return "", "", false
		}
		prefix := strings.TrimSpace(value[:end]) + "？"
		_, ok := elementarymath.ParseUniformTravelProblem(prefix)
		return prefix, tail, ok
	}
	primary, tail, ok := leading(q.RawTranscription)
	if !ok {
		return "", false
	}
	travel, _ := elementarymath.ParseUniformTravelProblem(primary)
	worksheet, full := readTravelWorksheetTail(tail, travel)
	selected := q.RawTranscription
	for _, observed := range q.EvidenceTranscriptions {
		prefix, observedTail, ok := leading(observed)
		if !ok || !k12.RecognitionSourceQuestionsEqualV1(primary, prefix) {
			return "", false
		}
		if full {
			other, read := readTravelWorksheetTail(observedTail, travel)
			full = read && worksheet.sameQuestions(other)
			if full && other.hasFinalValue && !worksheet.hasFinalValue {
				selected, worksheet = observed, other
			}
		}
	}
	if full {
		return selected, true
	}
	return primary, true
}
