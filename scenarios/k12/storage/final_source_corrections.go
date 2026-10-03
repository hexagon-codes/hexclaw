package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// FinalSourceCorrectionRequest 冻结终稿、原题来源及一次独立复核的模型。
type FinalSourceCorrectionRequest struct {
	CorrectionID             string                   `json:"correction_id"`
	AgentName                string                   `json:"agent_name"`
	JobID                    string                   `json:"job_id"`
	DispatchID               string                   `json:"dispatch_id"`
	OwnerScope               string                   `json:"owner_scope"`
	SubmissionID             string                   `json:"submission_id"`
	ProblemID                string                   `json:"problem_id"`
	InputRevision            int                      `json:"input_revision"`
	StructureVersion         int                      `json:"structure_version"`
	InputDigest              string                   `json:"input_digest"`
	OriginalArtifactID       string                   `json:"original_artifact_id"`
	OriginalArtifactDigest   string                   `json:"original_artifact_digest"`
	SourceDigest             string                   `json:"source_digest"`
	Model                    k12.GradingModelSnapshot `json:"model"`
	RecoveryOf               string                   `json:"recovery_of,omitempty"`
	RecoveryResponseDigest   string                   `json:"recovery_response_digest,omitempty"`
	AcceptDuplicateExecution bool                     `json:"accept_duplicate_execution,omitempty"`
}

type FinalSourceCorrection struct {
	Request        FinalSourceCorrectionRequest `json:"request"`
	RequestDigest  string                       `json:"request_digest"`
	Status         string                       `json:"status"`
	ResponseJSON   string                       `json:"response_json,omitempty"`
	ResponseDigest string                       `json:"response_digest,omitempty"`
	Failure        string                       `json:"failure,omitempty"`
}

func readFinalSourceCorrection(row rowScanner) (FinalSourceCorrection, error) {
	var v FinalSourceCorrection
	var raw string
	err := row.Scan(&raw, &v.RequestDigest, &v.Status, &v.ResponseJSON, &v.ResponseDigest, &v.Failure)
	if errors.Is(err, sql.ErrNoRows) {
		return v, records.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal([]byte(raw), &v.Request)
	if err == nil && v.ResponseJSON != "" && problemAssetRequestDigest([]byte(v.ResponseJSON)) != v.ResponseDigest {
		err = ErrGradingFinalArtifactConflict
	}
	return v, err
}

const finalSourceCorrectionColumns = `request_json,request_digest,status,response_json,response_digest,failure`

func (s *Store) GetFinalSourceCorrection(ctx context.Context, agent, id string) (FinalSourceCorrection, error) {
	return readFinalSourceCorrection(s.db.QueryRowContext(ctx, `SELECT `+finalSourceCorrectionColumns+` FROM k12_final_source_corrections WHERE agent_name=? AND correction_id=?`, agent, id))
}

func (s *Store) PrepareFinalSourceCorrection(ctx context.Context, r FinalSourceCorrectionRequest) (FinalSourceCorrection, bool, error) {
	var zero FinalSourceCorrection
	raw, err := json.Marshal(r)
	if err != nil {
		return zero, false, err
	}
	digest := strings.TrimPrefix(problemAssetRequestDigest(raw), "sha256:")
	if r.CorrectionID == "" || r.OwnerScope == "" || r.InputRevision < 1 || r.SourceDigest == "" {
		return zero, false, ErrProblemSourceActionConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE k12_grading_jobs SET updated_at=updated_at WHERE agent_name=? AND record_id=?`, r.AgentName, r.JobID); err != nil {
		return zero, false, err
	}
	prior, err := readFinalSourceCorrection(tx.QueryRowContext(ctx, `SELECT `+finalSourceCorrectionColumns+` FROM k12_final_source_corrections WHERE agent_name=? AND correction_id=?`, r.AgentName, r.CorrectionID))
	if err == nil {
		if prior.RequestDigest != digest {
			return zero, false, records.ErrVersionConflict
		}
		return prior, true, tx.Commit()
	}
	if !errors.Is(err, records.ErrNotFound) {
		return zero, false, err
	}
	current, err := getGradingFinalArtifactByJobVia(ctx, tx, r.AgentName, r.JobID)
	if err != nil {
		return zero, false, err
	}
	if current.ArtifactID != r.OriginalArtifactID || current.ArtifactDigest != r.OriginalArtifactDigest || current.OriginalSourceDigest != r.SourceDigest {
		return zero, false, records.ErrVersionConflict
	}
	recovery := r.RecoveryOf != "" || r.RecoveryResponseDigest != "" || r.AcceptDuplicateExecution
	if recovery {
		if r.RecoveryOf == "" || r.RecoveryResponseDigest == "" || !r.AcceptDuplicateExecution {
			return zero, false, ErrProblemSourceActionConflict
		}
		previous, err := readFinalSourceCorrection(tx.QueryRowContext(ctx, `SELECT `+finalSourceCorrectionColumns+` FROM k12_final_source_corrections WHERE agent_name=? AND correction_id=?`, r.AgentName, r.RecoveryOf))
		if err != nil {
			return zero, false, err
		}
		if previous.Status != "outcome_unknown" || previous.ResponseDigest != r.RecoveryResponseDigest {
			return zero, false, records.ErrVersionConflict
		}
		// 新授权必须对应同一冻结请求；旧未知回执保持原样，不能分叉成多个恢复。
		expected := previous.Request
		expected.CorrectionID = r.CorrectionID
		expected.RecoveryOf = r.RecoveryOf
		expected.RecoveryResponseDigest = r.RecoveryResponseDigest
		expected.AcceptDuplicateExecution = true
		expectedRaw, err := json.Marshal(expected)
		if err != nil || string(expectedRaw) != string(raw) {
			return zero, false, records.ErrVersionConflict
		}
		var children int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM k12_final_source_corrections WHERE agent_name=? AND json_extract(request_json,'$.recovery_of')=?`, r.AgentName, r.RecoveryOf).Scan(&children); err != nil {
			return zero, false, err
		}
		if children != 0 {
			return zero, false, fmt.Errorf("source recovery already authorized")
		}
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_final_source_corrections WHERE agent_name=? AND job_id=? AND problem_id=? AND (status IN ('prepared','sent','succeeded') OR (status='outcome_unknown' AND ?=0))`, r.AgentName, r.JobID, r.ProblemID, recovery).Scan(&pending); err != nil {
		return zero, false, err
	}
	if pending != 0 {
		return zero, false, fmt.Errorf("source correction requires reconciliation")
	}
	old, err := getGradingAssessmentItemVia(ctx, tx, r.AgentName, r.JobID, r.ProblemID)
	if err != nil {
		return zero, false, err
	}
	if old.InputRevision != r.InputRevision || old.InputDigest != r.InputDigest || old.StructureVersion != r.StructureVersion {
		return zero, false, records.ErrVersionConflict
	}
	now := nowUnix()
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_final_source_corrections(correction_id,agent_name,job_id,problem_id,request_digest,request_json,status,created_at,updated_at) VALUES(?,?,?,?,?,?,'prepared',?,?)`, r.CorrectionID, r.AgentName, r.JobID, r.ProblemID, digest, string(raw), now, now)
	if err != nil {
		return zero, false, err
	}
	return FinalSourceCorrection{Request: r, RequestDigest: digest, Status: "prepared"}, false, tx.Commit()
}

// ClaimFinalSourceCorrection 先持久标记发送，崩溃后不自动重发。
func (s *Store) ClaimFinalSourceCorrection(ctx context.Context, agent, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE k12_final_source_corrections SET status='sent',updated_at=? WHERE agent_name=? AND correction_id=? AND status='prepared'`, nowUnix(), agent, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ReconcileFinalSourceCorrectionNotSent 只核实发送前区域或冻结策略的已知拒绝，保留原响应及错误。
func (s *Store) ReconcileFinalSourceCorrectionNotSent(ctx context.Context, agent, id, payload, digest string) error {
	command, err := s.GetFinalSourceCorrection(ctx, agent, id)
	if err != nil {
		return err
	}
	policyErr := k12.ValidateModelInvocationRequestPolicy(k12.GradingStageRecognizing, command.Request.Model, k12.ApprovedRecognizingRequestPolicy())
	if command.Failure != "original source region unavailable" && (policyErr == nil || command.Failure != policyErr.Error()) {
		return records.ErrVersionConflict
	}
	res, err := s.db.ExecContext(ctx, `UPDATE k12_final_source_corrections SET status='failed',updated_at=? WHERE agent_name=? AND correction_id=? AND status='outcome_unknown' AND failure=? AND response_json=? AND response_digest=?`, nowUnix(), agent, id, command.Failure, payload, digest)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return records.ErrVersionConflict
	}
	return nil
}

func (s *Store) SettleFinalSourceCorrection(ctx context.Context, agent, id, status, payload, failure string) error {
	if status != "succeeded" && status != "outcome_unknown" && status != "failed" {
		return records.ErrIllegalTransition
	}
	digest := ""
	if payload != "" {
		if !json.Valid([]byte(payload)) {
			return ErrProblemSourceActionConflict
		}
		digest = problemAssetRequestDigest([]byte(payload))
	}
	if status == "succeeded" && payload == "" {
		return ErrProblemSourceActionConflict
	}
	res, err := s.db.ExecContext(ctx, `UPDATE k12_final_source_corrections SET status=?,response_json=?,response_digest=?,failure=?,updated_at=? WHERE agent_name=? AND correction_id=? AND status='sent'`, status, payload, digest, failure, nowUnix(), agent, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return records.ErrVersionConflict
	}
	return err
}

func FinalSourceInputDigest(command FinalSourceCorrection, problem string, revision int) string {
	return problemSourceInputDigest(command.RequestDigest, problem, revision)
}

func finalRevisionMissing(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table: k12_grading_final_")
}

func readFinalRevision(row rowScanner) (k12.GradingFinalArtifact, error) {
	var a k12.GradingFinalArtifact
	var raw, owner string
	if err := row.Scan(&raw, &owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) || finalRevisionMissing(err) {
			return a, records.ErrNotFound
		}
		return a, err
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return a, err
	}
	a.AnnotatedAssetOwnerScope = owner
	return a, a.Validate()
}
func getFinalRevisionByID(ctx context.Context, db dbQueryer, agent, id string) (k12.GradingFinalArtifact, error) {
	return readFinalRevision(db.QueryRowContext(ctx, `SELECT artifact_json,asset_owner_scope FROM k12_grading_final_revisions WHERE agent_name=? AND artifact_id=?`, agent, id))
}
func getFinalRevisionHead(ctx context.Context, db dbQueryer, agent, job string) (k12.GradingFinalArtifact, error) {
	return readFinalRevision(db.QueryRowContext(ctx, `SELECT r.artifact_json,r.asset_owner_scope FROM k12_grading_final_heads h JOIN k12_grading_final_revisions r ON r.artifact_id=h.artifact_id AND r.agent_name=h.agent_name WHERE h.agent_name=? AND h.job_id=?`, agent, job))
}

// ListFinalSourceQuestions 返回已发布纠正的来源；在途复核不改变当前题目。
func (s *Store) ListFinalSourceQuestions(ctx context.Context, agent, submission string) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.source_question_json FROM k12_grading_final_revisions r JOIN k12_grading_jobs j ON j.agent_name=r.agent_name AND j.record_id=r.job_id WHERE r.agent_name=? AND j.submission_id=? ORDER BY r.created_at,r.artifact_id`, agent, submission)
	if finalRevisionMissing(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(raw))
	}
	return out, rows.Err()
}

// CommitFinalBlankSourceCorrection 原子追加空白来源、无作答评估与完整终稿，不产生新的学生作答。
func (s *Store) CommitFinalBlankSourceCorrection(ctx context.Context, id string, a k12.GradingFinalArtifact, source json.RawMessage, item k12.GradingAssessmentItem) (k12.GradingFinalArtifact, error) {
	if err := a.Validate(); err != nil {
		return a, err
	}
	if !a.HasAnnotatedAsset() || item.Status != k12.GradingAssessmentUnanswered || !json.Valid(source) {
		return a, ErrGradingFinalArtifactConflict
	}
	if err := item.ValidateTerminalParentGuideReference(); err != nil {
		return a, err
	}
	if _, err := s.openGradingFinalAnnotatedAsset(ctx, a); err != nil {
		return a, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return a, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE k12_grading_jobs SET updated_at=updated_at WHERE agent_name=? AND record_id=?`, a.AgentName, a.JobID); err != nil {
		return a, err
	}
	command, err := readFinalSourceCorrection(tx.QueryRowContext(ctx, `SELECT `+finalSourceCorrectionColumns+` FROM k12_final_source_corrections WHERE agent_name=? AND correction_id=?`, a.AgentName, id))
	if err != nil {
		return a, err
	}
	r := command.Request
	if command.Status == "completed" {
		return readFinalRevision(tx.QueryRowContext(ctx, `SELECT artifact_json,asset_owner_scope FROM k12_grading_final_revisions WHERE correction_id=? AND agent_name=?`, id, a.AgentName))
	}
	if command.Status != "succeeded" {
		return a, records.ErrIllegalTransition
	}
	var response struct {
		Verified bool                         `json:"verified"`
		Question struct{ AnswerState string } `json:"question"`
	}
	if json.Unmarshal([]byte(command.ResponseJSON), &response) != nil || !response.Verified || response.Question.AnswerState != "blank" {
		return a, ErrGradingFinalArtifactConflict
	}
	current, err := getGradingFinalArtifactByJobVia(ctx, tx, a.AgentName, a.JobID)
	if err != nil {
		return a, err
	}
	if a.JobID != r.JobID || current.ArtifactID != r.OriginalArtifactID || current.ArtifactDigest != r.OriginalArtifactDigest || a.OriginalSourceDigest != r.SourceDigest || a.AnnotatedAssetOwnerScope != r.OwnerScope || a.ArtifactID == current.ArtifactID {
		return a, records.ErrVersionConflict
	}
	old, err := getGradingAssessmentItemVia(ctx, tx, r.AgentName, r.JobID, r.ProblemID)
	if err != nil {
		return a, err
	}
	if item.AgentName != r.AgentName || item.JobID != r.JobID || item.StructureVersion != old.StructureVersion || item.CurrentDisposition != k12.GradingAssessmentDispositionCurrent || item.ConfirmedVersion != item.InputRevision || old.InputRevision != r.InputRevision || old.InputDigest != r.InputDigest || item.ProblemID != r.ProblemID || item.AttemptID != old.AttemptID || item.InputRevision != old.InputRevision+1 || item.PublishedRevision != old.PublishedRevision+1 || item.InputDigest != FinalSourceInputDigest(command, r.ProblemID, item.InputRevision) {
		return a, records.ErrVersionConflict
	}
	// 相同任务的其他题必须逐项沿用原终稿摘要。
	var before, after []string
	if json.Unmarshal([]byte(current.OrderedCurrentDigestsJSON), &before) != nil || json.Unmarshal([]byte(a.OrderedCurrentDigestsJSON), &after) != nil || len(before) != len(after) || a.TotalCount != current.TotalCount || a.PublishedCount != current.PublishedCount || a.SkippedCount != current.SkippedCount {
		return a, ErrGradingFinalArtifactConflict
	}
	changed := 0
	for i := range before {
		if before[i] != after[i] {
			if before[i] != old.ResultDigest || after[i] != item.ResultDigest {
				return a, ErrGradingFinalArtifactConflict
			}
			changed++
		}
	}
	if changed != 1 {
		return a, ErrGradingFinalArtifactConflict
	}
	now := nowUnix()
	scope := problemSourceActionScope{AgentName: r.AgentName, JobID: r.JobID, SubmissionID: r.SubmissionID, StructureVersion: r.StructureVersion}
	head, err := currentProblemInputRevisionHead(ctx, tx, scope, r.ProblemID, r.InputRevision, now)
	if err != nil {
		return a, err
	}
	var q struct {
		Raw           string `json:"raw_transcription"`
		Canonical     string `json:"canonical_markdown"`
		InputDigest   string `json:"input_digest"`
		AnswerState   string
		StudentAnswer string
	}
	if json.Unmarshal(source, &q) != nil || q.InputDigest != item.InputDigest || q.AnswerState != "blank" || q.StudentAnswer != "" {
		return a, ErrGradingFinalArtifactConflict
	}
	// 本次只修正作答归属，不改变已识别题干。
	if q.Canonical != head.QuestionCanonicalMarkdown {
		return a, ErrGradingFinalArtifactConflict
	}
	head.AnswerRaw, head.AnswerCanonicalMarkdown, head.AnswerBBoxJSON = "", "", ""
	affected, _ := json.Marshal([]string{r.ProblemID})
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_problem_source_action_receipts(command_receipt_id,owner_scope,agent_name,dispatch_id,job_id,problem_id,idempotency_key,request_digest,action,structure_version,expected_input_revision,result_input_revision,request_json,affected_problem_ids_json,response_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?, 'correct_text',?,?,?,?,?,?,?,?)`, id, r.OwnerScope, r.AgentName, r.DispatchID, r.JobID, r.ProblemID, id, command.RequestDigest, r.StructureVersion, r.InputRevision, item.InputRevision, command.ResponseJSON, string(affected), `{"status":"completed"}`, now, now)
	if err != nil {
		return a, err
	}
	if err = appendProblemInputRevision(ctx, tx, scope, r.ProblemID, r.InputRevision, item.InputRevision, id, command.RequestDigest, head, now); err != nil {
		return a, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_grading_assessment_items (`+gradingAssessmentItemColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,NULL,'{}',NULL,?,0,?,?,?)`, item.AgentName, item.JobID, item.ProblemID, item.AttemptID, item.ConfirmedVersion, item.InputRevision, item.PublishedRevision, item.CurrentDisposition, item.StructureVersion, item.InputDigest, item.Status, item.ResultJSON, item.ResultDigest, item.ProjectionRecordID, item.ProjectionStatus, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		return a, err
	}
	if err = s.commitAssessmentSourceCorrectionTx(ctx, tx, item); err != nil {
		return a, err
	}
	correction := k12.GradingAssessmentCorrection{CorrectionID: id, OriginalResultDigest: old.ResultDigest, Reason: "source", Assessment: item, Revision: 1, CreatedAt: now}
	var lastRaw string
	lastErr := tx.QueryRowContext(ctx, `SELECT correction_json FROM k12_assessment_corrections WHERE agent_name=? AND job_id=? AND problem_id=? AND input_revision=? ORDER BY correction_revision DESC LIMIT 1`, r.AgentName, r.JobID, r.ProblemID, old.InputRevision).Scan(&lastRaw)
	if lastErr == nil {
		var last k12.GradingAssessmentCorrection
		if err = json.Unmarshal([]byte(lastRaw), &last); err != nil {
			return a, err
		}
		correction.Revision = last.Revision + 1
		correction.PreviousCorrectionID = last.CorrectionID
	} else if !errors.Is(lastErr, sql.ErrNoRows) {
		return a, lastErr
	}
	correctionJSON, _ := json.Marshal(correction)
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_assessment_corrections(correction_id,agent_name,job_id,problem_id,input_revision,correction_revision,previous_correction_id,original_result_digest,request_digest,correction_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, r.AgentName, r.JobID, r.ProblemID, old.InputRevision, correction.Revision, correction.PreviousCorrectionID, old.ResultDigest, command.RequestDigest, string(correctionJSON), now)
	if err != nil {
		return a, err
	}
	payload, _ := json.Marshal(AssessmentCorrectedPayload{CorrectionID: id, AgentName: r.AgentName, JobID: r.JobID, ProblemID: r.ProblemID, InputRevision: old.InputRevision, Revision: correction.Revision, OriginalEventID: gradingAssessmentEventID(old, "mistake_recorded")})
	if _, err = appendOutboxEvent(ctx, tx, OutboxEvent{EventID: "assessment-corrected:" + id, AgentName: r.AgentName, AggregateID: r.JobID + ":" + r.ProblemID, EventType: EventAssessmentCorrected, PayloadVersion: 1, Payload: string(payload)}); err != nil {
		return a, err
	}
	raw, _ := json.Marshal(a)
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_grading_final_revisions(artifact_id,agent_name,job_id,previous_artifact_id,correction_id,artifact_json,asset_owner_scope,source_question_json,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, a.ArtifactID, a.AgentName, a.JobID, current.ArtifactID, id, string(raw), a.AnnotatedAssetOwnerScope, string(source), now)
	if err != nil {
		return a, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_grading_final_heads(agent_name,job_id,artifact_id) VALUES(?,?,?) ON CONFLICT(agent_name,job_id) DO UPDATE SET artifact_id=excluded.artifact_id`, a.AgentName, a.JobID, a.ArtifactID)
	if err != nil {
		return a, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE k12_final_source_corrections SET status='completed',updated_at=? WHERE correction_id=? AND status='succeeded'`, now, id)
	if err != nil {
		return a, err
	}
	if err = tx.Commit(); err != nil {
		return a, err
	}
	if s.notifyOutbox != nil {
		s.notifyOutbox()
	}
	return a, nil
}
