package k12storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// ProblemAssetCorrectionPublicationSource 绑定当前纠正与仍待替代的原资产版本。
type ProblemAssetCorrectionPublicationSource struct {
	Correction       k12.GradingAssessmentCorrection
	OriginalVersion  k12.ProblemAssetVersion
	ExpectedRevision int
	PublicationID    string
	AlreadyPublished bool
}

// GetProblemAssetCorrectionPublicationSource 只读取资格；发布事务仍须重新核对最新纠正及版本。
func (s *Store) GetProblemAssetCorrectionPublicationSource(ctx context.Context, agentName, correctionID string) (ProblemAssetCorrectionPublicationSource, error) {
	var source ProblemAssetCorrectionPublicationSource
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return source, err
	}
	defer tx.Rollback()
	correction, err := readAssessmentCorrection(tx.QueryRowContext(ctx, `SELECT correction_json FROM k12_assessment_corrections
        WHERE agent_name=? AND correction_id=?`, agentName, correctionID))
	if err != nil {
		return source, err
	}
	item := correction.Assessment
	original, err := getGradingAssessmentItemRevisionVia(ctx, tx, agentName, item.JobID, item.ProblemID, item.InputRevision)
	if err != nil {
		return source, err
	}
	latest, err := latestAssessmentCorrection(ctx, tx, original)
	if err != nil {
		return source, err
	}
	answer := original.AnswerSource
	if latest.CorrectionID != correctionID || original.CurrentDisposition != k12.GradingAssessmentDispositionCurrent ||
		correction.Reason != k12.AssessmentCorrectionAnswer || answer == nil || answer.Kind != k12.ProblemAnswerAsset ||
		item.SolveInvocationID == "" || (item.Status != k12.GradingAssessmentCorrect && item.Status != k12.GradingAssessmentWrong &&
		item.Status != k12.GradingAssessmentProcessIssue && item.Status != k12.GradingAssessmentBlankSolved) {
		return source, ErrProblemAssetUnavailable
	}
	adoption, err := scanProblemAssetAdoption(tx.QueryRowContext(ctx, `SELECT `+assetAdoptionColumns+` FROM k12_problem_asset_adoptions
        WHERE adoption_id=? AND job_id=? AND problem_id=? AND input_revision=?`, answer.AdoptionID, item.JobID, item.ProblemID, item.InputRevision))
	if err != nil {
		return source, err
	}
	if adoption.AssetID != answer.AssetID || adoption.AssetVersion != answer.AssetVersion || adoption.InputDigest != item.InputDigest || adoption.FactsDigest != answer.FactsDigest {
		return source, ErrProblemAssetEvidence
	}
	source.Correction = correction
	source.PublicationID = "assessment-correction:" + correctionID
	var publishedAsset string
	err = tx.QueryRowContext(ctx, `SELECT asset_id FROM k12_problem_asset_publications WHERE owner_id=? AND publication_id=?`, adoption.OwnerID, source.PublicationID).Scan(&publishedAsset)
	if err == nil {
		if publishedAsset != adoption.AssetID {
			return source, ErrProblemAssetConflict
		}
		source.AlreadyPublished = true
		return source, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return source, err
	}
	var currentVersion int
	var state string
	err = tx.QueryRowContext(ctx, `SELECT current_version,revision,status FROM k12_problem_assets WHERE owner_id=? AND asset_id=?`, adoption.OwnerID, adoption.AssetID).
		Scan(&currentVersion, &source.ExpectedRevision, &state)
	if err != nil {
		return source, err
	}
	if currentVersion != adoption.AssetVersion || state != "archived" {
		return source, ErrProblemAssetUnavailable
	}
	source.OriginalVersion, err = getProblemAssetVersion(ctx, tx, adoption.OwnerID, adoption.AssetID, adoption.AssetVersion)
	if err != nil {
		return source, err
	}
	return source, tx.Commit()
}

// PublishCorrectedProblemAsset 在既有发布事务中同时核对纠正身份，不能恢复被更新替代的旧结论。
func (s *Store) PublishCorrectedProblemAsset(ctx context.Context, publication k12.ProblemAssetPublication, jobID, correctionID string) (k12.ProblemAssetVersion, bool, error) {
	if jobID == "" || correctionID == "" || publication.PublicationID != "assessment-correction:"+correctionID || publication.ReplacesVersion < 1 {
		return k12.ProblemAssetVersion{}, false, ErrProblemAssetEvidence
	}
	return s.publishProblemAsset(ctx, publication, jobID, correctionID)
}
