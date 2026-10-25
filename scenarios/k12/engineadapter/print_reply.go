package engineadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

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
		if metadata["print_artifacts"] == "" {
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
		requestID := msg.Metadata["request_id"]
		if requestID == "" {
			requestID = msg.ID
		}
		if agent == "" || requestID == "" || msg.SessionID == "" {
			return ctx, fmt.Errorf("print reply identity is incomplete")
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
			}
		}
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
				if err != nil {
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
		ctx = storage.WithAssistantCompletion(ctx, storage.AssistantCompletion{Attachments: attachments, OwnsDelivery: source.Platform == "dingtalk", Commit: func(commitCtx context.Context, tx *sql.Tx, message *storage.MessageRecord) (bool, error) {
			if requestcancel.IsExplicit(commitCtx) {
				return false, context.Canceled
			}
			return deps.Records.CommitConversationPrintReply(commitCtx, tx, message, source)
		}})
		return ctx, nil
	}
}
