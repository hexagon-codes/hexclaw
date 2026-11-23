package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
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
	projectReferencePrintReadings(&printed, false)
	if printed.Subject == "数学" && (!referenceNumberMatches(printed, printed.RawTranscription) ||
		slices.ContainsFunc(printed.EvidenceTranscriptions, func(text string) bool { return !referenceNumberMatches(printed, text) })) {
		return false
	}
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

// 参考解比较的是当前小问。仅从比较副本移除与冻结材料一致的前缀。
// 独立裁决可以只复述材料的连续末段；求解仍使用原题和全部共享材料。
func projectReferencePrintReadings(q *RecognizedQuestion, adjudicationMayRepeatMaterialSuffix bool) {
	source := q.ImageSourceContext
	if q.Subject != "数学" || len(q.SourceNumberPath) < 2 || source == nil ||
		source.Version != k12.RecognitionImageScopeVersionV1 || source.ScopeDigest == "" || len(source.TargetIDs) != 1 {
		return
	}
	parentheses := strings.NewReplacer("（", "(", "）", ")")
	project := func(text string, allowMaterialSuffix bool) string {
		lines := strings.Split(strings.TrimSpace(text), "\n")
		for _, material := range source.Materials {
			if !sharedReferenceMaterial(material) {
				continue
			}
			frozen := strings.TrimSpace(material.Text)
			unlabelled := stripReviewMaterialParentNumber(*q, frozen)
			key := k12.RecognitionSourceComparisonV1(unlabelled)
			var prefix strings.Builder
			lineCount := len(lines)
			for i := 0; i+1 < len(lines) && prefix.Len() < len(key); i++ {
				line := lines[i]
				if i == 0 && unlabelled != frozen {
					line = stripReviewMaterialParentNumber(*q, line)
				}
				prefix.WriteString(k12.RecognitionSourceComparisonV1(line))
				if !strings.HasPrefix(key, prefix.String()) {
					break
				}
				if prefix.Len() == len(key) {
					reading := strings.Join(lines[:i+1], "\n")
					if unlabelled != frozen {
						reading = stripReviewMaterialParentNumber(*q, reading)
					}
					if k12.RecognitionSourceQuestionsEqualV1(reading, unlabelled) {
						lines = lines[i+1:]
					}
				}
			}
			if allowMaterialSuffix && len(lines) == lineCount {
				materialLines := strings.Split(unlabelled, "\n")
				for first := 1; first < len(materialLines); first++ {
					count := len(materialLines) - first
					if count < len(lines) && k12.RecognitionSourceQuestionsEqualV1(strings.Join(lines[:count], "\n"), strings.Join(materialLines[first:], "\n")) {
						lines = lines[count:]
						break
					}
				}
			}
		}
		local := mathEmptyPrintedSlot.ReplaceAllString(parentheses.Replace(strings.Join(lines, "\n")), "()")
		local = referenceTypesetWorkLine.ReplaceAllString(local, "$1")
		return mathWorkLineSuffix.ReplaceAllString(local, "$1")
	}
	q.RawTranscription = project(q.RawTranscription, false)
	for i, reading := range q.EvidenceTranscriptions {
		q.EvidenceTranscriptions[i] = project(reading, adjudicationMayRepeatMaterialSuffix && i == len(q.EvidenceTranscriptions)-1)
	}
}

// 横线的排版宽度只在算式标签后的空作答区域有效，实际印出的尺寸、数字和算式照常比较。
var referenceTypesetWorkLine = regexp.MustCompile(`((?:列式|算式)[：:])[ \t\x{3000}]*(?:\\\(\\underline\{\\hspace\{[0-9]+(?:\.[0-9]+)?(?:cm|mm|in|pt|em|ex)\}\}\\\))[ \t\x{3000}]*$`)

// FrozenSharedBlankReadingsAgree 比较已独立核实完整作答区域的三次读数。
// 只投影当前小问实际归属的共享材料，原题、学生作答和每份物理观察均保持原样。
func FrozenSharedBlankReadingsAgree(plan k12.RecognitionLayoutPlanV2, target k12.RecognitionLayoutTargetV2, readings [3]RecognizedQuestion) bool {
	if plan.Scope == nil || plan.AuthorizedPlanDigest == "" || target.TargetID == "" || len(target.SourceNumberPath) < 2 ||
		slices.ContainsFunc(plan.Scope.Gaps, func(gap k12.RecognitionImageSourceGapV1) bool { return gap.Order == target.ManifestOrder }) {
		return false
	}
	source := &k12.RecognitionImageSourceContextV1{
		Version: k12.RecognitionImageScopeVersionV1, ScopeDigest: plan.AuthorizedPlanDigest, TargetIDs: []string{target.TargetID},
	}
	for _, dep := range plan.Scope.Dependencies {
		if len(dep.ForOrders) >= 2 && slices.Contains(dep.ForOrders, target.ManifestOrder) {
			source.Materials = append(source.Materials, dep)
		}
	}
	if len(source.Materials) == 0 {
		return false
	}
	var original string
	for i, q := range readings {
		if q.Subject != "数学" || !slices.Equal(q.SourceNumberPath, target.SourceNumberPath) || q.AnswerState != AnswerStateBlank ||
			q.StudentAnswer != "" || q.AnswerRawTranscription != "" || q.AnswerCanonicalMarkdown != "" || len(q.OCRSignals) != 0 ||
			q.RecognitionConfidence == nil || *q.RecognitionConfidence < ocrConfidenceConfirmationThreshold {
			return false
		}
		q.ImageSourceContext, q.EvidenceTranscriptions = source, nil
		projectReferencePrintReadings(&q, false)
		if !referenceNumberMatches(q, q.RawTranscription) {
			return false
		}
		if i == 0 {
			original = q.RawTranscription
		} else if !k12.RecognitionSourceQuestionsEqualV1(original, q.RawTranscription) {
			return false
		}
	}
	return true
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
	return worksheetWritingMention.MatchString(signal) && worksheetSignalsAllowReference([]string{signal}, false)
}

type printedReference struct {
	question  string
	worksheet *elementarymath.TravelWorksheet
}

// 类型化恢复未复制观察明细时，只读取同任务同输入的冻结审计用于风险分离。
func (o *GradingOrchestrator) independentReferenceQuestion(ctx context.Context, job GradingJobView, req PhotoGradeRequest, q RecognizedQuestion) (printedReference, bool, error) {
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
	if q.Subject == "数学" && disputedWorksheetQuantity(q.OCRSignals) {
		return printedReference{}, false, nil
	}
	if independentReferenceAnswerEligible(req, q) {
		if k12.UsesWorksheetQuantities(job.Fields.ModelSnapshot.QuantityAnswerPolicy) && req.practiceReference == nil &&
			q.Subject == "数学" && q.ProblemKind == ProblemKindStandalone && q.ImageSourceContext != nil && len(q.ImageSourceContext.Materials) == 0 {
			original, known := elementarymath.ParseTravelWorksheet(q.RawTranscription)
			canonical, complete := elementarymath.ParseTravelWorksheet(q.Question)
			if known && complete && original == canonical && !slices.ContainsFunc(q.ImageSourceContext.Recoveries, func(r k12.RecognitionImageSourceRecoveryV1) bool { return r.Gap.Kind != "handwriting" }) {
				return printedReference{question: q.RawTranscription}, true, nil
			}
		}
		return printedReference{}, true, nil
	}
	if o.deps.Records == nil || q.Subject != "数学" || q.SourceRegion == nil {
		return printedReference{}, false, nil
	}
	receipt, ok := o.readRecognitionReceipt(job.Record.RecordID)
	if !ok || receipt.AgentName != job.Record.AgentName {
		return printedReference{}, false, nil
	}
	questionIndex := slices.IndexFunc(receipt.Questions, func(prior RecognizedQuestion) bool {
		return prior.ProblemID == q.ProblemID && prior.AttemptID == q.AttemptID
	})
	if questionIndex < 0 || q.ImageSourceContext == nil || len(q.ImageSourceContext.TargetIDs) != 1 {
		return printedReference{}, false, nil
	}
	prior := receipt.Questions[questionIndex]
	// 布局页标识与绑定后的资产 URI 分属不同投影，像素归属由原计划和目标核对。
	if !slices.Equal(prior.SourceNumberPath, q.SourceNumberPath) || prior.ImageSourceContext == nil ||
		prior.ImageSourceContext.ScopeDigest != q.ImageSourceContext.ScopeDigest || !slices.Equal(prior.ImageSourceContext.TargetIDs, q.ImageSourceContext.TargetIDs) || prior.RawTranscription != q.RawTranscription ||
		prior.CanonicalMarkdown != q.CanonicalMarkdown || prior.AnswerRawTranscription != q.AnswerRawTranscription || prior.AnswerCanonicalMarkdown != q.AnswerCanonicalMarkdown {
		return printedReference{}, false, nil
	}
	runtime, err := o.deps.Records.LoadRecognitionLayoutPlanRuntimeV2(ctx, job.Record.AgentName, receipt.InvocationID)
	if errors.Is(err, records.ErrNotFound) {
		return printedReference{}, false, nil
	}
	if err != nil {
		return printedReference{}, false, err
	}
	plan := runtime.AuthorizedPlan
	if plan == nil || !plan.SourceAdjudication || plan.AuthorizedPlanDigest != q.ImageSourceContext.ScopeDigest ||
		runtime.Header.JobID != job.Record.RecordID {
		return printedReference{}, false, nil
	}
	targetIndex := slices.IndexFunc(plan.Targets, func(target k12.RecognitionLayoutTargetV2) bool {
		return target.TargetID == q.ImageSourceContext.TargetIDs[0] && slices.Equal(target.SourceNumberPath, q.SourceNumberPath)
	})
	if targetIndex < 0 {
		return printedReference{}, false, nil
	}
	unit, err := k12.RecognitionLayoutAdjudicationUnitV2(targetIndex + 1)
	if err != nil {
		return printedReference{}, false, err
	}
	physicalCalls, err := o.deps.Records.ListModelPhysicalInvocations(ctx, job.Record.AgentName, job.Record.RecordID)
	if err != nil {
		return printedReference{}, false, err
	}
	var proof *k12.ModelPhysicalInvocation
	for i := range physicalCalls {
		physical := &physicalCalls[i]
		if physical.ParentInvocationID == receipt.InvocationID && physical.PhysicalUnit == unit {
			if proof != nil {
				return printedReference{}, false, nil
			}
			proof = physical
		}
	}
	reviewWorksheet := k12.UsesWorksheetQuantities(job.Fields.ModelSnapshot.QuantityAnswerPolicy) && req.practiceReference == nil
	if proof == nil && reviewWorksheet {
		return o.referenceFromReviewedWorksheet(ctx, runtime, physicalCalls, q, targetIndex, "", "")
	}
	if !referenceAdjudicationMatchesTarget(proof, plan.AuthorizedPlanDigest, plan.Targets[targetIndex].TargetID) {
		return printedReference{}, false, nil
	}
	payload, err := o.deps.Records.LoadSucceededModelPhysicalInvocationResultContent(ctx, job.Record.AgentName, proof.PhysicalInvocationID, proof.ResultDigest)
	if err != nil {
		return printedReference{}, false, err
	}
	if reviewWorksheet {
		if question, eligible, err := o.referenceFromReviewedWorksheet(ctx, runtime, physicalCalls, q, targetIndex, proof.PhysicalInvocationID, payload); eligible || err != nil {
			return question, eligible, err
		}
	}
	if plan.Scope != nil && slices.ContainsFunc(q.ImageSourceContext.Materials, sharedReferenceMaterial) &&
		slices.ContainsFunc(plan.Scope.Gaps, func(gap k12.RecognitionImageSourceGapV1) bool {
			return gap.Order == plan.Targets[targetIndex].ManifestOrder && gap.Kind != "handwriting"
		}) {
		return printedReference{}, false, nil
	}
	question, eligible := independentReferenceFromAdjudication(req, q, payload)
	return printedReference{question: question}, eligible, nil
}

// 裁决只授权当前一题，整页计划摘要与单题目标集合分别核对。
func referenceAdjudicationMatchesTarget(proof *k12.ModelPhysicalInvocation, planDigest, targetID string) bool {
	if proof == nil || proof.Stage != k12.GradingStageRecognizing || proof.Status != k12.ModelInvocationSucceeded ||
		proof.ReusedFromPhysicalInvocationID != "" || proof.PlanDigest != planDigest || !validModelInvocationDigest(proof.RequestDigest) {
		return false
	}
	digest, err := k12.RecognitionLayoutTargetExactSetDigestV2([]string{targetID})
	return err == nil && proof.CandidateExactSetDigest == digest
}

// 只采用本题的印刷证据。标题、邻题及其它批次不参与当前题的准入。
func (o *GradingOrchestrator) referenceFromReviewedWorksheet(ctx context.Context, runtime k12.RecognitionLayoutPlanRuntimeV2, calls []k12.ModelPhysicalInvocation, q RecognizedQuestion, targetIndex int, adjudicationID, adjudication string) (printedReference, bool, error) {
	plan := runtime.AuthorizedPlan
	if plan == nil || plan.Scope == nil || plan.InitialReadMode != k12.RecognitionLayoutManifestWithContentV1 || targetIndex < 0 || targetIndex >= len(plan.Targets) ||
		q.parentSourceUnclear || q.ParentProblemID != "" || q.ProblemKind != ProblemKindStandalone {
		return printedReference{}, false, nil
	}
	target := plan.Targets[targetIndex]
	for _, dep := range plan.Scope.Dependencies {
		if slices.Contains(dep.ForOrders, target.ManifestOrder) {
			return printedReference{}, false, nil
		}
	}
	handwritingGap := false
	for _, gap := range plan.Scope.Gaps {
		if gap.Order != target.ManifestOrder {
			continue
		}
		if gap.Kind != "handwriting" {
			return printedReference{}, false, nil
		}
		handwritingGap = true
	}
	var batch *k12.RecognitionLayoutReviewBatchAuthorizationV1
	var alias string
	for i := range runtime.ReviewBatches {
		b := &runtime.ReviewBatches[i]
		for index, member := range b.Members {
			if member.CandidateID != target.TargetID {
				continue
			}
			if batch != nil || b.PlanDigest != plan.AuthorizedPlanDigest {
				return printedReference{}, false, nil
			}
			batch, alias = b, "t"+strconv.Itoa(index+1)
		}
	}
	if batch == nil {
		return printedReference{}, false, nil
	}
	var initial, reviewed string
	for _, call := range calls {
		if call.ParentInvocationID != runtime.Header.ParentInvocationID || call.Stage != k12.GradingStageRecognizing {
			continue
		}
		first := call.PhysicalInvocationID == runtime.ManifestPhysicalInvocationID && call.ResultDigest == runtime.ManifestResultDigest
		if !first && call.PhysicalUnit != batch.PhysicalUnit {
			continue
		}
		if call.Status != k12.ModelInvocationSucceeded || call.ReusedFromPhysicalInvocationID != "" || !validModelInvocationDigest(call.RequestDigest) ||
			(!first && (call.PlanDigest != plan.AuthorizedPlanDigest || call.CandidateExactSetDigest != batch.ExactSetDigest)) {
			return printedReference{}, false, nil
		}
		dest := &reviewed
		if first {
			dest = &initial
		}
		if *dest != "" {
			return printedReference{}, false, nil
		}
		payload, err := o.deps.Records.LoadSucceededModelPhysicalInvocationResultContent(ctx, runtime.Header.AgentName, call.PhysicalInvocationID, call.ResultDigest)
		if err != nil {
			return printedReference{}, false, err
		}
		*dest = payload
	}
	readings, ok := worksheetTargetReadings(initial, reviewed, target, alias, handwritingGap)
	if !ok || !printedNumberMatches(q.RawTranscription, target.SourceNumberPath) {
		return printedReference{}, false, nil
	}
	thirdLegible := true
	if adjudication != "" {
		if q.SourceRegion == nil {
			return printedReference{}, false, nil
		}
		observation, err := k12.ParseRecognitionFinalAnswerResponseV1(adjudication, *q.SourceRegion)
		if err != nil || !observation.TargetOwnershipConfirmed {
			return printedReference{}, false, nil
		}
		var result struct {
			Items []worksheetReading `json:"items"`
		}
		if json.Unmarshal(observation.LegacyEnvelope, &result) != nil || len(result.Items) != 1 || result.Items[0].Kind != "question" || result.Items[0].Recognition.Subject != q.Subject {
			return printedReference{}, false, nil
		}
		if !printedNumberMatches(observation.QuestionRawTranscription, target.SourceNumberPath) {
			slog.WarnContext(ctx, "K12 reference excluded a different printed question number", "job_id", runtime.Header.JobID, "target_id", target.TargetID, "excluded_invocation_id", adjudicationID,
				"manifest_invocation_id", runtime.ManifestPhysicalInvocationID, "review_unit", batch.PhysicalUnit)
		} else {
			signals := result.Items[0].Recognition.Signals
			if disputedWorksheetQuantity(signals) {
				return printedReference{}, false, nil
			}
			reading := observation.QuestionRawTranscription
			thirdLegible = observation.QuestionCompleteAndLegible || len(signals) > 0 && worksheetReadingAllowsReference(reading, signals, false)
			if !thirdLegible {
				if prefix, ok := worksheetBeforeObscuredLastBlank(reading, signals); ok {
					reading = prefix
				} else if !handwritingGap && !hasOnlyTrailingSourceRisk(signals) {
					return printedReference{}, false, nil
				}
			}
			readings = append(readings, reading)
		}
	}
	ref, ok := corroboratedPrintedReference(q.RawTranscription, target, readings, thirdLegible)
	return ref, ok, nil
}

type worksheetReading struct {
	Kind        string `json:"kind"`
	TargetID    string `json:"target_id"`
	Recognition struct {
		Question string   `json:"question"`
		Subject  string   `json:"subject"`
		Signals  []string `json:"ocr_signals"`
	} `json:"recognition"`
}

// 小数及正文中的数字不是题号。印在子题前的短标签按冻结路径的末级核对。
var leadingPrintedNumber = regexp.MustCompile(`^(?:第([0-9]+)题|[（(]([0-9]+)[）)]|([0-9]+)([、．.]))`)

// 父题编号可能随共享材料一起转写；只在原文准确匹配冻结材料时剥离它。
func referenceNumberMatches(q RecognizedQuestion, text string) bool {
	if len(q.SourceNumberPath) > 1 && q.ImageSourceContext != nil {
		text = strings.TrimSpace(text)
		for {
			longest := ""
			for _, material := range q.ImageSourceContext.Materials {
				prefix := strings.TrimSpace(material.Text)
				if len(prefix) > len(longest) && len(text) > len(prefix) && strings.HasPrefix(text, prefix) {
					longest = prefix
				}
			}
			if longest == "" {
				break
			}
			text = strings.TrimSpace(text[len(longest):])
		}
	}
	return printedNumberMatches(text, q.SourceNumberPath)
}

func printedNumberMatches(text string, path []string) bool {
	text = strings.TrimSpace(text)
	match := leadingPrintedNumber.FindStringSubmatch(text)
	if match == nil {
		return true
	}
	tail := text[len(match[0]):]
	next, _ := utf8.DecodeRuneInString(tail)
	if (match[4] == "." || match[4] == "．") && unicode.IsDigit(next) {
		return true
	}
	if match[2] != "" {
		next, _ = utf8.DecodeRuneInString(strings.TrimSpace(tail))
		if next != utf8.RuneError && (unicode.IsDigit(next) || strings.ContainsRune("+-−×÷=^*/", next)) {
			return true
		}
	}
	number := firstNonEmpty(match[1], firstNonEmpty(match[2], match[3]))
	if len(path) == 0 {
		return false
	}
	normalize := func(s string) string { return strings.Trim(strings.TrimSpace(s), "（）()、.．") }
	if len(path) > 1 && number == normalize(path[0]) && leadingPrintedNumber.MatchString(strings.TrimSpace(tail)) {
		return printedNumberMatches(tail, path[1:])
	}
	return number == normalize(path[len(path)-1])
}

func worksheetTargetReadings(initial, reviewed string, target k12.RecognitionLayoutTargetV2, alias string, handwritingGap bool) ([]string, bool) {
	entries, err := k12.CanonicalRecognitionLayoutInitialReadEntriesV1(initial)
	if err != nil || target.ManifestOrder < 1 || target.ManifestOrder > len(entries) {
		return nil, false
	}
	var entry struct {
		Number []string         `json:"source_number_path"`
		Read   worksheetReading `json:"initial_read"`
	}
	var batch struct {
		Items []worksheetReading `json:"items"`
	}
	if json.Unmarshal(entries[target.ManifestOrder-1], &entry) != nil || !slices.Equal(entry.Number, target.SourceNumberPath) ||
		json.Unmarshal([]byte(reviewed), &batch) != nil {
		return nil, false
	}
	pair := []worksheetReading{entry.Read}
	for _, item := range batch.Items {
		if item.TargetID == target.TargetID || item.TargetID == alias {
			pair = append(pair, item)
		}
	}
	if len(pair) != 2 {
		return nil, false
	}
	var readings []string
	for _, read := range pair {
		value := strings.TrimSpace(read.Recognition.Question)
		if read.Kind != "question" || read.Recognition.Subject != "数学" || value == "" || !printedNumberMatches(value, target.SourceNumberPath) {
			return nil, false
		}
		if !worksheetReadingAllowsReference(value, read.Recognition.Signals, handwritingGap) {
			var ok bool
			value, ok = worksheetBeforeObscuredLastBlank(value, read.Recognition.Signals)
			if !ok {
				return nil, false
			}
		}
		readings = append(readings, value)
	}
	return readings, true
}

func corroboratedPrintedReference(source string, target k12.RecognitionLayoutTargetV2, readings []string, thirdLegible bool) (printedReference, bool) {
	if len(readings) < 2 {
		return printedReference{}, false
	}
	plain := func(s string) string {
		s = k12.RecognitionQuestionSourceTextV1(s, target.SourceNumberPath, target.DisplayLabel)
		if at := strings.IndexAny(s, "？?"); at >= 0 && elementarymath.IsTravelRateSource(s[:at]) {
			_, size := utf8.DecodeRuneInString(s[at:])
			head, tail := s[:at+size], strings.TrimSpace(s[at+size:])
			for _, caption := range []string{"图示文字：", "图示文字:", "图示：", "图示:", "图中标注：", "图中标注:"} {
				tail = strings.TrimPrefix(tail, caption)
			}
			s = head + strings.ReplaceAll(tail, "这样的一1份", "这样的1份")
		}
		return s
	}
	if sheet, confirmedSource, ok := elementarymath.CorroboratedTravelWorksheet(source, readings); ok {
		source = confirmedSource
		if !sheet.DerivationPrinted {
			if at := travelDerivationHeading.FindStringIndex(source); at != nil {
				source = strings.TrimSpace(source[:at[0]])
			}
		} else if !sheet.ReasonRequested {
			if at := travelReasonHeading.FindStringIndex(source); at != nil {
				source = strings.TrimSpace(source[:at[0]])
			}
		}
		return printedReference{question: source, worksheet: &sheet}, true
	}
	main, travel, rate := elementarymath.TravelQuestionPrefix(source)
	sameText := thirdLegible
	for _, text := range readings {
		sameText = sameText && k12.RecognitionSourceQuestionsEqualV1(plain(text), plain(source))
		if rate {
			_, other, ok := elementarymath.TravelQuestionPrefix(text)
			rate = ok && other == travel
		}
	}
	if sameText {
		return printedReference{question: source}, true
	}
	if rate {
		return printedReference{question: main}, true
	}
	return printedReference{}, false
}

// 仅肯定印刷内容清楚的完整分句可忽略。剩余分句按各自的题面和笔迹来源判断。
var worksheetPrintedAnswerFrame = regexp.MustCompile(`(?:预印|印刷)(?:的)?(?:答题框|作答框|填空框|方框|括号|横线|分数线|空格)`)
var worksheetHandwritingTerms = strings.NewReplacer("可辨认的填字", "可辨认的手写答案", "是否有填写", "是否有手写作答", "填写内容", "手写作答内容")
var worksheetAnswerSlotLocation = regexp.MustCompile(`(?:末尾|题末|底部|最后)[^，。；,;]*(?:填空|答题框|作答栏|空格|一格)`)
var worksheetWritingMention = regexp.MustCompile(`(?i)手写|笔迹|笔画|填字|填写|填答|作答|墨迹|填空[中内]|答案[栏框]|handwrit|filled answer|answer box`)

// 遮挡的施事可以是笔迹，真正需要归属的是被遮挡的对象。
var worksheetOccludedObject = regexp.MustCompile(`(?:盖住|遮住|覆盖|挡住|遮挡)(?:了|住)?(.+)$`)
var worksheetObstruction = regexp.MustCompile(`盖住|遮住|覆盖|挡住|遮挡`)
var worksheetKnownAnswerObject = regexp.MustCompile(`^(?:了)?(?:原有的|部分|该|这个)?(?:手写|笔迹|填字|作答|答案栏|答案框|方框|填空框|横线)`)
var worksheetObscuredWritingNoun = regexp.MustCompile(`^的(?:手写(?:答案|作答|内容|数字)|笔迹|笔画|字迹|墨迹)$`)

// 两种候选读数可以使用不同的数词写法。判断的是原文观察所指的对象，不能据此猜一个值。
const worksheetNumerals = `\p{Nd}零〇两兩一二三四五六七八九十百千万萬亿億半`

var worksheetAlternativeValues = regexp.MustCompile(`(?i)[` + worksheetNumerals + `]+[^` + worksheetNumerals + `，。；,;]{0,16}(?:还是|或(?:者)?|\bor\b)[^，。；,;]{0,8}[` + worksheetNumerals + `]+`)
var worksheetOperandOwner = regexp.MustCompile(`路程|距离|速度|用时|时长|单价|总价|长度|面积|体积|半径|直径`)
var worksheetAnswerOwner = regexp.MustCompile(`(?i)手写(?:的)?(?:答案|数[字值]|分数|结果)|学生(?:写|填|的答案)|作答|填字|答案[栏框]|写的是|filled answer|handwritten answer`)
var worksheetObservationContinuation = regexp.MustCompile(`^(?:(?:填空内容|空内内容)?(?:无法|不能|难以|不确定)|未将|未能|是否|辨认不出|以\s*[?？]\s*保留)`)
var worksheetReadableRemainder = regexp.MustCompile(`^(?:分母|分子|其余|余下)[^，。；]*(?:可读|可辨|清晰可辨)$`)
var worksheetClauseBreak = regexp.MustCompile(`[，,。；;！!]+|但是|不过|但`)

var quotedWorksheetSlot = regexp.MustCompile(`“[^”\n]+”|"[^"\n]+"|「[^」\n]+」`)
var worksheetSlotLabel = regexp.MustCompile(`^([\p{Han}]{2,16})(?:\(\)|□)$`)

func quotesFrozenAnswerSlot(source, label string) bool {
	match := worksheetSlotLabel.FindStringSubmatch(label)
	if match == nil || worksheetPrintedRisk.MatchString(match[1]) {
		return false
	}
	_, tail, found := strings.Cut(source, match[1])
	if !found || strings.Contains(tail, match[1]) {
		return false
	}
	after, blank := strings.CutPrefix(tail, "()")
	if !blank {
		after, blank = strings.CutPrefix(tail, "□")
	}
	return blank && !strings.HasPrefix(after, "/")
}

// 复读中引用的空位标签属于作答位置。只在原题精确出现一次、确有空框、观察明确指向填入
// 内容时才合并引用，避免引号里的句号被拆成另一条风险，也不吞掉引用外的数值或印刷风险。
func worksheetReadingAllowsReference(question string, signals []string, handwritingGap bool) bool {
	compact := func(s string) string {
		return strings.Map(func(r rune) rune {
			switch {
			case unicode.IsSpace(r):
				return -1
			case r == '（':
				return '('
			case r == '）':
				return ')'
			}
			return r
		}, s)
	}
	source := compact(question)
	for _, signal := range signals {
		if strings.Contains(signal, "填空内容") || worksheetWritingMention.MatchString(signal) {
			signal = quotedWorksheetSlot.ReplaceAllStringFunc(signal, func(quote string) string {
				label := strings.TrimRight(compact(strings.Trim(quote, "“”\"「」")), "。.")
				if quotesFrozenAnswerSlot(source, label) {
					return "作答空位"
				}
				return quote
			})
		}
		if !worksheetSignalsAllowReference([]string{signal}, handwritingGap) {
			return false
		}
	}
	return true
}

// 将页尾观察绑定到题面的演算或算理问项。只把有风险的部分截去，前面的图示与各空仍须独立读数一致。
func worksheetBeforeObscuredLastBlank(question string, signals []string) (string, bool) {
	end := len(question)
	for _, signal := range signals {
		if worksheetReadingAllowsReference(question, []string{signal}, false) {
			continue
		}
		located := false
		for _, part := range worksheetClauseBreak.Split(signal, -1) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if worksheetOperandOwner.MatchString(part) || worksheetAlternativeValues.MatchString(part) || worksheetEarlierPrintRisk.MatchString(part) {
				return "", false
			}
			if worksheetEndPosition.MatchString(part) {
				heading := travelReasonHeading
				switch {
				case worksheetReasonSlot.MatchString(part):
				case worksheetEndEquation.MatchString(part):
					heading = travelDerivationHeading
				default:
					return "", false
				}
				at := heading.FindStringIndex(question)
				if at == nil {
					return "", false
				}
				located, end = true, min(end, at[0])
				continue
			}
			if !located || !worksheetWritingMention.MatchString(part) && !worksheetObservationContinuation.MatchString(part) {
				return "", false
			}
		}
		if !located {
			return "", false
		}
	}
	if end == len(question) {
		return "", false
	}
	prefix := strings.TrimSpace(question[:end])
	sheet, ok := elementarymath.ParseTravelWorksheet(prefix)
	return prefix, ok && sheet.BlankCount == 4
}

var worksheetEndPosition = regexp.MustCompile(`末尾|最后|底部|下(?:边)?缘|题末`)
var worksheetReasonSlot = regexp.MustCompile(`依据|算理|理由|填空|空格|空位|括号`)
var worksheetEndEquation = regexp.MustCompile(`推算式|计算式|演算|算式|等式|等号链`)
var worksheetEarlierPrintRisk = regexp.MustCompile(`主问|首行|题干|上部|条件|单位|运算符|图示|尺寸|整页`)

const worksheetPrintedSubject = `(?:印刷(?:的)?(?:题干|题面|原文|文字|条件|内容|部分|数值)?|题干|题意|题面|原题|主问|主要条件|题目条件|条件|图示|尺寸|运算符|算式|单位|数量关系|题号)`

var worksheetPrintedClear = regexp.MustCompile(`^` + worksheetPrintedSubject + `(?:(?:、|和|及|与)` + worksheetPrintedSubject + `)*(?:均|都|仍|完整|完全)?(?:清晰可辨|完整清晰|清晰完整|清晰|清楚|完整|可辨认|可读|明确)$`)
var worksheetPrintedRisk = regexp.MustCompile(`(?i)题干|题意|题面|原题|主问|条件|印刷|运算符|算式|单位|数量关系|题号|图示|尺寸|整页|半页|冲突|不一致|printed|question|condition|operator|unit|conflict|source_gap`)

// 只继承候选数值自己的主语。“学生写的路程”属于作答；另一观察中的路程仍属于题面。
func worksheetAnswerOwnsAlternatives(clause string, inherited bool) bool {
	for _, pair := range worksheetAlternativeValues.FindAllStringIndex(clause, -1) {
		before := clause[:pair[0]]
		answers := worksheetAnswerOwner.FindAllStringIndex(before, -1)
		operands := worksheetOperandOwner.FindAllStringIndex(before, -1)
		if len(operands) == 0 {
			if len(answers) == 0 && !inherited && !worksheetAnswerSlotLocation.MatchString(before) {
				return false
			}
			continue
		}
		if len(answers) == 0 {
			return false
		}
		answer, operand := answers[len(answers)-1], operands[len(operands)-1]
		if answer[0] < operand[0] {
			switch strings.TrimSpace(before[answer[1]:operand[0]]) {
			case "", "的", "中的", "里的", "中", "里":
			default:
				return false
			}
		}
	}
	return true
}

func worksheetSignalsAllowReference(signals []string, handwritingGap bool) bool {
	for _, signal := range signals {
		if strings.EqualFold(strings.TrimSpace(signal), "source_gap_handwriting") {
			continue
		}
		observation := worksheetHandwritingTerms.Replace(worksheetPrintedAnswerFrame.ReplaceAllString(signal, "方框"))
		handwritingScope := false
		answerOwner := false
		for _, part := range worksheetClauseBreak.Split(observation, -1) {
			clause := strings.TrimSpace(part)
			if clause == "" {
				continue
			}
			if worksheetPrintedClear.MatchString(clause) {
				handwritingScope = false
				answerOwner = false
				continue
			}
			if worksheetPrintedRisk.MatchString(clause) {
				return false
			}
			if worksheetObscuresSource(clause) {
				return false
			}
			writtenAnswer := worksheetAnswerOwner.MatchString(clause)
			explicitAnswer := writtenAnswer || worksheetAnswerSlotLocation.MatchString(clause)
			if !worksheetAnswerOwnsAlternatives(clause, answerOwner) {
				return false
			}
			answerOwner = answerOwner || explicitAnswer
			if writtenAnswer || worksheetWritingMention.MatchString(clause) ||
				worksheetAnswerSlotLocation.MatchString(clause) && worksheetWritingMention.MatchString(observation) {
				handwritingScope = true
				continue
			}
			if handwritingGap && worksheetAnswerSlotLocation.MatchString(clause) || handwritingScope && (worksheetObservationContinuation.MatchString(clause) || worksheetReadableRemainder.MatchString(clause)) {
				continue
			}
			return false
		}
	}
	return true
}

func worksheetObscuresSource(clause string) bool {
	if i := strings.IndexAny(clause, "被受"); i >= 0 && worksheetObstruction.MatchString(clause[i:]) {
		// 被动句中，笔迹可能是遮挡物，归属取决于被遮挡的主语。
		subject := clause[:i]
		if obscured := worksheetOccludedObject.FindStringSubmatch(clause[i:]); obscured != nil &&
			strings.Contains(subject, "有") && worksheetObscuredWritingNoun.MatchString(obscured[1]) {
			// 在“有被遮挡的笔迹”这类定语结构中，被遮挡的对象是末尾名词。
			return false
		}
		return !worksheetWritingMention.MatchString(subject) && !worksheetAnswerSlotLocation.MatchString(subject) && !strings.Contains(subject, "方框")
	}
	if m := worksheetOccludedObject.FindStringSubmatchIndex(clause); m != nil {
		return worksheetWritingMention.MatchString(clause[:m[0]]) && !worksheetKnownAnswerObject.MatchString(clause[m[2]:m[3]])
	}
	return false
}

// 同一来源仍给出两个可能的数值时，识别置信度或整题可读标志不能消除这个分歧。
func disputedWorksheetQuantity(signals []string) bool {
	return slices.ContainsFunc(signals, func(signal string) bool {
		return worksheetAlternativeValues.MatchString(signal) && !worksheetSignalsAllowReference([]string{signal}, true)
	})
}

func sharedReferenceMaterial(material k12.RecognitionImageScopeDependencyV1) bool {
	return len(material.ForOrders) >= 2
}

// 在三份印刷题面已核对一致时，含糊的字迹描述不裁决印刷事实。
// 明确的印刷风险及归属不明的数值争议仍然否决参考解。
func hasPrintedReferenceRisk(signals []string) bool {
	return slices.ContainsFunc(signals, func(signal string) bool {
		if worksheetSignalsAllowReference([]string{signal}, false) {
			return false
		}
		return disputedWorksheetQuantity([]string{signal}) || slices.ContainsFunc(worksheetClauseBreak.Split(signal, -1), func(clause string) bool {
			clause = strings.TrimSpace(clause)
			return worksheetPrintedRisk.MatchString(clause) && !worksheetPrintedClear.MatchString(clause)
		})
	})
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
	if err != nil || !observed.TargetOwnershipConfirmed || !referenceNumberMatches(q, observed.QuestionRawTranscription) {
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
	if disputedWorksheetQuantity(envelope.Items[0].Recognition.OCRSignals) {
		return "", false
	}
	printed := cloneRecognizedQuestions([]RecognizedQuestion{q})[0]
	printed.EvidenceTranscriptions = append(printed.EvidenceTranscriptions, observed.QuestionRawTranscription)
	printed.RecognitionConfidence = envelope.Items[0].Recognition.Confidence
	printed.OCRSignals = envelope.Items[0].Recognition.OCRSignals
	if len(source.Recoveries) == 0 && observed.QuestionCompleteAndLegible && req.practiceReference == nil &&
		q.ProblemKind == ProblemKindStandalone && len(q.SourceNumberPath) >= 2 && !q.parentSourceUnclear &&
		q.RecognitionConfidence != nil && *q.RecognitionConfidence >= ocrConfidenceConfirmationThreshold &&
		slices.ContainsFunc(source.Materials, sharedReferenceMaterial) {
		// 同一目标的三次题面观察决定参考解；作答完整标志不证明作答可判。
		if hasPrintedReferenceRisk(q.OCRSignals) || hasPrintedReferenceRisk(printed.OCRSignals) {
			return "", false
		}
		projectReferencePrintReadings(&printed, true)
		for _, reading := range printed.EvidenceTranscriptions {
			if !k12.RecognitionSourceQuestionsEqualV1(printed.RawTranscription, reading) {
				return "", false
			}
		}
		printed.OCRSignals = nil
		return "", independentReferenceAnswerEligible(req, printed)
	}
	if observed.ActiveAnswerComplete {
		return "", false
	}
	if len(source.Recoveries) > 0 {
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
		value = k12.RecognitionQuestionSourceTextV1(elementarymath.SeparateMixedFraction(value), q.SourceNumberPath, q.DisplayLabel)
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
	originals := append([]string{q.RawTranscription}, q.EvidenceTranscriptions...)
	tails := []string{tail}
	for _, observed := range q.EvidenceTranscriptions {
		prefix, observedTail, ok := leading(observed)
		if !ok || !k12.RecognitionSourceQuestionsEqualV1(primary, prefix) {
			return "", false
		}
		tails = append(tails, observedTail)
	}
	return corroboratedTravelSource(primary, originals, tails, travel), true
}
