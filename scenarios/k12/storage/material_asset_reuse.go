package k12storage

import (
	"context"
	"encoding/json"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// ReuseMaterialProblemAsset 把当前来源题关联到既有独立验证答案，不制造新的验证或学生作答。
// 来源、输入、资产当前修订及未知回执在同一写事务内核对。
func (s *Store) ReuseMaterialProblemAsset(ctx context.Context, p MaterialPreparation, asset k12.ProblemAssetVersion) error {
	visualResult, err := materialResultWithVisual(p, "{}")
	if err != nil {
		return err
	}
	facts, err := materialFactsForResult(ctx, s.db, p, visualResult)
	if err != nil {
		return err
	}
	identity, err := facts.ExactIdentity(p.OwnerID)
	if err != nil {
		return err
	}
	if asset.OwnerID != p.OwnerID || asset.FactsDigest != identity.FactsDigest {
		return ErrProblemAssetEvidence
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE k12_material_preparations SET updated_at=updated_at WHERE task_id=? AND owner_id=? AND input_digest=? AND state='running'`, p.TaskID, p.OwnerID, p.InputDigest)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrMaterialPreparationFenced
	}
	if err = materialSourceCurrent(ctx, tx, p); err != nil {
		return err
	}
	var unknown bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_material_invocations WHERE task_id=? AND status IN ('sent','outcome_unknown'))`, p.TaskID).Scan(&unknown); err != nil {
		return err
	}
	if unknown {
		return ErrMaterialPreparationUnknown
	}
	if err = validateProblemAssetCurrent(ctx, tx, k12.ProblemAssetAdoption{OwnerID: p.OwnerID, AssetID: asset.AssetID, AssetVersion: asset.Version, AssetRevision: asset.Revision, FactsDigest: identity.FactsDigest}); err != nil {
		return err
	}
	// 重新读取不可变版本，不能信任调用方传入的答案正文。
	stored, err := getProblemAssetVersion(ctx, tx, p.OwnerID, asset.AssetID, asset.Version)
	if err != nil {
		return err
	}
	var answer struct {
		Solution string
		Evidence struct{ Verdict, EvidenceType string }
	}
	if json.Unmarshal([]byte(stored.AnswerResultJSON), &answer) != nil || answer.Solution == "" || answer.Solution != stored.Answer || answer.Evidence.Verdict != "agree" || answer.Evidence.EvidenceType != "numeric_exec" {
		return ErrProblemAssetEvidence
	}
	stored.AnswerResultJSON, err = materialResultWithVisual(p, stored.AnswerResultJSON)
	if err != nil {
		return err
	}
	policy, err := json.Marshal(map[string]any{"kind": "asset_reuse", "asset_id": stored.AssetID, "asset_version": stored.Version, "asset_revision": stored.Revision, "facts_digest": stored.FactsDigest})
	if err != nil {
		return err
	}
	digest := problemAssetRequestDigest([]byte(stored.AnswerResultJSON))
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_material_invocations(invocation_id,task_id,operation,request_digest,execution_kind,status,result_json,result_digest,created_at,updated_at) VALUES(?,?,'reuse_verified',?,'asset_reuse','succeeded',?,?,?,?)`, p.TaskID+":asset-reuse", p.TaskID, p.InputDigest, stored.AnswerResultJSON, digest, nowUnix(), nowUnix())
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE k12_material_preparations SET state='published',policy=?,reason='',result_json=?,result_digest=?,asset_id=?,asset_version=?,updated_at=? WHERE task_id=?`, string(policy), stored.AnswerResultJSON, digest, stored.AssetID, stored.Version, nowUnix(), p.TaskID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
