package engineadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/requestcancel"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/session"
	"github.com/hexagon-codes/hexclaw/storage"
)

// NewPrintReplyResolver 把精确 K12 冻结引用接到平台原回合，不注册另一条发送通道。
func NewPrintReplyResolver(deps usecase.Deps) func(context.Context, *adapter.Message, map[string]string) (context.Context, error) {
	return func(ctx context.Context, msg *adapter.Message, metadata map[string]string) (context.Context, error) {
		if metadata["print_artifacts"] == "" && metadata["text_solve_refs"] == "" {
			return ctx, nil
		}
		if requestcancel.IsExplicit(ctx) {
			return ctx, context.Canceled
		}
		if _, ok := storage.AssistantCompletionFromContext(ctx); ok {
			return ctx, nil
		}
		if msg == nil || deps.Records == nil {
			return ctx, fmt.Errorf("print reply dependencies are unavailable")
		}
		agent := msg.Metadata["routed_agent"]
		if agent == "" {
			agent = msg.Metadata["role"]
		}
		// 未指定角色的真实会话沿工具入口使用内置默认助手，不要求客户端补造角色。
		if agent == "" && msg.UserID != "" && msg.ID != "" && msg.SessionID != "" {
			agent = "default"
			if msg.Metadata == nil {
				msg.Metadata = make(map[string]string)
			}
			msg.Metadata["routed_agent"] = agent
		}
		requestID := msg.Metadata["request_id"]
		if requestID == "" {
			requestID = msg.ID
		}
		if agent == "" || requestID == "" || msg.SessionID == "" {
			return ctx, fmt.Errorf("print reply identity is incomplete")
		}
		if metadata["print_artifacts"] == "" {
			content, err := textReplyRequiredContent(ctx, deps, msg.UserID, agent, metadata["text_solve_refs"], nil)
			if err != nil {
				return ctx, err
			}
			if len(content) > 0 {
				ctx = storage.WithAssistantCompletion(ctx, storage.AssistantCompletion{RequiredContent: content})
			}
			return ctx, nil
		}
		var refs []k12.PrintReplyArtifact
		if json.Unmarshal([]byte(metadata["print_artifacts"]), &refs) != nil || len(refs) == 0 {
			return ctx, fmt.Errorf("invalid print artifact references")
		}
		source := k12.ConversationPrintReply{Owner: msg.UserID, AgentName: agent, SessionID: msg.SessionID, RequestID: requestID, SourceMessageID: msg.ID, Platform: string(msg.Platform), ChatID: msg.ChatID, Locale: metadata["locale"]}
		if source.Locale == "" {
			source.Locale = msg.Metadata["user_locale"]
		}
		if source.Locale == "" {
			source.Locale = "und"
		}
		if identity, ok := adapter.PlatformInstanceIdentityFromContext(ctx); ok && identity.Platform == source.Platform {
			source.InstanceID = identity.ID
		}
		var attachments []adapter.Attachment
		var requiredContent []string
		var readySources []string
		seen := map[string]bool{}
		for _, ref := range refs {
			if ref.Kind != k12.PrintReplyArtifactKind || ref.AgentName != agent {
				return ctx, fmt.Errorf("print artifact agent mismatch")
			}
			actual, attachment, err := deps.ResolvePrintReplyArtifact(ctx, agent, ref.ArtifactID)
			if err != nil {
				return ctx, err
			}
			if actual != ref {
				return ctx, fmt.Errorf("print artifact frozen reference mismatch")
			}
			if !seen[ref.ArtifactID] {
				seen[ref.ArtifactID] = true
				attachments = append(attachments, adapter.Attachment{
					ID: actual.ArtifactID, Type: "file", Name: attachment.Name, Mime: attachment.MIME,
					Size: attachment.Size, ContentDigest: attachment.ContentDigest, URL: actual.URL,
					Source: &adapter.AttachmentSource{OpenReader: attachment.OpenReader},
				})
				source.Artifacts = append(source.Artifacts, actual)
				pdf, err := deps.GetPrintableArtifactPDF(ctx, agent, ref.ArtifactID)
				if err != nil {
					return ctx, err
				}
				if (pdf.Artifact.SourceKind == k12.PrintSourceTextSolveFinalization || pdf.Artifact.SourceKind == k12.PrintSourceGradingFinalArtifact) && strings.Contains(pdf.Artifact.SourceRef, ":teaching-report:") {
					requiredContent = append(requiredContent, pdf.Artifact.CanonicalMarkdown)
					readySources = append(readySources, pdf.Artifact.SourceRef)
				}
			}
		}
		textContent, err := textReplyRequiredContent(ctx, deps, msg.UserID, agent, metadata["text_solve_refs"], readySources)
		if err != nil {
			return ctx, err
		}
		requiredContent = append(requiredContent, textContent...)
		if source.Platform == "dingtalk" {
			saved, loadErr := deps.Records.GetConversationPrintReply(ctx, agent, requestID+":assistant")
			if loadErr == nil {
				if saved.Owner != source.Owner || saved.SessionID != source.SessionID || saved.RequestID != source.RequestID || saved.Platform != source.Platform || saved.InstanceID != source.InstanceID || saved.ChatID != source.ChatID || !slices.Equal(saved.Artifacts, source.Artifacts) {
					return ctx, fmt.Errorf("print reply replay origin mismatch")
				}
				if _, err := deps.Records.ConversationPrintReplyMessage(ctx, saved); err != nil {
					return ctx, err
				}
				// 已提交回复只接续原目标，绑定变化不能重新解释原投递授权。
				source = saved
			} else {
				if !errors.Is(loadErr, sql.ErrNoRows) && !errors.Is(loadErr, records.ErrNotFound) {
					return ctx, loadErr
				}
				targets, err := deps.ResolveDeliveryTargets(ctx, agent)
				// 原私聊事件可直接回送；没有辅导绑定时继续核对原发送人，不能要求另建助手。
				if err != nil && !errors.Is(err, usecase.ErrNoActiveDirectBindings) {
					return ctx, err
				}
				for _, target := range targets {
					if target.Target.Platform == source.Platform && target.Target.InstanceID == source.InstanceID && target.Target.ChatID == source.ChatID {
						source.BindingID = target.BindingID
						break
					}
				}
				if source.BindingID == "" && source.ChatID != source.Owner {
					return ctx, fmt.Errorf("original group delivery binding is unavailable")
				}
			}
		}
		ctx = session.WithAssistantMessageID(ctx, requestID+":assistant")
		ctx = storage.WithAssistantCompletion(ctx, storage.AssistantCompletion{Attachments: attachments, RequiredContent: requiredContent, OwnsDelivery: source.Platform == "dingtalk", Commit: func(commitCtx context.Context, tx *sql.Tx, message *storage.MessageRecord) (bool, error) {
			if requestcancel.IsExplicit(commitCtx) {
				return false, context.Canceled
			}
			return deps.Records.CommitConversationPrintReply(commitCtx, tx, message, source)
		}})
		return ctx, nil
	}
}

// textReplyRequiredContent 只按本轮可信结算读取正文，PDF失败也不丢失已完成答案。
func textReplyRequiredContent(ctx context.Context, deps usecase.Deps, owner, agent, encoded string, readySources []string) ([]string, error) {
	if encoded == "" {
		return nil, nil
	}
	// 结算与工具入口使用同一应用owner，IM发送人与API认证用户不是资产owner。
	if deps.TextbookOwnerID != "" {
		owner = deps.TextbookOwnerID
	}
	var refs []struct {
		RunID          string                        `json:"solve_run_id"`
		ID             string                        `json:"finalization_id"`
		Digest         string                        `json:"finalization_digest"`
		PDFStatus      string                        `json:"pdf_status"`
		GradeReportRef *k12.TextGradeReportReference `json:"grade_report_ref,omitempty"`
	}
	if err := json.Unmarshal([]byte(encoded), &refs); err != nil {
		return nil, fmt.Errorf("invalid text report references: %w", err)
	}
	var content []string
	for _, ref := range refs {
		// 旧引用没有完整结算摘要时保持原回复，不补造历史来源。
		if ref.Digest == "" || ref.PDFStatus == "" {
			continue
		}
		ready := false
		for _, source := range readySources {
			if !strings.HasPrefix(source, "text_solve:"+ref.ID+":"+ref.Digest+":") {
				continue
			}
			if ref.GradeReportRef != nil {
				ready = ready || strings.HasSuffix(source, ":grade:"+ref.GradeReportRef.InvocationID+":"+ref.GradeReportRef.ResultDigest)
			} else {
				ready = ready || !strings.Contains(source, ":grade:")
			}
		}
		if ready {
			continue
		}
		var body string
		var err error
		if ref.GradeReportRef != nil {
			body, err = deps.TextSolveTeachingReportContentWithGrade(ctx, owner, agent, ref.RunID, ref.ID, ref.Digest, *ref.GradeReportRef)
		} else {
			body, err = deps.TextSolveTeachingReportContent(ctx, owner, agent, ref.RunID, ref.ID, ref.Digest)
		}
		if err != nil {
			return nil, err
		}
		content = append(content, body)
	}
	return content, nil
}
