package k12storage

import (
	"context"
	"fmt"
	"strings"
)

// ReviewPaperSource 仅携带错题已经提交的冻结来源，供只读卷面投影核对身份。
// ResultJSON 的摘要和其中题目身份由调用方验证，不以最新可变题干代替旧输入。
type ReviewPaperSource struct {
	AgentName, RecordID, JobID, ProblemID, AttemptID   string
	InputRevision, PublishedRevision, ConfirmedVersion int
	InputDigest, ResultJSON, ResultDigest              string
	PageAssetID, PageDigest                            string
	// 父题仅提供首版同页印刷编号凭据，不用其可变题面替换冻结内容。
	ParentProblemID, ParentPageAssetID, ParentKind string
	ParentCanonicalVersion                         int
	ParentSourceNumberPathJSON, ParentStemRaw      string
}

const reviewPaperSourceColumns = `a.agent_name,a.projection_record_id,a.job_id,
            a.problem_id,a.attempt_id,a.input_revision,a.published_revision,a.confirmed_version,a.input_digest,
            a.result_json,a.result_digest,COALESCE(p.page_asset_id,''),COALESCE(p.content_digest,''),
            COALESCE(pp.problem_id,''),COALESCE(pp.page_asset_id,''),COALESCE(pp.problem_kind,''),
            COALESCE(pp.canonical_version,0),COALESCE(pp.source_number_path_json,''),COALESCE(pp.stem_raw,'')`

const reviewPaperSourceFrom = `FROM k12_grading_assessment_items a
            LEFT JOIN k12_page_assets p ON p.agent_name=a.agent_name AND p.page_asset_id=
                json_extract(CASE WHEN json_valid(a.result_json) THEN a.result_json ELSE '{}' END,'$.Recognized.page_asset_id')
            LEFT JOIN k12_problems pp ON pp.agent_name=a.agent_name AND pp.problem_id=
                json_extract(CASE WHEN json_valid(a.result_json) THEN a.result_json ELSE '{}' END,'$.Recognized.parent_problem_id')`

// ListReviewPaperSources 按实例和错题集合批量读取来源，不写回错题或批改回执。
// 缺少关联或页资产的旧记录仍可用原题出卷，只是不取得同页去重凭据。
func (s *Store) ListReviewPaperSources(ctx context.Context, agentName string, recordIDs []string) ([]ReviewPaperSource, error) {
	var sources []ReviewPaperSource
	const batchSize = 200
	for start := 0; start < len(recordIDs); start += batchSize {
		end := min(start+batchSize, len(recordIDs))
		args := []any{agentName}
		for _, id := range recordIDs[start:end] {
			args = append(args, id)
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", end-start), ",")
		rows, err := s.db.QueryContext(ctx, `SELECT `+reviewPaperSourceColumns+` `+reviewPaperSourceFrom+`
            WHERE a.agent_name=? AND a.projection_record_id IN (`+placeholders+`)
                AND a.current_disposition='current' AND a.projection_status='committed'
            ORDER BY a.projection_record_id,a.created_at,a.job_id,a.problem_id`, args...)
		if err != nil {
			return nil, fmt.Errorf("k12storage: list review paper sources: %w", err)
		}
		for rows.Next() {
			source, err := scanReviewPaperSource(rows)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("k12storage: scan review paper source: %w", err)
			}
			sources = append(sources, source)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("k12storage: read review paper sources: %w", err)
		}
	}
	seen := make(map[string]bool)
	for _, source := range sources {
		seen[source.RecordID] = true
	}
	for start := 0; start < len(recordIDs); start += batchSize {
		end := min(start+batchSize, len(recordIDs))
		args := []any{agentName}
		missing := false
		for _, id := range recordIDs[start:end] {
			args = append(args, id)
			missing = missing || !seen[id]
		}
		if !missing {
			continue
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", end-start), ",")
		rows, err := s.db.QueryContext(ctx, `SELECT record_id FROM k12_review_source_archive WHERE agent_name=? AND record_id IN (`+placeholders+`) ORDER BY record_id`, args...)
		if err != nil {
			return nil, err
		}
		var candidates []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[id] {
				candidates = append(candidates, id)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for _, id := range candidates {
			snapshot, found, err := restoredReviewSource(ctx, s.db, agentName, id)
			if err != nil {
				return nil, err
			}
			if found {
				sources = append(sources, snapshot.Source)
				seen[id] = true
			}
		}
	}
	return sources, nil
}

func scanReviewPaperSource(row rowScanner, extra ...any) (ReviewPaperSource, error) {
	var source ReviewPaperSource
	args := []any{&source.AgentName, &source.RecordID, &source.JobID, &source.ProblemID,
		&source.AttemptID, &source.InputRevision, &source.PublishedRevision, &source.ConfirmedVersion, &source.InputDigest,
		&source.ResultJSON, &source.ResultDigest, &source.PageAssetID, &source.PageDigest,
		&source.ParentProblemID, &source.ParentPageAssetID, &source.ParentKind, &source.ParentCanonicalVersion,
		&source.ParentSourceNumberPathJSON, &source.ParentStemRaw}
	args = append(args, extra...)
	err := row.Scan(args...)
	return source, err
}
