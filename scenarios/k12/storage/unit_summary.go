package k12storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/toolkit/util/idgen"
)

var ErrUnitSummaryConflict = errors.New("unit summary immutable request conflict")
var ErrUnitSummaryCAS = errors.New("unit summary checkpoint changed")

// 私有checkpoint包含模型输入与冻结来源正文；公开领域DTO仅返回资料内容与引用。
type unitAttemptPayload struct {
	Attempt              k12.UnitSummaryAttempt    `json:"attempt"`
	OwnerID              string                    `json:"owner_id"`
	RequestJSON          string                    `json:"request_json"`
	FinalUserText        string                    `json:"final_user_text"`
	ResolutionJSON       string                    `json:"resolution_json"`
	Context              k12.UnitSummaryContext    `json:"context"`
	Candidate            *k12.UnitSummaryCandidate `json:"candidate"`
	InvocationRef        string                    `json:"invocation_ref"`
	ResolveInputJSON     string                    `json:"resolve_input_json"`
	GeneratedContentJSON string                    `json:"generated_content_json,omitempty"`
}

func encodeUnitAttempt(a k12.UnitSummaryAttempt) (string, error) {
	b, e := json.Marshal(unitAttemptPayload{a, a.OwnerID, a.RequestJSON, a.FinalUserText, a.ResolutionJSON, a.Context, a.Candidate, a.InvocationRef, a.ResolveInputJSON, a.GeneratedContentJSON})
	return string(b), e
}
func scanUnitAttempt(row rowScanner) (k12.UnitSummaryAttempt, error) {
	var raw string
	var a k12.UnitSummaryAttempt
	var doc sql.NullString
	var seq sql.NullInt64
	err := row.Scan(&a.AttemptID, &a.OwnerID, &a.AgentName, &doc, &a.SessionID, &a.SourceMessageID, &a.IdempotencyKey, &a.RequestDigest, &seq, &a.State, &a.Stage, &a.LeaseOwner, &a.LeaseEpoch, &a.LeaseExpiresAt, &a.Revision, &raw, &a.CreatedAt, &a.UpdatedAt, &a.ReceivedSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return a, records.ErrNotFound
	}
	if err != nil {
		return a, err
	}
	var p unitAttemptPayload
	if err = json.Unmarshal([]byte(raw), &p); err != nil {
		return a, err
	}
	p.Attempt.AttemptID = a.AttemptID
	p.Attempt.OwnerID = a.OwnerID
	p.Attempt.AgentName = a.AgentName
	p.Attempt.DocumentID = doc.String
	p.Attempt.SessionID = a.SessionID
	p.Attempt.SourceMessageID = a.SourceMessageID
	p.Attempt.IdempotencyKey = a.IdempotencyKey
	p.Attempt.RequestDigest = a.RequestDigest
	p.Attempt.RequestSeq = seq.Int64
	p.Attempt.ReceivedSeq = a.ReceivedSeq
	p.Attempt.State = a.State
	p.Attempt.Stage = a.Stage
	p.Attempt.LeaseOwner = a.LeaseOwner
	p.Attempt.LeaseEpoch = a.LeaseEpoch
	p.Attempt.LeaseExpiresAt = a.LeaseExpiresAt
	p.Attempt.Revision = a.Revision
	p.Attempt.CreatedAt = a.CreatedAt
	p.Attempt.UpdatedAt = a.UpdatedAt
	p.Attempt.RequestJSON = p.RequestJSON
	p.Attempt.FinalUserText = p.FinalUserText
	p.Attempt.ResolutionJSON = p.ResolutionJSON
	p.Attempt.Context = p.Context
	p.Attempt.Candidate = p.Candidate
	p.Attempt.InvocationRef = p.InvocationRef
	p.Attempt.ResolveInputJSON = p.ResolveInputJSON
	p.Attempt.GeneratedContentJSON = p.GeneratedContentJSON
	return p.Attempt, nil
}

const unitAttemptColumns = `attempt_id,owner_id,agent_name,document_id,session_id,source_message_id,idempotency_key,request_digest,request_seq,state,stage,lease_owner,lease_epoch,lease_expires_at,revision,payload_json,created_at,updated_at,received_seq`
const unitDocumentColumns = `document_id,owner_id,agent_name,subject,scope_key,current_revision_id,head_version,next_request_seq,accepted_request_seq,created_at,updated_at`

func scanUnitDocument(row rowScanner) (k12.UnitSummaryDocument, error) {
	var d k12.UnitSummaryDocument
	var current sql.NullString
	err := row.Scan(&d.DocumentID, &d.OwnerID, &d.AgentName, &d.Subject, &d.ScopeKey, &current, &d.HeadVersion, &d.NextRequestSeq, &d.AcceptedRequestSeq, &d.CreatedAt, &d.UpdatedAt)
	d.CurrentRevisionID = current.String
	if errors.Is(err, sql.ErrNoRows) {
		err = records.ErrNotFound
	}
	return d, err
}

func (s *Store) CreateUnitSummaryAttempt(ctx context.Context, a k12.UnitSummaryAttempt) (k12.UnitSummaryAttempt, bool, error) {
	expectedOwner, expectedDigest := a.OwnerID, a.RequestDigest
	if a.OwnerID == "" || a.AgentName == "" || a.IdempotencyKey == "" || a.RequestDigest == "" {
		return a, false, fmt.Errorf("unit summary owner, agent and immutable request required")
	}
	if err := ensureAgentRegistered(ctx, s.db, a.AgentName); err != nil {
		return a, false, err
	}
	if a.AttemptID == "" {
		a.AttemptID = "usj-" + idgen.ShortID()
	}
	a.State = "resolving"
	a.Stage = "resolve"
	a.CreatedAt = nowUnix()
	a.UpdatedAt = a.CreatedAt
	raw, err := encodeUnitAttempt(a)
	if err != nil {
		return a, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return a, false, err
	}
	defer tx.Rollback()
	order, err := tx.ExecContext(ctx, `INSERT INTO k12_unit_summary_received_orders(attempt_id,agent_name) VALUES(?,?)`, a.AttemptID, a.AgentName)
	if err != nil {
		return a, false, err
	}
	a.ReceivedSeq, err = order.LastInsertId()
	if err != nil {
		return a, false, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO k12_unit_summary_attempts (`+unitAttemptColumns+`) VALUES(?,?,?,NULL,?,?,?,?,NULL,?,?, '',0,0,0,?,?,?,?) ON CONFLICT(agent_name,idempotency_key) DO NOTHING`, a.AttemptID, a.OwnerID, a.AgentName, a.SessionID, a.SourceMessageID, a.IdempotencyKey, a.RequestDigest, a.State, a.Stage, raw, a.CreatedAt, a.UpdatedAt, a.ReceivedSeq)
	if err != nil {
		return a, false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		if err = tx.Rollback(); err != nil {
			return a, false, err
		}
	} else {
		if err = tx.Commit(); err != nil {
			return a, false, err
		}
	}
	a, err = scanUnitAttempt(s.db.QueryRowContext(ctx, `SELECT `+unitAttemptColumns+` FROM k12_unit_summary_attempts WHERE agent_name=? AND idempotency_key=?`, a.AgentName, a.IdempotencyKey))
	if err != nil {
		return a, false, err
	}
	if a.OwnerID != expectedOwner || a.RequestDigest != expectedDigest {
		return a, false, ErrUnitSummaryConflict
	}
	return a, n == 0, nil
}

func (s *Store) GetUnitSummaryAttempt(ctx context.Context, owner, agent, id string) (k12.UnitSummaryAttempt, error) {
	return scanUnitAttempt(s.db.QueryRowContext(ctx, `SELECT `+unitAttemptColumns+` FROM k12_unit_summary_attempts WHERE owner_id=? AND agent_name=? AND attempt_id=?`, owner, agent, id))
}
func (s *Store) GetUnitSummaryAttemptByKey(ctx context.Context, owner, agent, key string) (k12.UnitSummaryAttempt, error) {
	return scanUnitAttempt(s.db.QueryRowContext(ctx, `SELECT `+unitAttemptColumns+` FROM k12_unit_summary_attempts WHERE owner_id=? AND agent_name=? AND idempotency_key=?`, owner, agent, key))
}
func (s *Store) GetUnitSummaryDocument(ctx context.Context, owner, agent, id string) (k12.UnitSummaryDocument, error) {
	return scanUnitDocument(s.db.QueryRowContext(ctx, `SELECT `+unitDocumentColumns+` FROM k12_unit_summary_documents WHERE owner_id=? AND agent_name=? AND document_id=?`, owner, agent, id))
}

func (s *Store) ClaimUnitSummaryAttempt(ctx context.Context, owner, agent, id, worker string, until int64) (k12.UnitSummaryAttempt, bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE k12_unit_summary_attempts SET lease_owner=?,lease_epoch=lease_epoch+1,lease_expires_at=?,revision=revision+1,updated_at=? WHERE attempt_id=? AND owner_id=? AND agent_name=? AND lease_expires_at<=? AND state NOT IN ('succeeded','reused','superseded','failed','needs_input','ignored')`, worker, until, nowUnix(), id, owner, agent, nowUnix())
	if err != nil {
		return k12.UnitSummaryAttempt{}, false, err
	}
	n, _ := res.RowsAffected()
	a, err := s.GetUnitSummaryAttempt(ctx, owner, agent, id)
	return a, n == 1, err
}
func (s *Store) ReleaseUnitSummaryWorkerLeases(ctx context.Context, worker string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE k12_unit_summary_attempts SET lease_owner='',lease_expires_at=0,lease_epoch=lease_epoch+1,revision=revision+1,updated_at=? WHERE lease_owner=? AND state NOT IN ('succeeded','reused','superseded','failed','needs_input','ignored')`, nowUnix(), worker)
	return err
}

func saveUnitAttempt(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, before, after k12.UnitSummaryAttempt) (k12.UnitSummaryAttempt, error) {
	after.Revision = before.Revision + 1
	after.UpdatedAt = nowUnix()
	raw, err := encodeUnitAttempt(after)
	if err != nil {
		return before, err
	}
	var doc any
	if after.DocumentID != "" {
		doc = after.DocumentID
	}
	var seq any
	if after.RequestSeq > 0 {
		seq = after.RequestSeq
	}
	res, err := db.ExecContext(ctx, `UPDATE k12_unit_summary_attempts SET document_id=?,request_seq=?,state=?,stage=?,lease_owner=?,lease_expires_at=?,revision=?,payload_json=?,updated_at=? WHERE attempt_id=? AND owner_id=? AND agent_name=? AND revision=? AND lease_epoch=? AND lease_owner=?`, doc, seq, after.State, after.Stage, after.LeaseOwner, after.LeaseExpiresAt, after.Revision, raw, after.UpdatedAt, before.AttemptID, before.OwnerID, before.AgentName, before.Revision, before.LeaseEpoch, before.LeaseOwner)
	if err != nil {
		return before, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return before, ErrUnitSummaryCAS
	}
	return after, nil
}
func (s *Store) CheckpointUnitSummaryAttempt(ctx context.Context, before, after k12.UnitSummaryAttempt) (k12.UnitSummaryAttempt, error) {
	return saveUnitAttempt(ctx, s.db, before, after)
}

func (s *Store) BindUnitSummaryDocument(ctx context.Context, a k12.UnitSummaryAttempt, scopeKey string) (k12.UnitSummaryAttempt, k12.UnitSummaryDocument, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return a, k12.UnitSummaryDocument{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_unit_summary_documents (`+unitDocumentColumns+`) VALUES(?,?,?,?,?,NULL,0,0,0,?,?) ON CONFLICT(owner_id,agent_name,scope_key) DO NOTHING`, "usd-"+idgen.ShortID(), a.OwnerID, a.AgentName, a.Context.Subject, scopeKey, nowUnix(), nowUnix())
	if err != nil {
		return a, k12.UnitSummaryDocument{}, err
	}
	doc, err := scanUnitDocument(tx.QueryRowContext(ctx, `SELECT `+unitDocumentColumns+` FROM k12_unit_summary_documents WHERE owner_id=? AND agent_name=? AND scope_key=?`, a.OwnerID, a.AgentName, scopeKey))
	if err != nil {
		return a, doc, err
	}
	if a.DocumentID != "" {
		if a.DocumentID != doc.DocumentID {
			return a, doc, ErrUnitSummaryConflict
		}
		return a, doc, tx.Commit()
	}
	if a.ReceivedSeq <= 0 {
		return a, doc, fmt.Errorf("unit summary received sequence is unavailable")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE k12_unit_summary_documents SET next_request_seq=max(next_request_seq,?),updated_at=? WHERE document_id=?`, a.ReceivedSeq, nowUnix(), doc.DocumentID); err != nil {
		return a, doc, err
	}
	doc.NextRequestSeq = max(doc.NextRequestSeq, a.ReceivedSeq)
	next := a
	next.DocumentID = doc.DocumentID
	next.RequestSeq = a.ReceivedSeq
	next.State = "queued"
	next.Stage = "generate"
	next, err = saveUnitAttempt(ctx, tx, a, next)
	if err != nil {
		return a, doc, err
	}
	return next, doc, tx.Commit()
}

type unitRevisionPayload struct {
	Revision k12.UnitSummaryRevision `json:"revision"`
	Context  k12.UnitSummaryContext  `json:"context"`
}

func scanUnitRevision(row rowScanner) (k12.UnitSummaryRevision, error) {
	var raw string
	err := row.Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return k12.UnitSummaryRevision{}, records.ErrNotFound
	}
	if err != nil {
		return k12.UnitSummaryRevision{}, err
	}
	var p unitRevisionPayload
	err = json.Unmarshal([]byte(raw), &p)
	p.Revision.Context = p.Context
	return p.Revision, err
}
func (s *Store) GetUnitSummaryRevision(ctx context.Context, owner, agent, document, id string) (k12.UnitSummaryRevision, error) {
	return scanUnitRevision(s.db.QueryRowContext(ctx, `SELECT r.payload_json FROM k12_unit_summary_revisions r JOIN k12_unit_summary_documents d ON d.document_id=r.document_id AND d.agent_name=r.agent_name WHERE d.owner_id=? AND r.agent_name=? AND r.document_id=? AND r.revision_id=?`, owner, agent, document, id))
}
func (s *Store) FindUnitSummaryRevision(ctx context.Context, owner, agent, id string) (k12.UnitSummaryRevision, error) {
	return scanUnitRevision(s.db.QueryRowContext(ctx, `SELECT r.payload_json FROM k12_unit_summary_revisions r JOIN k12_unit_summary_documents d ON d.document_id=r.document_id AND d.agent_name=r.agent_name WHERE d.owner_id=? AND r.agent_name=? AND r.revision_id=?`, owner, agent, id))
}

// PublishUnitSummary 校验lease、成功水位、head与冻结PDF，在同一短事务决定复用或发布。
func (s *Store) PublishUnitSummary(ctx context.Context, a k12.UnitSummaryAttempt, candidate *k12.UnitSummaryRevision) (k12.UnitSummaryAttempt, k12.UnitSummaryRevision, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return a, k12.UnitSummaryRevision{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE k12_unit_summary_documents SET head_version=head_version WHERE document_id=? AND owner_id=? AND agent_name=?`, a.DocumentID, a.OwnerID, a.AgentName); err != nil {
		return a, k12.UnitSummaryRevision{}, err
	}
	live, err := scanUnitAttempt(tx.QueryRowContext(ctx, `SELECT `+unitAttemptColumns+` FROM k12_unit_summary_attempts WHERE attempt_id=? AND owner_id=? AND agent_name=?`, a.AttemptID, a.OwnerID, a.AgentName))
	if err != nil {
		return a, k12.UnitSummaryRevision{}, err
	}
	if live.Revision != a.Revision || live.LeaseEpoch != a.LeaseEpoch || live.LeaseOwner != a.LeaseOwner || live.LeaseExpiresAt < nowUnix() {
		return a, k12.UnitSummaryRevision{}, ErrUnitSummaryCAS
	}
	doc, err := scanUnitDocument(tx.QueryRowContext(ctx, `SELECT `+unitDocumentColumns+` FROM k12_unit_summary_documents WHERE document_id=? AND owner_id=? AND agent_name=?`, a.DocumentID, a.OwnerID, a.AgentName))
	if err != nil {
		return a, k12.UnitSummaryRevision{}, err
	}
	var current k12.UnitSummaryRevision
	if doc.CurrentRevisionID != "" {
		current, err = scanUnitRevision(tx.QueryRowContext(ctx, `SELECT payload_json FROM k12_unit_summary_revisions WHERE document_id=? AND agent_name=? AND revision_id=?`, doc.DocumentID, a.AgentName, doc.CurrentRevisionID))
		if err != nil {
			return a, current, err
		}
	}
	next := a
	next.LeaseOwner = ""
	next.LeaseExpiresAt = 0
	next.Stage = "complete"
	next.FailureKind = ""
	next.FailureDetail = ""
	if a.RequestSeq < doc.AcceptedRequestSeq {
		next.State = "superseded"
	} else if current.RevisionID != "" && current.ContentDigest == a.ContentDigest {
		next.State = "reused"
	} else {
		if candidate == nil || a.Candidate == nil || candidate.Version != doc.HeadVersion+1 || a.Candidate.ExpectedHeadVersion != doc.HeadVersion || candidate.ArtifactID != a.Candidate.ArtifactID || candidate.ByteDigest != a.Candidate.ByteDigest {
			return a, current, ErrUnitSummaryCAS
		}
		if candidate.GeneratedDate != time.Unix(nowUnix(), 0).In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02") {
			return a, current, ErrUnitSummaryCAS
		}
		candidate.GeneratedAt = nowUnix()
		var digest string
		var size int64
		err = tx.QueryRowContext(ctx, `SELECT r.byte_digest,r.byte_size FROM k12_print_artifact_renders r JOIN k12_print_artifacts p ON p.artifact_id=r.artifact_id WHERE p.artifact_id=? AND p.agent_name=? AND p.source_kind='unit_summary'`, candidate.ArtifactID, a.AgentName).Scan(&digest, &size)
		if err != nil {
			return a, current, err
		}
		if digest != candidate.ByteDigest || size != candidate.ByteSize {
			return a, current, ErrUnitSummaryConflict
		}
		raw, e := json.Marshal(unitRevisionPayload{*candidate, candidate.Context})
		if e != nil {
			return a, current, e
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO k12_unit_summary_revisions (revision_id,document_id,agent_name,version,attempt_id,content_digest,canonical_markdown,artifact_id,byte_digest,generated_at,payload_json) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, candidate.RevisionID, doc.DocumentID, a.AgentName, candidate.Version, a.AttemptID, candidate.ContentDigest, candidate.CanonicalMarkdown, candidate.ArtifactID, candidate.ByteDigest, candidate.GeneratedAt, string(raw))
		if err != nil {
			return a, current, err
		}
		current = *candidate
		next.State = "succeeded"
		if _, err = tx.ExecContext(ctx, `UPDATE k12_unit_summary_documents SET current_revision_id=?,head_version=?,updated_at=? WHERE document_id=? AND head_version=?`, current.RevisionID, current.Version, nowUnix(), doc.DocumentID, doc.HeadVersion); err != nil {
			return a, current, err
		}
	}
	if next.State != "superseded" {
		if _, err = tx.ExecContext(ctx, `UPDATE k12_unit_summary_documents SET accepted_request_seq=max(accepted_request_seq,?),updated_at=? WHERE document_id=?`, a.RequestSeq, nowUnix(), doc.DocumentID); err != nil {
			return a, current, err
		}
	}
	next.ResultRevisionID = current.RevisionID
	next.ArtifactID = current.ArtifactID
	next, err = saveUnitAttempt(ctx, tx, a, next)
	if err != nil {
		return a, current, err
	}
	ref, _ := json.Marshal(map[string]any{"kind": "unit_summary", "attempt_id": a.AttemptID, "document_id": doc.DocumentID, "revision_id": current.RevisionID, "artifact_id": current.ArtifactID, "content_digest": current.ContentDigest})
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_unit_summary_message_projections(attempt_id,event_kind,entity_revision,session_id,message_id,reference_json) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`, a.AttemptID, next.State, next.Revision, a.SessionID, a.SourceMessageID, string(ref))
	if err != nil {
		return a, current, err
	}
	return next, current, tx.Commit()
}

func (s *Store) ListUnitSummaryDocuments(ctx context.Context, owner, agent, session, cursor string, limit int) ([]k12.UnitSummaryDocument, string, error) {
	if err := ensureAgentRegistered(ctx, s.db, agent); err != nil {
		return nil, "", err
	}
	if limit < 1 || limit > 100 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+strings.ReplaceAll(unitDocumentColumns, ",", ",d.")+` FROM k12_unit_summary_documents d WHERE owner_id=? AND agent_name=? AND current_revision_id<>'' AND (?='' OR EXISTS(SELECT 1 FROM k12_unit_summary_attempts a WHERE a.document_id=d.document_id AND a.session_id=?)) AND document_id>? ORDER BY document_id LIMIT ?`, owner, agent, session, session, cursor, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []k12.UnitSummaryDocument{}
	for rows.Next() {
		d, e := scanUnitDocument(rows)
		if e != nil {
			return nil, "", e
		}
		out = append(out, d)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[len(out)-1].DocumentID
	}
	return out, next, nil
}
func (s *Store) ListUnitSummaryAttempts(ctx context.Context, owner, agent, session string, unfinished bool) ([]k12.UnitSummaryAttempt, error) {
	where := ""
	if unfinished {
		where = " AND state NOT IN ('succeeded','reused','superseded','ignored')"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+unitAttemptColumns+` FROM k12_unit_summary_attempts WHERE owner_id=? AND agent_name=? AND (?='' OR session_id=?)`+where+` ORDER BY updated_at DESC LIMIT 100`, owner, agent, session, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []k12.UnitSummaryAttempt{}
	for rows.Next() {
		a, e := scanUnitAttempt(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) ListRecoverableUnitSummaryAttempts(ctx context.Context) ([]k12.UnitSummaryAttempt, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+unitAttemptColumns+` FROM k12_unit_summary_attempts WHERE state IN ('resolving','queued','generating','reconciling','rendering','publishing') AND lease_expires_at<=? ORDER BY updated_at LIMIT 100`, nowUnix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []k12.UnitSummaryAttempt{}
	for rows.Next() {
		a, e := scanUnitAttempt(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ResumeUnitSummaryAttempt 同命令重放返回原checkpoint；首次摘要始终不被补答覆盖。
func (s *Store) ResumeUnitSummaryAttempt(ctx context.Context, before, after k12.UnitSummaryAttempt, key, digest string) (k12.UnitSummaryAttempt, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return before, false, err
	}
	defer tx.Rollback()
	var existing, raw string
	err = tx.QueryRowContext(ctx, `SELECT request_digest,result_json FROM k12_unit_summary_resume_commands WHERE attempt_id=? AND command_key=?`, before.AttemptID, key).Scan(&existing, &raw)
	if err == nil {
		if existing != digest {
			return before, false, ErrUnitSummaryConflict
		}
		if err = tx.Rollback(); err != nil {
			return before, false, err
		}
		a, readErr := s.GetUnitSummaryAttempt(ctx, before.OwnerID, before.AgentName, before.AttemptID)
		return a, true, readErr
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return before, false, err
	}
	after.LeaseOwner = ""
	after.LeaseExpiresAt = 0
	after.FailureKind = ""
	after.FailureDetail = ""
	after, err = saveUnitAttempt(ctx, tx, before, after)
	if err != nil {
		return before, false, err
	}
	raw, err = encodeUnitAttempt(after)
	if err != nil {
		return before, false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO k12_unit_summary_resume_commands(attempt_id,command_key,request_digest,applied_revision,result_json) VALUES(?,?,?,?,?)`, before.AttemptID, key, digest, after.Revision, raw)
	if err != nil {
		return before, false, err
	}
	return after, false, tx.Commit()
}

func (s *Store) ReadUnitSummaryFollowup(ctx context.Context, session, messageID string) (string, error) {
	var text string
	err := s.db.QueryRowContext(ctx, `SELECT content FROM messages WHERE id=? AND session_id=? AND role='user'`, messageID, session).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		err = records.ErrNotFound
	}
	return text, err
}

func (s *Store) ReplayUnitSummaryResume(ctx context.Context, owner, agent, id, key, digest string) (bool, error) {
	var stored string
	err := s.db.QueryRowContext(ctx, `SELECT c.request_digest FROM k12_unit_summary_resume_commands c JOIN k12_unit_summary_attempts a ON a.attempt_id=c.attempt_id WHERE a.owner_id=? AND a.agent_name=? AND a.attempt_id=? AND c.command_key=?`, owner, agent, id, key).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if stored != digest {
		return false, ErrUnitSummaryConflict
	}
	return true, nil
}

type UnitSummaryMaterial struct {
	DocumentID   string `json:"document_id"`
	Title        string `json:"title"`
	Subject      string `json:"subject"`
	Generation   int64  `json:"generation"`
	SourceDigest string `json:"source_digest"`
	Content      string `json:"-"`
	Preview      string `json:"preview,omitempty"`
}

type UnitSummarySessionDocument struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}
type UnitSummaryProvidedMaterial struct {
	Name  string `json:"name"`
	Mime  string `json:"mime"`
	Pages *int   `json:"pages,omitempty"`
	Text  string `json:"text"`
}

func (s *Store) ReadUnitSummaryProvidedMaterials(ctx context.Context, session, message string) ([]UnitSummaryProvidedMaterial, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT attachments FROM messages WHERE id=? AND session_id=? AND role='user'`, message, session).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		err = records.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var content struct {
		Materials []UnitSummaryProvidedMaterial `json:"k12_provided_materials"`
	}
	if raw == "" || raw == "{}" {
		return nil, nil
	}
	if err = json.Unmarshal([]byte(raw), &content); err != nil {
		return nil, err
	}
	return content.Materials, nil
}

// 会话富内容沿原attachments列读取；文档提取正文仍是同一持久user message的内容。
func (s *Store) ReadUnitSummarySessionSources(ctx context.Context, session, message string) ([]UnitSummarySessionDocument, int, error) {
	var metadata, attachments string
	err := s.db.QueryRowContext(ctx, `SELECT metadata,attachments FROM messages WHERE id=? AND session_id=? AND role='user'`, message, session).Scan(&metadata, &attachments)
	if errors.Is(err, sql.ErrNoRows) {
		err = records.ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	var sources struct {
		Documents   []UnitSummarySessionDocument `json:"documents"`
		Attachments []json.RawMessage            `json:"attachments"`
	}
	if attachments != "" && attachments != "{}" {
		if err = json.Unmarshal([]byte(attachments), &sources); err != nil {
			return nil, 0, err
		}
	}
	if len(sources.Documents) == 0 && len(sources.Attachments) == 0 && metadata != "" {
		if err = json.Unmarshal([]byte(metadata), &sources); err != nil {
			return nil, 0, err
		}
	}
	return sources.Documents, len(sources.Attachments), nil
}

// ReadUnitSummaryMaterials 使用当前授权知识源正文与代次；不能借数学进度推测其他学科。
func (s *Store) ReadUnitSummaryMaterials(ctx context.Context, owner, agent string) ([]UnitSummaryMaterial, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id,d.title,s.subject,b.content_generation,s.blob_sha256,d.content FROM kb_documents d JOIN kb_semantic_document_bindings b ON b.document_id=d.id JOIN kb_ingest_document_sources s ON s.document_id=d.id AND s.owner_id=b.owner_id AND s.content_generation=b.content_generation WHERE b.owner_id=? AND b.lifecycle_state='active' AND b.text_state='ready' AND d.deleted=0 AND (s.agent_id='' OR s.agent_id=?) AND length(trim(d.content))>0 ORDER BY d.id`, owner, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UnitSummaryMaterial{}
	for rows.Next() {
		var m UnitSummaryMaterial
		if err = rows.Scan(&m.DocumentID, &m.Title, &m.Subject, &m.Generation, &m.SourceDigest, &m.Content); err != nil {
			return nil, err
		}
		preview := []rune(m.Content)
		if len(preview) > 600 {
			preview = preview[:600]
		}
		m.Preview = string(preview)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReadUnitSummaryStudentEvidence只读当前孩子已经可靠确认的实际作答，不生成掌握或错题结论。
func (s *Store) ReadUnitSummaryStudentEvidence(ctx context.Context, agent string) ([]k12.UnitSummaryFrozenSource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.problem_id,p.subject,p.stem_markdown,a.attempt_id,a.answer_markdown,a.input_digest,a.confirmed_version FROM k12_problems p JOIN k12_attempts a ON a.agent_name=p.agent_name AND a.problem_id=p.problem_id WHERE p.agent_name=? AND p.problem_kind<>'compound_parent' AND a.answer_state='present' AND a.confirmed_version>0 AND p.canonical_version=a.confirmed_version AND a.input_digest<>'' ORDER BY a.updated_at DESC,p.problem_id LIMIT 50`, agent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []k12.UnitSummaryFrozenSource{}
	for rows.Next() {
		var problem, subject, stem, attempt, answer, digest string
		var version int
		if err = rows.Scan(&problem, &subject, &stem, &attempt, &answer, &digest, &version); err != nil {
			return nil, err
		}
		out = append(out, k12.UnitSummaryFrozenSource{UnitSummarySource: k12.UnitSummarySource{Ref: "student-work:" + attempt, Origin: "student_work", Label: "当前孩子的实际作答 · " + subject, Locator: problem, Generation: int64(version), SourceDigest: digest}, Text: "题目：\n" + stem + "\n\n学生实际作答：\n" + answer})
	}
	return out, rows.Err()
}

// FlushUnitSummaryMessageProjections 只合并引用；缺失消息时保留pending，不生成或追加聊天。
func (s *Store) FlushUnitSummaryMessageProjections(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT attempt_id,event_kind,entity_revision,session_id,message_id,reference_json FROM k12_unit_summary_message_projections WHERE state='pending' ORDER BY entity_revision LIMIT 100`)
	if err != nil {
		return err
	}
	type projection struct {
		attempt, kind, session, message, ref string
		revision                             int
	}
	items := []projection{}
	for rows.Next() {
		var p projection
		if err = rows.Scan(&p.attempt, &p.kind, &p.revision, &p.session, &p.message, &p.ref); err != nil {
			rows.Close()
			return err
		}
		items = append(items, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range items {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		var raw string
		e = tx.QueryRowContext(ctx, `SELECT metadata FROM messages WHERE id=? AND session_id=?`, p.message, p.session).Scan(&raw)
		if errors.Is(e, sql.ErrNoRows) {
			tx.Rollback()
			continue
		}
		if e != nil {
			tx.Rollback()
			return e
		}
		meta := map[string]any{}
		// 普通消息允许空Metadata，富附件envelope由独立Attachments列持有；仅合并本列引用，不改附件或正文。
		if strings.TrimSpace(raw) != "" {
			if e = json.Unmarshal([]byte(raw), &meta); e != nil {
				tx.Rollback()
				return e
			}
			if meta == nil {
				meta = map[string]any{}
			}
		}
		var reference map[string]any
		_ = json.Unmarshal([]byte(p.ref), &reference)
		var refs []map[string]any
		switch value := meta["artifacts"].(type) {
		case string:
			_ = json.Unmarshal([]byte(value), &refs)
		case []any:
			for _, v := range value {
				if m, ok := v.(map[string]any); ok {
					refs = append(refs, m)
				}
			}
		}
		found := false
		for i, r := range refs {
			if r["attempt_id"] == p.attempt {
				refs[i] = reference
				found = true
			}
		}
		if !found {
			refs = append(refs, reference)
		}
		encoded, _ := json.Marshal(refs)
		meta["artifacts"] = string(encoded)
		merged, _ := json.Marshal(meta)
		_, e = tx.ExecContext(ctx, `UPDATE messages SET metadata=? WHERE id=? AND session_id=?`, string(merged), p.message, p.session)
		if e == nil {
			_, e = tx.ExecContext(ctx, `UPDATE k12_unit_summary_message_projections SET state='done' WHERE attempt_id=? AND event_kind=? AND entity_revision=?`, p.attempt, p.kind, p.revision)
		}
		if e != nil {
			tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	return nil
}
