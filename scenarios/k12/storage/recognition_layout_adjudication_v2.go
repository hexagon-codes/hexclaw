package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/sqliteutil"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type recognitionAdjudicationAuthority struct {
	planID, status, originalJSON, originalDigest string
	parent                                       k12.ModelInvocation
	unit                                         k12.RecognitionPhysicalUnit
}

func loadRecognitionAdjudicationAuthority(ctx context.Context, q dbQueryer, owner, parentID string, in k12.RecognitionLayoutAdjudicationRequestV2) (recognitionAdjudicationAuthority, error) {
	var out recognitionAdjudicationAuthority
	parent, err := getModelInvocationByIDVia(ctx, q, parentID)
	if err != nil {
		return out, err
	}
	if parent.AgentName != owner || parent.Stage != k12.GradingStageRecognizing {
		return out, ErrModelPhysicalInvocationConflict
	}
	out.parent = parent
	var planJSON, digest, jobID string
	if err := q.QueryRowContext(ctx, `SELECT plan_id,status,authorized_plan_digest,authorized_plan_json,job_id FROM k12_recognition_layout_plans WHERE parent_invocation_id=? AND agent_name=?`, parentID, owner).Scan(&out.planID, &out.status, &digest, &planJSON, &jobID); err != nil {
		return out, err
	}
	var plan k12.RecognitionLayoutPlanV2
	if json.Unmarshal([]byte(planJSON), &plan) != nil || k12.ValidateRecognitionLayoutPlanV2(plan) != nil || !plan.SourceAdjudication || digest != in.PlanDigest || jobID != parent.JobID {
		return out, ErrModelPhysicalInvocationConflict
	}
	ordinal := 0
	for i, target := range plan.Targets {
		if target.TargetID == in.CandidateID {
			ordinal = i + 1
			break
		}
	}
	if ordinal == 0 {
		return out, ErrModelPhysicalInvocationConflict
	}
	out.unit, err = k12.RecognitionLayoutAdjudicationUnitV2(ordinal)
	if err != nil {
		return out, err
	}
	var primaryID, primaryDigest, repairID, repairDigest string
	if err := q.QueryRowContext(ctx, `SELECT a.source_batch_physical_invocation_id,a.source_batch_result_digest,
	 s.source_physical_invocation_id,s.source_physical_result_digest,r.result_json,r.result_digest
	 FROM k12_recognition_layout_repair_authorizations a
	 JOIN k12_recognition_layout_repair_settlements s ON s.plan_id=a.plan_id AND s.repair_authorization_id=a.repair_authorization_id
	 JOIN k12_recognition_layout_candidate_results r ON r.plan_id=s.plan_id AND r.candidate_id=s.candidate_id
	 AND r.source_physical_invocation_id=s.source_physical_invocation_id AND r.source_physical_result_digest=s.source_physical_result_digest
	 WHERE a.plan_id=? AND a.candidate_id=? AND a.repair_round=1 AND s.classification='valid'`, out.planID, in.CandidateID).Scan(&primaryID, &primaryDigest, &repairID, &repairDigest, &out.originalJSON, &out.originalDigest); err != nil {
		return out, err
	}
	if primaryID != in.PrimaryPhysicalInvocationID || primaryDigest != in.PrimaryPhysicalResultDigest || repairID != in.RepairPhysicalInvocationID || repairDigest != in.RepairPhysicalResultDigest {
		return out, ErrModelPhysicalInvocationConflict
	}
	for _, source := range []struct{ id, digest string }{{primaryID, primaryDigest}, {repairID, repairDigest}} {
		child, err := getModelPhysicalInvocationByIDVia(ctx, q, owner, source.id)
		if err != nil {
			return out, err
		}
		if child.ParentInvocationID != parentID || child.Status != k12.ModelInvocationSucceeded || child.Attempt != 1 || child.ResultDigest != source.digest || child.PlanDigest != digest {
			return out, ErrModelPhysicalInvocationConflict
		}
	}
	if err := validateRecognitionAdjudicationConflict(out.originalJSON, in.ConflictKind); err != nil {
		return out, err
	}
	return out, nil
}

func validateRecognitionAdjudicationConflict(originalJSON, conflictKind string) error {
	var observations struct {
		Question []string `json:"evidence_transcriptions"`
		Answer   []string `json:"answer_evidence_transcriptions"`
	}
	if json.Unmarshal([]byte(originalJSON), &observations) != nil {
		return ErrModelPhysicalInvocationConflict
	}
	different := func(values []string) bool {
		return len(values) == 2 && strings.TrimSpace(values[0]) != strings.TrimSpace(values[1])
	}
	questionConflict, answerConflict := different(observations.Question), different(observations.Answer)
	valid := false
	switch conflictKind {
	case "question":
		valid = questionConflict
	case "answer", "answer_ownership":
		valid = answerConflict
	case "both":
		valid = questionConflict && answerConflict
	}
	if !valid {
		return fmt.Errorf("%w: adjudication requires conflicting retained observations", ErrModelPhysicalInvocationConflict)
	}
	return nil
}

func recognitionAdjudicationAuthorization(in k12.RecognitionLayoutAdjudicationRequestV2, authority recognitionAdjudicationAuthority) (k12.RecognitionLayoutAdjudicationAuthorizationV2, string) {
	raw, _ := json.Marshal(in)
	evidence, _ := json.Marshal(struct {
		Contract, ParentID, OriginalDigest string
		Request                            json.RawMessage
	}{"recognition_layout_adjudication_authorization_v2", authority.parent.InvocationID, authority.originalDigest, raw})
	digest := physicalInvocationResultDigest(string(evidence))
	return k12.RecognitionLayoutAdjudicationAuthorizationV2{AuthorizationID: "adjudication:" + digest, AuthorizationDigest: digest, CandidateID: in.CandidateID, PhysicalUnit: authority.unit}, string(raw)
}

func (s *Store) AuthorizeRecognitionLayoutAdjudicationV2(ctx context.Context, owner, parentID string, in k12.RecognitionLayoutAdjudicationRequestV2) (out k12.RecognitionLayoutAdjudicationAuthorizationV2, created bool, err error) {
	err = sqliteutil.RetryOnBusy(ctx, func() error {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, `UPDATE k12_recognition_layout_plans SET updated_at=updated_at WHERE agent_name=? AND parent_invocation_id=?`, owner, parentID); e != nil {
			return e
		}
		authority, e := loadRecognitionAdjudicationAuthority(ctx, tx, owner, parentID, in)
		if e != nil {
			return e
		}
		var requestJSON string
		out, requestJSON = recognitionAdjudicationAuthorization(in, authority)
		var existingID, existingDigest string
		e = tx.QueryRowContext(ctx, `SELECT authorization_id,authorization_digest FROM k12_recognition_layout_adjudication_authorizations WHERE plan_id=? AND candidate_id=?`, authority.planID, in.CandidateID).Scan(&existingID, &existingDigest)
		if e == nil {
			if existingID != out.AuthorizationID || existingDigest != out.AuthorizationDigest {
				return ErrModelPhysicalInvocationConflict
			}
			created = false
			return tx.Commit()
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if authority.parent.Status != k12.ModelInvocationSent || (authority.status != "authorized" && authority.status != "running") {
			return records.ErrIllegalTransition
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO k12_recognition_layout_adjudication_authorizations(plan_id,candidate_id,authorization_id,authorization_digest,physical_unit,request_json,original_candidate_json,original_candidate_digest,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, authority.planID, in.CandidateID, out.AuthorizationID, out.AuthorizationDigest, out.PhysicalUnit, requestJSON, authority.originalJSON, authority.originalDigest, nowUnix())
		if e != nil {
			return e
		}
		created = true
		return tx.Commit()
	})
	return
}

func validateRecognitionLayoutAdjudicationAuthorizationVia(ctx context.Context, q dbQueryer, parent k12.ModelInvocation, invocation k12.ModelPhysicalInvocation, planID string) error {
	var requestJSON, authorizationID, digest string
	if err := q.QueryRowContext(ctx, `SELECT request_json,authorization_id,authorization_digest FROM k12_recognition_layout_adjudication_authorizations WHERE plan_id=? AND physical_unit=?`, planID, invocation.PhysicalUnit).Scan(&requestJSON, &authorizationID, &digest); err != nil {
		return err
	}
	var in k12.RecognitionLayoutAdjudicationRequestV2
	if json.Unmarshal([]byte(requestJSON), &in) != nil {
		return ErrModelPhysicalInvocationConflict
	}
	authority, err := loadRecognitionAdjudicationAuthority(ctx, q, parent.AgentName, parent.InvocationID, in)
	if err != nil {
		return err
	}
	want, _ := recognitionAdjudicationAuthorization(in, authority)
	exact, err := k12.RecognitionLayoutTargetExactSetDigestV2([]string{in.CandidateID})
	if err != nil || want.AuthorizationID != authorizationID || want.AuthorizationDigest != digest || authority.unit != invocation.PhysicalUnit || in.PlanDigest != invocation.PlanDigest || exact != invocation.CandidateExactSetDigest {
		return ErrModelPhysicalInvocationConflict
	}
	return nil
}

func recognitionAdjudicationSettlementProjection(parentID string, in k12.RecognitionLayoutAdjudicationSettlementV2) (k12.RecognitionLayoutAdjudicationSettlementResultV2, string, error) {
	var out k12.RecognitionLayoutAdjudicationSettlementResultV2
	priorValid := func(v string) bool { return v == "primary" || v == "repair" || v == "both" }
	if in.Adopted {
		if !priorValid(in.MatchedQuestionPrior) || !priorValid(in.MatchedAnswerPrior) || in.ResultKind != k12.RecognitionLayoutCandidateQuestionV2 {
			return out, "", ErrModelPhysicalInvocationConflict
		}
		digest, err := recognitionLayoutCandidateResultDigestV2(parentID, k12.RecognitionLayoutPrimaryBatchSettlementV2{PlanDigest: in.PlanDigest, SourcePhysicalInvocationID: in.SourcePhysicalInvocationID, SourcePhysicalUnit: in.SourcePhysicalUnit, SourcePhysicalResultDigest: in.SourcePhysicalResultDigest}, k12.RecognitionLayoutCandidateSettlementV2{CandidateID: in.CandidateID, ResultKind: in.ResultKind, ResultJSON: in.ResultJSON})
		if err != nil {
			return out, "", err
		}
		out.FrozenResult = &k12.RecognitionLayoutCandidateResultReceiptV2{CandidateID: in.CandidateID, ResultKind: in.ResultKind, ResultDigest: digest}
	} else if in.MatchedQuestionPrior != "" || in.MatchedAnswerPrior != "" || in.ResultKind != "" || len(in.ResultJSON) != 0 {
		return out, "", ErrModelPhysicalInvocationConflict
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return out, "", err
	}
	identity, _ := json.Marshal(struct {
		Contract, ParentID string
		Settlement         json.RawMessage
	}{"recognition_layout_adjudication_settlement_v2", parentID, raw})
	out.Adopted, out.SettlementDigest = in.Adopted, physicalInvocationResultDigest(string(identity))
	return out, string(raw), nil
}

func (s *Store) SettleRecognitionLayoutAdjudicationV2(ctx context.Context, owner, parentID string, in k12.RecognitionLayoutAdjudicationSettlementV2) (out k12.RecognitionLayoutAdjudicationSettlementResultV2, created bool, err error) {
	out, raw, err := recognitionAdjudicationSettlementProjection(parentID, in)
	if err != nil {
		return out, false, err
	}
	err = sqliteutil.RetryOnBusy(ctx, func() error {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, `UPDATE k12_recognition_layout_plans SET updated_at=updated_at WHERE agent_name=? AND parent_invocation_id=?`, owner, parentID); e != nil {
			return e
		}
		parent, e := getModelInvocationByIDVia(ctx, tx, parentID)
		if e != nil {
			return e
		}
		if parent.AgentName != owner {
			return ErrModelPhysicalInvocationConflict
		}
		var planID, digest, unit, candidateID string
		e = tx.QueryRowContext(ctx, `SELECT a.plan_id,a.authorization_digest,a.physical_unit,a.candidate_id FROM k12_recognition_layout_adjudication_authorizations a JOIN k12_recognition_layout_plans p ON p.plan_id=a.plan_id WHERE a.authorization_id=? AND p.parent_invocation_id=? AND p.agent_name=?`, in.AuthorizationID, parentID, owner).Scan(&planID, &digest, &unit, &candidateID)
		if e != nil {
			return e
		}
		if digest != in.AuthorizationDigest || unit != string(in.SourcePhysicalUnit) || candidateID != in.CandidateID {
			return ErrModelPhysicalInvocationConflict
		}
		child, e := getModelPhysicalInvocationByIDVia(ctx, tx, owner, in.SourcePhysicalInvocationID)
		if e != nil {
			return e
		}
		if e = validateRecognitionLayoutAdjudicationAuthorizationVia(ctx, tx, parent, child, planID); e != nil {
			return e
		}
		if child.ParentInvocationID != parentID || child.PhysicalUnit != in.SourcePhysicalUnit || child.PlanDigest != in.PlanDigest || child.Status != k12.ModelInvocationSucceeded || child.ResultDigest != in.SourcePhysicalResultDigest {
			return ErrModelPhysicalInvocationConflict
		}
		var storedRaw, storedDigest string
		e = tx.QueryRowContext(ctx, `SELECT settlement_json,settlement_digest FROM k12_recognition_layout_adjudication_settlements WHERE authorization_id=?`, in.AuthorizationID).Scan(&storedRaw, &storedDigest)
		if e == nil {
			if storedRaw != raw || storedDigest != out.SettlementDigest {
				return ErrModelPhysicalInvocationConflict
			}
			created = false
			return tx.Commit()
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if parent.Status != k12.ModelInvocationSent {
			return records.ErrIllegalTransition
		}
		if _, e = loadRecognitionLayoutFinalPhysicalEvidenceV2(ctx, tx, parent, child.PhysicalInvocationID, child.PhysicalUnit, child.PlanDigest, child.CandidateExactSetDigest, child.ResultDigest); e != nil {
			return e
		}
		resultDigest := ""
		if out.FrozenResult != nil {
			resultDigest = out.FrozenResult.ResultDigest
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO k12_recognition_layout_adjudication_settlements(authorization_id,source_physical_invocation_id,settlement_json,settlement_digest,result_digest,created_at) VALUES(?,?,?,?,?,?)`, in.AuthorizationID, child.PhysicalInvocationID, raw, out.SettlementDigest, resultDigest, nowUnix())
		if e != nil {
			return e
		}
		created = true
		return tx.Commit()
	})
	return
}

type recognitionAdjudicationOverlay struct {
	settlement   k12.RecognitionLayoutAdjudicationSettlementV2
	projection   k12.RecognitionLayoutAdjudicationSettlementResultV2
	originalJSON string
}

// 终态重放仅依赖不可变回执；首次最终化另核物理响应正文。
func loadRecognitionAdjudicationOverlays(ctx context.Context, q dbQueryer, authority recognitionLayoutFinalizationAuthorityV2, requireContent bool) (map[string]recognitionAdjudicationOverlay, []k12.RecognitionLayoutPhysicalResultEvidenceV2, error) {
	overlays := map[string]recognitionAdjudicationOverlay{}
	var physical []k12.RecognitionLayoutPhysicalResultEvidenceV2
	if !authority.Plan.SourceAdjudication {
		return overlays, physical, nil
	}
	for _, target := range authority.Plan.Targets {
		var requestJSON, authorizationID, digest, originalJSON, originalDigest string
		err := q.QueryRowContext(ctx, `SELECT request_json,authorization_id,authorization_digest,original_candidate_json,original_candidate_digest FROM k12_recognition_layout_adjudication_authorizations WHERE plan_id=? AND candidate_id=?`, authority.PlanID, target.TargetID).Scan(&requestJSON, &authorizationID, &digest, &originalJSON, &originalDigest)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		var request k12.RecognitionLayoutAdjudicationRequestV2
		if json.Unmarshal([]byte(requestJSON), &request) != nil {
			return nil, nil, ErrModelPhysicalInvocationConflict
		}
		base, err := loadRecognitionAdjudicationAuthority(ctx, q, authority.Parent.AgentName, authority.Parent.InvocationID, request)
		if err != nil {
			return nil, nil, err
		}
		want, _ := recognitionAdjudicationAuthorization(request, base)
		if want.AuthorizationID != authorizationID || want.AuthorizationDigest != digest || originalJSON != base.originalJSON || originalDigest != base.originalDigest {
			return nil, nil, ErrModelPhysicalInvocationConflict
		}
		var raw, settlementDigest, resultDigest, sourceID string
		if err := q.QueryRowContext(ctx, `SELECT settlement_json,settlement_digest,result_digest,source_physical_invocation_id FROM k12_recognition_layout_adjudication_settlements WHERE authorization_id=?`, authorizationID).Scan(&raw, &settlementDigest, &resultDigest, &sourceID); err != nil {
			return nil, nil, err
		}
		var in k12.RecognitionLayoutAdjudicationSettlementV2
		if json.Unmarshal([]byte(raw), &in) != nil {
			return nil, nil, ErrModelPhysicalInvocationConflict
		}
		projection, canonical, err := recognitionAdjudicationSettlementProjection(authority.Parent.InvocationID, in)
		if err != nil || canonical != raw || projection.SettlementDigest != settlementDigest || in.AuthorizationID != authorizationID || in.AuthorizationDigest != digest || in.CandidateID != target.TargetID || in.SourcePhysicalInvocationID != sourceID || in.SourcePhysicalUnit != want.PhysicalUnit || in.PlanDigest != authority.Plan.AuthorizedPlanDigest {
			return nil, nil, ErrModelPhysicalInvocationConflict
		}
		if (projection.FrozenResult == nil && resultDigest != "") || (projection.FrozenResult != nil && projection.FrozenResult.ResultDigest != resultDigest) {
			return nil, nil, ErrModelPhysicalInvocationConflict
		}
		child, err := getModelPhysicalInvocationByIDVia(ctx, q, authority.Parent.AgentName, sourceID)
		if err != nil {
			return nil, nil, err
		}
		if err := validateRecognitionLayoutAdjudicationAuthorizationVia(ctx, q, authority.Parent, child, authority.PlanID); err != nil {
			return nil, nil, err
		}
		if child.ParentInvocationID != authority.Parent.InvocationID || child.Status != k12.ModelInvocationSucceeded || child.ResultDigest != in.SourcePhysicalResultDigest {
			return nil, nil, ErrModelPhysicalInvocationConflict
		}
		evidence := k12.RecognitionLayoutPhysicalResultEvidenceV2{PhysicalInvocationID: sourceID, PhysicalUnit: child.PhysicalUnit, ResultDigest: child.ResultDigest, PlanDigest: child.PlanDigest, CandidateExactSetDigest: child.CandidateExactSetDigest, Attempt: child.Attempt}
		if requireContent {
			evidence, err = loadRecognitionLayoutFinalPhysicalEvidenceV2(ctx, q, authority.Parent, sourceID, child.PhysicalUnit, child.PlanDigest, child.CandidateExactSetDigest, child.ResultDigest)
			if err != nil {
				return nil, nil, err
			}
		}
		physical = append(physical, evidence)
		overlays[target.TargetID] = recognitionAdjudicationOverlay{in, projection, originalJSON}
	}
	return overlays, physical, nil
}

func applyRecognitionAdjudicationOverlay(result k12.RecognitionLayoutCandidateFinalResultV2, overlay recognitionAdjudicationOverlay) k12.RecognitionLayoutCandidateFinalResultV2 {
	if !overlay.projection.Adopted {
		return result
	}
	in := overlay.settlement
	result.OriginalCandidateJSON = json.RawMessage(overlay.originalJSON)
	result.Adjudication = &k12.RecognitionLayoutAdjudicationReceiptV2{AuthorizationID: in.AuthorizationID, SettlementDigest: overlay.projection.SettlementDigest, MatchedQuestionPrior: in.MatchedQuestionPrior, MatchedAnswerPrior: in.MatchedAnswerPrior}
	result.ResultKind, result.ResultDigest, result.ResultJSON = in.ResultKind, overlay.projection.FrozenResult.ResultDigest, append(json.RawMessage(nil), in.ResultJSON...)
	result.SourcePhysicalInvocationID, result.SourcePhysicalUnit, result.SourcePhysicalResultDigest = in.SourcePhysicalInvocationID, in.SourcePhysicalUnit, in.SourcePhysicalResultDigest
	return result
}

// validateRecognitionAdjudicationReuse 只复用相同原始冲突与裁片授权的成功物理回执。
func validateRecognitionAdjudicationReuse(ctx context.Context, q dbQueryer, parent k12.ModelInvocation, current k12.ModelPhysicalInvocation, prior k12.ModelInvocation, source k12.ModelPhysicalInvocation) error {
	var requests [2]k12.RecognitionLayoutAdjudicationRequestV2
	var originals [2]string
	for i, pair := range []struct {
		parent k12.ModelInvocation
		child  k12.ModelPhysicalInvocation
	}{{parent, current}, {prior, source}} {
		var raw, planID string
		if err := q.QueryRowContext(ctx, `SELECT a.request_json,a.plan_id,a.original_candidate_json FROM k12_recognition_layout_adjudication_authorizations a JOIN k12_recognition_layout_plans p ON p.plan_id=a.plan_id WHERE p.parent_invocation_id=? AND p.agent_name=? AND a.physical_unit=?`, pair.parent.InvocationID, pair.parent.AgentName, pair.child.PhysicalUnit).Scan(&raw, &planID, &originals[i]); err != nil {
			return err
		}
		if json.Unmarshal([]byte(raw), &requests[i]) != nil {
			return ErrModelPhysicalInvocationConflict
		}
		if err := validateRecognitionLayoutAdjudicationAuthorizationVia(ctx, q, pair.parent, pair.child, planID); err != nil {
			return err
		}
	}
	left, right := requests[0], requests[1]
	if left.CandidateID != right.CandidateID || left.ConflictKind != right.ConflictKind || left.PrimaryPhysicalResultDigest != right.PrimaryPhysicalResultDigest || left.RepairPhysicalResultDigest != right.RepairPhysicalResultDigest || originals[0] != originals[1] || current.CandidateExactSetDigest != source.CandidateExactSetDigest {
		return fmt.Errorf("%w: adjudication replay observations changed", ErrModelPhysicalInvocationConflict)
	}
	return nil
}
