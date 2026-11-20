package k12storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// FinalSourceCorrectionRequest 冻结终稿、原题来源及一次独立复核的模型。
type FinalSourceCorrectionRequest struct {
	CorrectionID             string                     `json:"correction_id"`
	AgentName                string                     `json:"agent_name"`
	JobID                    string                     `json:"job_id"`
	DispatchID               string                     `json:"dispatch_id"`
	OwnerScope               string                     `json:"owner_scope"`
	SubmissionID             string                     `json:"submission_id"`
	ProblemID                string                     `json:"problem_id"`
	InputRevision            int                        `json:"input_revision"`
	StructureVersion         int                        `json:"structure_version"`
	InputDigest              string                     `json:"input_digest"`
	OriginalArtifactID       string                     `json:"original_artifact_id"`
	OriginalArtifactDigest   string                     `json:"original_artifact_digest"`
	SourceDigest             string                     `json:"source_digest"`
	Model                    k12.GradingModelSnapshot   `json:"model"`
	RecoveryOf               string                     `json:"recovery_of,omitempty"`
	RecoveryResponseDigest   string                     `json:"recovery_response_digest,omitempty"`
	AcceptDuplicateExecution bool                       `json:"accept_duplicate_execution,omitempty"`
	ReferenceRetry           *finalSourceReferenceRetry `json:"reference_retry,omitempty"`
}

// finalSourceReferenceRetry 冻结明确失败的物理调用，不改变原识图或已经取得的参考解身份。
type finalSourceReferenceRetry struct {
	RequestDigest string   `json:"request_digest"`
	InvocationIDs []string `json:"invocation_ids"`
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
	if err == nil && v.Request.ReferenceRetry != nil && strings.TrimPrefix(problemAssetRequestDigest([]byte(raw)), "sha256:") != v.RequestDigest {
		err = ErrGradingFinalArtifactConflict
	}
	return v, err
}

const finalSourceCorrectionColumns = `request_json,request_digest,status,response_json,response_digest,failure`

func (s *Store) GetFinalSourceCorrection(ctx context.Context, agent, id string) (FinalSourceCorrection, error) {
	return readFinalSourceCorrection(s.db.QueryRowContext(ctx, `SELECT `+finalSourceCorrectionColumns+` FROM k12_final_source_corrections WHERE agent_name=? AND correction_id=?`, agent, id))
}

// ResolveGradingFinalSourcePage 只解析终稿已冻结的原页；批注关系不是报告型终稿的来源授权。
func (s *Store) ResolveGradingFinalSourcePage(ctx context.Context, ownerScope, agentName, dispatchID, artifactID string) (PageAssetMetadata, error) {
	agentName, dispatchID = strings.TrimSpace(agentName), strings.TrimSpace(dispatchID)
	artifact, err := getGradingFinalArtifactVia(ctx, s.db, agentName, strings.TrimSpace(artifactID))
	if err != nil {
		return PageAssetMetadata{}, err
	}
	return resolveGradingFinalSourcePageVia(ctx, s.db, ownerScope, agentName, dispatchID, artifact)
}

func resolveGradingFinalSourcePageVia(ctx context.Context, q dbQueryer, ownerScope, agentName, dispatchID string, artifact k12.GradingFinalArtifact) (PageAssetMetadata, error) {
	var owner string
	if err := q.QueryRowContext(ctx, `SELECT owner_scope FROM k12_image_task_owner_scopes WHERE dispatch_id=? AND agent_name=?`, dispatchID, agentName).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PageAssetMetadata{}, records.ErrNotFound
		}
		return PageAssetMetadata{}, err
	}
	if ownerScope == "" || owner != ownerScope || artifact.AgentName != agentName {
		return PageAssetMetadata{}, records.ErrNotFound
	}
	dispatch, err := getImageTaskDispatch(ctx, q, agentName, dispatchID, "")
	if err != nil {
		return PageAssetMetadata{}, err
	}
	if dispatch.TargetObjectType != k12.ImageTaskTargetHomeworkSubmission || len(dispatch.SourceAssetRefs) != 1 {
		return PageAssetMetadata{}, records.ErrVersionConflict
	}
	homework, err := getHomeworkSubmission(ctx, q, agentName, dispatch.TargetObjectID)
	if err != nil {
		return PageAssetMetadata{}, err
	}
	if homework.DispatchID != dispatchID || homework.GradingJobID != artifact.JobID {
		return PageAssetMetadata{}, records.ErrVersionConflict
	}
	page, err := scanPageAsset(q.QueryRowContext(ctx, `SELECT `+pageAssetColumns+` FROM k12_page_assets
		WHERE owner_scope=? AND agent_name=? AND page_asset_id=? AND storage_state='ready'`, ownerScope, agentName, dispatch.SourceAssetRefs[0]))
	if errors.Is(err, sql.ErrNoRows) {
		return PageAssetMetadata{}, ErrPageAssetNotFound
	}
	if err != nil {
		return PageAssetMetadata{}, err
	}
	if artifact.HasAnnotatedAsset() {
		if artifact.AnnotatedAssetOwnerScope != ownerScope || artifact.OriginalSourceDigest != page.ContentDigest {
			return PageAssetMetadata{}, records.ErrVersionConflict
		}
	} else {
		// 已落盘终稿从报告表读取；新修订沿同一冻结快照加载器核验，不能只信调用者的投影指针。
		frozen, err := loadGradingFinalReport(ctx, q, artifact)
		if err != nil {
			return PageAssetMetadata{}, err
		}
		report := frozen.TeachingReport
		if report == nil {
			// 旧终稿没有报告；无批注修订从已提交的同页命令继承原图来源。
			command, err := readFinalSourceCorrection(q.QueryRowContext(ctx, `SELECT c.request_json,c.request_digest,c.status,c.response_json,c.response_digest,c.failure
				FROM k12_grading_final_revisions r JOIN k12_final_source_corrections c
				ON c.agent_name=r.agent_name AND c.correction_id=r.correction_id
				WHERE r.agent_name=? AND r.job_id=? AND r.artifact_id=?`, agentName, artifact.JobID, artifact.ArtifactID))
			if errors.Is(err, records.ErrNotFound) || finalRevisionMissing(err) {
				return PageAssetMetadata{}, records.ErrVersionConflict
			}
			if err != nil {
				return PageAssetMetadata{}, err
			}
			if command.Status != "completed" || command.Request.AgentName != agentName || command.Request.JobID != artifact.JobID ||
				command.Request.DispatchID != dispatchID || command.Request.OwnerScope != ownerScope || command.Request.SourceDigest != page.ContentDigest {
				return PageAssetMetadata{}, records.ErrVersionConflict
			}
			return page, nil
		}
		if report.Source.Kind != k12.TeachingReportSourceGrading || report.Source.SourceID != artifact.JobID || len(report.SourcePages) != 1 {
			return PageAssetMetadata{}, records.ErrVersionConflict
		}
		source := report.SourcePages[0]
		if source.AssetID != page.PageAssetID || source.AssetDigest != page.ContentDigest || source.SourceDigest != page.ContentDigest ||
			source.MIME != page.MediaType || source.Width != page.PixelWidth || source.Height != page.PixelHeight {
			return PageAssetMetadata{}, records.ErrVersionConflict
		}
	}
	return page, nil
}

func (s *Store) PrepareFinalSourceCorrection(ctx context.Context, r FinalSourceCorrectionRequest) (FinalSourceCorrection, bool, error) {
	var zero FinalSourceCorrection
	if r.ReferenceRetry != nil {
		return zero, false, ErrProblemSourceActionConflict
	}
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
		storedRequest := prior.Request
		storedRequest.ReferenceRetry = nil
		storedRaw, err := json.Marshal(storedRequest)
		if err != nil || !bytes.Equal(storedRaw, raw) || prior.Request.ReferenceRetry == nil && prior.RequestDigest != digest {
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
	if current.ArtifactID != r.OriginalArtifactID || current.ArtifactDigest != r.OriginalArtifactDigest {
		return zero, false, records.ErrVersionConflict
	}
	page, err := resolveGradingFinalSourcePageVia(ctx, tx, r.OwnerScope, r.AgentName, r.DispatchID, current)
	if err != nil {
		return zero, false, err
	}
	if page.ContentDigest != r.SourceDigest {
		return zero, false, records.ErrVersionConflict
	}
	recovery := r.RecoveryOf != "" || r.RecoveryResponseDigest != "" || r.AcceptDuplicateExecution
	recoveryPayload, recoveryDigest := "", ""
	retrySource := ""
	if recovery {
		if r.RecoveryOf == "" || r.RecoveryResponseDigest == "" {
			return zero, false, ErrProblemSourceActionConflict
		}
		previous, err := readFinalSourceCorrection(tx.QueryRowContext(ctx, `SELECT `+finalSourceCorrectionColumns+` FROM k12_final_source_corrections WHERE agent_name=? AND correction_id=?`, r.AgentName, r.RecoveryOf))
		if err != nil {
			return zero, false, err
		}
		if previous.ResponseDigest != r.RecoveryResponseDigest {
			return zero, false, records.ErrVersionConflict
		}
		switch previous.Status {
		case "outcome_unknown":
			// 未知调用只接受同一冻结请求的显式重试授权，旧回执保持原样。
			if !r.AcceptDuplicateExecution {
				return zero, false, ErrProblemSourceActionConflict
			}
			expected := previous.Request
			expected.CorrectionID = r.CorrectionID
			expected.RecoveryOf = r.RecoveryOf
			expected.RecoveryResponseDigest = r.RecoveryResponseDigest
			expected.AcceptDuplicateExecution = true
			expectedRaw, err := json.Marshal(expected)
			if err != nil || string(expectedRaw) != string(raw) {
				return zero, false, records.ErrVersionConflict
			}
		case "succeeded":
			expected := previous.Request
			if r.AcceptDuplicateExecution || expected.ReferenceRetry != nil || previous.ResponseJSON == "" {
				return zero, false, records.ErrVersionConflict
			}
			expected.CorrectionID = r.CorrectionID
			expected.RecoveryOf = r.RecoveryOf
			expected.RecoveryResponseDigest = r.RecoveryResponseDigest
			expected.AcceptDuplicateExecution = false
			expectedRaw, err := json.Marshal(expected)
			if err != nil || !bytes.Equal(expectedRaw, raw) {
				return zero, false, records.ErrVersionConflict
			}
			failed, unresolved, err := finalSourceReferenceInvocations(ctx, tx, previous)
			if err != nil {
				return zero, false, err
			}
			if unresolved || len(failed) == 0 {
				return zero, false, records.ErrVersionConflict
			}
			r.ReferenceRetry = &finalSourceReferenceRetry{RequestDigest: previous.RequestDigest, InvocationIDs: failed}
			raw, err = json.Marshal(r)
			if err != nil {
				return zero, false, err
			}
			digest = strings.TrimPrefix(problemAssetRequestDigest(raw), "sha256:")
			retrySource = r.RecoveryOf
			recoveryPayload, recoveryDigest = previous.ResponseJSON, previous.ResponseDigest
		case "completed":
			prior := previous.Request
			if prior.OwnerScope != r.OwnerScope || prior.AgentName != r.AgentName || prior.JobID != r.JobID || prior.DispatchID != r.DispatchID || prior.SubmissionID != r.SubmissionID || prior.ProblemID != r.ProblemID || prior.SourceDigest != r.SourceDigest || prior.Model != r.Model || r.InputRevision != prior.InputRevision+1 || current.ArtifactID != "final-"+prior.CorrectionID {
				return zero, false, records.ErrVersionConflict
			}
			old, err := getGradingAssessmentItemVia(ctx, tx, r.AgentName, r.JobID, r.ProblemID)
			if err != nil {
				return zero, false, err
			}
			if old.Status != k12.GradingAssessmentUnanswered || old.InputRevision != r.InputRevision || old.InputDigest != r.InputDigest {
				return zero, false, records.ErrVersionConflict
			}
			var frozen string
			if err := tx.QueryRowContext(ctx, `SELECT source_question_json FROM k12_grading_final_revisions WHERE agent_name=? AND correction_id=? AND artifact_id=?`, r.AgentName, prior.CorrectionID, current.ArtifactID).Scan(&frozen); err != nil {
				return zero, false, err
			}
			var question struct {
				Raw           string `json:"raw_transcription"`
				Canonical     string `json:"canonical_markdown"`
				InputDigest   string `json:"input_digest"`
				AnswerState   string
				StudentAnswer string
			}
			scope := problemSourceActionScope{AgentName: r.AgentName, JobID: r.JobID, SubmissionID: r.SubmissionID, StructureVersion: r.StructureVersion}
			head, err := currentProblemInputRevisionHead(ctx, tx, scope, r.ProblemID, r.InputRevision, nowUnix())
			if err != nil {
				return zero, false, err
			}
			if json.Unmarshal([]byte(frozen), &question) != nil || question.AnswerState != "blank" || question.StudentAnswer != "" || question.InputDigest != r.InputDigest || question.Raw != head.StemRaw || question.Canonical != head.QuestionCanonicalMarkdown || head.AnswerRaw != "" || head.AnswerCanonicalMarkdown != "" {
				return zero, false, records.ErrVersionConflict
			}
			var response struct {
				Raw      string
				Question struct{ AnswerState string }
			}
			if json.Unmarshal([]byte(previous.ResponseJSON), &response) != nil || response.Raw == "" || response.Question.AnswerState != "blank" {
				return zero, false, ErrGradingFinalArtifactConflict
			}
			// 新命令明确引用原成功来源证据，不伪造一次已发送的视觉调用。
			recoveryPayload, recoveryDigest = previous.ResponseJSON, previous.ResponseDigest
		default:
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
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM k12_final_source_corrections c
		WHERE c.agent_name=? AND c.job_id=? AND c.problem_id=? AND c.correction_id<>?
		AND (c.status IN ('prepared','sent','succeeded') OR (c.status='outcome_unknown' AND ?=0))
		AND NOT (c.status='succeeded' AND EXISTS (SELECT 1 FROM k12_final_source_corrections child
			WHERE child.agent_name=c.agent_name AND child.job_id=c.job_id AND child.problem_id=c.problem_id
			AND json_extract(child.request_json,'$.recovery_of')=c.correction_id
			AND json_extract(child.request_json,'$.reference_retry.request_digest')=c.request_digest))`, r.AgentName, r.JobID, r.ProblemID, retrySource, recovery).Scan(&pending); err != nil {
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
	status := "prepared"
	if recoveryPayload != "" {
		status = "succeeded"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_final_source_corrections(correction_id,agent_name,job_id,problem_id,request_digest,request_json,status,response_json,response_digest,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, r.CorrectionID, r.AgentName, r.JobID, r.ProblemID, digest, string(raw), status, recoveryPayload, recoveryDigest, now, now)
	if err != nil {
		return zero, false, err
	}
	return FinalSourceCorrection{Request: r, RequestDigest: digest, Status: status, ResponseJSON: recoveryPayload, ResponseDigest: recoveryDigest}, false, tx.Commit()
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
	return problemSourceInputDigest(command.ReferenceRequestDigest(), problem, revision)
}

func (c FinalSourceCorrection) ReferenceRequestDigest() string {
	if c.Request.ReferenceRetry != nil {
		return c.Request.ReferenceRetry.RequestDigest
	}
	return c.RequestDigest
}

func (c FinalSourceCorrection) CanRetryReferenceInvocation(id string) bool {
	return c.Request.ReferenceRetry != nil && slices.Contains(c.Request.ReferenceRetry.InvocationIDs, id)
}

// FinalSourceReferenceReplayState 使用原输入的物理账本区分确定失败和不能重发的未决状态。
func (s *Store) FinalSourceReferenceReplayState(ctx context.Context, c FinalSourceCorrection) (retryRequired, unresolved bool, err error) {
	superseded, err := finalSourceReferenceSuperseded(ctx, s.db, c)
	if err != nil || superseded {
		return false, superseded, err
	}
	failed, unresolved, err := finalSourceReferenceInvocations(ctx, s.db, c)
	for _, id := range failed {
		if !c.CanRetryReferenceInvocation(id) {
			return true, unresolved, err
		}
	}
	return false, unresolved, err
}

func finalSourceReferenceSuperseded(ctx context.Context, q dbQueryer, c FinalSourceCorrection) (bool, error) {
	r := c.Request
	var next bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM k12_final_source_corrections
		WHERE agent_name=? AND job_id=? AND problem_id=? AND json_extract(request_json,'$.recovery_of')=?
		AND json_extract(request_json,'$.reference_retry.request_digest')=?)`, r.AgentName, r.JobID, r.ProblemID, r.CorrectionID, c.RequestDigest).Scan(&next)
	return next, err
}

func finalSourceReferenceInvocations(ctx context.Context, q dbQueryer, c FinalSourceCorrection) (failed []string, unresolved bool, err error) {
	r := c.Request
	revision := r.InputRevision + 1
	rows, err := q.QueryContext(ctx, `SELECT item_invocation_id,operation,operation_attempt,status,failure_class,failure_code
		FROM k12_grading_item_invocations WHERE agent_name=? AND job_id=? AND problem_id=? AND input_revision=? AND input_digest=?
		AND operation IN ('solve','solve_generate','solve_verify','parent_guide') ORDER BY operation_attempt DESC,item_invocation_id`,
		r.AgentName, r.JobID, r.ProblemID, revision, FinalSourceInputDigest(c, r.ProblemID, revision))
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	succeeded := map[k12.GradingItemOperation]bool{}
	for rows.Next() {
		var id, class, code string
		var operation k12.GradingItemOperation
		var attempt int
		var status k12.ModelInvocationStatus
		if err = rows.Scan(&id, &operation, &attempt, &status, &class, &code); err != nil {
			return nil, false, err
		}
		switch status {
		case k12.ModelInvocationSent, k12.ModelInvocationOutcomeUnknown, k12.ModelInvocationReconciled:
			unresolved = true
		case k12.ModelInvocationSucceeded:
			succeeded[operation] = true
		case k12.ModelInvocationFailed:
			if !succeeded[operation] && class == "provider_response" && code == "http_503" {
				failed = append(failed, id)
			}
		}
	}
	return failed, unresolved, rows.Err()
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
	var snapshot struct {
		ReportSnapshotJSON string `json:"report_snapshot_json"`
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return a, err
	}
	a.ReportSnapshotJSON = snapshot.ReportSnapshotJSON
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
	return s.commitFinalBlankSourceCorrection(ctx, id, a, source, item, "", nil, false, nil)
}

// CommitFinalBlankSourceCorrectionWithReview 仅接纳同一成功回执的派生核验结论；原始响应、题目及摘要必须完全相同。
// 重新解析不会写回回执，提交仍沿原身份与修订版本核验。
func (s *Store) CommitFinalBlankSourceCorrectionWithReview(ctx context.Context, id string, a k12.GradingFinalArtifact, source json.RawMessage, item k12.GradingAssessmentItem, responseDigest string, reviewed json.RawMessage) (k12.GradingFinalArtifact, error) {
	return s.commitFinalBlankSourceCorrection(ctx, id, a, source, item, responseDigest, reviewed, false, nil)
}

// CommitFinalSourceReferenceCorrection 只在本次输入的持久参考解及讲解齐备后发布来源修订。
func (s *Store) CommitFinalSourceReferenceCorrection(ctx context.Context, id string, a k12.GradingFinalArtifact, source json.RawMessage, item k12.GradingAssessmentItem, responseDigest string, reviewed json.RawMessage, publication *k12.ProblemAssetPublication) (k12.GradingFinalArtifact, error) {
	return s.commitFinalBlankSourceCorrection(ctx, id, a, source, item, responseDigest, reviewed, true, publication)
}

func (s *Store) commitFinalBlankSourceCorrection(ctx context.Context, id string, a k12.GradingFinalArtifact, source json.RawMessage, item k12.GradingAssessmentItem, responseDigest string, reviewed json.RawMessage, reference bool, publication *k12.ProblemAssetPublication) (k12.GradingFinalArtifact, error) {
	if err := a.Validate(); err != nil {
		return a, err
	}
	if !json.Valid(source) || !reference && item.Status != k12.GradingAssessmentUnanswered || reference && item.Status != k12.GradingAssessmentBlankSolved && item.Status != k12.GradingAssessmentOutOfScope && item.Status != k12.GradingAssessmentUntrusted {
		return a, ErrGradingFinalArtifactConflict
	}
	if err := item.ValidateTerminalParentGuideReference(); err != nil {
		return a, err
	}
	if reference {
		if err := s.validateAssessmentInvocationRef(ctx, item, item.SolveInvocationID, k12.GradingItemOperationSolve, k12.GradingItemOperationSolveVerify, k12.GradingItemOperationSolveGenerate); err != nil {
			return a, err
		}
		if err := s.validateAssessmentInvocationRef(ctx, item, item.ParentGuideInvocationID, k12.GradingItemOperationParentGuide); err != nil {
			return a, err
		}
		if item.GradeInvocationID != "" {
			return a, ErrGradingFinalArtifactConflict
		}
	}
	if a.HasAnnotatedAsset() {
		if _, err := s.openGradingFinalAnnotatedAsset(ctx, a); err != nil {
			return a, err
		}
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
	if superseded, err := finalSourceReferenceSuperseded(ctx, tx, command); err != nil || superseded {
		if err != nil {
			return a, err
		}
		return a, records.ErrVersionConflict
	}
	type sourceReview struct {
		Verified bool            `json:"verified"`
		Question json.RawMessage `json:"question"`
		Raw      string          `json:"raw"`
	}
	var response sourceReview
	if json.Unmarshal([]byte(command.ResponseJSON), &response) != nil {
		return a, ErrGradingFinalArtifactConflict
	}
	if len(reviewed) != 0 {
		var parsed sourceReview
		if responseDigest != command.ResponseDigest || json.Unmarshal(reviewed, &parsed) != nil || !parsed.Verified || response.Raw == "" || parsed.Raw != response.Raw || !bytes.Equal(parsed.Question, response.Question) {
			return a, ErrGradingFinalArtifactConflict
		}
		response.Verified = true
	}
	var question struct{ AnswerState string }
	if json.Unmarshal(response.Question, &question) != nil || !response.Verified || question.AnswerState != "blank" {
		return a, ErrGradingFinalArtifactConflict
	}
	current, err := getGradingFinalArtifactByJobVia(ctx, tx, a.AgentName, a.JobID)
	if err != nil {
		return a, err
	}
	if a.JobID != r.JobID || current.ArtifactID != r.OriginalArtifactID || current.ArtifactDigest != r.OriginalArtifactDigest || a.ArtifactID == current.ArtifactID ||
		(a.HasAnnotatedAsset() != current.HasAnnotatedAsset() && (!reference || a.HasAnnotatedAsset())) {
		return a, records.ErrVersionConflict
	}
	page, err := resolveGradingFinalSourcePageVia(ctx, tx, r.OwnerScope, r.AgentName, r.DispatchID, current)
	if err != nil {
		return a, err
	}
	if page.ContentDigest != r.SourceDigest {
		return a, records.ErrVersionConflict
	}
	if a.HasAnnotatedAsset() || a.ReportSnapshotJSON != "" {
		if _, err := resolveGradingFinalSourcePageVia(ctx, tx, r.OwnerScope, r.AgentName, r.DispatchID, a); err != nil {
			return a, err
		}
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
	if err = appendProblemInputRevision(ctx, tx, scope, r.ProblemID, r.InputRevision, item.InputRevision, id, command.ReferenceRequestDigest(), head, now); err != nil {
		return a, err
	}
	if err = validateAssessmentAssetSource(ctx, tx, item); err != nil {
		return a, err
	}
	var solveID, guideID any
	if item.SolveInvocationID != "" {
		solveID = item.SolveInvocationID
	}
	if item.ParentGuideInvocationID != "" {
		guideID = item.ParentGuideInvocationID
	}
	answerSource := "{}"
	if item.AnswerSource != nil {
		raw, err := json.Marshal(item.AnswerSource)
		if err != nil {
			return a, err
		}
		answerSource = string(raw)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_grading_assessment_items (`+gradingAssessmentItemColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?,?,0,?,?,?)`, item.AgentName, item.JobID, item.ProblemID, item.AttemptID, item.ConfirmedVersion, item.InputRevision, item.PublishedRevision, item.CurrentDisposition, item.StructureVersion, item.InputDigest, item.Status, item.ResultJSON, item.ResultDigest, solveID, answerSource, guideID, item.ProjectionRecordID, item.ProjectionStatus, item.CreatedAt, item.UpdatedAt)
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
	if publication != nil {
		if publication.Verification.AgentName != item.AgentName || publication.Verification.InvocationID != item.SolveInvocationID || publication.Verification.InputDigest != item.InputDigest {
			return a, ErrProblemAssetEvidence
		}
		payload, err := json.Marshal(publication)
		if err != nil {
			return a, err
		}
		if _, err := appendOutboxEvent(ctx, tx, OutboxEvent{EventID: gradingAssessmentEventID(item, "asset_prepare"), AgentName: item.AgentName, AggregateID: item.JobID, EventType: EventProblemAssetPrepare, PayloadVersion: 1, Payload: string(payload)}); err != nil {
			return a, err
		}
	}
	raw, _ := json.Marshal(struct {
		k12.GradingFinalArtifact
		ReportSnapshotJSON string `json:"report_snapshot_json,omitempty"`
	}{a, a.ReportSnapshotJSON})
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
