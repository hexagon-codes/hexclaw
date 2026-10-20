package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12usecase "github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type imFinalReplyDelivery interface {
	PrepareAndSendFinalReply(context.Context, k12usecase.FinalReplyDeliveryRequest, []k12usecase.ResolvedDeliveryTarget) (k12.DeliveryBatch, bool, error)
	GetPrintableArtifactPDF(context.Context, string, string) (k12usecase.PrintableArtifactView, error)
}

type imFinalReplyDependencies struct {
	delivery          imFinalReplyDelivery
	resolveInstanceID func(string, string) (string, error)
	ownerID           string
	getUnitAttempt    func(context.Context, string, string, string) (k12usecase.UnitSummaryView, error)
	getUnitDocument   func(context.Context, string, string, string, string) (k12usecase.UnitSummaryView, error)
}

// imToolCallDigest 保留各渠道既有投影，钉钉使用不含结果片段的状态尾注。
func imToolCallDigest(calls []adapter.ToolCall, platform string) string {
	if platform == string(adapter.PlatformDingtalk) {
		return adapter.ToolCallStatusDigest(calls)
	}
	return adapter.ToolCallDigest(calls)
}

// imReplyModelSummary 只读取本轮冻结的实际模型身份，供短回复与 PDF 说明共用。
func imReplyModelSummary(reply *adapter.Reply, locale string) string {
	if reply == nil {
		return ""
	}
	model := adapter.ExecutionSummaryModel{
		Stage: "answer_generation", Provider: reply.Metadata["provider"],
		ProviderDisplayName: reply.Metadata["provider_display_name"],
		ProviderInstanceID:  reply.Metadata["provider_instance_id"], Model: reply.Metadata["model"],
	}
	if reply.Usage != nil {
		if strings.TrimSpace(model.Model) == "" {
			model.Model, model.Provider = reply.Usage.Model, reply.Usage.Provider
		} else if strings.TrimSpace(model.Provider) == "" && model.Model == reply.Usage.Model {
			model.Provider = reply.Usage.Provider
		}
	}
	return adapter.RenderExecutionSummary(locale, adapter.ExecutionSummary{Models: []adapter.ExecutionSummaryModel{model}})
}

func newIMFinalReplyDependencies(deps *k12usecase.Deps, resolve func(string, string) (string, error), units *k12usecase.UnitSummaryCoordinator) imFinalReplyDependencies {
	result := imFinalReplyDependencies{delivery: deps, resolveInstanceID: resolve, ownerID: deps.TextbookOwnerID}
	if units != nil {
		result.getUnitAttempt = units.Get
		result.getUnitDocument = units.GetDocument
	}
	return result
}

// maybeDeliverIMFinalReply 只接管成功的私聊终稿，来源仍由持久事件与助手消息证明。
// 批次建立后不再返回适配器正文或处理错误，避免再次发送或释放入站去重记录。
func maybeDeliverIMFinalReply(ctx context.Context, msg *adapter.Message, reply *adapter.Reply, handlerErr error, deps imFinalReplyDependencies) (*adapter.Reply, error) {
	if handlerErr != nil || msg == nil || reply == nil || deps.delivery == nil || deps.resolveInstanceID == nil ||
		msg.Platform != adapter.PlatformDingtalk || msg.Metadata["conversation_type"] != "1" ||
		msg.UserID == "" || msg.ChatID != msg.UserID || msg.ID == "" || msg.InstanceID == "" ||
		len(msg.Attachments) != 0 || len(reply.Attachments) != 0 || reply.Interactive != nil ||
		reply.Metadata["persist_error"] != "" || msg.Metadata["persist_error"] != "" ||
		reply.AssistantMessageID == "" || reply.Metadata["routed_agent"] == "" {
		return reply, handlerErr
	}
	canonical := reply.MessageContent
	locale := msg.Metadata["user_locale"]
	if canonical == nil || canonical.Validate() != nil || canonical.Markdown != reply.Content ||
		len(canonical.Attachments) != 0 || locale == "" || canonical.Locale != locale {
		return reply, handlerErr
	}
	unit, hasUnit, unitErr := imFinalReplyUnitReference(reply.Metadata["artifacts"])
	if unitErr != nil {
		slog.WarnContext(ctx, "IM final reply material reference is invalid", "error", unitErr)
		return reply, handlerErr
	}
	if hasUnit && unit.State != "succeeded" && unit.State != "reused" {
		return reply, handlerErr
	}
	readableBody := canonical.Markdown
	if len(reply.ToolCalls) > 0 {
		readableBody = strings.TrimSuffix(readableBody, imToolCallDigest(reply.ToolCalls, string(msg.Platform)))
	}
	if !hasUnit && !k12usecase.FinalReplyUsesPDF(readableBody, false) {
		return reply, handlerErr
	}
	instanceID, err := deps.resolveInstanceID(string(msg.Platform), msg.InstanceID)
	if err != nil {
		slog.WarnContext(ctx, "IM final reply instance resolution failed", "error", err)
		return reply, handlerErr
	}
	proof := k12.AcceptedPrivateReplySource{
		AgentName: reply.Metadata["routed_agent"], Platform: string(msg.Platform), InstanceID: instanceID,
		SenderID: msg.UserID, MessageID: msg.ID, AssistantMessageID: reply.AssistantMessageID,
	}
	objectID, err := k12.PrivateReplyObjectID(proof)
	if err != nil {
		slog.WarnContext(ctx, "IM final reply source identity is invalid", "error", err)
		return reply, handlerErr
	}
	authorityID, err := k12.PrivateReplyAuthorityID(proof)
	if err != nil {
		slog.WarnContext(ctx, "IM final reply event authority is invalid", "error", err)
		return reply, handlerErr
	}
	request := k12usecase.FinalReplyDeliveryRequest{
		AgentName: proof.AgentName, ObjectKind: "im_final_reply", ObjectID: objectID,
		SourceKind: k12.PrintSourceIMFinalReply, SourceRef: reply.AssistantMessageID,
		Message: k12usecase.DeliveryMessage{Content: readableBody}, Producer: canonical.ProducerKind,
		Locale: locale, PrivateEvent: &proof, DingTalkSuffix: imReplyModelSummary(reply, locale),
	}
	if hasUnit {
		completeDocument, err := fillIMFinalReplyUnitPDF(ctx, deps, unit, &request)
		if err != nil {
			slog.WarnContext(ctx, "IM final reply existing material PDF is unavailable", "error", err)
			return reply, handlerErr
		}
		if !completeDocument {
			return reply, handlerErr
		}
	}
	// 该身份只引用已接纳事件，不创建或冒充路由绑定。
	targets := []k12usecase.ResolvedDeliveryTarget{{BindingID: authorityID, Target: k12.DeliveryTarget{
		Platform: proof.Platform, InstanceID: proof.InstanceID, ChatID: proof.SenderID,
	}}}
	batch, accepted, sendErr := deps.delivery.PrepareAndSendFinalReply(ctx, request, targets)
	if sendErr != nil {
		slog.WarnContext(ctx, "IM final reply delivery failed", "batch_id", batch.BatchID, "error", sendErr)
	}
	if accepted || batch.BatchID != "" {
		return nil, nil
	}
	// 未形成可见发送批次时保留完整原回复，准备失败不能丢失业务正文。
	return reply, handlerErr
}

type imFinalReplyUnitArtifact struct {
	Kind          string `json:"kind"`
	State         string `json:"state"`
	AttemptID     string `json:"attempt_id"`
	DocumentID    string `json:"document_id"`
	RevisionID    string `json:"revision_id"`
	ArtifactID    string `json:"artifact_id"`
	ContentDigest string `json:"content_digest"`
}

func imFinalReplyUnitReference(encoded string) (imFinalReplyUnitArtifact, bool, error) {
	if strings.TrimSpace(encoded) == "" {
		return imFinalReplyUnitArtifact{}, false, nil
	}
	var references []imFinalReplyUnitArtifact
	if err := json.Unmarshal([]byte(encoded), &references); err != nil {
		return imFinalReplyUnitArtifact{}, false, fmt.Errorf("invalid artifact references")
	}
	var selected imFinalReplyUnitArtifact
	found := false
	for _, ref := range references {
		if ref.Kind != "unit_summary" {
			continue
		}
		if found && ref != selected {
			return imFinalReplyUnitArtifact{}, false, fmt.Errorf("multiple unit material references are not a single final document")
		}
		selected, found = ref, true
	}
	return selected, found, nil
}

// fillIMFinalReplyUnitPDF 只读取同一归属的精确资料版本及已冻结 PDF，不触发生成或渲染。
func fillIMFinalReplyUnitPDF(ctx context.Context, deps imFinalReplyDependencies, ref imFinalReplyUnitArtifact, request *k12usecase.FinalReplyDeliveryRequest) (bool, error) {
	if deps.ownerID == "" || deps.getUnitAttempt == nil || deps.getUnitDocument == nil || ref.AttemptID == "" || ref.DocumentID == "" || ref.RevisionID == "" || ref.ArtifactID == "" || ref.ContentDigest == "" {
		return false, fmt.Errorf("unit material reference is incomplete")
	}
	attempt, err := deps.getUnitAttempt(ctx, deps.ownerID, request.AgentName, ref.AttemptID)
	if err != nil {
		return false, err
	}
	if attempt.Job.AttemptID != ref.AttemptID || attempt.Job.AgentName != request.AgentName || attempt.Job.OwnerID != deps.ownerID ||
		attempt.Job.State != ref.State || !attempt.Delivery.Complete || attempt.Job.DocumentID != ref.DocumentID ||
		attempt.Job.ResultRevisionID != ref.RevisionID || attempt.Job.ArtifactID != ref.ArtifactID {
		return false, fmt.Errorf("unit material attempt does not match the final reply")
	}
	// 只读本次任务已判定的完整资料动作，普通追问不追加文件。
	switch attempt.Job.Intent {
	case "generate", "revise", "open":
	default:
		return false, nil
	}
	view, err := deps.getUnitDocument(ctx, deps.ownerID, request.AgentName, ref.DocumentID, ref.RevisionID)
	if err != nil {
		return false, err
	}
	material := view.Material
	if !view.Delivery.Complete || view.Document == nil || view.Document.AgentName != request.AgentName ||
		view.Document.DocumentID != ref.DocumentID || material == nil || material.AgentName != request.AgentName ||
		material.DocumentID != ref.DocumentID || material.RevisionID != ref.RevisionID ||
		material.ArtifactID != ref.ArtifactID || material.ContentDigest != ref.ContentDigest {
		return false, fmt.Errorf("unit material source identity does not match the final reply")
	}
	pdf, err := deps.delivery.GetPrintableArtifactPDF(ctx, request.AgentName, ref.ArtifactID)
	if err != nil {
		return false, err
	}
	if pdf.Artifact.AgentName != request.AgentName || pdf.Artifact.ArtifactID != ref.ArtifactID ||
		pdf.Artifact.SourceKind != k12.PrintSourceUnitSummary || pdf.Artifact.CanonicalMarkdown != material.CanonicalMarkdown ||
		pdf.Render.ArtifactID != ref.ArtifactID || pdf.Render.ByteDigest != material.ByteDigest || pdf.Render.ByteSize != material.ByteSize {
		return false, fmt.Errorf("unit material PDF does not match the frozen revision")
	}
	source, err := json.Marshal(struct {
		DocumentID    string `json:"document_id"`
		RevisionID    string `json:"revision_id"`
		ArtifactID    string `json:"artifact_id"`
		ContentDigest string `json:"content_digest"`
	}{ref.DocumentID, ref.RevisionID, ref.ArtifactID, ref.ContentDigest})
	if err != nil {
		return false, err
	}
	request.SourceKind, request.SourceRef = k12.PrintSourceUnitSummary, string(source)
	request.Title, request.Document, request.PDF = material.Title, true, &pdf
	if !k12usecase.FinalReplyUsesPDF(request.Message.Content, false) {
		request.Summary = request.Message.Content
	}
	return true, nil
}
