package k12storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/toolkit/util/idgen"
)

var (
	ErrProblemAssetConflict    = errors.New("problem asset receipt conflict")
	ErrProblemAssetUnavailable = errors.New("problem asset is not available")
	ErrProblemAssetEvidence    = errors.New("problem asset requires matching successful verification")
)

// PublishProblemAsset 只消费用例已完成的题目验证，不调用模型、不猜测缺失事实。
// 发布回执、不可变版本和后续事件在一个事务提交；响应丢失时复用原发布。
func (s *Store) PublishProblemAsset(ctx context.Context, p k12.ProblemAssetPublication) (k12.ProblemAssetVersion, bool, error) {
	return s.publishProblemAsset(ctx, p, "")
}

// PublishAssessedProblemAsset 在发布事务内核对后台任务所依赖的批改仍为当前结果。
func (s *Store) PublishAssessedProblemAsset(ctx context.Context, p k12.ProblemAssetPublication, jobID string) (k12.ProblemAssetVersion, bool, error) {
	if strings.TrimSpace(jobID) == "" {
		return k12.ProblemAssetVersion{}, false, ErrProblemAssetEvidence
	}
	return s.publishProblemAsset(ctx, p, jobID)
}

func (s *Store) publishProblemAsset(ctx context.Context, p k12.ProblemAssetPublication, assessedJobID string) (k12.ProblemAssetVersion, bool, error) {
	identity, err := p.Facts.ExactIdentity(p.OwnerID)
	if err != nil {
		return k12.ProblemAssetVersion{}, false, err
	}
	if strings.TrimSpace(p.PublicationID) == "" || strings.TrimSpace(p.Answer) == "" ||
		p.Verification.FactsDigest != identity.FactsDigest || p.Verification.Policy == "" {
		return k12.ProblemAssetVersion{}, false, ErrProblemAssetEvidence
	}
	if (p.ReplacesVersion == 0) != (p.ExpectedRevision == 0) || p.ReplacesVersion < 0 || p.ExpectedRevision < 0 {
		return k12.ProblemAssetVersion{}, false, ErrProblemAssetConflict
	}

	var answerResult struct {
		Solution   string
		OutOfScope bool
		Evidence   struct {
			Verdict, EvidenceType, SolverOutputDigest, VerificationInputDigest, VerificationRunID string
		}
	}
	if json.Unmarshal([]byte(p.AnswerResultJSON), &answerResult) != nil || answerResult.Solution != p.Answer || answerResult.OutOfScope || answerResult.Evidence.Verdict != "agree" || answerResult.Evidence.EvidenceType != "numeric_exec" {
		return k12.ProblemAssetVersion{}, false, ErrProblemAssetEvidence
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return k12.ProblemAssetVersion{}, false, err
	}
	digest := problemAssetRequestDigest(raw)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return k12.ProblemAssetVersion{}, false, err
	}
	defer tx.Rollback()
	assetID := "asset-" + identity.Key
	now := nowUnix()
	// 首条写语句取得 SQLite 写锁；同题竞争方读取已提交版本，不覆盖答案。
	res, err := tx.ExecContext(ctx, `INSERT INTO k12_problem_assets
		(asset_id,owner_id,normalization_version,facts_digest,current_version,revision,status,created_at,updated_at)
		VALUES(?,?,?,?,1,1,'active',?,?) ON CONFLICT(owner_id,normalization_version,facts_digest) DO NOTHING`,
		assetID, p.OwnerID, identity.NormalizationVersion, identity.FactsDigest, now, now)
	if err != nil {
		return k12.ProblemAssetVersion{}, false, err
	}
	created, err := res.RowsAffected()
	if err != nil {
		return k12.ProblemAssetVersion{}, false, err
	}
	var priorDigest, priorAsset string
	var priorVersion int
	err = tx.QueryRowContext(ctx, `SELECT request_digest,asset_id,asset_version FROM k12_problem_asset_publications
		WHERE owner_id=? AND publication_id=?`, p.OwnerID, p.PublicationID).Scan(&priorDigest, &priorAsset, &priorVersion)
	if err == nil {
		if priorDigest != digest {
			return k12.ProblemAssetVersion{}, false, ErrProblemAssetConflict
		}
		v, readErr := getProblemAssetVersion(ctx, tx, p.OwnerID, priorAsset, priorVersion)
		if readErr != nil {
			return k12.ProblemAssetVersion{}, false, readErr
		}
		return v, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return k12.ProblemAssetVersion{}, false, err
	}
	inv, err := scanGradingItemInvocation(tx.QueryRowContext(ctx, `SELECT `+gradingItemInvocationColumns+
		` FROM k12_grading_item_invocations WHERE agent_name=? AND item_invocation_id=?`,
		p.Verification.AgentName, p.Verification.InvocationID))
	if err != nil {
		return k12.ProblemAssetVersion{}, false, fmt.Errorf("%w: %v", ErrProblemAssetEvidence, err)
	}
	if inv.Status != k12.ModelInvocationSucceeded || inv.InputDigest == "" ||
		inv.InputDigest != p.Verification.InputDigest || inv.ResultDigest != p.Verification.ResultDigest ||
		!json.Valid([]byte(inv.ResultJSON)) {
		return k12.ProblemAssetVersion{}, false, ErrProblemAssetEvidence
	}
	if assessedJobID != "" {
		if inv.JobID != assessedJobID {
			return k12.ProblemAssetVersion{}, false, ErrProblemAssetEvidence
		}
		var current bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_grading_assessment_items
			WHERE agent_name=? AND job_id=? AND problem_id=? AND input_revision=? AND input_digest=?
			AND current_disposition='current' AND solve_invocation_id=?
			AND status IN ('correct','wrong','correct_with_process_issue','blank_solved','untrusted'))`,
			inv.AgentName, inv.JobID, inv.ProblemID, inv.InputRevision, inv.InputDigest, inv.InvocationID).Scan(&current)
		if err != nil {
			return k12.ProblemAssetVersion{}, false, err
		}
		if !current {
			return k12.ProblemAssetVersion{}, false, ErrProblemAssetUnavailable
		}
	}
	var generator *k12.GradingItemInvocation
	switch p.Verification.Kind {
	case k12.ProblemAnswerDeterministic:
		if inv.ExecutionKind != k12.GradingExecutionLocalDeterministic || inv.Operation != k12.GradingItemOperationSolve || inv.ResultJSON != p.AnswerResultJSON {
			return k12.ProblemAssetVersion{}, false, ErrProblemAssetEvidence
		}
	case k12.ProblemAnswerModel:
		if answerResult.Evidence.SolverOutputDigest != p.Verification.SolverOutputDigest ||
			answerResult.Evidence.VerificationInputDigest != p.Verification.VerificationInputDigest ||
			answerResult.Evidence.VerificationRunID != p.Verification.VerificationRunID {
			return k12.ProblemAssetVersion{}, false, ErrProblemAssetEvidence
		}
		generated, err := validateModelAssetProof(ctx, tx, p.Verification, inv)
		if err != nil {
			return k12.ProblemAssetVersion{}, false, err
		}
		var selected struct{ Output string }
		if decodeAssetPhysicalResult(generated, &selected) != nil || selected.Output != p.Answer {
			return k12.ProblemAssetVersion{}, false, ErrProblemAssetEvidence
		}
		generator = &generated
	default:
		return k12.ProblemAssetVersion{}, false, ErrProblemAssetEvidence
	}
	var version, revision int
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT asset_id,current_version,revision,status FROM k12_problem_assets
		WHERE owner_id=? AND normalization_version=? AND facts_digest=?`,
		p.OwnerID, identity.NormalizationVersion, identity.FactsDigest).Scan(&assetID, &version, &revision, &state); err != nil {
		return k12.ProblemAssetVersion{}, false, err
	}
	replacing := p.ReplacesVersion > 0
	if replacing {
		if created != 0 || p.ReplacesVersion != version || p.ExpectedRevision != revision {
			return k12.ProblemAssetVersion{}, false, ErrProblemAssetConflict
		}
		var reusedVerification bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_problem_asset_publications
			WHERE owner_id=? AND asset_id=? AND json_extract(verification_json,'$.agent_name')=?
			AND json_extract(verification_json,'$.invocation_id')=?)`, p.OwnerID, assetID,
			p.Verification.AgentName, p.Verification.InvocationID).Scan(&reusedVerification); err != nil {
			return k12.ProblemAssetVersion{}, false, err
		}
		if reusedVerification {
			return k12.ProblemAssetVersion{}, false, ErrProblemAssetEvidence
		}
		// 新答案已完整核验后才移动活动指针，旧版本和历史采用保持不可变。
		// 已知错误的旧答案只可由这个显式替代命令恢复服务，普通积累不能复活它。
		version++
		revision++
		if _, err = tx.ExecContext(ctx, `UPDATE k12_problem_assets SET current_version=?,revision=?,status='active',updated_at=?
			WHERE owner_id=? AND asset_id=?`, version, revision, now, p.OwnerID, assetID); err != nil {
			return k12.ProblemAssetVersion{}, false, err
		}
	} else if state != "active" {
		return k12.ProblemAssetVersion{}, false, ErrProblemAssetUnavailable
	}
	if created == 1 || replacing {
		facts, err := json.Marshal(p.Facts)
		if err != nil {
			return k12.ProblemAssetVersion{}, false, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO k12_problem_asset_versions
			(owner_id,asset_id,asset_version,published_revision,facts_json,facts_digest,answer,answer_result_json,created_at)
			VALUES(?,?,?,?,?,?,?,?,?)`, p.OwnerID, assetID, version, revision, string(facts), identity.FactsDigest, p.Answer, p.AnswerResultJSON, now)
		if err != nil {
			return k12.ProblemAssetVersion{}, false, err
		}
	}
	v, err := getProblemAssetVersion(ctx, tx, p.OwnerID, assetID, version)
	if err != nil {
		return k12.ProblemAssetVersion{}, false, err
	}
	if v.Answer != p.Answer {
		return k12.ProblemAssetVersion{}, false, ErrProblemAssetConflict
	}
	proof, _ := json.Marshal(p.Verification)
	invocation, _ := json.Marshal(struct {
		Verification k12.GradingItemInvocation
		Generation   *k12.GradingItemInvocation
	}{inv, generator})
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_problem_asset_publications
		(owner_id,publication_id,request_digest,asset_id,asset_version,verification_json,invocation_json,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, p.OwnerID, p.PublicationID, digest, assetID, version, string(proof), string(invocation), now)
	if err != nil {
		return k12.ProblemAssetVersion{}, false, err
	}
	if created == 1 || replacing {
		payload, _ := json.Marshal(map[string]any{"owner_id": p.OwnerID, "asset_id": assetID, "asset_version": version, "revision": revision, "replaces_version": p.ReplacesVersion})
		if _, err = appendOutboxEvent(ctx, tx, OutboxEvent{EventID: fmt.Sprintf("asset-published-%s-v%d", identity.Key, version), AgentName: p.Verification.AgentName,
			AggregateID: assetID, EventType: "k12.problem_asset.published", PayloadVersion: 1, Payload: string(payload)}); err != nil {
			return k12.ProblemAssetVersion{}, false, err
		}
	}
	if err = tx.Commit(); err != nil {
		return k12.ProblemAssetVersion{}, false, err
	}
	if (created == 1 || replacing) && s.notifyOutbox != nil {
		s.notifyOutbox()
	}
	return v, created == 1 || replacing, nil
}

func problemAssetRequestDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func getProblemAssetVersion(ctx context.Context, db dbHandle, owner, asset string, version int) (k12.ProblemAssetVersion, error) {
	var v k12.ProblemAssetVersion
	var facts string
	err := db.QueryRowContext(ctx, `SELECT owner_id,asset_id,asset_version,published_revision,facts_json,facts_digest,answer,answer_result_json,created_at
		FROM k12_problem_asset_versions WHERE owner_id=? AND asset_id=? AND asset_version=?`, owner, asset, version).
		Scan(&v.OwnerID, &v.AssetID, &v.Version, &v.Revision, &facts, &v.FactsDigest, &v.Answer, &v.AnswerResultJSON, &v.CreatedAt)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal([]byte(facts), &v.Facts)
	return v, err
}

// FindExactProblemAsset 只读取当前有效完整事实；相似度候选不能通过此入口采用。
func (s *Store) FindExactProblemAsset(ctx context.Context, owner string, facts k12.ProblemAssetFacts) (k12.ProblemAssetVersion, error) {
	identity, err := facts.ExactIdentity(owner)
	if err != nil {
		return k12.ProblemAssetVersion{}, err
	}
	var asset string
	var version int
	err = s.db.QueryRowContext(ctx, `SELECT asset_id,current_version FROM k12_problem_assets
		WHERE owner_id=? AND normalization_version=? AND facts_digest=? AND status='active'`,
		owner, identity.NormalizationVersion, identity.FactsDigest).Scan(&asset, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return k12.ProblemAssetVersion{}, ErrProblemAssetUnavailable
	}
	if err != nil {
		return k12.ProblemAssetVersion{}, err
	}
	return getProblemAssetVersion(ctx, s.db, owner, asset, version)
}

const assetAdoptionColumns = `adoption_id,owner_id,job_id,problem_id,input_revision,input_digest,asset_id,asset_version,asset_revision,facts_digest,created_at`

func scanProblemAssetAdoption(row rowScanner) (k12.ProblemAssetAdoption, error) {
	var a k12.ProblemAssetAdoption
	err := row.Scan(&a.AdoptionID, &a.OwnerID, &a.JobID, &a.ProblemID, &a.InputRevision, &a.InputDigest, &a.AssetID, &a.AssetVersion, &a.AssetRevision, &a.FactsDigest, &a.CreatedAt)
	return a, err
}

// AdoptProblemAsset 以任务题目输入修订建立独立回执，学生的不同作答不能共享评分。
// 重放返回历史回执不代表资格仍有效；最终提交还需 ValidateProblemAssetAdoption。
func (s *Store) AdoptProblemAsset(ctx context.Context, a k12.ProblemAssetAdoption) (k12.ProblemAssetAdoption, bool, error) {
	if a.OwnerID == "" || a.JobID == "" || a.ProblemID == "" || a.InputRevision < 1 || a.InputDigest == "" ||
		a.AssetID == "" || a.AssetVersion < 1 || a.AssetRevision < 1 || a.FactsDigest == "" {
		return k12.ProblemAssetAdoption{}, false, ErrProblemAssetConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return k12.ProblemAssetAdoption{}, false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE k12_problem_assets SET revision=revision WHERE owner_id=? AND asset_id=?`, a.OwnerID, a.AssetID); err != nil {
		return k12.ProblemAssetAdoption{}, false, err
	}
	prior, err := scanProblemAssetAdoption(tx.QueryRowContext(ctx, `SELECT `+assetAdoptionColumns+` FROM k12_problem_asset_adoptions
		WHERE owner_id=? AND job_id=? AND problem_id=? AND input_revision=?`, a.OwnerID, a.JobID, a.ProblemID, a.InputRevision))
	if err == nil {
		if prior.InputDigest != a.InputDigest || prior.AssetID != a.AssetID || prior.AssetVersion != a.AssetVersion ||
			prior.AssetRevision != a.AssetRevision || prior.FactsDigest != a.FactsDigest {
			return k12.ProblemAssetAdoption{}, false, ErrProblemAssetConflict
		}
		return prior, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return k12.ProblemAssetAdoption{}, false, err
	}
	if err = validateProblemAssetCurrent(ctx, tx, a); err != nil {
		return k12.ProblemAssetAdoption{}, false, err
	}
	a.AdoptionID = "adopt-" + idgen.NanoID()
	a.CreatedAt = nowUnix()
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_problem_asset_adoptions (`+assetAdoptionColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		a.AdoptionID, a.OwnerID, a.JobID, a.ProblemID, a.InputRevision, a.InputDigest, a.AssetID, a.AssetVersion, a.AssetRevision, a.FactsDigest, a.CreatedAt)
	if err != nil {
		return k12.ProblemAssetAdoption{}, false, err
	}
	return a, true, tx.Commit()
}

func validateProblemAssetCurrent(ctx context.Context, db dbHandle, a k12.ProblemAssetAdoption) error {
	var exists bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_problem_assets a JOIN k12_problem_asset_versions v
		ON v.owner_id=a.owner_id AND v.asset_id=a.asset_id AND v.asset_version=a.current_version
		WHERE a.owner_id=? AND a.asset_id=? AND a.status='active' AND a.current_version=? AND a.revision=?
		AND a.facts_digest=? AND v.facts_digest=?)`, a.OwnerID, a.AssetID, a.AssetVersion, a.AssetRevision, a.FactsDigest, a.FactsDigest).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrProblemAssetUnavailable
	}
	return nil
}

func (s *Store) GetProblemAssetAdoption(ctx context.Context, owner, id string) (k12.ProblemAssetAdoption, error) {
	return scanProblemAssetAdoption(s.db.QueryRowContext(ctx, `SELECT `+assetAdoptionColumns+
		` FROM k12_problem_asset_adoptions WHERE owner_id=? AND adoption_id=?`, owner, id))
}

// ValidateProblemAssetAdoption 供提交边界重新核对，不能用曾经命中代替当前资格。
func (s *Store) ValidateProblemAssetAdoption(ctx context.Context, owner, id string) error {
	a, err := s.GetProblemAssetAdoption(ctx, owner, id)
	if err != nil {
		return err
	}
	return validateProblemAssetCurrent(ctx, s.db, a)
}

// ArchiveProblemAsset 只退出未来采用，保留版本、发布与采用历史；旧修订不能覆盖新状态。
func (s *Store) ArchiveProblemAsset(ctx context.Context, owner, asset string, expectedRevision int) error {
	res, err := s.db.ExecContext(ctx, `UPDATE k12_problem_assets SET status='archived',revision=revision+1,updated_at=?
		WHERE owner_id=? AND asset_id=? AND revision=? AND status='active'`, nowUnix(), owner, asset, expectedRevision)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	var replay bool
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_problem_assets
		WHERE owner_id=? AND asset_id=? AND revision=? AND status='archived')`, owner, asset, expectedRevision+1).Scan(&replay)
	if err != nil {
		return err
	}
	if !replay {
		return ErrProblemAssetConflict
	}
	return nil
}

// FindProblemAssetAdoption 恢复同一题目输入的既有采用，不重新选择答案。
func (s *Store) FindProblemAssetAdoption(ctx context.Context, owner, job, problem string, revision int) (k12.ProblemAssetAdoption, error) {
	return scanProblemAssetAdoption(s.db.QueryRowContext(ctx, `SELECT `+assetAdoptionColumns+` FROM k12_problem_asset_adoptions WHERE owner_id=? AND job_id=? AND problem_id=? AND input_revision=?`, owner, job, problem, revision))
}

func (s *Store) GetProblemAssetVersion(ctx context.Context, owner, asset string, version int) (k12.ProblemAssetVersion, error) {
	return getProblemAssetVersion(ctx, s.db, owner, asset, version)
}
