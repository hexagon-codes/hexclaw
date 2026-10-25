package k12storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// ReviewSourceArchive 保存关联及当时实际提交的来源，不把恢复解释成重新批改。
type ReviewSourceArchive struct {
	Groups    []k12.ReviewSourceGroup `json:"groups"`
	Snapshots []ReviewSourceSnapshot  `json:"snapshots"`
	Pages     []PageAssetMetadata     `json:"pages,omitempty"`
}

// ReviewSourceSnapshot 的有效标记来自原持久状态，导入后仍复核当前题目与输入。
type ReviewSourceSnapshot struct {
	Member k12.ReviewSourceGroupMember `json:"member"`
	Source ReviewPaperSource           `json:"source"`
	Active bool                        `json:"active"`
}

func sourceResultDigest(raw string) string {
	sum := sha256.Sum256(append([]byte{0}, []byte(raw)...))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func reviewSnapshotKey(member k12.ReviewSourceGroupMember) string {
	raw, _ := json.Marshal(member)
	return string(raw)
}

func validateReviewSnapshot(agent string, snapshot ReviewSourceSnapshot) error {
	m, s := snapshot.Member, snapshot.Source
	if s.AgentName != agent || s.RecordID != m.RecordID || s.JobID != m.JobID || s.ProblemID != m.ProblemID ||
		s.AttemptID != m.AttemptID || s.InputRevision != m.InputRevision || s.ConfirmedVersion != m.ConfirmedVersion ||
		s.InputDigest != m.InputDigest || s.ResultDigest != m.ResultDigest || s.PageDigest != m.PageDigest ||
		sourceResultDigest(s.ResultJSON) != s.ResultDigest || snapshot.Active && s.PublishedRevision != s.InputRevision {
		return fmt.Errorf("review source archive identity mismatch")
	}
	// ResultJSON沿原PhotoGradeItem的snake_case字段读取。
	var envelope struct {
		Recognized struct {
			ProblemID        string   `json:"problem_id"`
			AttemptID        string   `json:"attempt_id"`
			PageAssetID      string   `json:"page_asset_id"`
			InputDigest      string   `json:"input_digest"`
			ConfirmedVersion int      `json:"confirmed_version"`
			CanonicalVersion int      `json:"canonical_version"`
			ParentProblemID  string   `json:"parent_problem_id"`
			SourceNumberPath []string `json:"source_number_path"`
		} `json:"Recognized"`
	}
	if json.Unmarshal([]byte(s.ResultJSON), &envelope) != nil {
		return fmt.Errorf("review source archive result is invalid")
	}
	q := envelope.Recognized
	if q.ProblemID != m.ProblemID || q.AttemptID != m.AttemptID || q.PageAssetID != s.PageAssetID ||
		q.InputDigest != m.InputDigest || q.ConfirmedVersion != m.ConfirmedVersion || q.CanonicalVersion < 1 ||
		!sameReviewSourcePath(q.SourceNumberPath, m.SourceNumberPath) {
		return fmt.Errorf("review source archive frozen input mismatch")
	}
	var parentPath []string
	if s.ParentProblemID != "" && s.ParentProblemID == q.ParentProblemID && s.ParentPageAssetID == s.PageAssetID && s.ParentKind == "compound_parent" && s.ParentCanonicalVersion > 0 {
		if json.Unmarshal([]byte(s.ParentSourceNumberPathJSON), &parentPath) != nil {
			return fmt.Errorf("review source archive parent numbering is invalid")
		}
	}
	if !sameReviewSourcePath(parentPath, m.ParentSourceNumberPath) {
		return fmt.Errorf("review source archive parent numbering mismatch")
	}
	return nil
}

// ValidateReviewSourceArchive 核对版本化载荷内部身份；当前资格由读取时另行判断。
func ValidateReviewSourceArchive(agent string, archive *ReviewSourceArchive) error {
	if archive == nil {
		return nil
	}
	members, groups := map[string]bool{}, map[string]bool{}
	for _, group := range archive.Groups {
		normalized, err := group.Normalized()
		if err != nil || normalized.AgentName != agent || normalized.GroupID != group.GroupID ||
			normalized.CanonicalStatementDigest != group.CanonicalStatementDigest || groups[normalized.GroupID] {
			return fmt.Errorf("invalid archived review source group")
		}
		groups[normalized.GroupID] = true
		for _, member := range normalized.Members {
			members[reviewSnapshotKey(member)] = true
		}
	}
	seen := map[string]bool{}
	pages := map[string]bool{}
	for _, page := range archive.Pages {
		if page.AgentName != agent || pages[page.PageAssetID] {
			return fmt.Errorf("invalid archived review page identity")
		}
		if _, err := normalizePageAssetIdentity(page); err != nil {
			return err
		}
		pages[page.PageAssetID] = true
	}
	for _, snapshot := range archive.Snapshots {
		key := reviewSnapshotKey(snapshot.Member)
		if !members[key] || seen[key] {
			return fmt.Errorf("invalid archived review source snapshot set")
		}
		if err := validateReviewSnapshot(agent, snapshot); err != nil {
			return err
		}
		if !pages[snapshot.Source.PageAssetID] {
			return fmt.Errorf("archived review page metadata is missing")
		}
		seen[key] = true
	}
	return nil
}

// ExportReviewSourceArchive 保留全部关联；来源已不可读的历史组不会被标成有效。
func (s *Store) ExportReviewSourceArchive(ctx context.Context, agent string) (*ReviewSourceArchive, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	archive, err := s.ExportReviewSourceArchiveTx(ctx, tx, agent)
	if err != nil {
		return nil, err
	}
	return archive, tx.Commit()
}

// ExportReviewSourceArchiveTx 与已有恢复快照共享同一读写事务。
func (s *Store) ExportReviewSourceArchiveTx(ctx context.Context, tx *sql.Tx, agent string) (*ReviewSourceArchive, error) {
	rows, err := tx.QueryContext(ctx, `SELECT group_id FROM k12_review_source_groups WHERE agent_name=? ORDER BY created_at,group_id`, agent)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	archive := &ReviewSourceArchive{}
	seen := map[string]bool{}
	for _, id := range ids {
		group, err := readReviewSourceGroup(ctx, tx, agent, id)
		if err != nil {
			return nil, err
		}
		archive.Groups = append(archive.Groups, group)
		for _, member := range group.Members {
			key := reviewSnapshotKey(member)
			if seen[key] {
				continue
			}
			seen[key] = true
			var disposition string
			source, err := scanReviewPaperSource(tx.QueryRowContext(ctx, `SELECT `+reviewPaperSourceColumns+`,a.current_disposition `+reviewPaperSourceFrom+`
WHERE a.agent_name=? AND a.projection_record_id=? AND a.job_id=? AND a.problem_id=? AND a.attempt_id=?
AND a.input_revision=? AND a.confirmed_version=? AND a.input_digest=? AND a.result_digest=?`,
				agent, member.RecordID, member.JobID, member.ProblemID, member.AttemptID, member.InputRevision,
				member.ConfirmedVersion, member.InputDigest, member.ResultDigest), &disposition)
			if errors.Is(err, sql.ErrNoRows) {
				var payload, digest string
				err = tx.QueryRowContext(ctx, `SELECT payload,payload_digest FROM k12_review_source_archive WHERE agent_name=? AND record_id=?`, agent, member.RecordID).Scan(&payload, &digest)
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					return nil, err
				}
				var snapshot ReviewSourceSnapshot
				if k12.ReviewSourceTextDigest(payload) != digest || json.Unmarshal([]byte(payload), &snapshot) != nil {
					return nil, fmt.Errorf("restored review source archive is invalid")
				}
				if reflect.DeepEqual(snapshot.Member, member) {
					archive.Snapshots = append(archive.Snapshots, snapshot)
				}
				continue
			}
			if err != nil {
				return nil, err
			}
			archive.Snapshots = append(archive.Snapshots, ReviewSourceSnapshot{Member: member, Source: source,
				Active: disposition == "current" && source.InputRevision == source.PublishedRevision})
		}
	}
	pageIDs := map[string]bool{}
	for _, snapshot := range archive.Snapshots {
		pageIDs[snapshot.Source.PageAssetID] = true
		if snapshot.Source.ParentPageAssetID != "" {
			pageIDs[snapshot.Source.ParentPageAssetID] = true
		}
	}
	ordered := make([]string, 0, len(pageIDs))
	for id := range pageIDs {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		page, err := scanPageAsset(tx.QueryRowContext(ctx, `SELECT `+pageAssetColumns+` FROM k12_page_assets WHERE agent_name=? AND page_asset_id=?`, agent, id))
		if err != nil {
			return nil, err
		}
		archive.Pages = append(archive.Pages, page)
	}
	if err := ValidateReviewSourceArchive(agent, archive); err != nil {
		return nil, err
	}
	return archive, nil
}

func appendReviewSourceGroup(ctx context.Context, tx *sql.Tx, group k12.ReviewSourceGroup) error {
	digests, err := json.Marshal(group.Evidence.SourceDigests)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO k12_review_source_groups (`+reviewSourceGroupColumns+`) VALUES(?,?,?,?,?,?,?,?,?)`,
		group.GroupID, group.AgentName, group.CanonicalRecordID, group.CanonicalStatement, group.CanonicalStatementDigest,
		group.Evidence.Kind, group.Evidence.Note, string(digests), group.CreatedAt); err != nil {
		return err
	}
	for _, m := range group.Members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO k12_review_source_group_members (group_id,agent_name,`+reviewSourceGroupMemberColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			group.GroupID, group.AgentName, m.RecordID, m.QuestionDigest, m.JobID, m.ProblemID, m.AttemptID,
			m.ConfirmedVersion, m.InputRevision, m.InputDigest, m.ResultDigest, m.PageDigest,
			reviewSourcePathJSON(m.SourceNumberPath), reviewSourcePathJSON(m.ParentSourceNumberPath)); err != nil {
			return err
		}
	}
	return nil
}

// ImportReviewSourceArchiveTx 复用原恢复事务，只保存冻结关系，不创建执行/投递任务。
func (s *Store) ImportReviewSourceArchiveTx(ctx context.Context, tx *sql.Tx, agent, archiveID string, archive *ReviewSourceArchive) error {
	if archive == nil {
		return nil
	}
	if err := ValidateReviewSourceArchive(agent, archive); err != nil {
		return err
	}
	pageArchive := ProblemSourceArchiveV6{}
	for _, page := range archive.Pages {
		pageArchive.PageAssets = append(pageArchive.PageAssets, archivePageAsset(page))
	}
	if err := s.insertProblemSourceArchiveV6(ctx, tx, pageArchive); err != nil {
		return err
	}
	selected := map[string]ReviewSourceSnapshot{}
	eligible := map[string]bool{}
	for _, snapshot := range archive.Snapshots {
		current, err := reviewSnapshotCurrent(ctx, tx, snapshot)
		if err != nil {
			return err
		}
		previous, exists := selected[snapshot.Member.RecordID]
		if !exists || current && !eligible[snapshot.Member.RecordID] || current == eligible[snapshot.Member.RecordID] && snapshot.Source.InputRevision > previous.Source.InputRevision {
			selected[snapshot.Member.RecordID] = snapshot
			eligible[snapshot.Member.RecordID] = current
		}
	}
	// 显式恢复替换本归档关联的派生来源选择，不能沿用上一份归档的有效标记。
	cleared := map[string]bool{}
	for _, group := range archive.Groups {
		for _, member := range group.Members {
			if cleared[member.RecordID] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM k12_review_source_archive WHERE agent_name=? AND record_id=?`, agent, member.RecordID); err != nil {
				return err
			}
			cleared[member.RecordID] = true
		}
	}
	ids := make([]string, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		raw, err := json.Marshal(selected[id])
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO k12_review_source_archive(agent_name,record_id,archive_id,payload,payload_digest) VALUES(?,?,?,?,?)
ON CONFLICT(agent_name,record_id) DO UPDATE SET archive_id=excluded.archive_id,payload=excluded.payload,payload_digest=excluded.payload_digest`,
			agent, id, archiveID, string(raw), k12.ReviewSourceTextDigest(string(raw))); err != nil {
			return err
		}
	}
	for _, group := range archive.Groups {
		if _, err := readReviewSourceGroup(ctx, tx, agent, group.GroupID); err == nil {
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := appendReviewSourceGroup(ctx, tx, group); err != nil {
			return err
		}
	}
	return nil
}

// restoredReviewSource 不覆盖任何已有批改账本，包括已失效或已被替代的来源。
func restoredReviewSource(ctx context.Context, db dbHandle, agent, recordID string) (ReviewSourceSnapshot, bool, error) {
	var snapshot ReviewSourceSnapshot
	var hasLive int
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_grading_assessment_items WHERE agent_name=? AND projection_record_id=?)`, agent, recordID).Scan(&hasLive); err != nil || hasLive != 0 {
		return snapshot, false, err
	}
	var raw, digest string
	err := db.QueryRowContext(ctx, `SELECT payload,payload_digest FROM k12_review_source_archive WHERE agent_name=? AND record_id=?`, agent, recordID).Scan(&raw, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, false, nil
	}
	if err != nil {
		return snapshot, false, err
	}
	if digest != k12.ReviewSourceTextDigest(raw) || json.Unmarshal([]byte(raw), &snapshot) != nil {
		return snapshot, false, fmt.Errorf("restored review source digest mismatch")
	}
	if err := validateReviewSnapshot(agent, snapshot); err != nil {
		return snapshot, false, err
	}
	if !snapshot.Active || snapshot.Member.RecordID != recordID {
		return snapshot, false, nil
	}
	valid, err := reviewSnapshotCurrent(ctx, db, snapshot)
	return snapshot, valid, err
}

func reviewSnapshotCurrent(ctx context.Context, db dbHandle, snapshot ReviewSourceSnapshot) (bool, error) {
	if !snapshot.Active {
		return false, nil
	}
	var frozen struct {
		Recognized struct {
			ParentProblemID string `json:"parent_problem_id"`
		} `json:"Recognized"`
	}
	if err := json.Unmarshal([]byte(snapshot.Source.ResultJSON), &frozen); err != nil {
		return false, err
	}
	var question, pageID, pageDigest, inputDigest, parentID, parentPage, parentKind, parentPathJSON string
	var version, parentVersion int
	err := db.QueryRowContext(ctx, `SELECT m.question,p.page_asset_id,pa.content_digest,a.input_digest,a.confirmed_version,
COALESCE(pp.problem_id,''),COALESCE(pp.page_asset_id,''),COALESCE(pp.problem_kind,''),COALESCE(pp.canonical_version,0),COALESCE(pp.source_number_path_json,'[]')
FROM k12_mistakes m JOIN k12_problems p ON p.agent_name=m.agent_name AND p.problem_id=?
JOIN k12_attempts a ON a.agent_name=p.agent_name AND a.problem_id=p.problem_id AND a.attempt_id=?
JOIN k12_page_assets pa ON pa.agent_name=p.agent_name AND pa.page_asset_id=p.page_asset_id
LEFT JOIN k12_problems pp ON pp.agent_name=p.agent_name AND pp.problem_id=?
WHERE m.agent_name=? AND m.record_id=?`, snapshot.Member.ProblemID, snapshot.Member.AttemptID, frozen.Recognized.ParentProblemID, snapshot.Source.AgentName, snapshot.Member.RecordID).
		Scan(&question, &pageID, &pageDigest, &inputDigest, &version, &parentID, &parentPage, &parentKind, &parentVersion, &parentPathJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	valid := k12.ReviewSourceTextDigest(question) == snapshot.Member.QuestionDigest && pageID == snapshot.Source.PageAssetID &&
		pageDigest == snapshot.Member.PageDigest && inputDigest == snapshot.Member.InputDigest && version == snapshot.Member.ConfirmedVersion
	var parentPath []string
	if parentID != "" && parentID == frozen.Recognized.ParentProblemID && parentPage == pageID && parentKind == "compound_parent" && parentVersion > 0 {
		if err := json.Unmarshal([]byte(parentPathJSON), &parentPath); err != nil {
			return false, err
		}
	}
	valid = valid && sameReviewSourcePath(parentPath, snapshot.Member.ParentSourceNumberPath)
	return valid, nil
}

// ReplaceReviewSourceArchiveTx 仅供显式恢复回退，替换已核对快照的派生关联状态。
func (s *Store) ReplaceReviewSourceArchiveTx(ctx context.Context, tx *sql.Tx, agent, archiveID string, archive *ReviewSourceArchive) error {
	if err := ValidateReviewSourceArchive(agent, archive); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM k12_review_source_groups WHERE agent_name=?`, agent); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM k12_review_source_archive WHERE agent_name=?`, agent); err != nil {
		return err
	}
	return s.ImportReviewSourceArchiveTx(ctx, tx, agent, archiveID, archive)
}
