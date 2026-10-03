package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// ProblemSourceArchiveRecognitionAdjudication 保留独立裁决的原始事实与采用回执。
type ProblemSourceArchiveRecognitionAdjudication struct {
	PlanID                     string `json:"plan_id"`
	CandidateID                string `json:"candidate_id"`
	AuthorizationID            string `json:"authorization_id"`
	AuthorizationDigest        string `json:"authorization_digest"`
	PhysicalUnit               string `json:"physical_unit"`
	RequestJSON                string `json:"request_json"`
	OriginalCandidateJSON      string `json:"original_candidate_json"`
	OriginalCandidateDigest    string `json:"original_candidate_digest"`
	AuthorizedAt               int64  `json:"authorized_at"`
	SourcePhysicalInvocationID string `json:"source_physical_invocation_id"`
	SettlementJSON             string `json:"settlement_json"`
	SettlementDigest           string `json:"settlement_digest"`
	ResultDigest               string `json:"result_digest"`
	SettledAt                  int64  `json:"settled_at"`
}

func loadProblemSourceArchiveRecognitionAdjudications(ctx context.Context, q dbQueryer, planID string) ([]ProblemSourceArchiveRecognitionAdjudication, error) {
	rows, err := q.QueryContext(ctx, `SELECT a.plan_id,a.candidate_id,a.authorization_id,a.authorization_digest,a.physical_unit,a.request_json,a.original_candidate_json,a.original_candidate_digest,a.created_at,s.source_physical_invocation_id,s.settlement_json,s.settlement_digest,s.result_digest,s.created_at
	 FROM k12_recognition_layout_adjudication_authorizations a
	 JOIN k12_recognition_layout_candidates c ON c.plan_id=a.plan_id AND c.candidate_id=a.candidate_id
	 LEFT JOIN k12_recognition_layout_adjudication_settlements s ON s.authorization_id=a.authorization_id
	 WHERE a.plan_id=? ORDER BY c.ordinal`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProblemSourceArchiveRecognitionAdjudication
	for rows.Next() {
		var v ProblemSourceArchiveRecognitionAdjudication
		if err := rows.Scan(&v.PlanID, &v.CandidateID, &v.AuthorizationID, &v.AuthorizationDigest, &v.PhysicalUnit, &v.RequestJSON, &v.OriginalCandidateJSON, &v.OriginalCandidateDigest, &v.AuthorizedAt, &v.SourcePhysicalInvocationID, &v.SettlementJSON, &v.SettlementDigest, &v.ResultDigest, &v.SettledAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func validateProblemSourceArchiveRecognitionAdjudications(aggregate ProblemSourceArchiveRecognitionLayoutV2, parent k12.ModelInvocation, plan k12.RecognitionLayoutPlanV2, physical map[string]k12.ModelPhysicalInvocation) (map[string]recognitionAdjudicationOverlay, []string, error) {
	overlays := map[string]recognitionAdjudicationOverlay{}
	var ids []string
	invalid := errors.New("recognition adjudication archive evidence drifted")
	if len(aggregate.Adjudications) > 0 && !plan.SourceAdjudication {
		return nil, nil, invalid
	}
	for _, row := range aggregate.Adjudications {
		ordinal := 0
		for i, target := range plan.Targets {
			if target.TargetID == row.CandidateID {
				ordinal = i + 1
			}
		}
		unit, err := k12.RecognitionLayoutAdjudicationUnitV2(ordinal)
		if _, exists := overlays[row.CandidateID]; exists || err != nil || row.PlanID != aggregate.Plan.PlanID || row.PhysicalUnit != string(unit) {
			return nil, nil, invalid
		}
		var request k12.RecognitionLayoutAdjudicationRequestV2
		if json.Unmarshal([]byte(row.RequestJSON), &request) != nil || request.CandidateID != row.CandidateID || request.PlanDigest != plan.AuthorizedPlanDigest || validateRecognitionAdjudicationConflict(row.OriginalCandidateJSON, request.ConflictKind) != nil {
			return nil, nil, invalid
		}
		originalOK, primaryOK, repairOK := false, false, false
		for _, original := range aggregate.CandidateResults {
			if original.CandidateID == row.CandidateID {
				originalOK = original.ResultJSON == row.OriginalCandidateJSON && original.ResultDigest == row.OriginalCandidateDigest && original.SourcePhysicalInvocationID == request.RepairPhysicalInvocationID && original.SourcePhysicalResultDigest == request.RepairPhysicalResultDigest
			}
		}
		for _, prior := range aggregate.RepairAuthorizations {
			if prior.CandidateID == row.CandidateID {
				primaryOK = prior.SourceBatchPhysicalInvocationID == request.PrimaryPhysicalInvocationID && prior.SourceBatchResultDigest == request.PrimaryPhysicalResultDigest
			}
		}
		for _, prior := range aggregate.RepairSettlements {
			if prior.CandidateID == row.CandidateID {
				repairOK = prior.Classification == "valid" && prior.SourcePhysicalInvocationID == request.RepairPhysicalInvocationID && prior.SourcePhysicalResultDigest == request.RepairPhysicalResultDigest
			}
		}
		if !originalOK || !primaryOK || !repairOK {
			return nil, nil, invalid
		}
		want, canonicalRequest := recognitionAdjudicationAuthorization(request, recognitionAdjudicationAuthority{parent: parent, originalDigest: row.OriginalCandidateDigest, unit: unit})
		if canonicalRequest != row.RequestJSON || want.AuthorizationID != row.AuthorizationID || want.AuthorizationDigest != row.AuthorizationDigest {
			return nil, nil, invalid
		}
		var settlement k12.RecognitionLayoutAdjudicationSettlementV2
		if json.Unmarshal([]byte(row.SettlementJSON), &settlement) != nil {
			return nil, nil, invalid
		}
		projection, canonical, err := recognitionAdjudicationSettlementProjection(parent.InvocationID, settlement)
		if err != nil || canonical != row.SettlementJSON || projection.SettlementDigest != row.SettlementDigest || settlement.AuthorizationID != row.AuthorizationID || settlement.AuthorizationDigest != row.AuthorizationDigest || settlement.PlanDigest != plan.AuthorizedPlanDigest || settlement.CandidateID != row.CandidateID || settlement.SourcePhysicalInvocationID != row.SourcePhysicalInvocationID || settlement.SourcePhysicalUnit != unit {
			return nil, nil, invalid
		}
		if (projection.FrozenResult == nil && row.ResultDigest != "") || (projection.FrozenResult != nil && projection.FrozenResult.ResultDigest != row.ResultDigest) {
			return nil, nil, invalid
		}
		child, ok := physical[row.SourcePhysicalInvocationID]
		exact, err := k12.RecognitionLayoutTargetExactSetDigestV2([]string{row.CandidateID})
		if !ok || err != nil || child.ParentInvocationID != parent.InvocationID || child.AgentName != parent.AgentName || child.JobID != parent.JobID || child.Stage != k12.GradingStageRecognizing || child.Status != k12.ModelInvocationSucceeded || child.Attempt != 1 || child.PhysicalUnit != unit || child.PlanDigest != plan.AuthorizedPlanDigest || child.CandidateExactSetDigest != exact || child.ResultDigest != settlement.SourcePhysicalResultDigest {
			return nil, nil, invalid
		}
		overlays[row.CandidateID] = recognitionAdjudicationOverlay{settlement, projection, row.OriginalCandidateJSON}
	}
	for _, target := range plan.Targets {
		if overlay, ok := overlays[target.TargetID]; ok {
			ids = append(ids, overlay.settlement.SourcePhysicalInvocationID)
		}
	}
	return overlays, ids, nil
}

func insertProblemSourceArchiveRecognitionAdjudications(ctx context.Context, tx *sql.Tx, rows []ProblemSourceArchiveRecognitionAdjudication) error {
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO k12_recognition_layout_adjudication_authorizations(plan_id,candidate_id,authorization_id,authorization_digest,physical_unit,request_json,original_candidate_json,original_candidate_digest,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, row.PlanID, row.CandidateID, row.AuthorizationID, row.AuthorizationDigest, row.PhysicalUnit, row.RequestJSON, row.OriginalCandidateJSON, row.OriginalCandidateDigest, row.AuthorizedAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO k12_recognition_layout_adjudication_settlements(authorization_id,source_physical_invocation_id,settlement_json,settlement_digest,result_digest,created_at) VALUES(?,?,?,?,?,?)`, row.AuthorizationID, row.SourcePhysicalInvocationID, row.SettlementJSON, row.SettlementDigest, row.ResultDigest, row.SettledAt); err != nil {
			return err
		}
	}
	return nil
}
