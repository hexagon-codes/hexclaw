package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 印刷题干回执与整份作答采纳分离，首次结算必须复核实际 Provider 原文。
func validateRecognitionPrintedQuestionSettlementV1(ctx context.Context, q dbQueryer, parent k12.ModelInvocation, planID string, in k12.RecognitionLayoutAdjudicationSettlementV2, requireContent bool) error {
	if in.PrintedQuestionEvidence == nil {
		return nil
	}
	var planJSON, originalJSON, headerJSON string
	if err := q.QueryRowContext(ctx, `SELECT p.authorized_plan_json,a.original_candidate_json,p.layout_header_json FROM k12_recognition_layout_plans p JOIN k12_recognition_layout_adjudication_authorizations a ON a.plan_id=p.plan_id WHERE p.plan_id=? AND p.parent_invocation_id=? AND p.agent_name=? AND a.authorization_id=? AND a.candidate_id=?`, planID, parent.InvocationID, parent.AgentName, in.AuthorizationID, in.CandidateID).Scan(&planJSON, &originalJSON, &headerJSON); err != nil {
		return err
	}
	var plan k12.RecognitionLayoutPlanV2
	var header k12.RecognitionLayoutPlanHeaderV2
	var original struct {
		Subject   string   `json:"subject"`
		Questions []string `json:"evidence_transcriptions"`
	}
	if json.Unmarshal([]byte(planJSON), &plan) != nil || json.Unmarshal([]byte(headerJSON), &header) != nil || json.Unmarshal([]byte(originalJSON), &original) != nil || original.Subject != "语文" ||
		k12.ValidateRecognitionLayoutPlanV2(plan) != nil || plan.AuthorizedPlanDigest != in.PlanDigest || !k12.RecognitionFinalAnswerExecutionPolicy(header.ExecutionPolicy) ||
		header.AgentName != parent.AgentName || header.ParentInvocationID != parent.InvocationID || header.JobID != parent.JobID || header.PageDigest != plan.PageDigest ||
		k12.ValidateRecognitionPrintedQuestionEvidenceV1(*in.PrintedQuestionEvidence, plan, original.Questions) != nil {
		return fmt.Errorf("%w: printed question evidence source differs", ErrModelPhysicalInvocationConflict)
	}
	if !requireContent {
		return nil
	}
	var raw sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT result_content FROM k12_model_physical_invocations WHERE physical_invocation_id=? AND parent_invocation_id=? AND agent_name=?`, in.SourcePhysicalInvocationID, parent.InvocationID, parent.AgentName).Scan(&raw); err != nil {
		return err
	}
	if !raw.Valid || physicalInvocationResultDigest(raw.String) != in.SourcePhysicalResultDigest {
		return ErrModelPhysicalInvocationConflict
	}
	for _, target := range plan.Targets {
		if target.TargetID != in.CandidateID {
			continue
		}
		proof, err := k12.RecognitionPrintedQuestionEvidenceFromResponseV1(raw.String, plan, target, original.Questions)
		if err != nil || proof == nil || !reflect.DeepEqual(proof, in.PrintedQuestionEvidence) {
			return fmt.Errorf("%w: printed question evidence is detached from its response", ErrModelPhysicalInvocationConflict)
		}
		return nil
	}
	return ErrModelPhysicalInvocationConflict
}
