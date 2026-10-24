package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	imagegen "github.com/hexagon-codes/ai-core/media/image"
	videogen "github.com/hexagon-codes/ai-core/media/video"
	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/internal/requestcancel"
	"github.com/hexagon-codes/hexclaw/session"
	"github.com/hexagon-codes/hexclaw/storage"
)

func directMediaKind(msg *adapter.Message) string {
	return RequestedMediaKind(msg.Metadata["model"], msg.Metadata)
}

// RequestedMediaKind 与既有显式模型入口共用同一模态识别，不改变模型能力声明。
func RequestedMediaKind(model string, metadata map[string]string) string {
	if isImageGeneration(model, metadata) {
		return "image"
	}
	if isVideoGeneration(model, metadata) {
		return "video"
	}
	return ""
}

func (e *ReActEngine) processDirectMedia(ctx context.Context, msg *adapter.Message, kind string) (*adapter.Reply, error) {
	if err := mediaflow.RetainRequest(ctx); err != nil {
		return nil, err
	}
	if err := e.sessions.SaveUserMessage(ctx, msg.SessionID, msg); err != nil {
		return nil, err
	}
	provider := msg.Metadata["provider_instance_id"]
	if provider == "" {
		provider = msg.Metadata["provider"]
	}
	origin := mediaflow.Origin{Owner: msg.UserID, Kind: "conversation", RequestID: messageRequestID(msg), Ordinal: "direct-media", SessionID: msg.SessionID, RequestDigest: mediaflow.SourceRequestDigest(msg.Content, msg.Attachments), AgentName: msg.Metadata["routed_agent"], Platform: string(msg.Platform), ChatID: msg.ChatID}
	if identity, ok := adapter.PlatformInstanceIdentityFromContext(ctx); ok && identity.Platform == string(msg.Platform) {
		origin.InstanceID = identity.ID
	}
	req := mediaflow.Request{Origin: origin, Kind: kind, Provider: provider, Model: msg.Metadata["model"]}
	if kind == "image" {
		req.Image = &imagegen.Request{Prompt: msg.Content}
	} else {
		req.Video = &videogen.Request{Prompt: msg.Content}
	}
	op, err := e.mediaFlow.Begin(ctx, req)
	if err != nil {
		return nil, err
	}
	completionCtx := context.WithoutCancel(ctx)
	op, err = e.mediaFlow.Wait(completionCtx, op.Owner, op.ID, false)
	if err != nil {
		return nil, err
	}
	if op.CancelRequested {
		mediaflow.CancelRequest(ctx)
		return nil, fmt.Errorf("media operation %s: cancel requested", op.ID)
	}
	if op.SaveState != "ready" {
		return nil, fmt.Errorf("media operation %s: %s", op.ID, op.ErrorCode)
	}
	encoded, err := json.Marshal(op.Assets)
	if err != nil {
		return nil, err
	}
	metadata := map[string]string{"artifacts": string(encoded), "provider": op.Route.ProviderID, "model": op.Route.Model, "producer_kind": "chat", "locale": msg.Metadata["user_locale"]}
	completionCtx, content, err := e.prepareMediaReply(completionCtx, msg, metadata, "Media generated.")
	if err != nil {
		return nil, err
	}
	record, err := e.sessions.SaveAssistantReply(completionCtx, msg.SessionID, content, session.AssistantMeta{Provider: op.Route.ProviderID, Model: op.Route.Model, AgentName: assistantAgentName(msg.Metadata), RequestID: messageRequestID(msg), ReplyMetadata: metadata})
	if err != nil {
		return nil, err
	}
	metadata = withAssistantMessageID(metadata, record.ID)
	return &adapter.Reply{Content: content, Metadata: metadata, AssistantMessageID: record.ID}, nil
}

// SetMediaFlow 由装配层注入所有媒体入口共用的应用用例。
func (e *ReActEngine) SetMediaFlow(flow *mediaflow.Service) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.mediaFlow = flow
	if flow != nil {
		flow.SetReplyResumer(e.resumeInterruptedMediaTurn)
	}
}

func (e *ReActEngine) trackMediaRequest(msg *adapter.Message) func() {
	if e.mediaFlow == nil || msg == nil || messageRequestID(msg) == "" {
		return func() {}
	}
	key := msg.UserID + "\x00" + messageRequestID(msg)
	e.mu.Lock()
	if e.mediaRequests == nil {
		e.mediaRequests = make(map[string]int)
	}
	e.mediaRequests[key]++
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		e.mediaRequests[key]--
		if e.mediaRequests[key] <= 0 {
			delete(e.mediaRequests, key)
		}
		e.mu.Unlock()
	}
}

func (e *ReActEngine) mediaAssets(ctx context.Context, msg *adapter.Message, metadata map[string]string) ([]mediaflow.Asset, error) {
	if e.mediaFlow == nil || msg == nil || metadata["artifacts"] == "" {
		return nil, nil
	}
	var refs []mediaflow.Asset
	if json.Unmarshal([]byte(metadata["artifacts"]), &refs) != nil {
		return nil, nil
	}
	return e.mediaFlow.ResolveAssets(ctx, msg.UserID, refs)
}

func (e *ReActEngine) mediaAttachments(ctx context.Context, msg *adapter.Message, metadata map[string]string) ([]adapter.Attachment, error) {
	assets, err := e.mediaAssets(ctx, msg, metadata)
	if err != nil {
		return nil, err
	}
	var attachments []adapter.Attachment
	for _, asset := range assets {
		ref := asset
		attachments = append(attachments, adapter.Attachment{ID: ref.ID, Type: ref.Kind, Name: path.Base(ref.Path), Mime: ref.MIME, URL: "/api/v1/files/generated/" + ref.Path, ContentDigest: "sha256:" + ref.Digest, Size: ref.Size, Source: &adapter.AttachmentSource{OpenReader: func(openCtx context.Context) (io.ReadCloser, error) {
			return e.mediaFlow.OpenAsset(openCtx, msg.UserID, ref)
		}}})
	}
	domainAttachments, err := e.domainReplyAttachments(ctx, msg, metadata)
	if err != nil {
		return nil, err
	}
	return append(attachments, domainAttachments...), nil
}

// ResolveMediaReplyAttachments 为原回复批次恢复已冻结的媒体，不创建操作或投递意图。
func (e *ReActEngine) ResolveMediaReplyAttachments(ctx context.Context, owner, metadata string) ([]adapter.Attachment, error) {
	var saved map[string]json.RawMessage
	if err := json.Unmarshal([]byte(metadata), &saved); err != nil {
		return nil, err
	}
	var refs []mediaflow.Asset
	if raw := saved["media_assets"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &refs); err != nil {
			return nil, err
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	e.mu.RLock()
	flow := e.mediaFlow
	e.mu.RUnlock()
	if flow == nil {
		return nil, fmt.Errorf("media asset resolver is unavailable")
	}
	assets, err := flow.ResolveAssets(ctx, owner, refs)
	if err != nil {
		return nil, err
	}
	var resendOperations []string
	var encodedResends string
	if json.Unmarshal(saved["media_resend_operations"], &encodedResends) == nil {
		_ = json.Unmarshal([]byte(encodedResends), &resendOperations)
	}
	for _, asset := range assets {
		op, err := flow.Read(ctx, owner, asset.OperationID)
		if err != nil {
			return nil, err
		}
		if op.CancelRequested && !slices.Contains(resendOperations, op.ID) {
			return nil, requestcancel.ErrUserCancelled
		}
	}
	attachments := make([]adapter.Attachment, 0, len(assets))
	for _, asset := range assets {
		ref := asset
		attachments = append(attachments, adapter.Attachment{ID: ref.ID, Type: ref.Kind, Name: path.Base(ref.Path), Mime: ref.MIME, ContentDigest: "sha256:" + ref.Digest, Size: ref.Size, Source: &adapter.AttachmentSource{OpenReader: func(openCtx context.Context) (io.ReadCloser, error) {
			return flow.OpenAsset(openCtx, owner, ref)
		}}})
	}
	return attachments, nil
}

func (e *ReActEngine) finalizeMediaChunk(ctx context.Context, msg *adapter.Message, chunk *adapter.ReplyChunk, content string) error {
	if err := finalizeProducerChunk(chunk, content, msg.Metadata); err != nil {
		return err
	}
	attachments, err := e.mediaAttachments(ctx, msg, chunk.Metadata)
	if err != nil || len(attachments) == 0 || chunk.MessageContent == nil {
		return err
	}
	canonical, manifest, err := canonicalProducerProjection(chunk.MessageContent.ProducerKind, content, chunk.MessageContent.Locale, canonicalAttachmentRefs(attachments))
	if err != nil {
		return err
	}
	chunk.MessageContent, chunk.RenderManifest = canonical, manifest
	return nil
}

// prepareMediaReply 将产物交回既有回合完成路径，操作本身不写助手消息。
func (e *ReActEngine) prepareMediaReply(ctx context.Context, msg *adapter.Message, metadata map[string]string, content string) (context.Context, string, error) {
	ctx, err := e.prepareReplyArtifacts(ctx, msg, metadata)
	if err != nil {
		return ctx, content, err
	}
	assets, err := e.mediaAssets(ctx, msg, metadata)
	if err != nil {
		return ctx, content, err
	}
	var operations, resendOperations []string
	_ = json.Unmarshal([]byte(metadata["media_operations"]), &operations)
	_ = json.Unmarshal([]byte(metadata["media_resend_operations"]), &resendOperations)
	if len(assets) == 0 && len(operations) == 0 {
		return ctx, content, nil
	}
	completion := mediaflow.ReplyCompletion{Owner: msg.UserID, SessionID: msg.SessionID, RequestID: messageRequestID(msg), Assets: assets}
	completion.ResendOperations = resendOperations
	seen := make(map[string]bool)
	for _, id := range operations {
		if _, err := e.mediaFlow.Read(ctx, msg.UserID, id); err != nil {
			return ctx, content, err
		}
		if !seen[id] {
			completion.Operations = append(completion.Operations, id)
			seen[id] = true
		}
	}
	for _, asset := range assets {
		if !seen[asset.OperationID] {
			completion.Operations = append(completion.Operations, asset.OperationID)
			seen[asset.OperationID] = true
		}
		if msg.Platform == adapter.PlatformWeb || msg.Platform == adapter.PlatformAPI || msg.Platform == adapter.PlatformDesktop {
			url := "/api/v1/files/generated/" + asset.Path
			if !strings.Contains(content, url) {
				if asset.Kind == "image" {
					content += "\n\n![generated image](" + url + ")"
				} else {
					content += fmt.Sprintf("\n\n[generated %s](%s)", asset.Kind, url)
				}
			}
		}
	}
	if identity, ok := adapter.PlatformInstanceIdentityFromContext(ctx); ok && identity.Platform == string(msg.Platform) && msg.Platform == adapter.PlatformDingtalk {
		if domain, owns := storage.AssistantCompletionFromContext(ctx); owns && domain.OwnsDelivery {
			// 资产结算仍在原事务执行，渠道发送和恢复由已有原回复批次统一负责。
			delete(metadata, "media_delivery_id")
		} else {
			completion.Platform = identity.Platform
			completion.InstanceID = identity.ID
			completion.ChatID = msg.ChatID
			metadata["media_delivery_id"] = mediaflow.DeliveryID(completion)
		}
	}
	return mediaflow.WithReplyCompletion(ctx, completion), content, nil
}
