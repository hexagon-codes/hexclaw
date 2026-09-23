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
	"time"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// TutorContextRef 指向一次真实作答；原题快照不包含可变评分。
type TutorContextRef struct {
	OwnerScope, AgentName, ConversationKey, MessageID string
	Kind, JobID, ProblemID                            string
	InputRevision                                     int
	ResultDigest, PrintedNumber, QuestionJSON         string
	RequestDigest                                     string
	CreatedAt                                         int64
}

var ErrTutorContextConflict = errors.New("tutor context message identity conflict")

const tutorContextColumns = `owner_scope,agent_name,conversation_key,message_id,kind,job_id,problem_id,input_revision,result_digest,printed_number,question_json,request_digest,created_at`

// TutorConversationKey 显式包含物理实例，避免不同 IM 机器人的相同会话 ID 串联。
func TutorConversationKey(platform, instance, session string) string {
	value, _ := json.Marshal([]string{platform, instance, session})
	return string(value)
}

func scanTutorContext(row rowScanner) (TutorContextRef, error) {
	var ref TutorContextRef
	err := row.Scan(&ref.OwnerScope, &ref.AgentName, &ref.ConversationKey, &ref.MessageID,
		&ref.Kind, &ref.JobID, &ref.ProblemID, &ref.InputRevision, &ref.ResultDigest,
		&ref.PrintedNumber, &ref.QuestionJSON, &ref.RequestDigest, &ref.CreatedAt)
	return ref, err
}

// TutorSourceIdentity 只投影已存在的任务入站身份，不从展示文本猜测消息或 owner。
func (s *Store) TutorSourceIdentity(ctx context.Context, dispatch k12.ImageTaskDispatch) (TutorContextRef, error) {
	owner, err := s.GetImageTaskOwnerScope(ctx, dispatch.AgentName, dispatch.DispatchID)
	if errors.Is(err, ErrImageTaskNotFound) {
		return TutorContextRef{}, records.ErrNotFound
	}
	if err != nil {
		return TutorContextRef{}, err
	}
	ref := TutorContextRef{OwnerScope: owner, AgentName: dispatch.AgentName, MessageID: dispatch.SourceRef, Kind: "source"}
	switch dispatch.SourceKind {
	case k12.ImageTaskSourceDesktop:
		if dispatch.SourceSessionID == "" {
			return ref, records.ErrNotFound
		}
		ref.ConversationKey = TutorConversationKey("desktop", "", dispatch.SourceSessionID)
	case k12.ImageTaskSourceIM:
		var platform, instance, chat string
		err = s.db.QueryRowContext(ctx, `SELECT r.platform,r.instance_id,r.chat_id,r.provider_message_id
			FROM k12_im_inbound_receipts r JOIN k12_im_inbound_dispatches d ON d.receipt_id=r.receipt_id
			WHERE d.image_task_id=? AND r.owner_scope=? AND r.agent_name=?`,
			dispatch.DispatchID, owner, dispatch.AgentName).Scan(&platform, &instance, &chat, &ref.MessageID)
		if errors.Is(err, sql.ErrNoRows) {
			return ref, records.ErrNotFound
		}
		if err != nil {
			return ref, err
		}
		ref.ConversationKey = TutorConversationKey(platform, instance, chat)
	default:
		return ref, records.ErrNotFound
	}
	return ref, nil
}

// SaveTutorSourceRefs 在结果交付时登记已确认原题，重复读取不增加记录。
func (s *Store) SaveTutorSourceRefs(ctx context.Context, refs []TutorContextRef) error {
	if len(refs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, ref := range refs {
		if ref.Kind != "source" || ref.OwnerScope == "" || ref.AgentName == "" || ref.ConversationKey == "" || ref.MessageID == "" || !json.Valid([]byte(ref.QuestionJSON)) {
			return fmt.Errorf("tutor source identity is incomplete")
		}
		item, err := getGradingAssessmentItemVia(ctx, tx, ref.AgentName, ref.JobID, ref.ProblemID)
		if err != nil {
			return err
		}
		if item.InputRevision != ref.InputRevision || item.ResultDigest != ref.ResultDigest {
			return ErrTutorContextConflict
		}
		ref.CreatedAt = time.Now().Unix()
		if err := insertTutorRef(ctx, tx, ref); err != nil {
			return err
		}
		stored, err := scanTutorContext(tx.QueryRowContext(ctx, `SELECT `+tutorContextColumns+` FROM k12_tutor_context_refs
			WHERE owner_scope=? AND agent_name=? AND conversation_key=? AND message_id=? AND job_id=? AND problem_id=? AND input_revision=?`,
			ref.OwnerScope, ref.AgentName, ref.ConversationKey, ref.MessageID, ref.JobID, ref.ProblemID, ref.InputRevision))
		if err != nil {
			return err
		}
		if stored.Kind != ref.Kind || stored.QuestionJSON != ref.QuestionJSON || stored.PrintedNumber != ref.PrintedNumber || stored.ResultDigest != ref.ResultDigest {
			return ErrTutorContextConflict
		}
	}
	return tx.Commit()
}

func insertTutorRef(ctx context.Context, tx *sql.Tx, ref TutorContextRef) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO k12_tutor_context_refs (`+tutorContextColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`,
		ref.OwnerScope, ref.AgentName, ref.ConversationKey, ref.MessageID, ref.Kind, ref.JobID,
		ref.ProblemID, ref.InputRevision, ref.ResultDigest, ref.PrintedNumber, ref.QuestionJSON, ref.RequestDigest, ref.CreatedAt)
	return err
}

// ResolveTutorContext 重放保持同一作答；无明确引用时要求任务和原印刷题号均唯一。
func (s *Store) ResolveTutorContext(ctx context.Context, scope TutorContextRef, replyTo, number, query string) (TutorContextRef, bool, error) {
	if scope.OwnerScope == "" || scope.AgentName == "" || scope.ConversationKey == "" || scope.MessageID == "" {
		return TutorContextRef{}, false, records.ErrNotFound
	}
	sum := sha256.Sum256([]byte(replyTo + "\x00" + query))
	digest := hex.EncodeToString(sum[:])
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TutorContextRef{}, false, err
	}
	defer tx.Rollback()
	// 先取得同一数据库写事务，解析和绑定间不允许另一个来源改变候选集合。
	if _, err := tx.ExecContext(ctx, `UPDATE k12_tutor_context_refs SET created_at=created_at WHERE owner_scope=? AND agent_name=? AND conversation_key=? AND message_id=?`, scope.OwnerScope, scope.AgentName, scope.ConversationKey, scope.MessageID); err != nil {
		return TutorContextRef{}, false, err
	}
	ref, err := scanTutorContext(tx.QueryRowContext(ctx, `SELECT `+tutorContextColumns+` FROM k12_tutor_context_refs WHERE owner_scope=? AND agent_name=? AND conversation_key=? AND message_id=? AND kind='followup'`, scope.OwnerScope, scope.AgentName, scope.ConversationKey, scope.MessageID))
	if err == nil {
		if ref.RequestDigest != digest {
			return ref, false, ErrTutorContextConflict
		}
		return ref, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ref, false, err
	}
	filter := " AND kind='source'"
	args := []any{scope.OwnerScope, scope.AgentName, scope.ConversationKey}
	if strings.TrimSpace(replyTo) != "" {
		filter = " AND message_id=?"
		args = append(args, replyTo)
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+tutorContextColumns+` FROM k12_tutor_context_refs WHERE owner_scope=? AND agent_name=? AND conversation_key=?`+filter, args...)
	if err != nil {
		return ref, false, err
	}
	var candidates []TutorContextRef
	jobs := map[string]bool{}
	seen := map[string]bool{}
	for rows.Next() {
		candidate, err := scanTutorContext(rows)
		if err != nil {
			rows.Close()
			return ref, false, err
		}
		jobs[candidate.JobID] = true
		key := fmt.Sprintf("%s\x00%s\x00%d", candidate.JobID, candidate.ProblemID, candidate.InputRevision)
		if !seen[key] && (number == "" || candidate.PrintedNumber == number) {
			seen[key] = true
			candidates = append(candidates, candidate)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ref, false, err
	}
	if len(jobs) == 0 {
		return ref, false, records.ErrNotFound
	}
	if len(jobs) != 1 || len(candidates) != 1 {
		return ref, true, nil
	}
	ref = candidates[0]
	ref.Kind, ref.MessageID, ref.RequestDigest, ref.CreatedAt = "followup", scope.MessageID, digest, time.Now().Unix()
	if err := insertTutorRef(ctx, tx, ref); err != nil {
		return ref, false, err
	}
	return ref, false, tx.Commit()
}
