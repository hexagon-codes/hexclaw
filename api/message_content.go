package api

import (
	"context"
	"encoding/json"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assetstore"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
)

// projectMessageImages仅缩小本次历史响应，数据库原图与模型历史保持原样。
func projectMessageImages(messages []*storage.MessageRecord) {
	for _, message := range messages {
		if message == nil {
			continue
		}
		var metadata map[string]json.RawMessage
		if json.Unmarshal([]byte(message.Metadata), &metadata) != nil {
			continue
		}
		var attachments []map[string]json.RawMessage
		if json.Unmarshal(metadata["attachments"], &attachments) != nil {
			continue
		}
		changed := false
		for index, attachment := range attachments {
			var kind, data, sourceURL string
			_ = json.Unmarshal(attachment["type"], &kind)
			_ = json.Unmarshal(attachment["data"], &data)
			if kind != "image" {
				continue
			}
			_ = json.Unmarshal(attachment["url"], &sourceURL)
			reference := data
			if reference == "" {
				reference = sourceURL
			}
			if agent, file, err := assetstore.Parse(reference); err == nil {
				// 持久资产由原资产接口读取，不能作为内联图片解码。
				attachment["data"] = json.RawMessage(`""`)
				attachment["url"], _ = json.Marshal("/api/k12/assets/" + url.PathEscape(file) + "?agent=" + url.QueryEscape(agent))
				changed = true
				continue
			}
			if data == "" {
				continue
			}
			attachment["data"] = json.RawMessage(`""`)
			attachment["url"], _ = json.Marshal("/api/v1/sessions/" + url.PathEscape(message.SessionID) + "/messages/" + url.PathEscape(message.ID) + "/attachments/" + strconv.Itoa(index) + "/content")
			changed = true
		}
		if changed {
			metadata["attachments"], _ = json.Marshal(attachments)
			encoded, err := json.Marshal(metadata)
			if err == nil {
				message.Metadata = string(encoded)
			}
		}
	}
}

func canonicalChatContent(markdown string, metadata map[string]string, attachments ...messagecontent.AttachmentRef) *messagecontent.MessageContent {
	if strings.TrimSpace(markdown) == "" && len(attachments) == 0 {
		return nil
	}
	producer := messagecontent.ProducerChat
	if parsed, ok := messagecontent.ParseProducerKind(metadata["producer_kind"]); ok {
		producer = parsed
	}
	locale := strings.TrimSpace(metadata["locale"])
	if locale == "" {
		locale = "und"
	}
	content, err := messagecontent.New(producer, locale, markdown, attachments)
	if err != nil {
		return nil
	}
	return &content
}

func canonicalRenderProjection(producer messagecontent.ProducerKind, locale, markdown string, surface messagecontent.Surface, rendererVersion string, attachments ...messagecontent.AttachmentRef) (*messagecontent.MessageContent, *messagecontent.RenderManifest) {
	content, err := messagecontent.New(producer, locale, markdown, attachments)
	if err != nil {
		return nil, nil
	}
	parts := make([]messagecontent.RenderPart, 0, len(attachments)+1)
	if strings.TrimSpace(markdown) != "" {
		parts = append(parts, messagecontent.RenderPart{Kind: messagecontent.PartMarkdown, Text: markdown})
	}
	for _, attachment := range attachments {
		parts = append(parts, messagecontent.RenderPart{Kind: messagecontent.PartArtifact, ArtifactRef: attachment.AssetID, ArtifactDigest: attachment.Digest, AltText: attachment.AltText})
	}
	manifest, err := messagecontent.BuildManifest(content, messagecontent.RenderRequest{
		Surface:         surface,
		RendererVersion: rendererVersion,
		Capabilities: messagecontent.CapabilitySnapshot{
			Markdown:    true,
			TeXMath:     true,
			MathML:      true,
			Attachments: len(attachments) > 0,
		},
		Parts: parts,
	})
	if err != nil {
		return nil, nil
	}
	return &content, &manifest
}

func hydrateMessageContents(messages []*storage.MessageRecord) {
	for _, message := range messages {
		hydrateMessageRuntimeWire(message)
		if message == nil {
			continue
		}
		if message.ContentType != "" && message.ContentType != "text" {
			continue
		}
		metadata := make(map[string]string)
		mergeStringMetadata(metadata, message.Metadata)
		mergeStringMetadata(metadata, message.Meta)
		if message.Role == "assistant" && metadata["media_completion_digest"] != "" {
			if strings.TrimSpace(metadata["locale"]) == "" {
				metadata["locale"] = "und"
			}
			var assets []mediaflow.Asset
			if json.Unmarshal([]byte(metadata["artifacts"]), &assets) == nil && len(assets) > 0 {
				refs := make([]messagecontent.AttachmentRef, 0, len(assets))
				for _, asset := range assets {
					name := path.Base(asset.Path)
					refs = append(refs, messagecontent.AttachmentRef{AssetID: asset.ID, Digest: "sha256:" + asset.Digest, Name: name, AltText: name, MIME: asset.MIME})
				}
				message.MessageContent, message.RenderManifest = canonicalRenderProjection(messagecontent.ProducerChat, metadata["locale"], message.Content, messagecontent.SurfaceHistory, "history-markdown-v1", refs...)
			}
		}
		if message.Role == "assistant" && metadata["print_reply_digest"] != "" {
			var artifacts []struct {
				ArtifactID string `json:"artifact_id"`
				ByteDigest string `json:"byte_digest"`
				Name       string `json:"name"`
			}
			if json.Unmarshal([]byte(metadata["print_artifacts"]), &artifacts) == nil && len(artifacts) > 0 {
				var refs []messagecontent.AttachmentRef
				if message.MessageContent != nil {
					refs = append(refs, message.MessageContent.Attachments...)
				}
				for _, artifact := range artifacts {
					refs = append(refs, messagecontent.AttachmentRef{AssetID: artifact.ArtifactID, Digest: "sha256:" + artifact.ByteDigest, Name: artifact.Name, AltText: artifact.Name, MIME: "application/pdf"})
				}
				locale := metadata["locale"]
				if locale == "" {
					locale = "und"
				}
				message.MessageContent, message.RenderManifest = canonicalRenderProjection(messagecontent.ProducerChat, locale, message.Content, messagecontent.SurfaceHistory, "history-markdown-v1", refs...)
			}
		}
		if message.MessageContent == nil {
			message.MessageContent = canonicalChatContent(message.Content, metadata)
		}
		if message.MessageContent != nil && message.RenderManifest == nil {
			_, message.RenderManifest = canonicalRenderProjection(
				message.MessageContent.ProducerKind,
				message.MessageContent.Locale,
				message.MessageContent.Markdown,
				messagecontent.SurfaceHistory,
				"history-markdown-v1",
				message.MessageContent.Attachments...,
			)
		}
	}
}

// hydrateMessageContents 在历史出口补回同一消息已登记的文件身份，不读取文件内容。
func (s *Server) hydrateMessageContents(ctx context.Context, owner string, messages []*storage.MessageRecord) {
	hydrateMessageContents(messages)
	if s.externalTools == nil || owner == "" {
		return
	}
	for _, message := range messages {
		if message == nil || message.Role != "assistant" ||
			message.ContentType != "" && message.ContentType != "text" {
			continue
		}
		var persisted struct {
			Attachments []adapter.Attachment `json:"attachments"`
		}
		for _, raw := range []string{message.Attachments, message.Metadata, message.Meta} {
			persisted.Attachments = nil
			if json.Unmarshal([]byte(raw), &persisted) != nil {
				persisted.Attachments = nil
				continue
			}
			if len(persisted.Attachments) > 0 {
				break
			}
		}
		// 搜索结果可能只含轻量字段；仍从原消息的独立附件列恢复，不以请求产物替代消息归属。
		if len(persisted.Attachments) == 0 && s.store != nil {
			original, err := s.store.GetMessage(ctx, message.ID)
			if err == nil && original != nil && original.SessionID == message.SessionID && original.RequestID == message.RequestID {
				if json.Unmarshal([]byte(original.Attachments), &persisted) != nil {
					persisted.Attachments = nil
				} else if len(persisted.Attachments) > 0 {
					message.Attachments = original.Attachments
					message.Metadata = original.Metadata
				}
			}
		}
		if len(persisted.Attachments) == 0 {
			continue
		}
		var completion storage.AssistantCompletion
		seenRequests := map[string]bool{}
		resolveRequest := func(requestID string) {
			if requestID == "" || seenRequests[requestID] {
				return
			}
			seenRequests[requestID] = true
			invocation := skill.InvocationContext{AuthenticatedUserID: owner, RequestID: requestID, SessionID: message.SessionID}
			resolveCtx := storage.WithAssistantCompletion(skill.WithInvocationContext(ctx, invocation), storage.AssistantCompletion{})
			resolved, err := s.externalTools.ResolveReply(resolveCtx, &adapter.Message{UserID: owner}, nil)
			if err == nil {
				registered, _ := storage.AssistantCompletionFromContext(resolved)
				completion.Attachments = append(completion.Attachments, registered.Attachments...)
			}
		}
		resolveRequest(message.RequestID)
		// 转引附件或缺少请求字段的历史，优先按原资产身份找回登记请求，路径只关联旧附件。
		if provider, ok := s.store.(interface {
			ExternalToolExecutionRepository() storage.ExternalToolExecutionRepository
		}); ok {
			for _, attachment := range persisted.Attachments {
				found := false
				for _, registered := range completion.Attachments {
					if attachment.ID != "" && attachment.ID == registered.ID ||
						attachment.ID == "" && attachment.URL != "" && attachment.URL == registered.URL {
						found = true
						break
					}
				}
				if found {
					continue
				}
				if attachment.ID != "" {
					rows, err := provider.ExternalToolExecutionRepository().List(ctx, storage.ExternalToolExecutionFilter{OwnerID: owner, ArtifactID: attachment.ID, Limit: 1})
					if err == nil && len(rows) > 0 {
						resolveRequest(rows[0].RequestID)
					}
					continue
				}
				parsed, err := url.Parse(attachment.URL)
				const prefix = "/api/v1/files/generated/"
				if err != nil || !strings.HasPrefix(parsed.Path, prefix) {
					continue
				}
				record, err := provider.ExternalToolExecutionRepository().FindArtifact(ctx, owner, strings.TrimPrefix(parsed.Path, prefix))
				if err == nil && record != nil {
					resolveRequest(record.RequestID)
				}
			}
		}
		var refs []messagecontent.AttachmentRef
		metadata := map[string]string{}
		mergeStringMetadata(metadata, message.Metadata)
		mergeStringMetadata(metadata, message.Meta)
		if message.MessageContent != nil {
			refs = append(refs, message.MessageContent.Attachments...)
			metadata["producer_kind"] = string(message.MessageContent.ProducerKind)
			metadata["locale"] = message.MessageContent.Locale
		}
		originalCount := len(refs)
		for _, attachment := range persisted.Attachments {
			for _, registered := range completion.Attachments {
				if attachment.ID != "" && attachment.ID != registered.ID ||
					attachment.ID == "" && (attachment.URL == "" || attachment.URL != registered.URL) {
					continue
				}
				if registered.ID == "" || registered.Mime == "" || registered.ContentDigest == "" {
					continue
				}
				found := false
				for _, ref := range refs {
					if ref.AssetID == registered.ID {
						found = true
						break
					}
				}
				if !found {
					refs = append(refs, messagecontent.AttachmentRef{AssetID: registered.ID, Name: registered.Name, MIME: registered.Mime, Digest: registered.ContentDigest, AltText: registered.Name})
				}
				break
			}
		}
		if len(refs) == originalCount {
			continue
		}
		content := canonicalChatContent(message.Content, metadata, refs...)
		if content != nil {
			message.MessageContent, message.RenderManifest = canonicalRenderProjection(content.ProducerKind, content.Locale, content.Markdown, messagecontent.SurfaceHistory, "history-markdown-v1", content.Attachments...)
		}
	}
}

func hydrateMessageRuntimeWire(message *storage.MessageRecord) {
	if message == nil || message.Role != "assistant" {
		return
	}
	var runtimeMeta struct {
		AssistantMessageID  string                          `json:"assistant_message_id"`
		BackendMessageID    string                          `json:"backend_message_id"`
		MessageID           string                          `json:"message_id"`
		ReasoningDisclosure adapter.ReasoningDisclosure     `json:"reasoning_disclosure"`
		RuntimeEvents       []adapter.SequencedRuntimeEvent `json:"runtime_events"`
		LastSequence        uint64                          `json:"last_sequence"`
	}
	raw := message.Metadata
	if raw == "" || raw == "{}" {
		raw = message.Meta
	}
	_ = json.Unmarshal([]byte(raw), &runtimeMeta)
	message.AssistantMessageID = runtimeMeta.AssistantMessageID
	if message.AssistantMessageID == "" {
		message.AssistantMessageID = message.ID
	}
	message.BackendMessageID = runtimeMeta.BackendMessageID
	if message.BackendMessageID == "" {
		message.BackendMessageID = message.AssistantMessageID
	}
	message.MessageID = runtimeMeta.MessageID
	if message.MessageID == "" {
		message.MessageID = message.AssistantMessageID
	}
	message.ReasoningDisclosure = runtimeMeta.ReasoningDisclosure
	if message.ReasoningDisclosure.Visibility == "" {
		message.ReasoningDisclosure.Visibility = adapter.ReasoningNotExposed
	}
	message.RuntimeEvents = runtimeMeta.RuntimeEvents
	if message.RuntimeEvents == nil {
		message.RuntimeEvents = []adapter.SequencedRuntimeEvent{}
	}
	message.LastSequence = runtimeMeta.LastSequence
}

func mergeStringMetadata(target map[string]string, raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	var values map[string]any
	if json.Unmarshal([]byte(raw), &values) != nil {
		return
	}
	for key, value := range values {
		if text, ok := value.(string); ok {
			target[key] = text
		}
	}
}
