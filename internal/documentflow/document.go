// Package documentflow 管理普通会话的文档产物，不依赖专业场景或学习档案。
package documentflow

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
)

var (
	ErrNotFound = errors.New("document artifact not found")
	ErrConflict = errors.New("document artifact origin or content conflict")
	ErrNotReady = errors.New("document artifact is not ready")
)

// Origin 由工具调用上下文提供，模型不能指定归属和原消息身份。
type Origin struct {
	Owner      string `json:"owner"`
	SessionID  string `json:"session_id"`
	RequestID  string `json:"request_id"`
	ToolCallID string `json:"tool_call_id"`
	AgentName  string `json:"agent_name"`
	Platform   string `json:"platform"`
	InstanceID string `json:"instance_id"`
	ChatID     string `json:"chat_id"`
}

// Artifact 的文件字节和来源一起冻结，临时路径不构成下载或发送授权。
type Artifact struct {
	ID               string               `json:"artifact_id"`
	Origin           Origin               `json:"origin"`
	Name             string               `json:"name"`
	Format           render.Format        `json:"format"`
	MIME             string               `json:"mime"`
	ByteDigest       string               `json:"byte_digest"`
	Size             int64                `json:"size"`
	InputDigest      string               `json:"input_digest"`
	CreatedAt        time.Time            `json:"created_at"`
	State            string               `json:"state"`
	Error            string               `json:"error,omitempty"`
	Markdown         string               `json:"markdown"`
	Options          render.RenderOptions `json:"options"`
	StyleDigest      string               `json:"style_digest,omitempty"`
	ParentID         string               `json:"parent_artifact_id,omitempty"`
	RevisionKey      string               `json:"revision_key,omitempty"`
	ResolvedMarkdown string               `json:"resolved_markdown,omitempty"`
	Payload          []byte               `json:"-"`
}

type Repository interface {
	Get(context.Context, string, string) (Artifact, error)
	GetForMessage(context.Context, *storage.MessageRecord, string) (Artifact, error)
	ReplyReferences(context.Context, *storage.MessageRecord) ([]Reference, error)
	CanonicalInstanceID(context.Context, string, string) (string, error)
	Save(context.Context, Artifact) (Artifact, error)
	CommitReply(context.Context, *sql.Tx, *storage.MessageRecord, Origin, []Artifact) (bool, error)
}

type Renderer interface {
	Render(context.Context, string, render.Format, render.RenderOptions) (*render.RenderResult, error)
}

type Service struct {
	renderer    Renderer
	repo        Repository
	displayName func(context.Context, string) string
}

func NewService(renderer Renderer, repo Repository) *Service {
	return &Service{renderer: renderer, repo: repo}
}

func (s *Service) SetAgentDisplayNameResolver(resolve func(context.Context, string) string) {
	s.displayName = resolve
}

// ExportDocument 在渲染前查询原工具调用的文件，恢复只读取已冻结文件。
func (s *Service) ExportDocument(ctx context.Context, content string, format render.Format, opts render.RenderOptions, filename string) (Artifact, error) {
	inv := skill.CurrentInvocation(ctx)
	origin := Origin{Owner: inv.AuthenticatedUserID, SessionID: inv.SessionID, RequestID: inv.RequestID, ToolCallID: inv.ToolCallID, AgentName: inv.RoutedAgentName, Platform: inv.Platform, InstanceID: inv.PlatformInstanceID, ChatID: inv.ChatID}
	if origin.Owner == "" || origin.SessionID == "" || origin.RequestID == "" {
		return Artifact{}, fmt.Errorf("document source identity is incomplete")
	}
	if s == nil || s.renderer == nil || s.repo == nil {
		return Artifact{}, fmt.Errorf("document persistence is unavailable")
	}
	if origin.ToolCallID == "" {
		origin.ToolCallID = inv.OperationOrdinal
	}
	input := digest(struct {
		Content  string
		Format   render.Format
		Options  render.RenderOptions
		Filename string
	}{content, format, opts, filename})
	id := "document-" + digest(origin)
	previous, err := s.repo.Get(ctx, origin.Owner, id)
	if err == nil {
		if previous.Origin != origin || previous.InputDigest != input {
			return Artifact{}, ErrConflict
		}
		if previous.State == "ready" {
			return previous, nil
		}
		return s.renderPrepared(ctx, previous)
	}
	if !errors.Is(err, ErrNotFound) {
		return Artifact{}, err
	}
	if s.displayName != nil {
		opts.PDFMetadata.AgentDisplayName = s.displayName(ctx, origin.AgentName)
	}
	if strings.TrimSpace(filename) == "" {
		filename = "document"
	}
	_, name := render.SanitizeFilename(filename, format.Extension())
	artifact := Artifact{ID: id, Origin: origin, Name: name, Format: format, MIME: format.MIME(), InputDigest: input, CreatedAt: time.Now().UTC(), State: "prepared", Markdown: content, Options: opts, StyleDigest: styleDigestFor(format, opts)}
	artifact, err = s.repo.Save(ctx, artifact)
	if err != nil {
		return artifact, err
	}
	if artifact.State == "ready" {
		return artifact, nil
	}
	return s.renderPrepared(ctx, artifact)
}

// Resume 只重做原内容的文档阶段，不发起内容生成或变更原始来源。
func (s *Service) Resume(ctx context.Context, owner, id string) (Artifact, error) {
	if s == nil || s.repo == nil || s.renderer == nil {
		return Artifact{}, fmt.Errorf("document persistence or renderer is unavailable")
	}
	a, err := s.repo.Get(ctx, owner, id)
	if err != nil {
		return a, err
	}
	if a.State == "ready" {
		return s.Get(ctx, owner, id)
	}
	return s.renderPrepared(ctx, a)
}

func (s *Service) renderPrepared(ctx context.Context, artifact Artifact) (Artifact, error) {
	if artifact.StyleDigest != styleDigestFor(artifact.Format, artifact.Options) {
		return artifact, fmt.Errorf("document style revision changed; export a new artifact revision")
	}
	if artifact.ResolvedMarkdown == "" {
		resolved, err := render.PreprocessMarkdown(ctx, artifact.Markdown, render.PreprocessConfig{})
		if err != nil {
			return s.saveRenderFailure(ctx, artifact, err)
		}
		artifact.ResolvedMarkdown = resolved
		artifact, err = s.repo.Save(ctx, artifact)
		if err != nil {
			return artifact, err
		}
		if artifact.State == "ready" {
			return artifact, nil
		}
	}
	result, err := s.renderer.Render(ctx, artifact.ResolvedMarkdown, artifact.Format, artifact.Options)
	if err != nil {
		return s.saveRenderFailure(ctx, artifact, err)
	}
	if result == nil {
		return s.saveRenderFailure(ctx, artifact, fmt.Errorf("document renderer returned no file"))
	}
	if !result.CacheOwned && !result.CacheHit {
		defer os.Remove(result.Path)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		return s.saveRenderFailure(ctx, artifact, err)
	}
	if len(data) == 0 || result.Size != int64(len(data)) {
		return s.saveRenderFailure(ctx, artifact, fmt.Errorf("document bytes do not match render result"))
	}
	artifact.MIME = result.ContentType
	artifact.ByteDigest = bytesDigest(data)
	artifact.Size = int64(len(data))
	artifact.Payload = data
	artifact.State, artifact.Error = "ready", ""
	return s.repo.Save(ctx, artifact)
}

// Reexport 使用原文和已冻结资源创建独立排版修订，不改写旧文件或原工具身份。
func (s *Service) Reexport(ctx context.Context, owner, id, key string) (Artifact, error) {
	if s == nil || s.repo == nil || s.renderer == nil {
		return Artifact{}, fmt.Errorf("document persistence or renderer is unavailable")
	}
	if strings.TrimSpace(key) == "" {
		return Artifact{}, fmt.Errorf("an idempotency key is required")
	}
	original, err := s.repo.Get(ctx, owner, id)
	if err != nil {
		return original, err
	}
	revisionID := "document-" + digest([]string{owner, id, key})
	previous, err := s.repo.Get(ctx, owner, revisionID)
	if err == nil {
		if previous.ParentID != id || previous.RevisionKey != key {
			return previous, ErrConflict
		}
		if previous.State == "ready" {
			return s.Get(ctx, owner, revisionID)
		}
		return s.renderPrepared(ctx, previous)
	}
	if !errors.Is(err, ErrNotFound) {
		return Artifact{}, err
	}
	revision := original
	revision.ID, revision.ParentID, revision.RevisionKey = revisionID, id, key
	revision.State, revision.Error = "prepared", ""
	revision.Payload = nil
	revision.ByteDigest = ""
	revision.Size = 0
	revision.CreatedAt = time.Now().UTC()
	revision.StyleDigest = styleDigestFor(revision.Format, revision.Options)
	revision, err = s.repo.Save(ctx, revision)
	if err != nil {
		return revision, err
	}
	if revision.State == "ready" {
		return revision, nil
	}
	return s.renderPrepared(ctx, revision)
}

func styleDigestFor(format render.Format, opts render.RenderOptions) string {
	if format != render.FormatPDF || opts.PDFLayout == "custom" {
		return ""
	}
	return render.StandardPDFStyleDigest()
}

func (s *Service) saveRenderFailure(ctx context.Context, artifact Artifact, cause error) (Artifact, error) {
	artifact.State, artifact.Error = "failed", cause.Error()
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	saved, err := s.repo.Save(saveCtx, artifact)
	return saved, errors.Join(fmt.Errorf("document artifact %s failed: %w", artifact.ID, cause), err)
}

func (s *Service) Get(ctx context.Context, owner, id string) (Artifact, error) {
	if s == nil || s.repo == nil {
		return Artifact{}, ErrNotFound
	}
	artifact, err := s.repo.Get(ctx, owner, id)
	if err != nil {
		return Artifact{}, err
	}
	if artifact.State != "ready" {
		return artifact, ErrNotReady
	}
	if artifact.Origin.Owner != owner || artifact.ID != id || artifact.Size != int64(len(artifact.Payload)) || artifact.ByteDigest != bytesDigest(artifact.Payload) {
		return Artifact{}, ErrConflict
	}
	return artifact, nil
}

func digest(value any) string       { raw, _ := json.Marshal(value); return bytesDigest(raw) }
func bytesDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
