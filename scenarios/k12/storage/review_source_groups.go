package k12storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

var ErrReviewSourceGroupStale = errors.New("reviewed source group no longer matches current sources")

var ErrReviewSourceGroupConflict = errors.New("reviewed source group overlaps an active group")

const reviewSourceGroupColumns = `group_id,agent_name,canonical_record_id,canonical_statement,
    canonical_statement_digest,evidence_kind,evidence_note,source_digests_json,created_at`

const reviewSourceGroupMemberColumns = `record_id,question_digest,job_id,problem_id,attempt_id,
    confirmed_version,input_revision,input_digest,result_digest,page_digest,
    source_number_path_json,parent_source_number_path_json`

// PutReviewSourceGroup 独立核对原题、已发布批改输入和页来源后，原子追加同题关联。
// 幂等重放保留首次冻结的说明和时间，不更新错题、学习状态、答案或旧打印文件。
func (s *Store) PutReviewSourceGroup(ctx context.Context, group k12.ReviewSourceGroup) (k12.ReviewSourceGroup, bool, error) {
	group, err := group.Normalized()
	if err != nil {
		return k12.ReviewSourceGroup{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return k12.ReviewSourceGroup{}, false, fmt.Errorf("k12storage: begin reviewed source group: %w", err)
	}
	defer tx.Rollback()
	if err := validateReviewSourceGroupCurrent(ctx, tx, group); err != nil {
		return k12.ReviewSourceGroup{}, false, err
	}
	stored, err := readReviewSourceGroup(ctx, tx, group.AgentName, group.GroupID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return k12.ReviewSourceGroup{}, false, err
		}
		return stored, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return k12.ReviewSourceGroup{}, false, err
	}
	memberIDs := make([]string, 0, len(group.Members))
	for _, member := range group.Members {
		memberIDs = append(memberIDs, member.RecordID)
	}
	overlaps, err := reviewSourceGroupIDs(ctx, tx, group.AgentName, memberIDs)
	if err != nil {
		return k12.ReviewSourceGroup{}, false, err
	}
	for _, id := range overlaps {
		prior, err := readReviewSourceGroup(ctx, tx, group.AgentName, id)
		if errors.Is(err, k12.ErrInvalidReviewSourceGroup) {
			continue
		}
		if err != nil {
			return k12.ReviewSourceGroup{}, false, err
		}
		if err := validateReviewSourceGroupCurrent(ctx, tx, prior); err != nil {
			if errors.Is(err, ErrReviewSourceGroupStale) {
				continue
			}
			return k12.ReviewSourceGroup{}, false, err
		}
		return k12.ReviewSourceGroup{}, false, fmt.Errorf("%w: group %s", ErrReviewSourceGroupConflict, prior.GroupID)
	}
	digestsJSON, err := json.Marshal(group.Evidence.SourceDigests)
	if err != nil {
		return k12.ReviewSourceGroup{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO k12_review_source_groups (`+reviewSourceGroupColumns+`)
        VALUES(?,?,?,?,?,?,?,?,?)`, group.GroupID, group.AgentName, group.CanonicalRecordID,
		group.CanonicalStatement, group.CanonicalStatementDigest, group.Evidence.Kind, group.Evidence.Note,
		string(digestsJSON), group.CreatedAt); err != nil {
		return k12.ReviewSourceGroup{}, false, fmt.Errorf("k12storage: append reviewed source group: %w", err)
	}
	for _, member := range group.Members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO k12_review_source_group_members
            (group_id,agent_name,`+reviewSourceGroupMemberColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			group.GroupID, group.AgentName, member.RecordID, member.QuestionDigest, member.JobID,
			member.ProblemID, member.AttemptID, member.ConfirmedVersion, member.InputRevision, member.InputDigest,
			member.ResultDigest, member.PageDigest, reviewSourcePathJSON(member.SourceNumberPath),
			reviewSourcePathJSON(member.ParentSourceNumberPath)); err != nil {
			return k12.ReviewSourceGroup{}, false, fmt.Errorf("k12storage: append reviewed source member: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return k12.ReviewSourceGroup{}, false, fmt.Errorf("k12storage: commit reviewed source group: %w", err)
	}
	return group, true, nil
}

// ListReviewSourceGroups 只返回仍与完整已审核版本一致的关联。
// 任一成员失效则整组不参与本次归并；原账本仍保留，学习状态及到期时间不影响身份。
func (s *Store) ListReviewSourceGroups(ctx context.Context, agentName string, recordIDs []string) ([]k12.ReviewSourceGroup, error) {
	if len(recordIDs) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("k12storage: begin reviewed source snapshot: %w", err)
	}
	defer tx.Rollback()
	groupIDs, err := reviewSourceGroupIDs(ctx, tx, agentName, recordIDs)
	if err != nil {
		return nil, err
	}
	var groups []k12.ReviewSourceGroup
	for _, id := range groupIDs {
		group, err := readReviewSourceGroup(ctx, tx, agentName, id)
		if err != nil {
			// 旧格式或不完整账本不能阻断原题出卷，也不能成为有效归并依据。
			if errors.Is(err, k12.ErrInvalidReviewSourceGroup) {
				continue
			}
			return nil, err
		}
		if err := validateReviewSourceGroupCurrent(ctx, tx, group); err != nil {
			if errors.Is(err, ErrReviewSourceGroupStale) {
				continue
			}
			return nil, err
		}
		groups = append(groups, group)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("k12storage: close reviewed source snapshot: %w", err)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].CreatedAt != groups[j].CreatedAt {
			return groups[i].CreatedAt < groups[j].CreatedAt
		}
		return groups[i].GroupID < groups[j].GroupID
	})
	return groups, nil
}

func reviewSourceGroupIDs(ctx context.Context, tx *sql.Tx, agentName string, recordIDs []string) ([]string, error) {
	groupIDs := map[string]bool{}
	const batchSize = 200
	for start := 0; start < len(recordIDs); start += batchSize {
		end := min(start+batchSize, len(recordIDs))
		args := []any{agentName}
		for _, id := range recordIDs[start:end] {
			args = append(args, id)
		}
		placeholders := strings.TrimSuffix(strings.Repeat("?,", end-start), ",")
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT group_id FROM k12_review_source_group_members
            WHERE agent_name=? AND record_id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, fmt.Errorf("k12storage: find reviewed source groups: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			groupIDs[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	ids := make([]string, 0, len(groupIDs))
	for id := range groupIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func readReviewSourceGroup(ctx context.Context, tx *sql.Tx, agentName, groupID string) (k12.ReviewSourceGroup, error) {
	var group k12.ReviewSourceGroup
	var digestsJSON string
	err := tx.QueryRowContext(ctx, `SELECT `+reviewSourceGroupColumns+` FROM k12_review_source_groups
        WHERE agent_name=? AND group_id=?`, agentName, groupID).Scan(&group.GroupID, &group.AgentName,
		&group.CanonicalRecordID, &group.CanonicalStatement, &group.CanonicalStatementDigest,
		&group.Evidence.Kind, &group.Evidence.Note, &digestsJSON, &group.CreatedAt)
	if err != nil {
		return k12.ReviewSourceGroup{}, err
	}
	if err := json.Unmarshal([]byte(digestsJSON), &group.Evidence.SourceDigests); err != nil {
		return k12.ReviewSourceGroup{}, fmt.Errorf("%w: evidence encoding", k12.ErrInvalidReviewSourceGroup)
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+reviewSourceGroupMemberColumns+` FROM k12_review_source_group_members
        WHERE agent_name=? AND group_id=? ORDER BY record_id`, agentName, groupID)
	if err != nil {
		return k12.ReviewSourceGroup{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var member k12.ReviewSourceGroupMember
		var sourcePath, parentPath string
		if err := rows.Scan(&member.RecordID, &member.QuestionDigest, &member.JobID, &member.ProblemID,
			&member.AttemptID, &member.ConfirmedVersion, &member.InputRevision, &member.InputDigest,
			&member.ResultDigest, &member.PageDigest, &sourcePath, &parentPath); err != nil {
			return k12.ReviewSourceGroup{}, err
		}
		if json.Unmarshal([]byte(sourcePath), &member.SourceNumberPath) != nil ||
			json.Unmarshal([]byte(parentPath), &member.ParentSourceNumberPath) != nil {
			return k12.ReviewSourceGroup{}, fmt.Errorf("%w: member numbering encoding", k12.ErrInvalidReviewSourceGroup)
		}
		group.Members = append(group.Members, member)
	}
	if err := rows.Err(); err != nil {
		return k12.ReviewSourceGroup{}, err
	}
	return group.Normalized()
}

func validateReviewSourceGroupCurrent(ctx context.Context, tx *sql.Tx, group k12.ReviewSourceGroup) error {
	for _, member := range group.Members {
		if err := validateReviewSourceGroupMember(ctx, tx, group.AgentName, member); err != nil {
			return err
		}
	}
	return nil
}

func validateReviewSourceGroupMember(ctx context.Context, tx *sql.Tx, agentName string, member k12.ReviewSourceGroupMember) error {
	var question, resultJSON, pageID, pageDigest string
	var parentID, parentPageID, parentKind, parentPathJSON string
	var parentVersion int
	err := tx.QueryRowContext(ctx, `SELECT m.question,a.result_json,p.page_asset_id,p.content_digest,
        COALESCE(pp.problem_id,''),COALESCE(pp.page_asset_id,''),COALESCE(pp.problem_kind,''),
        COALESCE(pp.canonical_version,0),COALESCE(pp.source_number_path_json,'[]')
        FROM k12_mistakes m
        JOIN k12_grading_assessment_items a ON a.agent_name=m.agent_name AND a.projection_record_id=m.record_id
        JOIN k12_page_assets p ON p.agent_name=a.agent_name AND p.page_asset_id=
            json_extract(CASE WHEN json_valid(a.result_json) THEN a.result_json ELSE '{}' END,'$.Recognized.page_asset_id')
        LEFT JOIN k12_problems pp ON pp.agent_name=a.agent_name AND pp.problem_id=
            json_extract(CASE WHEN json_valid(a.result_json) THEN a.result_json ELSE '{}' END,'$.Recognized.parent_problem_id')
        WHERE m.agent_name=? AND m.record_id=? AND a.job_id=? AND a.problem_id=? AND a.attempt_id=?
            AND a.confirmed_version=? AND a.input_revision=? AND a.input_digest=? AND a.result_digest=?
            AND a.input_revision=a.published_revision AND a.current_disposition='current' AND a.projection_status='committed'`,
		agentName, member.RecordID, member.JobID, member.ProblemID, member.AttemptID, member.ConfirmedVersion,
		member.InputRevision, member.InputDigest, member.ResultDigest).Scan(&question, &resultJSON, &pageID, &pageDigest,
		&parentID, &parentPageID, &parentKind, &parentVersion, &parentPathJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: record %s", ErrReviewSourceGroupStale, member.RecordID)
	}
	if err != nil {
		return fmt.Errorf("k12storage: verify reviewed source member: %w", err)
	}
	var payload struct {
		Recognized struct {
			ProblemID        string   `json:"problem_id"`
			AttemptID        string   `json:"attempt_id"`
			ParentProblemID  string   `json:"parent_problem_id"`
			PageAssetID      string   `json:"page_asset_id"`
			InputDigest      string   `json:"input_digest"`
			ConfirmedVersion int      `json:"confirmed_version"`
			CanonicalVersion int      `json:"canonical_version"`
			SourceNumberPath []string `json:"source_number_path"`
		}
	}
	if json.Unmarshal([]byte(resultJSON), &payload) != nil {
		return fmt.Errorf("%w: record %s result encoding", ErrReviewSourceGroupStale, member.RecordID)
	}
	q := payload.Recognized
	resultHash := sha256.New()
	_, _ = resultHash.Write([]byte{0})
	_, _ = resultHash.Write([]byte(resultJSON))
	if k12.ReviewSourceTextDigest(question) != member.QuestionDigest || pageDigest != member.PageDigest ||
		"sha256:"+hex.EncodeToString(resultHash.Sum(nil)) != member.ResultDigest || q.ProblemID != member.ProblemID ||
		q.AttemptID != member.AttemptID || q.InputDigest != member.InputDigest || q.ConfirmedVersion != member.ConfirmedVersion ||
		q.CanonicalVersion < 1 || q.PageAssetID != pageID || !sameReviewSourcePath(q.SourceNumberPath, member.SourceNumberPath) {
		return fmt.Errorf("%w: record %s identity or content changed", ErrReviewSourceGroupStale, member.RecordID)
	}
	var parentPath []string
	if parentID != "" && parentID == q.ParentProblemID && parentPageID == pageID && parentKind == "compound_parent" && parentVersion > 0 {
		if err := json.Unmarshal([]byte(parentPathJSON), &parentPath); err != nil {
			return fmt.Errorf("%w: record %s parent numbering", ErrReviewSourceGroupStale, member.RecordID)
		}
	}
	if !sameReviewSourcePath(parentPath, member.ParentSourceNumberPath) {
		return fmt.Errorf("%w: record %s parent numbering changed", ErrReviewSourceGroupStale, member.RecordID)
	}
	return nil
}

func reviewSourcePathJSON(path []string) string {
	if len(path) == 0 {
		return "[]"
	}
	raw, _ := json.Marshal(path)
	return string(raw)
}

func sameReviewSourcePath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
