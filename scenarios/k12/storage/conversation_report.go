package k12storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/storage"
)

// GetConversationReportMessage 只读取认证用户当前会话、当前助手已保存的报告引用。
// messageID 为空时选择最近报告；不读取其他会话或历史图片，不触发恢复与发送。
func (s *Store) GetConversationReportMessage(ctx context.Context, user, agent, sessionID, messageID string, includePending bool) (*storage.MessageRecord, error) {
	if strings.TrimSpace(user) == "" || strings.TrimSpace(agent) == "" || strings.TrimSpace(sessionID) == "" {
		return nil, records.ErrNotFound
	}
	ready := `(COALESCE(json_extract(m.metadata,'$.print_reply_digest'),'')<>'' AND COALESCE(json_extract(m.metadata,'$.print_artifacts'),'')<>'')`
	if includePending {
		ready = `(` + ready + ` OR instr(COALESCE(json_extract(m.metadata,'$.text_solve_refs'),''),'"pdf_status"')>0)`
	}
	query := `SELECT m.id,m.session_id,m.request_id,m.content,m.metadata FROM messages m
 JOIN sessions s ON s.id=m.session_id
 WHERE s.id=? AND s.user_id=? AND m.role='assistant'
 AND json_extract(m.metadata,'$.agent_name')=? AND ` + ready
	args := []any{sessionID, user, agent}
	if messageID != "" {
		query += ` AND m.id=?`
		args = append(args, messageID)
	}
	query += ` ORDER BY m.created_at DESC,m.rowid DESC LIMIT 1`
	message := &storage.MessageRecord{Role: "assistant"}
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&message.ID, &message.SessionID, &message.RequestID, &message.Content, &message.Metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, records.ErrNotFound
	}
	return message, err
}

// ConversationImageReportScope 只由当前请求的认证与物理会话身份构造。
type ConversationImageReportScope struct {
	UserID, AgentName, SessionID string
	Platform, InstanceID, ChatID string
	AfterMessageID               string
}

// ConversationImageReportSource 是既有图片任务的来源关系，不创建助手消息或新的报告身份。
type ConversationImageReportSource struct {
	OwnerScope, AgentName, DispatchID string
	SourceSessionID, SourceMessageID  string
	JobID                             string
}

// GetLatestConversationImageReportSource 精确查询当前会话的原图任务，返回其持久 owner。
// Desktop 按原用户消息时序选择；IM 按已接纳回执时序及物理实例选择，不读取图片字节。
func (s *Store) GetLatestConversationImageReportSource(ctx context.Context, scope ConversationImageReportScope) (ConversationImageReportSource, error) {
	var source ConversationImageReportSource
	if scope.UserID == "" || scope.AgentName == "" || scope.SessionID == "" {
		return source, records.ErrNotFound
	}
	base := ` FROM k12_image_task_dispatches d
 JOIN k12_image_task_owner_scopes o ON o.agent_name=d.agent_name AND o.dispatch_id=d.dispatch_id
 JOIN k12_homework_submissions h ON h.agent_name=d.agent_name AND h.dispatch_id=d.dispatch_id
 JOIN k12_grading_jobs g ON g.agent_name=h.agent_name AND g.record_id=h.grading_job_id `
	var query string
	var args []any
	switch strings.ToLower(strings.TrimSpace(scope.Platform)) {
	case "", "web", "desktop", "api":
		query = `SELECT o.owner_scope,d.agent_name,d.dispatch_id,d.source_session_id,m.id,h.grading_job_id` + base + `
 JOIN messages m ON m.session_id=d.source_session_id AND m.id=d.source_ref AND m.role='user'
 JOIN sessions session ON session.id=m.session_id
 WHERE d.agent_name=? AND d.source_kind=? AND session.id=? AND session.user_id=?
 AND h.status='completed' AND g.status='completed' AND o.owner_scope<>''
 AND (?='' OR EXISTS(SELECT 1 FROM messages previous
   WHERE previous.id=? AND previous.session_id=session.id AND previous.role='assistant' AND m.rowid>previous.rowid))
 ORDER BY m.rowid DESC,d.attempt_generation DESC,d.created_at DESC LIMIT 1`
		args = []any{scope.AgentName, k12.ImageTaskSourceDesktop, scope.SessionID, scope.UserID, scope.AfterMessageID, scope.AfterMessageID}
	default:
		if scope.InstanceID == "" || scope.ChatID == "" {
			return source, records.ErrNotFound
		}
		query = `SELECT o.owner_scope,d.agent_name,d.dispatch_id,d.source_session_id,r.provider_message_id,h.grading_job_id` + base + `
 JOIN k12_im_inbound_dispatches inbound ON inbound.image_task_id=d.dispatch_id
 JOIN k12_im_inbound_receipts r ON r.receipt_id=inbound.receipt_id AND r.agent_name=d.agent_name AND r.owner_scope=o.owner_scope
 WHERE d.agent_name=? AND d.source_kind=? AND r.platform=? AND r.instance_id=? AND r.chat_id=?
 AND h.status='completed' AND g.status='completed' AND o.owner_scope<>''
 AND EXISTS(SELECT 1 FROM sessions session WHERE session.id=? AND session.user_id=?)
 AND (?='' OR EXISTS(SELECT 1 FROM messages previous WHERE previous.id=? AND previous.session_id=?
   AND previous.role='assistant' AND r.created_at>CAST(strftime('%s',previous.created_at) AS INTEGER)))
 ORDER BY r.created_at DESC,r.rowid DESC,d.attempt_generation DESC LIMIT 1`
		args = []any{scope.AgentName, k12.ImageTaskSourceIM, scope.Platform, scope.InstanceID, scope.ChatID, scope.SessionID, scope.UserID, scope.AfterMessageID, scope.AfterMessageID, scope.SessionID}
	}
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&source.OwnerScope, &source.AgentName, &source.DispatchID, &source.SourceSessionID, &source.SourceMessageID, &source.JobID)
	if errors.Is(err, sql.ErrNoRows) {
		return source, records.ErrNotFound
	}
	return source, err
}

// GetFrozenGradingReportForJob 只找该原作业已有的 PDF，保留历史修订和实际冻结顺序。
// 正文与终稿一致且 source_ref 精确携带终稿 ID/摘要；不重新打开原图、模板或渲染器。
func (s *Store) GetFrozenGradingReportForJob(ctx context.Context, agent, jobID string) (k12.PrintArtifact, error) {
	var artifactID string
	err := s.db.QueryRowContext(ctx, `WITH finals AS (
 SELECT artifact_id,artifact_digest,canonical_markdown FROM k12_grading_final_artifacts WHERE agent_name=? AND job_id=?
 UNION ALL
 SELECT artifact_id,json_extract(artifact_json,'$.artifact_digest'),json_extract(artifact_json,'$.canonical_markdown')
 FROM k12_grading_final_revisions WHERE agent_name=? AND job_id=?
 )
 SELECT p.artifact_id FROM k12_print_artifacts p
 JOIN finals f ON p.canonical_markdown=f.canonical_markdown AND (
   p.source_ref='final_artifact:'||f.artifact_id||':'||f.artifact_digest OR
   substr(p.source_ref,1,length('final_artifact:'||f.artifact_id||':'||f.artifact_digest)+1)='final_artifact:'||f.artifact_id||':'||f.artifact_digest||':')
 JOIN k12_print_artifact_renders r ON r.artifact_id=p.artifact_id AND r.format='pdf' AND r.content_type='application/pdf'
 WHERE p.agent_name=? AND p.source_kind='grading_final_artifact'
 ORDER BY p.created_at DESC,p.rowid DESC LIMIT 1`, agent, jobID, agent, jobID, agent).Scan(&artifactID)
	if errors.Is(err, sql.ErrNoRows) {
		return k12.PrintArtifact{}, records.ErrNotFound
	}
	if err != nil {
		return k12.PrintArtifact{}, err
	}
	return s.GetPrintArtifact(ctx, agent, artifactID)
}
