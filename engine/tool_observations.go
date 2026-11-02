package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/hexagon-codes/ai-core/llm"
	coretool "github.com/hexagon-codes/ai-core/tool"
	hexmcp "github.com/hexagon-codes/hexagon/mcp"
	hruntime "github.com/hexagon-codes/hexagon/runtime"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/internal/externaltools"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
)

// captureToolObservations 只保存真实媒体字节，原有 Runtime 消息继续拥有观察上下文。
// 同一执行已有的浏览器截图按内容摘要复用，不产生第二份媒体状态或重复文件。
func (e *ToolExecutor) captureToolObservations(ctx context.Context, toolName string, raw any, executionErr error) (any, error) {
	if e.externalTools == nil {
		return raw, executionErr
	}
	media, ok := raw.(interface{ ToolMessageContent() []llm.ContentPart })
	if !ok {
		return raw, executionErr
	}
	parts := media.ToolMessageContent()
	var images []hexmcp.BinaryContent
	for _, part := range parts {
		image, recognized, err := hexmcp.ParseImagePart(part, fmt.Sprintf("%s-observation-%d", toolName, len(images)+1))
		if err != nil {
			return raw, errors.Join(executionErr, err)
		}
		if recognized {
			images = append(images, image)
		}
	}
	var attachments []adapter.Attachment
	if source, hasAttachments := raw.(interface{ ToolAttachments() []adapter.Attachment }); hasAttachments {
		attachments = source.ToolAttachments()
	}
	if len(images) == 0 && len(attachments) == 0 {
		return raw, executionErr
	}
	detail := toolExecutionDetail(raw)
	executionID := skill.ExternalExecutionID(ctx)
	if detail != nil && detail.ExecutionID != "" {
		executionID = detail.ExecutionID
	}
	if executionID == "" {
		return raw, errors.Join(executionErr, errors.New("tool image persistence requires the original external execution identity"))
	}
	ctx = skill.WithExternalExecution(ctx, executionID)
	invocation := skill.CurrentInvocation(ctx)
	receipt, err := e.externalTools.Receipt(ctx, invocation.AuthenticatedUserID, executionID)
	if err != nil {
		return raw, errors.Join(executionErr, fmt.Errorf("tool result was received but its image ownership receipt could not be read; do not repeat the original action: %w", err))
	}
	if receipt.OwnerID != invocation.AuthenticatedUserID || receipt.RequestID != invocation.RequestID {
		return raw, errors.Join(executionErr, errors.New("tool image receipt does not belong to the current request"))
	}
	var manifest struct {
		Artifacts []externaltools.Artifact `json:"artifacts"`
	}
	if len(receipt.ResultManifestJSON) > 0 {
		if err = json.Unmarshal(receipt.ResultManifestJSON, &manifest); err != nil {
			return raw, errors.Join(executionErr, fmt.Errorf("tool result was received but its image ownership receipt is invalid; do not repeat the original action: %w", err))
		}
	}
	if sourceID, generation, projected := toolObservationSource(raw); projected {
		source, sourceErr := e.verifyToolObservationSource(ctx, receipt, sourceID, generation)
		if sourceErr != nil {
			return raw, errors.Join(executionErr, sourceErr)
		}
		if source.ID != receipt.ID && len(source.ResultManifestJSON) > 0 {
			var sourceManifest struct {
				Artifacts []externaltools.Artifact `json:"artifacts"`
			}
			if err = json.Unmarshal(source.ResultManifestJSON, &sourceManifest); err != nil {
				return raw, errors.Join(executionErr, fmt.Errorf("browser image ownership receipt is invalid: %w", err))
			}
			manifest.Artifacts = append(manifest.Artifacts, sourceManifest.Artifacts...)
		}
	}
	existing := make(map[string]map[string]any, len(manifest.Artifacts))
	type fileIdentity struct{ digest, name, mime string }
	existingFiles := make(map[fileIdentity]map[string]any, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		reference := map[string]any{
			"artifact_id": artifact.ID, "name": artifact.Name, "mime": artifact.MIME,
			"path": artifact.Path, "digest": artifact.Digest, "size": artifact.Size,
			"url": "/api/v1/files/generated/" + artifact.Path,
		}
		existing[artifact.Digest] = reference
		if artifact.Deliver {
			existingFiles[fileIdentity{artifact.Digest, artifact.Name, artifact.MIME}] = reference
		}
	}
	seen := make(map[string]bool)
	seenFiles := make(map[fileIdentity]bool)
	refs := make([]map[string]any, 0, len(images)+len(attachments))
	for index, attachment := range attachments {
		if attachment.Source == nil || attachment.Source.OpenReader == nil {
			return raw, errors.Join(executionErr, errors.New("tool binary content has no original byte source"))
		}
		reader, openErr := attachment.Source.OpenReader(ctx)
		if openErr != nil {
			return raw, errors.Join(executionErr, fmt.Errorf("open tool binary content: %w", openErr))
		}
		if reader == nil {
			return raw, errors.Join(executionErr, errors.New("tool binary source returned no byte reader"))
		}
		data, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		if err = errors.Join(readErr, closeErr); err != nil {
			return raw, errors.Join(executionErr, fmt.Errorf("read complete tool binary content: %w", err))
		}
		hash := sha256.Sum256(data)
		digest := "sha256:" + hex.EncodeToString(hash[:])
		name := attachment.Name
		if name == "" {
			name = fmt.Sprintf("%s-file-%d.bin", toolName, index+1)
		}
		identity := fileIdentity{digest, name, attachment.Mime}
		// 同字节的不同名称或 MIME 仍是独立交付；过程观察不能代替用户文件。
		if seenFiles[identity] {
			continue
		}
		seenFiles[identity] = true
		seen[digest] = true
		reference := existingFiles[identity]
		if reference == nil {
			reference, err = e.externalTools.SaveArtifact(ctx, name, attachment.Mime, bytes.NewReader(data))
			if err != nil {
				return raw, errors.Join(executionErr, fmt.Errorf("tool result was received but binary persistence failed; do not repeat the original action: %w", err))
			}
			existing[digest] = reference
			existingFiles[identity] = reference
		}
		refs = append(refs, reference)
	}
	for _, image := range images {
		descriptor := image.Descriptor()
		digest := descriptor.Digest
		if seen[digest] {
			continue
		}
		seen[digest] = true
		reference := existing[digest]
		if reference == nil {
			reference, err = e.externalTools.SaveObservation(ctx, descriptor.Name, descriptor.MIMEType, bytes.NewReader(image.Data))
			if err != nil {
				return raw, errors.Join(executionErr, fmt.Errorf("tool result was received but image persistence failed; do not repeat the original action: %w", err))
			}
		}
		refs = append(refs, reference)
	}
	encoded, err := json.Marshal(refs)
	if err != nil {
		return raw, errors.Join(executionErr, errors.New("tool image references could not be encoded"))
	}
	result, ok := raw.(*skill.Result)
	if !ok {
		result = &skill.Result{Data: raw}
		raw = result
	}
	if result.Metadata == nil {
		result.Metadata = make(map[string]string)
	}
	result.Metadata[skill.ToolObservationRefsMetadataKey] = string(encoded)
	return raw, executionErr
}

// toolObservationSource 只接受应用 Go 投影携带的派发身份，不读取上游 JSON 的 _meta。
func toolObservationSource(raw any) (string, uint64, bool) {
	switch value := raw.(type) {
	case *skill.Result:
		if value != nil {
			return toolObservationSource(value.Data)
		}
	case coretool.Result:
		return toolObservationSource(value.Output)
	case *coretool.Result:
		if value != nil {
			return toolObservationSource(value.Output)
		}
	case map[string]any:
		if result, ok := value["result"].(coretool.Result); ok {
			return toolObservationSource(result.Output)
		}
	case interface {
		ToolArtifactExecutionID() string
		ToolArtifactBrowserGeneration() uint64
	}:
		return value.ToolArtifactExecutionID(), value.ToolArtifactBrowserGeneration(), true
	}
	return "", 0, false
}

// verifyToolObservationSource 以持久回执确认图片来自当前调用的同一浏览器代次。
// 父链是已存在的执行归属，不为媒体另建可伪造的路径或身份映射。
func (e *ToolExecutor) verifyToolObservationSource(ctx context.Context, current storage.ExternalToolExecution, sourceID string, generation uint64) (storage.ExternalToolExecution, error) {
	source, err := e.externalTools.Receipt(ctx, current.OwnerID, sourceID)
	if err != nil {
		return storage.ExternalToolExecution{}, fmt.Errorf("browser image execution receipt could not be read: %w", err)
	}
	if source.BrowserGeneration != generation {
		return storage.ExternalToolExecution{}, errors.New("browser image generation does not match its execution receipt")
	}
	seen := make(map[string]bool)
	for node := source; ; {
		if node.OwnerID != current.OwnerID || node.RequestID != current.RequestID {
			return storage.ExternalToolExecution{}, errors.New("browser image execution does not belong to the current request")
		}
		if node.BrowserGeneration != 0 && node.BrowserGeneration != generation {
			return storage.ExternalToolExecution{}, errors.New("browser image parent generation does not match its execution receipt")
		}
		if node.ID == current.ID {
			return source, nil
		}
		if node.ParentExecutionID == "" || seen[node.ID] {
			return storage.ExternalToolExecution{}, errors.New("browser image execution is not a child of the current tool call")
		}
		seen[node.ID] = true
		node, err = e.externalTools.Receipt(ctx, current.OwnerID, node.ParentExecutionID)
		if err != nil {
			return storage.ExternalToolExecution{}, fmt.Errorf("browser image parent receipt could not be read: %w", err)
		}
	}
}

// prepareToolObservations 仅治理工具派生的媒体，不改真实用户原图或全局选模。
// 请求副本保留原始 Runtime 媒体，失败后的 Provider 回退仍可重新按实际模型准备。
func prepareToolObservations(ctx context.Context, req llm.CompletionRequest, provider llm.Provider, model string) (context.Context, llm.CompletionRequest) {
	var vision, capabilityChecked bool
	var copied bool
	for index, message := range req.Messages {
		if message.Name != hruntime.ToolMediaMessageName || !messageHasImagePart(message) {
			continue
		}
		if !capabilityChecked {
			vision = providerModelSupportsImageInput(provider, model)
			capabilityChecked = true
		}
		if !vision {
			if !copied {
				req.Messages = append([]llm.Message(nil), req.Messages...)
				copied = true
			}
			parts := make([]llm.ContentPart, 0, len(message.MultiContent)+1)
			for _, part := range message.MultiContent {
				if part.Type != "image_url" {
					parts = append(parts, part)
				}
			}
			parts = append(parts, llm.NewTextPart("The selected model has no confirmed image-input capability. This image was not observed; use the tool text, page snapshot, and registered file references."))
			req.Messages[index].MultiContent = parts
		}
	}
	return ctx, req
}

// labelToolObservationEgress 在图片预算之后按实际发送的媒体复用既有视觉用途。
// 原审计身份、累积数据分类和记忆标记保持；其它已确认用途不被替换。
func labelToolObservationEgress(ctx context.Context, req llm.CompletionRequest) context.Context {
	var images bool
	for _, message := range req.Messages {
		if message.Name == hruntime.ToolMediaMessageName && messageHasImagePart(message) {
			images = true
			break
		}
	}
	if !images {
		return ctx
	}
	requests, exists := egress.RequestsFromContext(ctx)
	if !exists || len(requests) == 0 {
		return egress.AddDataClasses(ctx, egress.ClassSensitiveMedia)
	}
	if requests[0].Purpose != egress.PurposeGeneralChat {
		return egress.AddDataClasses(ctx, egress.ClassSensitiveMedia)
	}
	classes := make([]egress.DataClass, 0, len(requests)+1)
	for _, request := range requests {
		classes = append(classes, request.DataClass)
	}
	classes = append(classes, egress.ClassSensitiveMedia)
	ctx = egress.WithRequest(ctx, egress.PurposeVisionChat, requests[0].AuditID, classes...)
	if requests[0].ChatMemoryEnabled {
		egress.EnableChatMemory(ctx)
	}
	return ctx
}
