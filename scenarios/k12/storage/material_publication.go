package k12storage

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"strings"
)

// SaveMaterialLocalVerification 登记真实本地确定性验算，不伪装为 Provider 调用。
func (s *Store) SaveMaterialLocalVerification(ctx context.Context, p MaterialPreparation, result string) error {
	var r struct {
		Solution string
		Evidence struct{ Verdict, EvidenceType string }
	}
	if json.Unmarshal([]byte(result), &r) != nil || strings.TrimSpace(r.Solution) == "" || r.Evidence.Verdict != "agree" || r.Evidence.EvidenceType != "numeric_exec" {
		return ErrProblemAssetEvidence
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE k12_material_preparations SET state='verified',result_json=?,result_digest=?,updated_at=? WHERE task_id=? AND state='running' AND input_digest=?`, result, problemAssetRequestDigest([]byte(result)), nowUnix(), p.TaskID, p.InputDigest)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrMaterialPreparationFenced
	}
	if err = materialSourceCurrent(ctx, tx, p); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_material_invocations(invocation_id,task_id,operation,request_digest,execution_kind,status,result_json,result_digest,created_at,updated_at) VALUES(?,?,'solve_verify',?,'local_deterministic','succeeded',?,?,?,?)`, p.TaskID+":local", p.TaskID, p.InputDigest, result, problemAssetRequestDigest([]byte(result)), nowUnix(), nowUnix())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// PublishPreparedMaterialAsset 核对独立资料回执和当前来源；不创建批改或学情记录。
func (s *Store) PublishPreparedMaterialAsset(ctx context.Context, taskID string) (k12.ProblemAssetVersion, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return k12.ProblemAssetVersion{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE k12_material_preparations SET updated_at=updated_at WHERE task_id=?`, taskID); err != nil {
		return k12.ProblemAssetVersion{}, err
	}
	p, err := scanMaterial(tx.QueryRowContext(ctx, `SELECT `+materialColumns+` FROM k12_material_preparations WHERE task_id=?`, taskID))
	if err != nil {
		return k12.ProblemAssetVersion{}, err
	}
	if err = materialSourceCurrent(ctx, tx, p); err != nil {
		return k12.ProblemAssetVersion{}, err
	}
	identity, err := p.Candidate.Facts.ExactIdentity(p.OwnerID)
	if err != nil {
		return k12.ProblemAssetVersion{}, err
	}
	var answer struct {
		Solution string
		Evidence struct{ Verdict, EvidenceType string }
	}
	if p.State != "verified" && p.State != "published" {
		return k12.ProblemAssetVersion{}, ErrProblemAssetEvidence
	}
	if json.Unmarshal([]byte(p.ResultJSON), &answer) != nil || answer.Solution == "" || answer.Evidence.Verdict != "agree" || answer.Evidence.EvidenceType != "numeric_exec" {
		return k12.ProblemAssetVersion{}, ErrProblemAssetEvidence
	}
	var proof bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_material_invocations WHERE task_id=? AND operation='solve_verify' AND execution_kind='local_deterministic' AND status='succeeded' AND request_digest=? AND result_json=? AND result_digest=?)`, p.TaskID, p.InputDigest, p.ResultJSON, p.ResultDigest).Scan(&proof); err != nil {
		return k12.ProblemAssetVersion{}, err
	}
	invocationJSON := fmt.Sprintf(`{"source_kind":"material_preparation","invocation_id":%q}`, p.TaskID+":local")
	if !proof {
		selected, verifyErr := materialModelVerifiedAnswer(ctx, tx, p, p.ResultJSON)
		if verifyErr != nil || selected != answer.Solution {
			return k12.ProblemAssetVersion{}, ErrProblemAssetEvidence
		}
		invocationJSON = materialModelProofJSON(p)
	}
	assetID := "asset-" + identity.Key
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_problem_assets(asset_id,owner_id,normalization_version,facts_digest,current_version,revision,status,created_at,updated_at) VALUES(?,?,?,?,1,1,'active',?,?) ON CONFLICT(owner_id,normalization_version,facts_digest) DO NOTHING`, assetID, p.OwnerID, identity.NormalizationVersion, identity.FactsDigest, nowUnix(), nowUnix())
	if err != nil {
		return k12.ProblemAssetVersion{}, err
	}
	var version, revision int
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT current_version,revision,status FROM k12_problem_assets WHERE owner_id=? AND asset_id=?`, p.OwnerID, assetID).Scan(&version, &revision, &state); err != nil {
		return k12.ProblemAssetVersion{}, err
	}
	if state != "active" {
		return k12.ProblemAssetVersion{}, ErrProblemAssetUnavailable
	}
	facts, _ := json.Marshal(p.Candidate.Facts)
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_problem_asset_versions(owner_id,asset_id,asset_version,published_revision,facts_json,facts_digest,answer,answer_result_json,created_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(owner_id,asset_id,asset_version) DO NOTHING`, p.OwnerID, assetID, version, revision, string(facts), identity.FactsDigest, answer.Solution, p.ResultJSON, nowUnix())
	if err != nil {
		return k12.ProblemAssetVersion{}, err
	}
	v, err := getProblemAssetVersion(ctx, tx, p.OwnerID, assetID, version)
	if err != nil {
		return v, err
	}
	if v.Answer != answer.Solution {
		return v, ErrProblemAssetConflict
	}
	verification, _ := json.Marshal(map[string]any{"source_kind": "material_preparation", "task_id": p.TaskID, "document_id": p.DocumentID, "source_revision": p.SourceRevision, "candidate_id": p.Candidate.ID, "input_digest": p.InputDigest, "result_digest": p.ResultDigest, "policy": p.Policy})
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_problem_asset_publications(owner_id,publication_id,request_digest,asset_id,asset_version,verification_json,invocation_json,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(owner_id,publication_id) DO NOTHING`, p.OwnerID, "material:"+p.TaskID, p.InputDigest, assetID, version, string(verification), invocationJSON, nowUnix())
	if err != nil {
		return v, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE k12_material_preparations SET state='published',asset_id=?,asset_version=?,updated_at=? WHERE task_id=?`, assetID, version, nowUnix(), p.TaskID)
	if err != nil {
		return v, err
	}
	return v, tx.Commit()
}
