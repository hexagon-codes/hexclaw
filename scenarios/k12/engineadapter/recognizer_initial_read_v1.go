package engineadapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image/png"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// 同一次整页读取同时提供目录与观察事实，作答归属不能从求解结果反推。
const recognitionLayoutInitialReadPromptV1 = `Read this original worksheet page once and return its complete layout manifest AND the first transcription for every manifest target in the SAME JSON response. Do not solve, grade, correct, or infer any answer.
Return exactly {"targets":[{"manifest_ref":"manifest_0001","manifest_order":1,"source_number_path":["1"],"display_label":"1.","source_section_path":[],"source_section_label":"","region":{"x":0,"y":0,"width":1,"height":1},"initial_read":{"kind":"question","recognition":{"question":"complete printed question including its shared conditions","subject":"数学","answer_state":"present","student_answer":"all active handwritten working and final answer","recognition_confidence":0.99,"ocr_signals":[],"answer_bbox":{"x":0,"y":0,"width":1,"height":1}},"shared_conditions":"verbatim printed conditions shared with this target, or empty","answer_ownership":"visible source evidence distinguishing this target's active handwriting from printed examples, cancelled work and neighboring handwriting"}}]}.
Number manifest_ref consecutively from manifest_0001, and manifest_order consecutively from 1; every reference has exactly one initial_read. List all independently answerable targets from top to bottom and left to right within each row, splitting each compound subquestion and preserving its shared conditions. Section headings, headers and decoration are not targets. Never omit, duplicate, merge or invent targets. Copy only visibly printed numbering and owning section into their paired path/label fields; without such evidence use [] and "" together. Source numbering and section describe printed identity, not the student's answer. region and answer_bbox use ABSOLUTE integer pixels in this original page. region must contain the complete target and its answer area; shared conditions must also be copied into question so the target remains independently understandable. answer_bbox is the tight rectangle around this target's active final handwriting (or working when no final value), never printed text or the whole question rectangle; use null for blank, unclear or uncertain location.
For a question, recognition has exactly question, subject, answer_state, student_answer, recognition_confidence, ocr_signals, answer_bbox. For a genuine title/instruction/decoration target use kind=non_question and recognition=null. Subject is 数学/语文/英语/物理/化学 or empty. Preserve every printed fraction numerator/bar/denominator, decimal, operator and unit. Keep TeX enclosed in \\( ... \\) or \\[ ... \\] and escape JSON only once. Do not infer clipped glyphs from arithmetic or handwriting.
student_answer transcribes ALL active handwritten working lines in order, including incorrect steps and final answer, separated by newlines. Printed worked examples, printed answers, printed cancellation/simplification marks and nearby handwriting are not this target's student answer. Clearly crossed-out abandoned work is not the active answer. If ownership or cancellation is uncertain, preserve it in ocr_signals instead of guessing. answer_state is present only for legible active handwriting; blank/unclear require student_answer="". Empty answer space does not lower confidence in clear printed text. Confidence and ocr_signals describe unresolved source/active-answer risks, not routine descriptions.
Do not add fields outside this contract. Do not output a separate answer list or an explanation.`

type recognitionInitialReadEntryV1 struct {
	manifest k12.RecognitionLayoutManifestTargetV2
	raw      json.RawMessage
	read     json.RawMessage
}

func parseRecognitionInitialReadEnvelopeV1(raw string) ([]recognitionInitialReadEntryV1, error) {
	fail := func(detail string) ([]recognitionInitialReadEntryV1, error) {
		return nil, fmt.Errorf("%w: recognizer: initial read %s", k12.ErrRecognitionProtocolInvalid, detail)
	}
	entries, err := k12.CanonicalRecognitionLayoutInitialReadEntriesV1(raw)
	if err != nil {
		return fail("manifest envelope or coverage is invalid")
	}
	seen := make(map[string]bool, len(entries))
	result := make([]recognitionInitialReadEntryV1, 0, len(entries))
	for _, entry := range entries {
		var fields map[string]json.RawMessage
		if json.Unmarshal(entry, &fields) != nil {
			return fail("target is not an object")
		}
		read, exists := fields["initial_read"]
		if !exists {
			return fail("target has no matching first read")
		}
		delete(fields, "initial_read")
		manifestJSON, err := json.Marshal(fields)
		if err != nil {
			return fail("manifest cannot be encoded")
		}
		manifest, err := parseRecognitionLayoutManifestTargetV2(manifestJSON)
		if err != nil || manifest.ManifestRef == "" || seen[manifest.ManifestRef] {
			return fail("manifest identity is invalid or duplicated")
		}
		seen[manifest.ManifestRef] = true
		result = append(result, recognitionInitialReadEntryV1{manifest, append(json.RawMessage(nil), entry...), append(json.RawMessage(nil), read...)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].manifest.ManifestOrder < result[j].manifest.ManifestOrder })
	return result, nil
}

func recognitionInitialReadProjectionV1(entry recognitionInitialReadEntryV1, target k12.RecognitionLayoutTargetV2) (json.RawMessage, error) {
	var read map[string]json.RawMessage
	if json.Unmarshal(entry.read, &read) != nil || !recognitionLayoutExactFieldsV2(read, map[string]struct{}{"kind": {}, "recognition": {}, "shared_conditions": {}, "answer_ownership": {}}) {
		return nil, fmt.Errorf("initial read fields are invalid")
	}
	for _, key := range []string{"shared_conditions", "answer_ownership"} {
		var value string
		if json.Unmarshal(read[key], &value) != nil {
			return nil, fmt.Errorf("initial read %s is invalid", key)
		}
	}
	var kind string
	if json.Unmarshal(read["kind"], &kind) != nil {
		return nil, fmt.Errorf("initial read kind is invalid")
	}
	recognition := read["recognition"]
	if kind == "question" {
		var fields map[string]json.RawMessage
		if json.Unmarshal(recognition, &fields) != nil || !recognitionLayoutExactFieldsV2(fields, map[string]struct{}{"question": {}, "subject": {}, "answer_state": {}, "student_answer": {}, "recognition_confidence": {}, "ocr_signals": {}, "answer_bbox": {}}) {
			return nil, fmt.Errorf("initial recognition fields are invalid")
		}
		if !bytes.Equal(bytes.TrimSpace(fields["answer_bbox"]), []byte("null")) {
			var box k12.SourcePixelRegion
			if json.Unmarshal(fields["answer_bbox"], &box) != nil || box.Width <= 0 || box.Height <= 0 || box.X < target.Region.X || box.Y < target.Region.Y || box.Width > target.Region.Width || box.Height > target.Region.Height || box.X-target.Region.X > target.Region.Width-box.Width || box.Y-target.Region.Y > target.Region.Height-box.Height {
				return nil, fmt.Errorf("initial answer rectangle is outside its target")
			}
			box.X -= target.Region.X
			box.Y -= target.Region.Y
			fields["answer_bbox"], _ = json.Marshal(box)
		}
		var shared, question string
		_ = json.Unmarshal(read["shared_conditions"], &shared)
		if json.Unmarshal(fields["question"], &question) != nil || (strings.TrimSpace(shared) != "" && !strings.Contains(question, shared)) {
			return nil, fmt.Errorf("initial question does not retain its shared conditions")
		}
		var err error
		recognition, err = json.Marshal(fields)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(map[string]any{"target_id": target.TargetID, "kind": kind, "recognition": recognition})
}

// 合图仅复制冻结原像素；过高的单裁片仍使用原单题图，不缩放或截断。
func buildRecognitionReviewImageV1(pagePNG []byte, plan k12.RecognitionLayoutPlanV2, targets []k12.RecognitionLayoutTargetV2) ([]byte, int, int, error) {
	ids := make([]string, len(targets))
	for index, target := range targets {
		ids[index] = target.TargetID
	}
	output, err := k12.BuildRecognitionLayoutReviewBatchImageV1(pagePNG, plan, ids)
	if err != nil {
		return nil, 0, 0, err
	}
	geometry, err := png.DecodeConfig(bytes.NewReader(output))
	return output, geometry.Width, geometry.Height, err
}

func buildRecognitionReviewPromptV1(targets []k12.RecognitionLayoutTargetV2) (string, error) {
	prompt, err := buildRecognitionLayoutBatchPromptV2(targets, k12.RecognitionLayoutCompactV4)
	if err != nil {
		return "", err
	}
	return "Independently transcribe these original-image crops. No previous reading or computed answer is supplied. Inspect every printed operand, operator and unit and identify only each target's own active handwriting. Do not solve, correct, infer missing pixels or borrow a neighboring target's answer. Preserve all unresolved source and ownership risks.\n\n" + prompt, nil
}

func (a *RecognizerAdapter) recognizeLayoutInitialReadV1(ctx context.Context, sourceImage []byte, headerDigest string) ([]usecase.RecognizedQuestion, error) {
	physicalCtx, cancel, err := recognitionLayoutPhysicalCallContextV2(ctx, time.Time{}, 120000)
	if err != nil {
		return nil, err
	}
	page, err := a.canonicalizeRecognitionPageV2(physicalCtx, sourceImage)
	if err != nil {
		cancel()
		return nil, err
	}
	whole, err := a.callRecognitionVisionPhysical(physicalCtx, k12.RecognitionPhysicalCall{PlanVersion: k12.RecognitionPlanVersionV2, PlanDigest: headerDigest, Unit: k12.RecognitionPhysicalUnitWholePage, Image: page.PNG}, recognitionLayoutInitialReadPromptV1)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("recognizer: initial read model call failed: %w", err)
	}
	plan, exists, err := k12.LookupRecognitionLayoutPlanV2(ctx)
	if err != nil {
		return nil, err
	}
	settlement := k12.RecognitionLayoutInitialReadSettlementV1{SourcePhysicalInvocationID: whole.InvocationID, SourcePhysicalUnit: k12.RecognitionPhysicalUnitWholePage, SourcePhysicalResultDigest: whole.ResultDigest}
	if exists {
		// 同身份回放直接取持久分类与观察，不再次套用当前的来源复核策略。
		if plan.InitialReadMode != k12.RecognitionLayoutManifestWithContentV1 || plan.ManifestInvocationID != whole.InvocationID || plan.ManifestResultDigest != whole.ResultDigest || plan.PageDigest != recognitionInitialReadDigestV1(page.PNG) {
			return nil, fmt.Errorf("%w: initial-read replay source drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
		}
	} else {
		entries, parseErr := parseRecognitionInitialReadEnvelopeV1(whole.Payload)
		if parseErr != nil {
			return nil, parseErr
		}
		manifest := make([]k12.RecognitionLayoutManifestTargetV2, len(entries))
		for index, entry := range entries {
			manifest[index] = entry.manifest
		}
		plan, err = k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{PagePNG: page.PNG, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: whole.InvocationID, ResultDigest: whole.ResultDigest}, Targets: manifest, RecognitionFormat: k12.RecognitionLayoutCompactV4, EnableSourceAdjudication: true, InitialReadMode: k12.RecognitionLayoutManifestWithContentV1})
		if err != nil {
			return nil, fmt.Errorf("%w: initial-read manifest cannot form a complete plan: %v", k12.ErrRecognitionProtocolInvalid, err)
		}
		settlement.Classification = k12.RecognitionLayoutBatchClassifiedV2
		for _, target := range plan.Targets {
			entry, bindingErr := recognitionInitialReadEntryForTargetV1(entries, target)
			if bindingErr != nil {
				return nil, bindingErr
			}
			candidate := k12.RecognitionLayoutInitialCandidateV1{CandidateID: target.TargetID, Classification: k12.RecognitionLayoutCandidateInvalidV2, FirstReadJSON: entry.raw}
			item, projectionErr := recognitionInitialReadProjectionV1(entry, target)
			if projectionErr == nil {
				payload, _ := json.Marshal(map[string]any{"items": []json.RawMessage{item}})
				decision := classifyRecognitionLayoutBatchV2(string(payload), []k12.RecognitionLayoutTargetV2{target}, k12.RecognitionLayoutCompactV1)
				if decision.classification == k12.RecognitionLayoutBatchClassifiedV2 && len(decision.candidates) == 1 {
					parsed := decision.candidates[0]
					candidate.Classification, candidate.ResultKind, candidate.ResultJSON = parsed.Classification, parsed.ResultKind, parsed.ResultJSON
					if len(decision.outcomes) == 1 && decision.outcomes[0].question != nil {
						question := usecase.EvaluateOCRConfirmationRiskForInitialReadMode(*decision.outcomes[0].question, plan.InitialReadMode)
						arithmeticView, arithmeticEligible := recognitionInitialReadArithmeticViewV1(question, entry)
						if question.ConfirmationRequired || question.AnswerState != usecase.AnswerStatePresent || !arithmeticEligible || !engine.ArithmeticTranscriptionConsistent(arithmeticView.Question, question.StudentAnswer) {
							candidate.Classification = k12.RecognitionLayoutCandidateReviewRequiredV2
						}
					}
				}
			}
			settlement.Candidates = append(settlement.Candidates, candidate)
		}
	}
	settlement.PlanDigest = plan.AuthorizedPlanDigest
	initial, _, err := k12.AuthorizeAndSettleRecognitionLayoutInitialReadV1(ctx, whole, plan, settlement)
	if err != nil {
		return nil, fmt.Errorf("recognizer: initial-read atomic settlement failed: %w", err)
	}
	if initial.Classification != k12.RecognitionLayoutBatchClassifiedV2 || !recognitionLayoutSHA256DigestV2.MatchString(initial.SettlementDigest) || len(initial.FirstReads) != len(plan.Targets) {
		return nil, fmt.Errorf("%w: initial-read settlement is incomplete", k12.ErrRecognitionProtocolInvalid)
	}
	runtime, err := k12.LoadRecognitionLayoutPlanV2Runtime(ctx)
	if err != nil {
		return nil, err
	}
	if runtime.HeaderDigest != headerDigest || runtime.Header.InitialReadMode != k12.RecognitionLayoutManifestWithContentV1 || runtime.Header.ParentInvocationID == "" || runtime.AuthorizedPlan == nil || !reflect.DeepEqual(*runtime.AuthorizedPlan, plan) {
		return nil, fmt.Errorf("%w: initial-read runtime drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	primary := make(map[string]recognitionLayoutBatchOutcomeV2, len(initial.FirstReads))
	for index, read := range initial.FirstReads {
		target := plan.Targets[index]
		if err := validateRecognitionInitialReadObservationV1(runtime.Header.ParentInvocationID, plan.AuthorizedPlanDigest, whole, target.TargetID, read); err != nil {
			return nil, err
		}
		if read.ResultKind == k12.RecognitionLayoutCandidateQuestionV2 && len(read.ResultJSON) > 0 {
			question, parseErr := parseRecognitionLayoutQuestionV2(read.ResultJSON, target, plan.RecognitionFormat)
			if parseErr != nil {
				return nil, parseErr
			}
			primary[target.TargetID] = recognitionLayoutBatchOutcomeV2{targetID: target.TargetID, question: &question, source: whole}
		}
	}
	batches, err := authorizeRecognitionReviewGroupsV1(ctx, page.PNG, plan, runtime, initial.ReviewAuthorizations)
	if err != nil {
		return nil, err
	}
	reviewed, err := a.recognizeReviewWaveV1(ctx, page.PNG, plan, runtime, batches, primary)
	if err != nil {
		return nil, err
	}
	if plan.SourceAdjudication {
		// 裁决复用既有规则；批内读数只为旧单题比较器构造瞬时投影，真实回执保持完整。
		projectedReviewed := make(map[string]recognitionLayoutBatchOutcomeV2, len(reviewed))
		for targetID, outcome := range reviewed {
			singleton, singletonErr := recognitionReviewSingletonPayloadV1(outcome.source.Payload, targetID, batches)
			if singletonErr != nil {
				return nil, singletonErr
			}
			projected := outcome
			projected.source.Payload = singleton
			projectedReviewed[targetID] = projected
		}
		if err := a.recognizeLayoutAdjudicationsV2(ctx, page.PNG, plan, runtime, primary, projectedReviewed); err != nil {
			return nil, err
		}
	}
	finalization, _, err := k12.FinalizeRecognitionLayoutPlanV2(ctx)
	if err != nil {
		return nil, err
	}
	return RecognizedQuestionsFromLayoutFinalizationV2(finalization, plan)
}

// 仅比较副本去掉首读中精确配对的非数值印刷指令，不改题干、学生步骤或来源事实。
func recognitionInitialReadArithmeticViewV1(question usecase.RecognizedQuestion, entry recognitionInitialReadEntryV1) (usecase.RecognizedQuestion, bool) {
	var observation struct {
		SharedConditions string `json:"shared_conditions"`
	}
	if json.Unmarshal(entry.read, &observation) != nil {
		return question, false
	}
	for _, label := range []string{"把下面每题的得数化简：", "计算下面各题，能简算的要简算："} {
		if strings.HasPrefix(question.Question, label) {
			// 解题 helper 接受标题，不等于首读已有独立冻结的共享条件证据。
			if observation.SharedConditions != label {
				return question, false
			}
			question.Question = strings.TrimSpace(strings.TrimPrefix(question.Question, label))
			return question, true
		}
	}
	return question, true
}

func recognitionInitialReadDigestV1(raw []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

// 持久摘要绑定父调用、计划与真实来源，完整观察和严格投影各自沿用其既有合同。
func validateRecognitionInitialReadObservationV1(parent, planDigest string, source k12.RecognitionPhysicalCallResult, targetID string, read k12.RecognitionLayoutInitialReadReceiptV1) error {
	fail := func() error {
		return fmt.Errorf("%w: persisted initial-read observation drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	if parent == "" || read.CandidateID != targetID {
		return fail()
	}
	initialSettlement := k12.RecognitionLayoutInitialReadSettlementV1{PlanDigest: planDigest, SourcePhysicalInvocationID: source.InvocationID, SourcePhysicalUnit: k12.RecognitionPhysicalUnitWholePage, SourcePhysicalResultDigest: source.ResultDigest}
	firstReadDigest, err := k12.RecognitionLayoutFirstReadDigestV1(parent, initialSettlement, read.RecognitionLayoutInitialCandidateV1)
	if err != nil || firstReadDigest != read.FirstReadDigest {
		return fail()
	}
	if len(read.ResultJSON) > 0 {
		resultSettlement := k12.RecognitionLayoutPrimaryBatchSettlementV2{PlanDigest: planDigest, SourcePhysicalInvocationID: source.InvocationID, SourcePhysicalUnit: k12.RecognitionPhysicalUnitWholePage, SourcePhysicalResultDigest: source.ResultDigest}
		candidate := k12.RecognitionLayoutCandidateSettlementV2{CandidateID: read.CandidateID, Classification: read.Classification, ResultKind: read.ResultKind, ResultJSON: read.ResultJSON}
		resultDigest, err := k12.RecognitionLayoutCandidateResultDigestV2(parent, resultSettlement, candidate)
		if err != nil || resultDigest != read.ResultDigest {
			return fail()
		}
	}
	return nil
}

func recognitionInitialReadEntryForTargetV1(entries []recognitionInitialReadEntryV1, target k12.RecognitionLayoutTargetV2) (recognitionInitialReadEntryV1, error) {
	region := target.Region
	if target.OriginalRegion != nil {
		region = *target.OriginalRegion
	}
	var selected *recognitionInitialReadEntryV1
	for index := range entries {
		manifest := entries[index].manifest
		if manifest.Region == region && manifest.DisplayLabel == target.DisplayLabel && manifest.SourceSectionLabel == target.SourceSectionLabel && slices.Equal(manifest.SourceNumberPath, target.SourceNumberPath) && slices.Equal(manifest.SourceSectionPath, target.SourceSectionPath) {
			if selected != nil {
				return recognitionInitialReadEntryV1{}, fmt.Errorf("%w: initial observation has duplicate target ownership", k12.ErrRecognitionProtocolInvalid)
			}
			selected = &entries[index]
		}
	}
	if selected == nil {
		return recognitionInitialReadEntryV1{}, fmt.Errorf("%w: initial observation is detached from its manifest target", k12.ErrRecognitionProtocolInvalid)
	}
	return *selected, nil
}

func recognitionInitialReadFinalSourceV1(candidate k12.RecognitionLayoutCandidateFinalResultV2, plan k12.RecognitionLayoutPlanV2, index int) bool {
	if candidate.SourcePhysicalUnit == k12.RecognitionPhysicalUnitWholePage {
		return candidate.SourcePhysicalInvocationID == plan.ManifestInvocationID && candidate.SourcePhysicalResultDigest == plan.ManifestResultDigest
	}
	unit := string(candidate.SourcePhysicalUnit)
	if strings.HasPrefix(unit, "layout_review_batch_") {
		ordinal, err := strconv.Atoi(strings.TrimPrefix(unit, "layout_review_batch_"))
		expected, unitErr := k12.RecognitionLayoutReviewUnitV1(ordinal)
		return err == nil && unitErr == nil && candidate.SourcePhysicalUnit == expected
	}
	adjudication, err := k12.RecognitionLayoutAdjudicationUnitV2(index + 1)
	return plan.SourceAdjudication && err == nil && candidate.SourcePhysicalUnit == adjudication && candidate.Adjudication != nil && len(candidate.OriginalCandidateJSON) > 0
}

func authorizeRecognitionReviewGroupsV1(ctx context.Context, pagePNG []byte, plan k12.RecognitionLayoutPlanV2, runtime k12.RecognitionLayoutPlanRuntimeV2, members []k12.RecognitionLayoutReviewMemberAuthorizationV1) ([]k12.RecognitionLayoutReviewBatchAuthorizationV1, error) {
	byID := make(map[string]k12.RecognitionLayoutReviewMemberAuthorizationV1, len(members))
	for _, member := range members {
		if _, exists := byID[member.CandidateID]; exists || member.ReviewRound != 1 || member.AuthorizationID == "" || !recognitionLayoutSHA256DigestV2.MatchString(member.AuthorizationDigest) {
			return nil, fmt.Errorf("%w: initial-read review member authorization drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
		}
		byID[member.CandidateID] = member
	}
	batches := append([]k12.RecognitionLayoutReviewBatchAuthorizationV1(nil), runtime.ReviewBatches...)
	covered := make(map[string]bool, len(members))
	for _, batch := range batches {
		for _, member := range batch.Members {
			if stored, exists := byID[member.CandidateID]; !exists || covered[member.CandidateID] || !reflect.DeepEqual(stored, member) {
				return nil, fmt.Errorf("%w: frozen review batch member drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
			}
			covered[member.CandidateID] = true
		}
	}
	remaining := make([]k12.RecognitionLayoutReviewMemberAuthorizationV1, 0, len(members))
	for _, target := range plan.Targets {
		if member, exists := byID[target.TargetID]; exists && !covered[target.TargetID] {
			remaining = append(remaining, member)
		}
	}
	if len(covered)+len(remaining) != len(members) {
		return nil, fmt.Errorf("%w: review member is outside the frozen plan", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	// 所有新分组先冻结，随后才进入有界调用；已冻结成员从不按当前剩余集合重分组。
	for start := 0; start < len(remaining); {
		end, height := start, 16
		for end < len(remaining) && end-start < 4 {
			target, err := recognitionLayoutTargetV2(plan, remaining[end].CandidateID)
			if err != nil {
				return nil, err
			}
			nextHeight := height + target.Region.Height
			if end > start {
				nextHeight += 8
			}
			if end > start && nextHeight > 768 {
				break
			}
			height = nextHeight
			end++
			if height > 768 {
				break
			}
		}
		unit, err := k12.RecognitionLayoutReviewUnitV1(len(batches) + 1)
		if err != nil {
			return nil, err
		}
		request := k12.RecognitionLayoutReviewBatchAuthorizationRequestV1{PlanDigest: plan.AuthorizedPlanDigest, PhysicalUnit: unit, Members: append([]k12.RecognitionLayoutReviewMemberAuthorizationV1(nil), remaining[start:end]...), RecognitionFormat: k12.RecognitionLayoutCompactV4}
		_, _, request, err = recognitionReviewRequestV1(pagePNG, plan, request)
		if err != nil {
			return nil, err
		}
		authorized, _, err := k12.AuthorizeRecognitionLayoutReviewBatchV1(ctx, request)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(authorized.RecognitionLayoutReviewBatchAuthorizationRequestV1, request) || authorized.AuthorizationID == "" || !recognitionLayoutSHA256DigestV2.MatchString(authorized.AuthorizationDigest) {
			return nil, fmt.Errorf("%w: review authorization projection drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
		}
		batches = append(batches, authorized)
		start = end
	}
	return batches, nil
}

func recognitionReviewRequestV1(pagePNG []byte, plan k12.RecognitionLayoutPlanV2, request k12.RecognitionLayoutReviewBatchAuthorizationRequestV1) ([]byte, string, k12.RecognitionLayoutReviewBatchAuthorizationRequestV1, error) {
	targets := make([]k12.RecognitionLayoutTargetV2, 0, len(request.Members))
	for _, member := range request.Members {
		target, err := recognitionLayoutTargetV2(plan, member.CandidateID)
		if err != nil {
			return nil, "", request, err
		}
		targets = append(targets, target)
	}
	image, width, height, err := buildRecognitionReviewImageV1(pagePNG, plan, targets)
	if err != nil {
		return nil, "", request, err
	}
	prompt, err := buildRecognitionReviewPromptV1(targets)
	if err != nil {
		return nil, "", request, err
	}
	request.ImageDigest, request.ImageWidth, request.ImageHeight = recognitionInitialReadDigestV1(image), width, height
	request.PromptDigest = recognitionInitialReadDigestV1([]byte(prompt))
	request.InputDigest, err = k12.RecognitionLayoutReviewBatchInputDigestV1(request)
	return image, prompt, request, err
}

type recognitionReviewExecutionV1 struct {
	index    int
	outcomes []recognitionLayoutBatchOutcomeV2
	err      error
}

func (a *RecognizerAdapter) recognizeReviewWaveV1(ctx context.Context, pagePNG []byte, plan k12.RecognitionLayoutPlanV2, runtime k12.RecognitionLayoutPlanRuntimeV2, batches []k12.RecognitionLayoutReviewBatchAuthorizationV1, initial map[string]recognitionLayoutBatchOutcomeV2) (map[string]recognitionLayoutBatchOutcomeV2, error) {
	result := make(map[string]recognitionLayoutBatchOutcomeV2)
	if len(batches) == 0 {
		return result, nil
	}
	if runtime.Header.AdapterWorkerHardCap != 2 || runtime.Header.EffectiveConcurrency < 1 || runtime.Header.PhysicalCallCapMillis != 120000 {
		return nil, fmt.Errorf("%w: review scheduling parameters drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	workerCount := min(2, runtime.Header.EffectiveConcurrency, len(batches))
	completed := make(chan recognitionReviewExecutionV1, workerCount)
	inFlight, next := 0, 0
	dispatch := func(index int) {
		inFlight++
		go func() {
			completed <- a.recognizeReviewBatchV1(ctx, pagePNG, plan, runtime, batches[index], initial, index)
		}()
	}
	for inFlight < workerCount && next < len(batches) {
		dispatch(next)
		next++
	}
	ordered := make([]recognitionReviewExecutionV1, len(batches))
	seen := make([]bool, len(batches))
	var stopped error
	for inFlight > 0 {
		current := <-completed
		inFlight--
		if current.index < 0 || current.index >= len(ordered) || seen[current.index] {
			stopped = fmt.Errorf("%w: review execution index drifted", k12.ErrRecognitionProtocolInvalid)
		} else {
			ordered[current.index], seen[current.index] = current, true
			if stopped == nil && current.err != nil {
				stopped = current.err
			}
		}
		if stopped == nil && next < len(batches) {
			dispatch(next)
			next++
		}
	}
	if stopped != nil {
		return nil, stopped
	}
	for _, current := range ordered {
		for _, outcome := range current.outcomes {
			if _, duplicate := result[outcome.targetID]; duplicate {
				return nil, fmt.Errorf("%w: review target appears in multiple batches", k12.ErrRecognitionLayoutPlanV2Unauthorized)
			}
			result[outcome.targetID] = outcome
		}
	}
	return result, nil
}

func (a *RecognizerAdapter) recognizeReviewBatchV1(ctx context.Context, pagePNG []byte, plan k12.RecognitionLayoutPlanV2, runtime k12.RecognitionLayoutPlanRuntimeV2, authorization k12.RecognitionLayoutReviewBatchAuthorizationV1, initial map[string]recognitionLayoutBatchOutcomeV2, index int) recognitionReviewExecutionV1 {
	result := recognitionReviewExecutionV1{index: index}
	image, prompt, actual, err := recognitionReviewRequestV1(pagePNG, plan, authorization.RecognitionLayoutReviewBatchAuthorizationRequestV1)
	if err != nil || !reflect.DeepEqual(actual, authorization.RecognitionLayoutReviewBatchAuthorizationRequestV1) {
		result.err = fmt.Errorf("%w: frozen review input does not match original pixels and prompt", k12.ErrRecognitionLayoutPlanV2Unauthorized)
		return result
	}
	targets := make([]k12.RecognitionLayoutTargetV2, len(actual.Members))
	ids := make([]string, len(actual.Members))
	for memberIndex, member := range actual.Members {
		targets[memberIndex], err = recognitionLayoutTargetV2(plan, member.CandidateID)
		if err != nil {
			result.err = err
			return result
		}
		ids[memberIndex] = member.CandidateID
	}
	exactSet, err := k12.RecognitionLayoutTargetExactSetDigestV2(ids)
	if err != nil || !reflect.DeepEqual(ids, authorization.OrderedTargetIDs) || exactSet != authorization.ExactSetDigest || authorization.ReviewRound != 1 {
		result.err = fmt.Errorf("%w: frozen review member exact-set drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
		return result
	}
	physicalCtx, cancel, err := recognitionLayoutPhysicalCallContextV2(ctx, time.UnixMilli(runtime.StageDeadlineAtUnixMillis), runtime.Header.PhysicalCallCapMillis)
	if err != nil {
		result.err = err
		return result
	}
	physical, err := a.callRecognitionVisionPhysical(physicalCtx, k12.RecognitionPhysicalCall{PlanVersion: k12.RecognitionPlanVersionV2, PlanDigest: plan.AuthorizedPlanDigest, Unit: authorization.PhysicalUnit, TargetIDs: ids, Image: image}, prompt)
	cancel()
	if err != nil {
		result.err = err
		return result
	}
	decision := classifyRecognitionLayoutBatchV2(physical.Payload, targets, plan.RecognitionFormat)
	for candidateIndex := range decision.candidates {
		candidate := &decision.candidates[candidateIndex]
		prior := initial[candidate.CandidateID]
		if candidate.ResultKind != k12.RecognitionLayoutCandidateQuestionV2 || prior.question == nil {
			continue
		}
		target := targets[candidateIndex]
		question, parseErr := parseRecognitionLayoutQuestionV2(candidate.ResultJSON, target, plan.RecognitionFormat)
		if parseErr != nil {
			result.err = parseErr
			return result
		}
		candidate.ResultJSON, err = mergeRecognitionIndependentReadV1(candidate.ResultJSON, *prior.question, question, plan.InitialReadMode)
		if err != nil {
			result.err = err
			return result
		}
		question, err = parseRecognitionLayoutQuestionV2(candidate.ResultJSON, target, plan.RecognitionFormat)
		if err != nil {
			result.err = err
			return result
		}
		for outcomeIndex := range decision.outcomes {
			if decision.outcomes[outcomeIndex].targetID == candidate.CandidateID {
				decision.outcomes[outcomeIndex].question = &question
			}
		}
	}
	settlement := k12.RecognitionLayoutReviewBatchSettlementV1{PlanDigest: plan.AuthorizedPlanDigest, AuthorizationID: authorization.AuthorizationID, AuthorizationDigest: authorization.AuthorizationDigest, SourcePhysicalInvocationID: physical.InvocationID, SourcePhysicalUnit: authorization.PhysicalUnit, SourcePhysicalResultDigest: physical.ResultDigest, Classification: decision.classification, AmbiguityKind: decision.ambiguityKind, Candidates: decision.candidates}
	projection, _, err := k12.SettleRecognitionLayoutReviewBatchV1(ctx, physical, settlement)
	if err != nil {
		result.err = err
		return result
	}
	if projection.Classification != decision.classification || !recognitionLayoutSHA256DigestV2.MatchString(projection.SettlementDigest) {
		result.err = fmt.Errorf("%w: review settlement projection drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
		return result
	}
	if decision.classification != k12.RecognitionLayoutBatchClassifiedV2 || len(projection.UnresolvedCandidateIDs) != 0 || len(projection.FrozenResults) != len(targets) || len(decision.outcomes) != len(targets) {
		result.err = fmt.Errorf("%w: independent review did not resolve every member", k12.ErrRecognitionProtocolInvalid)
		return result
	}
	resultSettlement := k12.RecognitionLayoutPrimaryBatchSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, SourcePhysicalInvocationID: physical.InvocationID, SourcePhysicalUnit: authorization.PhysicalUnit, SourcePhysicalResultDigest: physical.ResultDigest}
	for candidateIndex, candidate := range decision.candidates {
		frozen := projection.FrozenResults[candidateIndex]
		resultDigest, digestErr := k12.RecognitionLayoutCandidateResultDigestV2(runtime.Header.ParentInvocationID, resultSettlement, candidate)
		if digestErr != nil || candidate.Classification != k12.RecognitionLayoutCandidateValidV2 || frozen.CandidateID != candidate.CandidateID || frozen.ResultKind != candidate.ResultKind || frozen.ResultDigest != resultDigest {
			result.err = fmt.Errorf("%w: review candidate binding drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
			return result
		}
	}
	for outcomeIndex := range decision.outcomes {
		decision.outcomes[outcomeIndex].source = physical
	}
	result.outcomes = decision.outcomes
	return result
}

func mergeRecognitionIndependentReadV1(raw json.RawMessage, initial, reread usecase.RecognizedQuestion, initialReadMode string) (json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	risk := usecase.EvaluateOCRConfirmationRiskForInitialReadMode(reread, initialReadMode)
	ownershipChanged := initial.AnswerState != reread.AnswerState && (initial.AnswerState == usecase.AnswerStatePresent || reread.AnswerState == usecase.AnswerStatePresent)
	for _, reason := range risk.ConfirmationReasons {
		ownershipChanged = ownershipChanged || reason == usecase.OCRRiskEvidenceConflict
	}
	if ownershipChanged {
		fields["ocr_signals"], _ = json.Marshal(append(append([]string(nil), reread.OCRSignals...), "evidence_conflict"))
	}
	fields["evidence_transcriptions"], _ = json.Marshal([]string{initial.RawTranscription, reread.RawTranscription})
	fields["answer_evidence_transcriptions"], _ = json.Marshal([]string{initial.AnswerRawTranscription, reread.AnswerRawTranscription})
	merged, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return canonicalRecognitionLayoutResultJSONV2(merged)
}

func recognitionReviewSingletonPayloadV1(raw, targetID string, batches []k12.RecognitionLayoutReviewBatchAuthorizationV1) (string, error) {
	modelRef := ""
	for _, batch := range batches {
		for index, candidateID := range batch.OrderedTargetIDs {
			if candidateID == targetID {
				modelRef = fmt.Sprintf("t%d", index+1)
			}
		}
	}
	var envelope struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	payload := sanitizeModelJSON(extractJSONObject(raw))
	if completed, ok := k12.CompleteRecognitionLayoutBatchJSON(payload); ok {
		payload = completed
	}
	if modelRef == "" || json.Unmarshal([]byte(payload), &envelope) != nil {
		return "", fmt.Errorf("%w: independent review source projection is unavailable", k12.ErrRecognitionProtocolInvalid)
	}
	for _, item := range envelope.Items {
		var ref string
		if json.Unmarshal(item["target_id"], &ref) == nil && ref == modelRef {
			item["target_id"] = json.RawMessage(`"t1"`)
			payload, err := json.Marshal(map[string]any{"items": []map[string]json.RawMessage{item}})
			return string(payload), err
		}
	}
	return "", fmt.Errorf("%w: independent review target is absent", k12.ErrRecognitionProtocolInvalid)
}
