package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hexagon-codes/hexclaw/internal/documentflow"
	"github.com/hexagon-codes/hexclaw/storage"
)

// DocumentRepository 沿用应用数据库，文档归属来自真实会话而非场景注册。
type DocumentRepository struct{ db *sql.DB }

func (s *Store) DocumentRepository() *DocumentRepository { return &DocumentRepository{db: s.db} }

func (r *DocumentRepository) CanonicalInstanceID(ctx context.Context, platform, instance string) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM platform_instances WHERE provider=? AND (id=? OR name=?)`, platform, instance, instance).Scan(&id)
	return id, err
}

func (r *DocumentRepository) Get(ctx context.Context, owner, id string) (documentflow.Artifact, error) {
	var data string
	var raw []byte
	err := r.db.QueryRowContext(ctx, `SELECT data,payload FROM document_artifacts WHERE id=? AND owner=?`, id, owner).Scan(&data, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return documentflow.Artifact{}, documentflow.ErrNotFound
	}
	if err != nil {
		return documentflow.Artifact{}, err
	}
	var a documentflow.Artifact
	if err := json.Unmarshal([]byte(data), &a); err != nil {
		return a, err
	}
	a.Payload = raw
	return a, nil
}

func (r *DocumentRepository) GetForMessage(ctx context.Context, message *storage.MessageRecord, id string) (documentflow.Artifact, error) {
	var owner, metadata, content, request string
	err := r.db.QueryRowContext(ctx, `SELECT s.user_id,m.metadata,m.content,m.request_id FROM messages m JOIN sessions s ON s.id=m.session_id WHERE m.id=? AND m.session_id=? AND m.role='assistant'`, message.ID, message.SessionID).Scan(&owner, &metadata, &content, &request)
	if err != nil {
		return documentflow.Artifact{}, err
	}
	if content != message.Content || request != message.RequestID {
		return documentflow.Artifact{}, documentflow.ErrConflict
	}
	a, err := r.Get(ctx, owner, id)
	if err != nil {
		return a, err
	}
	if a.Origin.SessionID != message.SessionID || a.Origin.RequestID != message.RequestID {
		return a, documentflow.ErrConflict
	}
	var savedMeta map[string]json.RawMessage
	if json.Unmarshal([]byte(metadata), &savedMeta) != nil {
		return a, documentflow.ErrConflict
	}
	var encoded string
	refsJSON := savedMeta["artifacts"]
	if json.Unmarshal(refsJSON, &encoded) == nil {
		refsJSON = []byte(encoded)
	}
	var refs []documentflow.Reference
	if json.Unmarshal(refsJSON, &refs) != nil {
		return a, documentflow.ErrConflict
	}
	matched := false
	for _, ref := range refs {
		matched = matched || ref.Kind == "document_export" && ref == documentflow.ReferenceFor(a)
	}
	if !matched {
		return a, documentflow.ErrConflict
	}
	return a, nil
}

func (r *DocumentRepository) Save(ctx context.Context, a documentflow.Artifact) (documentflow.Artifact, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return documentflow.Artifact{}, err
	}
	defer tx.Rollback()
	if err := validateDocumentOrigin(ctx, tx, a.Origin); err != nil {
		return documentflow.Artifact{}, err
	}
	if a.State != "prepared" && a.State != "failed" && a.State != "ready" {
		return a, documentflow.ErrConflict
	}
	if a.Payload == nil {
		a.Payload = []byte{}
	}
	data, err := json.Marshal(a)
	if err != nil {
		return documentflow.Artifact{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO document_artifacts(id,owner,session_id,request_id,input_digest,data,payload,created_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, a.ID, a.Origin.Owner, a.Origin.SessionID, a.Origin.RequestID, a.InputDigest, string(data), a.Payload, a.CreatedAt)
	if err != nil {
		return documentflow.Artifact{}, err
	}
	var savedData string
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT data,payload FROM document_artifacts WHERE id=? AND owner=?`, a.ID, a.Origin.Owner).Scan(&savedData, &raw); err != nil {
		return documentflow.Artifact{}, err
	}
	var saved documentflow.Artifact
	if err := json.Unmarshal([]byte(savedData), &saved); err != nil {
		return saved, err
	}
	if saved.Origin != a.Origin || saved.InputDigest != a.InputDigest || saved.Markdown != a.Markdown || saved.Name != a.Name || saved.Format != a.Format || saved.StyleDigest != a.StyleDigest || saved.ParentID != a.ParentID || saved.RevisionKey != a.RevisionKey || saved.ResolvedMarkdown != "" && saved.ResolvedMarkdown != a.ResolvedMarkdown {
		return saved, documentflow.ErrConflict
	}
	saved.Payload = raw
	if saved.State == "ready" {
		return saved, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE document_artifacts SET data=?,payload=? WHERE id=? AND owner=?`, string(data), a.Payload, a.ID, a.Origin.Owner); err != nil {
		return a, err
	}
	return a, tx.Commit()
}

func validateDocumentOrigin(ctx context.Context, tx *sql.Tx, origin documentflow.Origin) error {
	var owner, platform, instance, chat string
	if err := tx.QueryRowContext(ctx, `SELECT user_id,platform,instance_id,chat_id FROM sessions WHERE id=?`, origin.SessionID).Scan(&owner, &platform, &instance, &chat); err != nil {
		return err
	}
	if instance != origin.InstanceID {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM platform_instances WHERE provider=? AND id=? AND name=?`, platform, origin.InstanceID, instance).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return documentflow.ErrConflict
		}
	}
	if owner != origin.Owner || platform != origin.Platform || chat != origin.ChatID {
		return documentflow.ErrConflict
	}
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE session_id=? AND role='user' AND request_id=?`, origin.SessionID, origin.RequestID).Scan(&found); err != nil {
		return err
	}
	if found != 1 {
		return fmt.Errorf("document source user message is unavailable")
	}
	return nil
}

// CommitReply 将文件引用与原助手消息一起提交，保持内容和附件的重放身份。
func (r *DocumentRepository) CommitReply(ctx context.Context, tx *sql.Tx, message *storage.MessageRecord, origin documentflow.Origin, artifacts []documentflow.Artifact) (bool, error) {
	if message == nil || message.Role != "assistant" || message.SessionID != origin.SessionID || message.RequestID != origin.RequestID {
		return false, documentflow.ErrConflict
	}
	if err := validateDocumentOrigin(ctx, tx, origin); err != nil {
		return false, err
	}
	refs := make([]documentflow.Reference, 0, len(artifacts))
	for _, a := range artifacts {
		if a.Origin.Owner != origin.Owner || a.Origin.SessionID != origin.SessionID || a.Origin.RequestID != origin.RequestID {
			return false, documentflow.ErrConflict
		}
		var data string
		if err := tx.QueryRowContext(ctx, `SELECT data FROM document_artifacts WHERE id=? AND owner=?`, a.ID, origin.Owner).Scan(&data); err != nil {
			return false, err
		}
		var saved documentflow.Artifact
		if json.Unmarshal([]byte(data), &saved) != nil || saved.Origin != a.Origin || saved.InputDigest != a.InputDigest || saved.ByteDigest != a.ByteDigest || saved.Size != a.Size {
			return false, documentflow.ErrConflict
		}
		refs = append(refs, documentflow.ReferenceFor(a))
	}
	var metadata map[string]any
	if message.Metadata != "" && json.Unmarshal([]byte(message.Metadata), &metadata) != nil {
		return false, fmt.Errorf("document assistant metadata is invalid")
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["document_artifacts"] = refs
	var previousBody, previousMetadata string
	loadErr := tx.QueryRowContext(ctx, `SELECT content,metadata FROM messages WHERE id=? AND session_id=? AND role='assistant' AND request_id=?`, message.ID, message.SessionID, message.RequestID).Scan(&previousBody, &previousMetadata)
	replayed := loadErr == nil
	if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) {
		return false, loadErr
	}
	if replayed {
		var saved map[string]json.RawMessage
		if previousBody != message.Content || json.Unmarshal([]byte(previousMetadata), &saved) != nil {
			return false, documentflow.ErrConflict
		}
		var savedRefs []documentflow.Reference
		if json.Unmarshal(saved["document_artifacts"], &savedRefs) != nil {
			return false, documentflow.ErrConflict
		}
		old, _ := json.Marshal(savedRefs)
		current, _ := json.Marshal(refs)
		if string(old) != string(current) {
			return false, documentflow.ErrConflict
		}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return false, err
	}
	message.Metadata = string(raw)
	return replayed, nil
}
