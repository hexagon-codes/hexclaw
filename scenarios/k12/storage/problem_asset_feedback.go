package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

const EventProblemAssetFeedback = "k12.problem_asset.answer_feedback"

var ErrProblemAssetFeedbackConflict = errors.New("answer feedback conflicts with its original assessment")

// ProblemAssetFeedbackCommand 明确引用原评估，不把标准答案或用户原因当作新作答。
type ProblemAssetFeedbackCommand struct {
	RequestID     string `json:"request_id"`
	OwnerID       string `json:"owner_id"`
	AgentName     string `json:"agent_name"`
	DispatchID    string `json:"dispatch_id"`
	JobID         string `json:"job_id"`
	ProblemID     string `json:"problem_id"`
	InputRevision int    `json:"input_revision"`
	ResultDigest  string `json:"result_digest"`
	Kind          string `json:"kind"`
	Reason        string `json:"reason"`
}

type ProblemAssetFeedbackTarget struct {
	AgentName     string `json:"agent_name"`
	JobID         string `json:"job_id"`
	ProblemID     string `json:"problem_id"`
	InputRevision int    `json:"input_revision"`
	ResultDigest  string `json:"result_digest"`
	AssetID       string `json:"asset_id"`
	AssetVersion  int    `json:"asset_version"`
}

type ProblemAssetFeedbackOutcome struct {
	Target       ProblemAssetFeedbackTarget `json:"target"`
	Status       string                     `json:"status"`
	CorrectionID string                     `json:"correction_id,omitempty"`
	Error        string                     `json:"error,omitempty"`
}

type ProblemAssetFeedback struct {
	FeedbackID string                        `json:"feedback_id"`
	Command    ProblemAssetFeedbackCommand   `json:"command"`
	Targets    []ProblemAssetFeedbackTarget  `json:"targets"`
	Status     string                        `json:"status"`
	Outcomes   []ProblemAssetFeedbackOutcome `json:"outcomes"`
	LastError  string                        `json:"last_error,omitempty"`
	CreatedAt  int64                         `json:"created_at"`
	UpdatedAt  int64                         `json:"updated_at"`
}

const feedbackColumns = `feedback_id,request_json,targets_json,status,outcomes_json,last_error,created_at,updated_at`

func readProblemAssetFeedback(row rowScanner) (ProblemAssetFeedback, error) {
	var f ProblemAssetFeedback
	var command, targets, outcomes string
	err := row.Scan(&f.FeedbackID, &command, &targets, &f.Status, &outcomes, &f.LastError, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return f, records.ErrNotFound
	}
	if err != nil {
		return f, err
	}
	for _, entry := range []struct {
		raw   string
		value any
	}{{command, &f.Command}, {targets, &f.Targets}, {outcomes, &f.Outcomes}} {
		if err = json.Unmarshal([]byte(entry.raw), entry.value); err != nil {
			return f, err
		}
	}
	return f, nil
}

func (s *Store) GetProblemAssetFeedback(ctx context.Context, owner, id string) (ProblemAssetFeedback, error) {
	return readProblemAssetFeedback(s.db.QueryRowContext(ctx, `SELECT `+feedbackColumns+` FROM k12_problem_asset_feedback WHERE owner_id=? AND feedback_id=?`, owner, id))
}

// AcceptProblemAssetFeedback 将反馈、版本停用及恢复事件放在同一短事务。
func (s *Store) AcceptProblemAssetFeedback(ctx context.Context, command ProblemAssetFeedbackCommand) (ProblemAssetFeedback, bool, error) {
	if strings.TrimSpace(command.RequestID) == "" || command.OwnerID == "" || command.AgentName == "" || command.DispatchID == "" || command.JobID == "" || command.ProblemID == "" || command.InputRevision < 1 || command.ResultDigest == "" || command.Kind != "answer_error" || strings.TrimSpace(command.Reason) == "" {
		return ProblemAssetFeedback{}, false, ErrProblemAssetFeedbackConflict
	}
	raw, err := json.Marshal(command)
	if err != nil {
		return ProblemAssetFeedback{}, false, err
	}
	id := "answer-feedback:" + problemAssetRequestDigest([]byte(command.OwnerID+"\x00"+command.RequestID))
	digest := problemAssetRequestDigest(raw)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ProblemAssetFeedback{}, false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE k12_grading_jobs SET updated_at=updated_at WHERE agent_name=? AND record_id=?`, command.AgentName, command.JobID); err != nil {
		return ProblemAssetFeedback{}, false, err
	}
	var priorDigest string
	err = tx.QueryRowContext(ctx, `SELECT request_digest FROM k12_problem_asset_feedback WHERE feedback_id=?`, id).Scan(&priorDigest)
	if err == nil {
		if priorDigest != digest {
			return ProblemAssetFeedback{}, false, ErrProblemAssetFeedbackConflict
		}
		prior, readErr := readProblemAssetFeedback(tx.QueryRowContext(ctx, `SELECT `+feedbackColumns+` FROM k12_problem_asset_feedback WHERE feedback_id=?`, id))
		if readErr != nil {
			return prior, false, readErr
		}
		return prior, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ProblemAssetFeedback{}, false, err
	}
	original, err := getGradingAssessmentItemRevisionVia(ctx, tx, command.AgentName, command.JobID, command.ProblemID, command.InputRevision)
	if err != nil {
		return ProblemAssetFeedback{}, false, err
	}
	if original.ResultDigest != command.ResultDigest || original.AnswerSource == nil || original.AnswerSource.Kind != k12.ProblemAnswerAsset {
		return ProblemAssetFeedback{}, false, ErrProblemAssetFeedbackConflict
	}
	adoption, err := scanProblemAssetAdoption(tx.QueryRowContext(ctx, `SELECT `+assetAdoptionColumns+` FROM k12_problem_asset_adoptions WHERE owner_id=? AND adoption_id=?`, command.OwnerID, original.AnswerSource.AdoptionID))
	if err != nil {
		return ProblemAssetFeedback{}, false, err
	}
	if adoption.JobID != command.JobID || adoption.ProblemID != command.ProblemID || adoption.InputRevision != command.InputRevision || adoption.AssetID != original.AnswerSource.AssetID || adoption.AssetVersion != original.AnswerSource.AssetVersion {
		return ProblemAssetFeedback{}, false, ErrProblemAssetFeedbackConflict
	}
	// 旧版本反馈仍登记影响，不停用已经独立发布的新版本。
	if _, err = tx.ExecContext(ctx, `UPDATE k12_problem_assets SET status='archived',revision=revision+1,updated_at=? WHERE owner_id=? AND asset_id=? AND current_version=? AND status='active'`, nowUnix(), command.OwnerID, adoption.AssetID, adoption.AssetVersion); err != nil {
		return ProblemAssetFeedback{}, false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.agent_name,a.job_id,a.problem_id,a.input_revision,a.result_digest
		FROM k12_grading_assessment_items a JOIN k12_problem_asset_adoptions d
		ON d.job_id=a.job_id AND d.problem_id=a.problem_id AND d.input_revision=a.input_revision
		WHERE d.owner_id=? AND d.asset_id=? AND d.asset_version=?
		AND json_extract(a.answer_source_json,'$.adoption_id')=d.adoption_id
		ORDER BY a.agent_name,a.job_id,a.problem_id,a.input_revision`, command.OwnerID, adoption.AssetID, adoption.AssetVersion)
	if err != nil {
		return ProblemAssetFeedback{}, false, err
	}
	targets := []ProblemAssetFeedbackTarget{}
	for rows.Next() {
		target := ProblemAssetFeedbackTarget{AssetID: adoption.AssetID, AssetVersion: adoption.AssetVersion}
		if err = rows.Scan(&target.AgentName, &target.JobID, &target.ProblemID, &target.InputRevision, &target.ResultDigest); err != nil {
			rows.Close()
			return ProblemAssetFeedback{}, false, err
		}
		targets = append(targets, target)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ProblemAssetFeedback{}, false, err
	}
	if len(targets) == 0 {
		return ProblemAssetFeedback{}, false, ErrProblemAssetFeedbackConflict
	}
	targetJSON, err := json.Marshal(targets)
	if err != nil {
		return ProblemAssetFeedback{}, false, err
	}
	now := nowUnix()
	if _, err = tx.ExecContext(ctx, `INSERT INTO k12_problem_asset_feedback(feedback_id,owner_id,agent_name,dispatch_id,job_id,problem_id,request_digest,request_json,targets_json,status,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,'pending',?,?)`, id, command.OwnerID, command.AgentName, command.DispatchID, command.JobID, command.ProblemID, digest, string(raw), string(targetJSON), now, now); err != nil {
		return ProblemAssetFeedback{}, false, err
	}
	payload, _ := json.Marshal(map[string]string{"feedback_id": id, "owner_id": command.OwnerID})
	if _, err = appendOutboxEvent(ctx, tx, OutboxEvent{EventID: id, AgentName: command.AgentName, AggregateID: command.JobID, EventType: EventProblemAssetFeedback, PayloadVersion: 1, Payload: string(payload)}); err != nil {
		return ProblemAssetFeedback{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return ProblemAssetFeedback{}, false, err
	}
	if s.notifyOutbox != nil {
		s.notifyOutbox()
	}
	return ProblemAssetFeedback{FeedbackID: id, Command: command, Targets: targets, Status: "pending", Outcomes: []ProblemAssetFeedbackOutcome{}, CreatedAt: now, UpdatedAt: now}, true, nil
}

func (s *Store) CompleteProblemAssetFeedback(ctx context.Context, owner, id, status, message string, outcomes []ProblemAssetFeedbackOutcome) error {
	if status != "completed" && status != "unresolved" && status != "outcome_unknown" {
		return ErrProblemAssetFeedbackConflict
	}
	raw, err := json.Marshal(outcomes)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE k12_problem_asset_feedback SET status=?,last_error=?,outcomes_json=?,updated_at=? WHERE owner_id=? AND feedback_id=? AND status='pending'`, status, message, string(raw), nowUnix(), owner, id)
	return err
}
