package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

var ErrAssessmentCorrectionConflict = errors.New("assessment correction conflicts with its source or predecessor")

const EventAssessmentCorrected = "k12.grading.assessment.corrected"

// AssessmentCorrectedPayload 仅保存关联，消费者从不可变纠正读取实际新结论。
type AssessmentCorrectedPayload struct {
	CorrectionID    string `json:"correction_id"`
	AgentName       string `json:"agent_name"`
	JobID           string `json:"job_id"`
	ProblemID       string `json:"problem_id"`
	InputRevision   int    `json:"input_revision"`
	Revision        int    `json:"revision"`
	OriginalEventID string `json:"original_event_id"`
}

func readAssessmentCorrection(row rowScanner) (k12.GradingAssessmentCorrection, error) {
	var raw string
	var correction k12.GradingAssessmentCorrection
	if err := row.Scan(&raw); err != nil {
		return correction, err
	}
	err := json.Unmarshal([]byte(raw), &correction)
	return correction, err
}

func latestAssessmentCorrection(ctx context.Context, db dbHandle, item k12.GradingAssessmentItem) (k12.GradingAssessmentCorrection, error) {
	return readAssessmentCorrection(db.QueryRowContext(ctx, `SELECT correction_json FROM k12_assessment_corrections
		WHERE agent_name=? AND job_id=? AND problem_id=? AND input_revision=? ORDER BY correction_revision DESC LIMIT 1`,
		item.AgentName, item.JobID, item.ProblemID, item.InputRevision))
}

// GetAssessmentCorrection 按孩子作用域读取历史纠正，不接受其他孩子的 ID。
func (s *Store) GetAssessmentCorrection(ctx context.Context, agentName, correctionID string) (k12.GradingAssessmentCorrection, error) {
	return readAssessmentCorrection(s.db.QueryRowContext(ctx, `SELECT correction_json FROM k12_assessment_corrections
		WHERE agent_name=? AND correction_id=?`, agentName, correctionID))
}

// GetEffectiveGradingAssessment 显式解析当前纠正；原历史读取接口仍返回原始回执。
func (s *Store) GetEffectiveGradingAssessment(ctx context.Context, agentName, jobID, problemID string) (k12.EffectiveGradingAssessment, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return k12.EffectiveGradingAssessment{}, err
	}
	defer tx.Rollback()
	item, err := getGradingAssessmentItemVia(ctx, tx, agentName, jobID, problemID)
	if err != nil {
		return k12.EffectiveGradingAssessment{}, err
	}
	view := k12.EffectiveGradingAssessment{Original: item, Current: item}
	correction, err := latestAssessmentCorrection(ctx, tx, item)
	if err == nil {
		view.Current, view.Correction = correction.Assessment, &correction
	} else if !errors.Is(err, sql.ErrNoRows) {
		return view, err
	}
	return view, tx.Commit()
}

func correctionInvocationMatches(ctx context.Context, tx *sql.Tx, item k12.GradingAssessmentItem, id string, operations ...k12.GradingItemOperation) error {
	if id == "" {
		return nil
	}
	call, err := scanGradingItemInvocation(tx.QueryRowContext(ctx, `SELECT `+gradingItemInvocationColumns+
		` FROM k12_grading_item_invocations WHERE agent_name=? AND item_invocation_id=?`, item.AgentName, id))
	if err != nil {
		return err
	}
	allowed := false
	for _, operation := range operations {
		allowed = allowed || call.Operation == operation
	}
	if !allowed || call.JobID != item.JobID || call.ProblemID != item.ProblemID || call.AttemptID != item.AttemptID ||
		call.InputRevision != item.InputRevision || call.InputDigest != item.InputDigest || call.Status != k12.ModelInvocationSucceeded {
		return ErrAssessmentCorrectionConflict
	}
	return nil
}

// AppendGradingAssessmentCorrection 只消费已经成功且属于同一作答的新判定。
// 新纠正、明确的学习投影与待传播事件同事务；不在事务中调用模型。
func (s *Store) AppendGradingAssessmentCorrection(ctx context.Context, requested k12.GradingAssessmentCorrection,
	effects GradingAssessmentEffects,
) (k12.GradingAssessmentCorrection, bool, error) {
	item := requested.Assessment
	if requested.CorrectionID == "" || requested.OriginalResultDigest == "" || requested.Revision != 0 || requested.CreatedAt != 0 ||
		(requested.Reason != k12.AssessmentCorrectionAnswer && requested.Reason != k12.AssessmentCorrectionGrading && requested.Reason != k12.AssessmentCorrectionGuidance) {
		return k12.GradingAssessmentCorrection{}, false, ErrAssessmentCorrectionConflict
	}
	if err := item.ValidateTerminalParentGuideReference(); err != nil {
		return k12.GradingAssessmentCorrection{}, false, err
	}
	if item.InputRevision != item.ConfirmedVersion || item.CurrentDisposition != k12.GradingAssessmentDispositionCurrent {
		return k12.GradingAssessmentCorrection{}, false, ErrAssessmentCorrectionConflict
	}
	if effects.AssetPublication != nil || effects.SourceCorrection {
		return k12.GradingAssessmentCorrection{}, false, errors.New("correction must not impersonate publication or a new input revision")
	}
	if err := validateGradingAssessmentEffectsForStatus(item.Status, effects); err != nil {
		return k12.GradingAssessmentCorrection{}, false, err
	}
	encoded, err := json.Marshal(struct {
		Request k12.GradingAssessmentCorrection
		Effects GradingAssessmentEffects
	}{requested, effects})
	if err != nil {
		return k12.GradingAssessmentCorrection{}, false, err
	}
	digest := problemAssetRequestDigest(encoded)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return k12.GradingAssessmentCorrection{}, false, err
	}
	defer tx.Rollback()
	// 首次写取得 SQLite 写锁，原任务状态及时间不变。
	if _, err = tx.ExecContext(ctx, `UPDATE k12_grading_jobs SET updated_at=updated_at WHERE agent_name=? AND record_id=?`, item.AgentName, item.JobID); err != nil {
		return k12.GradingAssessmentCorrection{}, false, err
	}
	var priorDigest, priorJSON string
	err = tx.QueryRowContext(ctx, `SELECT request_digest,correction_json FROM k12_assessment_corrections WHERE correction_id=?`, requested.CorrectionID).Scan(&priorDigest, &priorJSON)
	if err == nil {
		if priorDigest != digest {
			return k12.GradingAssessmentCorrection{}, false, ErrAssessmentCorrectionConflict
		}
		var prior k12.GradingAssessmentCorrection
		if err = json.Unmarshal([]byte(priorJSON), &prior); err != nil {
			return prior, false, err
		}
		return prior, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return k12.GradingAssessmentCorrection{}, false, err
	}
	original, err := getGradingAssessmentItemRevisionVia(ctx, tx, item.AgentName, item.JobID, item.ProblemID, item.InputRevision)
	if err != nil {
		return k12.GradingAssessmentCorrection{}, false, err
	}
	if original.CurrentDisposition != k12.GradingAssessmentDispositionCurrent || original.ResultDigest != requested.OriginalResultDigest ||
		original.AttemptID != item.AttemptID || original.InputDigest != item.InputDigest || original.PublishedRevision != item.PublishedRevision ||
		original.StructureVersion != item.StructureVersion || !reflect.DeepEqual(original.AnswerSource, requested.OriginalAnswerSource) {
		return k12.GradingAssessmentCorrection{}, false, ErrAssessmentCorrectionConflict
	}
	previous := original
	latest, err := latestAssessmentCorrection(ctx, tx, original)
	if errors.Is(err, sql.ErrNoRows) {
		if requested.PreviousCorrectionID != "" {
			return k12.GradingAssessmentCorrection{}, false, ErrAssessmentCorrectionConflict
		}
		requested.Revision = 1
	} else if err != nil {
		return k12.GradingAssessmentCorrection{}, false, err
	} else {
		if requested.PreviousCorrectionID != latest.CorrectionID {
			return k12.GradingAssessmentCorrection{}, false, ErrAssessmentCorrectionConflict
		}
		previous, requested.Revision = latest.Assessment, latest.Revision+1
	}
	if requested.Reason == k12.AssessmentCorrectionGuidance {
		if previous.Status != item.Status || previous.SolveInvocationID != item.SolveInvocationID || previous.GradeInvocationID != item.GradeInvocationID ||
			!reflect.DeepEqual(previous.AnswerSource, item.AnswerSource) || item.ParentGuideInvocationID == previous.ParentGuideInvocationID || effects.Mistake != nil || effects.Review != nil {
			return k12.GradingAssessmentCorrection{}, false, ErrAssessmentCorrectionConflict
		}
	} else if item.GradeInvocationID != "" && item.GradeInvocationID == previous.GradeInvocationID {
		return k12.GradingAssessmentCorrection{}, false, errors.New("assessment correction requires a new grading receipt")
	}
	for _, proof := range []struct {
		id string
		operations []k12.GradingItemOperation
	}{
		{item.SolveInvocationID, []k12.GradingItemOperation{k12.GradingItemOperationSolve, k12.GradingItemOperationSolveVerify}},
		{item.GradeInvocationID, []k12.GradingItemOperation{k12.GradingItemOperationGrade}},
		{item.ParentGuideInvocationID, []k12.GradingItemOperation{k12.GradingItemOperationParentGuide}},
	} {
		if err = correctionInvocationMatches(ctx, tx, item, proof.id, proof.operations...); err != nil {
			return k12.GradingAssessmentCorrection{}, false, err
		}
	}
	if err = validateAssessmentAssetSource(ctx, tx, item); err != nil {
		return k12.GradingAssessmentCorrection{}, false, err
	}
	if requested.Reason != k12.AssessmentCorrectionGuidance && item.Status != previous.Status && previous.ProjectionRecordID != "" && effects.Review == nil {
		return k12.GradingAssessmentCorrection{}, false, errors.New("affected learning projection requires an explicit correction")
	}
	if effects.Review != nil {
		if effects.Review.RecordID != previous.ProjectionRecordID {
			return k12.GradingAssessmentCorrection{}, false, ErrAssessmentCorrectionConflict
		}
		rows, err := s.queryRecordsVia(ctx, tx, mistakeMapper{}, `WHERE agent_name=? AND record_id=?`, item.AgentName, effects.Review.RecordID)
		if err != nil {
			return k12.GradingAssessmentCorrection{}, false, err
		}
		if len(rows) != 1 {
			return k12.GradingAssessmentCorrection{}, false, records.ErrNotFound
		}
		fields, err := k12.ParseMistakeFields(rows[0].Fields)
		if err != nil {
			return k12.GradingAssessmentCorrection{}, false, err
		}
		if effects.Review.Fields.ReviewStage > fields.ReviewStage || effects.Review.Fields.LastRetriedAt > fields.LastRetriedAt ||
			(effects.Review.NewStatus == k12.StatusMastered && rows[0].Status != k12.StatusMastered) {
			return k12.GradingAssessmentCorrection{}, false, errors.New("correction cannot add mastery or retry evidence")
		}
		if err = s.commitAssessmentReviewTx(ctx, tx, item.AgentName, *effects.Review); err != nil {
			return k12.GradingAssessmentCorrection{}, false, err
		}
		item.ProjectionRecordID, item.ProjectionCreated = effects.Review.RecordID, previous.ProjectionCreated
	} else if effects.Mistake != nil {
		if previous.ProjectionRecordID != "" {
			return k12.GradingAssessmentCorrection{}, false, ErrAssessmentCorrectionConflict
		}
		item.ProjectionRecordID, item.ProjectionCreated, _, err = s.commitAssessmentMistakeTx(ctx, tx, item.AgentName, *effects.Mistake, requested.CorrectionID+":mistake")
		if err != nil {
			return k12.GradingAssessmentCorrection{}, false, err
		}
	} else {
		item.ProjectionRecordID, item.ProjectionCreated = previous.ProjectionRecordID, previous.ProjectionCreated
	}
	requested.Assessment = item
	requested.CreatedAt = nowUnix()
	raw, err := json.Marshal(requested)
	if err != nil {
		return k12.GradingAssessmentCorrection{}, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_assessment_corrections
		(correction_id,agent_name,job_id,problem_id,input_revision,correction_revision,previous_correction_id,original_result_digest,request_digest,correction_json,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, requested.CorrectionID, item.AgentName, item.JobID, item.ProblemID, item.InputRevision,
		requested.Revision, requested.PreviousCorrectionID, requested.OriginalResultDigest, digest, string(raw), requested.CreatedAt)
	if err != nil {
		return k12.GradingAssessmentCorrection{}, false, err
	}
	payload, _ := json.Marshal(AssessmentCorrectedPayload{CorrectionID: requested.CorrectionID, AgentName: item.AgentName, JobID: item.JobID,
		ProblemID: item.ProblemID, InputRevision: item.InputRevision, Revision: requested.Revision, OriginalEventID: gradingAssessmentEventID(original, "mistake_recorded")})
	_, err = appendOutboxEvent(ctx, tx, OutboxEvent{EventID: "assessment-corrected:"+requested.CorrectionID, AgentName: item.AgentName,
		AggregateID: item.JobID+":"+item.ProblemID, EventType: EventAssessmentCorrected, PayloadVersion: 1, Payload: string(payload)})
	if err != nil {
		return k12.GradingAssessmentCorrection{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return k12.GradingAssessmentCorrection{}, false, err
	}
	if s.notifyOutbox != nil {
		s.notifyOutbox()
	}
	return requested, true, nil
}

// CorrectionSourceID 为文件摘要及后续投影提供稳定的原作答身份。
func CorrectionSourceID(item k12.GradingAssessmentItem) string {
	return strings.Join([]string{item.AgentName, item.JobID, item.ProblemID, fmt.Sprint(item.InputRevision)}, ":")
}
