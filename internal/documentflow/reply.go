package documentflow

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/storage"
)

type Reference struct {
	Kind        string `json:"kind"`
	ID          string `json:"artifact_id"`
	InputDigest string `json:"input_digest"`
	ByteDigest  string `json:"byte_digest"`
	Size        int64  `json:"size"`
	Name        string `json:"name"`
	MIME        string `json:"mime"`
	URL         string `json:"url"`
}

func ReferenceFor(a Artifact) Reference {
	return Reference{Kind: "document_export", ID: a.ID, InputDigest: a.InputDigest, ByteDigest: a.ByteDigest, Size: a.Size, Name: a.Name, MIME: a.MIME, URL: "/api/v1/document-artifacts/" + url.PathEscape(a.ID) + "/content"}
}

// ResolveAttachments 只读取原请求拥有的不可变文件，不接受任意文件路径。
func (s *Service) ResolveAttachments(ctx context.Context, msg *adapter.Message, encoded string) ([]Artifact, []adapter.Attachment, error) {
	var refs []Reference
	if encoded == "" {
		return nil, nil, nil
	}
	if err := json.Unmarshal([]byte(encoded), &refs); err != nil {
		return nil, nil, fmt.Errorf("invalid document references: %w", err)
	}
	var artifacts []Artifact
	var attachments []adapter.Attachment
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref.Kind != "document_export" {
			continue
		}
		if msg == nil {
			return nil, nil, ErrConflict
		}
		a, err := s.Get(ctx, msg.UserID, ref.ID)
		if err != nil {
			return nil, nil, err
		}
		requestID := msg.Metadata["request_id"]
		if requestID == "" {
			requestID = msg.ID
		}
		instanceID := msg.InstanceID
		if instanceID != a.Origin.InstanceID {
			instanceID, err = s.repo.CanonicalInstanceID(ctx, string(msg.Platform), instanceID)
			if err != nil {
				return nil, nil, err
			}
		}
		if a.Origin.SessionID != msg.SessionID || a.Origin.RequestID != requestID || a.Origin.Platform != string(msg.Platform) || a.Origin.InstanceID != instanceID || a.Origin.ChatID != msg.ChatID || ReferenceFor(a) != ref {
			return nil, nil, ErrConflict
		}
		if seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		payload := bytes.Clone(a.Payload)
		artifacts = append(artifacts, a)
		attachments = append(attachments, adapter.Attachment{ID: a.ID, Type: "file", Name: a.Name, Mime: a.MIME, Size: a.Size, ContentDigest: "sha256:" + a.ByteDigest, URL: ref.URL, Source: &adapter.AttachmentSource{OpenReader: func(readCtx context.Context) (io.ReadCloser, error) {
			if err := readCtx.Err(); err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader(payload)), nil
		}}})
	}
	return artifacts, attachments, nil
}

// ReplyResolver 在原助手消息事务中登记文档来源，附件沿应用已有回复链发送。
func (s *Service) ReplyResolver(ctx context.Context, msg *adapter.Message, metadata map[string]string) (context.Context, error) {
	artifacts, attachments, err := s.ResolveAttachments(ctx, msg, metadata["artifacts"])
	if err != nil || len(artifacts) == 0 {
		return ctx, err
	}
	previous, _ := storage.AssistantCompletionFromContext(ctx)
	completion := previous
	completion.Attachments = append(completion.Attachments, attachments...)
	completion.Commit = func(commitCtx context.Context, tx *sql.Tx, message *storage.MessageRecord) (bool, error) {
		replayed, err := s.repo.CommitReply(commitCtx, tx, message, artifacts[0].Origin, artifacts)
		if err != nil {
			return false, err
		}
		if previous.Commit != nil {
			replay, err := previous.Commit(commitCtx, tx, message)
			return replayed || replay, err
		}
		return replayed, nil
	}
	return storage.WithAssistantCompletion(ctx, completion), nil
}

// ResolveMessageAttachments 供唯一渠道投递计划读取原终稿所引用的文件。
func (s *Service) ResolveMessageAttachments(ctx context.Context, message *storage.MessageRecord) ([]adapter.Attachment, error) {
	if message == nil || message.Role != "assistant" {
		return nil, ErrConflict
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal([]byte(message.Metadata), &metadata); err != nil {
		return nil, err
	}
	var encoded string
	if raw := metadata["artifacts"]; len(raw) > 0 {
		if json.Unmarshal(raw, &encoded) != nil {
			encoded = string(raw)
		}
	}
	var refs []Reference
	if encoded == "" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(encoded), &refs); err != nil {
		return nil, err
	}
	var result []adapter.Attachment
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref.Kind != "document_export" || seen[ref.ID] {
			continue
		}
		a, err := s.repo.GetForMessage(ctx, message, ref.ID)
		if err != nil {
			return nil, err
		}
		if ReferenceFor(a) != ref || a.ByteDigest != bytesDigest(a.Payload) || a.Size != int64(len(a.Payload)) {
			return nil, ErrConflict
		}
		seen[ref.ID] = true
		result = append(result, attachmentFor(a))
	}
	return result, nil
}

func attachmentFor(a Artifact) adapter.Attachment {
	payload := bytes.Clone(a.Payload)
	return adapter.Attachment{ID: a.ID, Type: "file", Name: a.Name, Mime: a.MIME, Size: a.Size, ContentDigest: "sha256:" + a.ByteDigest, URL: ReferenceFor(a).URL, Source: &adapter.AttachmentSource{OpenReader: func(ctx context.Context) (io.ReadCloser, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(payload)), nil
	}}}
}
