package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 新证据只来自本次成功裁决；已有终稿按不可变结算回放，不再次依赖原响应正文。
func validateRecognitionFinalAnswerSettlementV1(ctx context.Context, q dbQueryer, parent k12.ModelInvocation, planID string, in k12.RecognitionLayoutAdjudicationSettlementV2, requireContent bool) error {
	if err := validateRecognitionPrintedQuestionSettlementV1(ctx, q, parent, planID, in, requireContent); err != nil {
		return err
	}
	if in.FinalAnswerEvidence == nil {
		return nil
	}
	var headerJSON, headerDigest, planJSON, planDigest, originalJSON, requestJSON string
	if err := q.QueryRowContext(ctx, `SELECT p.layout_header_json,p.header_digest,p.authorized_plan_json,p.authorized_plan_digest,a.original_candidate_json,a.request_json
		FROM k12_recognition_layout_plans p JOIN k12_recognition_layout_adjudication_authorizations a ON a.plan_id=p.plan_id
		WHERE p.plan_id=? AND p.parent_invocation_id=? AND p.agent_name=? AND a.authorization_id=? AND a.candidate_id=?`,
		planID, parent.InvocationID, parent.AgentName, in.AuthorizationID, in.CandidateID).Scan(&headerJSON, &headerDigest, &planJSON, &planDigest, &originalJSON, &requestJSON); err != nil {
		return err
	}
	var header k12.RecognitionLayoutPlanHeaderV2
	var plan k12.RecognitionLayoutPlanV2
	var request k12.RecognitionLayoutAdjudicationRequestV2
	if json.Unmarshal([]byte(headerJSON), &header) != nil || json.Unmarshal([]byte(planJSON), &plan) != nil ||
		json.Unmarshal([]byte(requestJSON), &request) != nil || (request.ConflictKind != "answer" && request.ConflictKind != "observation_uncertainty") ||
		!k12.RecognitionFinalAnswerExecutionPolicy(header.ExecutionPolicy) ||
		header.InitialReadMode != k12.RecognitionLayoutManifestWithContentV1 ||
		header.AgentName != parent.AgentName || header.ParentInvocationID != parent.InvocationID || header.JobID != parent.JobID ||
		plan.InitialReadMode != header.InitialReadMode || plan.PageDigest != header.PageDigest ||
		plan.AuthorizedPlanDigest != planDigest || planDigest != in.PlanDigest || k12.ValidateRecognitionLayoutPlanV2(plan) != nil {
		return fmt.Errorf("%w: final-answer evidence policy or source drifted", ErrModelPhysicalInvocationConflict)
	}
	if _, digest, err := k12.CanonicalRecognitionLayoutPlanHeaderV2(header); err != nil || digest != headerDigest {
		return ErrModelPhysicalInvocationConflict
	}
	var target *k12.RecognitionLayoutTargetV2
	for i := range plan.Targets {
		if plan.Targets[i].TargetID == in.CandidateID {
			target = &plan.Targets[i]
			break
		}
	}
	if target == nil || target.AdjudicationRegion == nil ||
		k12.ValidateRecognitionFinalAnswerEvidenceV1(*in.FinalAnswerEvidence, *target.AdjudicationRegion) != nil {
		return fmt.Errorf("%w: final-answer evidence target or region drifted", ErrModelPhysicalInvocationConflict)
	}
	sources, err := loadRecognitionFinalAnswerSourcesV1(ctx, q, parent, in.CandidateID, originalJSON, header.ExecutionPolicy)
	if err != nil {
		return err
	}
	if !requireContent {
		return nil
	}
	var raw sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT result_content FROM k12_model_physical_invocations WHERE physical_invocation_id=? AND parent_invocation_id=? AND agent_name=?`,
		in.SourcePhysicalInvocationID, parent.InvocationID, parent.AgentName).Scan(&raw); err != nil {
		return err
	}
	if !raw.Valid || physicalInvocationResultDigest(raw.String) != in.SourcePhysicalResultDigest {
		return fmt.Errorf("%w: final-answer physical response drifted", ErrModelPhysicalInvocationConflict)
	}
	sourcePayload, err := k12.RecognitionLayoutAdjudicationSourceResponseV1(raw.String, plan, *target)
	if err != nil {
		return err
	}
	response, err := k12.ParseRecognitionFinalAnswerResponseV1(sourcePayload, *target.AdjudicationRegion)
	if err != nil || response.Evidence == nil || !reflect.DeepEqual(response.Evidence, in.FinalAnswerEvidence) {
		return fmt.Errorf("%w: final-answer evidence is detached from its response", ErrModelPhysicalInvocationConflict)
	}
	var envelope struct {
		Items []struct {
			Recognition json.RawMessage `json:"recognition"`
		} `json:"items"`
	}
	if json.Unmarshal(response.LegacyEnvelope, &envelope) != nil || len(envelope.Items) != 1 {
		return ErrModelPhysicalInvocationConflict
	}
	third, err := recognitionFinalAnswerSourceV1(envelope.Items[0].Recognition)
	if err != nil {
		return err
	}
	if !k12.RecognitionFinalAnswerSourcesTrustedForPolicyV1(header.ExecutionPolicy, append(sources, third)...) {
		return ErrModelPhysicalInvocationConflict
	}
	return nil
}

func recognitionFinalAnswerSourceV1(raw json.RawMessage) (k12.RecognitionFinalAnswerSourceV1, error) {
	var source k12.RecognitionFinalAnswerSourceV1
	if json.Unmarshal(raw, &source) != nil || !k12.RecognitionFinalAnswerSourcesTrustedV1(source) {
		return source, fmt.Errorf("%w: final-answer source confidence or state is unresolved", ErrModelPhysicalInvocationConflict)
	}
	return source, nil
}

func loadRecognitionFinalAnswerSourcesV1(ctx context.Context, q dbQueryer, parent k12.ModelInvocation, candidateID, originalJSON, policy string) ([]k12.RecognitionFinalAnswerSourceV1, error) {
	initial, found, err := loadInitialReadV1(ctx, q, parent.AgentName, parent.InvocationID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrModelPhysicalInvocationConflict
	}
	var firstRaw json.RawMessage
	for _, first := range initial.Result.FirstReads {
		if first.CandidateID == candidateID {
			firstRaw = first.ResultJSON
			break
		}
	}
	var first k12.RecognitionFinalAnswerSourceV1
	if json.Unmarshal(firstRaw, &first) != nil {
		return nil, ErrModelPhysicalInvocationConflict
	}
	review, err := recognitionFinalAnswerSourceV1(json.RawMessage(originalJSON))
	if err != nil {
		return nil, err
	}
	var observations struct {
		Questions []string `json:"evidence_transcriptions"`
	}
	if json.Unmarshal([]byte(originalJSON), &observations) != nil || len(observations.Questions) != 2 ||
		!k12.RecognitionSourceQuestionsEqualV1(first.Question, observations.Questions[0]) ||
		!k12.RecognitionSourceQuestionsEqualV1(review.Question, observations.Questions[1]) ||
		!k12.RecognitionFinalAnswerSourcesTrustedForPolicyV1(policy, first, review) {
		return nil, fmt.Errorf("%w: final-answer source observations drifted", ErrModelPhysicalInvocationConflict)
	}
	return []k12.RecognitionFinalAnswerSourceV1{first, review}, nil
}

func validateRecognitionObservationUncertaintyV1(ctx context.Context, q dbQueryer, parent k12.ModelInvocation, planID string, plan k12.RecognitionLayoutPlanV2, candidateID, originalJSON string) error {
	var raw, digest string
	if err := q.QueryRowContext(ctx, `SELECT layout_header_json,header_digest FROM k12_recognition_layout_plans WHERE plan_id=? AND parent_invocation_id=? AND agent_name=?`, planID, parent.InvocationID, parent.AgentName).Scan(&raw, &digest); err != nil {
		return err
	}
	var header k12.RecognitionLayoutPlanHeaderV2
	if json.Unmarshal([]byte(raw), &header) != nil || !k12.RecognitionFinalAnswerExecutionPolicy(header.ExecutionPolicy) ||
		header.InitialReadMode != k12.RecognitionLayoutManifestWithContentV1 || plan.InitialReadMode != header.InitialReadMode ||
		header.ParentInvocationID != parent.InvocationID || header.AgentName != parent.AgentName || header.JobID != parent.JobID || header.PageDigest != plan.PageDigest {
		return ErrModelPhysicalInvocationConflict
	}
	if _, actual, err := k12.CanonicalRecognitionLayoutPlanHeaderV2(header); err != nil || actual != digest {
		return ErrModelPhysicalInvocationConflict
	}
	sources, err := loadRecognitionFinalAnswerSourcesV1(ctx, q, parent, candidateID, originalJSON, header.ExecutionPolicy)
	if err != nil {
		return err
	}
	var observations struct {
		Answers []string `json:"answer_evidence_transcriptions"`
	}
	if json.Unmarshal([]byte(originalJSON), &observations) != nil || len(observations.Answers) != 2 ||
		!k12.RecognitionSourceQuestionsEqualV1(observations.Answers[0], observations.Answers[1]) ||
		!k12.RecognitionSourceObservationUnresolvedV1(sources[1].OCRSignals) {
		return fmt.Errorf("%w: source observation uncertainty is absent", ErrModelPhysicalInvocationConflict)
	}
	return nil
}

// ValidateRecognitionFinalAnswerReceiptV1 只读取同任务已冻结的最终化回执，返回其原候选正文副本。
// 原物理正文归档后仍可验证；不会为尚未完成的计划触发最终化或模型调用。
func (s *Store) ValidateRecognitionFinalAnswerReceiptV1(ctx context.Context, agentName, jobID string, receipt k12.RecognitionFinalAnswerReceiptV1) (json.RawMessage, error) {
	if agentName == "" || jobID == "" {
		return nil, ErrModelPhysicalInvocationConflict
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var parentID string
	if err := tx.QueryRowContext(ctx, `SELECT p.parent_invocation_id FROM k12_recognition_layout_plans p
		JOIN k12_model_physical_invocations c ON c.parent_invocation_id=p.parent_invocation_id AND c.agent_name=p.agent_name
		WHERE p.agent_name=? AND p.job_id=? AND p.authorized_plan_digest=? AND p.status='succeeded' AND c.physical_invocation_id=?`,
		agentName, jobID, receipt.PlanDigest, receipt.SourcePhysicalInvocationID).Scan(&parentID); err != nil {
		return nil, err
	}
	authority, err := loadRecognitionLayoutFinalizationAuthorityV2(ctx, tx, agentName, parentID)
	if err != nil {
		return nil, err
	}
	if authority.Status != "succeeded" || authority.Parent.JobID != jobID ||
		!k12.RecognitionFinalAnswerExecutionPolicy(authority.Header.ExecutionPolicy) ||
		(authority.Parent.Status != k12.ModelInvocationSent && authority.Parent.Status != k12.ModelInvocationSucceeded) {
		return nil, ErrModelPhysicalInvocationConflict
	}
	final, raw, found, err := loadStoredRecognitionLayoutFinalizationV2(ctx, tx, authority)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrModelPhysicalInvocationConflict
	}
	if _, err := validateStoredRecognitionLayoutFinalizationV2(ctx, tx, parentID, final, raw); err != nil {
		return nil, err
	}
	for _, candidate := range final.CandidateResults {
		if candidate.CandidateID != receipt.CandidateID {
			continue
		}
		if candidate.FinalAnswerReceipt == nil || !reflect.DeepEqual(*candidate.FinalAnswerReceipt, receipt) {
			return nil, ErrModelPhysicalInvocationConflict
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return append(json.RawMessage(nil), candidate.ResultJSON...), nil
	}
	return nil, ErrModelPhysicalInvocationConflict
}
