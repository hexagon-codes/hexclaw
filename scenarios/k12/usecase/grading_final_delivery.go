package usecase

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hexagon-codes/hexclaw/adapter/dingtalk"
	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// PrepareAndSendGradingFinalArtifact resolves formal IM content exclusively
// from final_artifact and delegates to the shared durable batch sender.
func (d Deps) PrepareAndSendGradingFinalArtifact(
	ctx context.Context,
	agentName, finalArtifactID string,
) (k12.DeliveryBatch, bool, error) {
	return d.PrepareAndSendGradingFinalArtifactExact(
		ctx, agentName, finalArtifactID, "",
	)
}

// PrepareAndSendGradingFinalArtifactExact verifies the same immutable identity
// used by print/export before creating or replaying a delivery batch.
func (d Deps) PrepareAndSendGradingFinalArtifactExact(
	ctx context.Context,
	agentName, finalArtifactID, expectedDigest string,
) (k12.DeliveryBatch, bool, error) {
	finalArtifact, err := d.getExactGradingFinalArtifact(
		ctx, strings.TrimSpace(agentName), strings.TrimSpace(finalArtifactID),
		strings.TrimSpace(expectedDigest),
	)
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	return d.PrepareAndSendK12FinalReplyBatch(ctx, finalArtifact.AgentName, k12.PrintSourceGradingFinalArtifact,
		finalArtifact.ArtifactID+":"+finalArtifact.ArtifactDigest, finalArtifact.ArtifactID, finalArtifact.ArtifactDigest, DeliveryMessage{}, nil)
}

var finalReplyComments = regexp.MustCompile(`(?s)<!--.*?-->`)
var finalReplyLinks = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)(?:\{[^}]*\})?`)
var finalReplyControls = regexp.MustCompile(`(?m)^[ \t]*(?:#{1,6}[ \t]+|>[ \t]*|[-+*][ \t]+)|^[ \t]*(?:\x60{3,}|~{3,}).*$|^[ \t|:-]+$`)

// K12FinalReplyReadableCharacters 只数用户可见字符，不把链接目标、图片字节或排版语法算作正文。
func K12FinalReplyReadableCharacters(markdown string) int {
	text := finalReplyComments.ReplaceAllString(markdown, "")
	text = finalReplyLinks.ReplaceAllString(text, "$1")
	text = finalReplyControls.ReplaceAllString(text, "")
	text = strings.Map(func(r rune) rune {
		if strings.ContainsRune("*`_$", r) {
			return -1
		}
		return r
	}, text)
	return utf8.RuneCountInString(strings.Join(strings.Fields(html.UnescapeString(text)), " "))
}

func k12FinalReplyUsesPDF(artifact k12.GradingFinalArtifact, intent k12.ImageTaskIntent) bool {
	if intent != k12.ImageTaskIntentBlankWorksheet && intent != k12.ImageTaskIntentCompletedHomework {
		return false
	}
	return (intent == k12.ImageTaskIntentBlankWorksheet && artifact.TotalCount > 1) || K12FinalReplyReadableCharacters(artifact.CanonicalMarkdown) >= 600
}

// K12FinalArtifactPDFTitle 由冻结标题与冻结日期派生文件名，不使用内部任务身份。
func K12FinalArtifactPDFTitle(artifact k12.GradingFinalArtifact) string {
	heading, _, _ := strings.Cut(strings.TrimSpace(artifact.CanonicalMarkdown), "\n")
	heading = strings.TrimSpace(strings.TrimPrefix(heading, "# "))
	if heading == "" {
		heading = "家长辅导指南"
	}
	return heading + " · " + time.Unix(artifact.CreatedAt, 0).In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02")
}

func k12FinalReplySummary(artifact k12.GradingFinalArtifact) string {
	canonical := strings.TrimSpace(artifact.CanonicalMarkdown)
	if split := strings.Index(canonical, "\n### "); split >= 0 {
		summary := strings.TrimSpace(canonical[:split])
		if note := strings.Index(canonical, "\n\n## 说明\n"); note >= 0 {
			summary += "\n\n" + strings.TrimSpace(canonical[note:])
		}
		return summary + "\n\n完整答案与讲法见附件"
	}
	heading, _, _ := strings.Cut(canonical, "\n")
	summary := fmt.Sprintf("%s\n\n共 %d 题 · %d 题结果已整理", heading, artifact.TotalCount, artifact.PublishedCount)
	if artifact.SkippedCount > 0 {
		summary += fmt.Sprintf(" · %d 题未完成判定", artifact.SkippedCount)
	}
	return summary + "\n\n完整答案与讲法见附件"
}

func (d Deps) finalReplyIntent(ctx context.Context, artifact k12.GradingFinalArtifact) (k12.ImageTaskIntent, error) {
	job, err := d.GetGradingJob(ctx, artifact.AgentName, artifact.JobID)
	if errors.Is(err, records.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if job.Fields.SourceKind != "image_task" {
		return "", nil
	}
	id, err := gradingFinalImageTaskDispatchID(job)
	if err != nil {
		return "", err
	}
	dispatch, err := d.Records.GetImageTaskDispatch(ctx, artifact.AgentName, id)
	if err != nil {
		return "", err
	}
	return dispatch.TaskIntent, nil
}

func (d Deps) finalReplyFullMessage(ctx context.Context, artifact k12.GradingFinalArtifact, intent k12.ImageTaskIntent) (DeliveryMessage, error) {
	message := DeliveryMessage{Content: artifact.CanonicalMarkdown}
	if intent == k12.ImageTaskIntentBlankWorksheet {
		return message, nil
	}
	annotated, err := d.Records.OpenGradingFinalAnnotatedAsset(ctx, artifact.AgentName, artifact.ArtifactID)
	if err != nil {
		return DeliveryMessage{}, err
	}
	message.Attachments = []DeliveryAttachment{{Name: "批注原图" + path.Ext(artifact.AnnotatedAssetID), MIME: annotated.MIME, Data: annotated.Data}}
	return message, nil
}

// PrepareAndSendK12FinalReplyBatch 是自动图片回复与显式发送的唯一长指南投影入口。
// 旧计划只查询；全文退路必须是原计划零发送且有明确失败证据。
func (d Deps) PrepareAndSendK12FinalReplyBatch(ctx context.Context, agentName, objectKind, objectID, finalID, expectedDigest string, full DeliveryMessage, targets []ResolvedDeliveryTarget) (k12.DeliveryBatch, bool, error) {
	artifact, err := d.getExactGradingFinalArtifact(ctx, agentName, finalID, expectedDigest)
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	if err = artifact.Validate(); err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	if artifact.ArtifactDigest != k12.ComputeGradingFinalArtifactDigest(artifact) {
		return k12.DeliveryBatch{}, false, errors.New("final artifact digest mismatch")
	}
	existing, lookupErr := d.GetK12FinalReplyBatch(ctx, agentName, objectKind, objectID)
	if lookupErr == nil && !k12storage.PDFPreparationFallbackAllowed(existing) {
		batch, e := d.QueryDeliveryBatch(ctx, agentName, existing.BatchID)
		return batch, false, e
	}
	if lookupErr != nil && !errors.Is(lookupErr, records.ErrNotFound) {
		return k12.DeliveryBatch{}, false, lookupErr
	}
	intent, err := d.finalReplyIntent(ctx, artifact)
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	if full.Content == "" {
		full, err = d.finalReplyFullMessage(ctx, artifact, intent)
		if err != nil {
			return k12.DeliveryBatch{}, false, err
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(full.Content), strings.TrimSpace(artifact.CanonicalMarkdown)) {
		return k12.DeliveryBatch{}, false, errors.New("final reply canonical source mismatch")
	}
	if intent == k12.ImageTaskIntentBlankWorksheet && len(full.Attachments) != 0 {
		return k12.DeliveryBatch{}, false, errors.New("blank worksheet final reply must not contain a grading image")
	}
	if intent != k12.ImageTaskIntentBlankWorksheet {
		if len(full.Attachments) != 1 || full.Attachments[0].MIME != artifact.AnnotatedMIME || strings.TrimPrefix(deliveryDigest(string(full.Attachments[0].Data)), "sha256:") != artifact.AnnotatedDigest {
			return k12.DeliveryBatch{}, false, errors.New("final reply annotated image identity mismatch")
		}
	}
	if lookupErr == nil {
		frozenTargets := deliveryBatchResolvedTargets(existing)
		return d.sendK12FinalReplyFallback(ctx, existing, full, frozenTargets)
	}
	if targets == nil {
		transport, ok := d.Delivery.(BatchDeliveryTransport)
		if !ok {
			return k12.DeliveryBatch{}, false, ErrDeliveryUnavailable
		}
		targets, err = transport.ResolveTextTargets(ctx, agentName)
		if err != nil {
			return k12.DeliveryBatch{}, false, err
		}
	}
	allDing := len(targets) > 0
	for _, target := range targets {
		if target.Target.Platform != "dingtalk" {
			allDing = false
		}
	}
	message := full
	if allDing && k12FinalReplyUsesPDF(artifact, intent) {
		objectID += ":" + imageFinalPDFRenderContractVersion
		pdf, _, prepareErr := d.PrepareGradingFinalArtifactPDFExact(ctx, agentName, finalID, expectedDigest, K12FinalArtifactPDFTitle(artifact))
		if prepareErr == nil && len(pdf.Render.Payload) <= dingtalk.MaxOutboundPDFBytes() {
			message.Content = k12FinalReplySummary(artifact)
			if suffix := strings.TrimSpace(strings.TrimPrefix(full.Content, artifact.CanonicalMarkdown)); suffix != "" {
				message.Content += "\n\n" + suffix
			}
			_, filename := render.SanitizeFilename(strings.ReplaceAll(pdf.Artifact.Title, "/", "-"), "pdf")
			message.Attachments = append(append([]DeliveryAttachment(nil), full.Attachments...), DeliveryAttachment{Name: filename, MIME: "application/pdf", Data: pdf.Render.Payload})
		} else {
			slog.Warn("K12 final reply PDF preparation failed before visible send; preserving full canonical reply", "agent_id", agentName, "final_artifact_id", finalID, "pdf_preparation_failed", prepareErr != nil, "pdf_size_exceeded", prepareErr == nil)
		}
	}
	batch, created, sendErr := d.PrepareAndSendMessageBatchForTargets(ctx, agentName, objectKind, objectID, message, targets)
	if k12storage.PDFPreparationFallbackAllowed(batch) {
		return d.sendK12FinalReplyFallback(ctx, batch, full, targets)
	}
	return batch, created, sendErr
}

// GetK12FinalReplyBatch 不读取或重新生成媒体；旧对象身份始终优先于新投影。
func (d Deps) GetK12FinalReplyBatch(ctx context.Context, agentName, objectKind, objectID string) (k12.DeliveryBatch, error) {
	batch, err := d.Records.GetLatestDeliveryBatchForObject(ctx, agentName, objectKind, objectID)
	if errors.Is(err, records.ErrNotFound) {
		return d.Records.GetLatestDeliveryBatchForObject(ctx, agentName, objectKind, objectID+":"+imageFinalPDFRenderContractVersion)
	}
	return batch, err
}

func K12FinalReplyNeedsFallback(batch k12.DeliveryBatch) bool {
	return k12storage.PDFPreparationFallbackAllowed(batch)
}

func deliveryBatchResolvedTargets(batch k12.DeliveryBatch) []ResolvedDeliveryTarget {
	seen := map[string]bool{}
	out := []ResolvedDeliveryTarget{}
	for _, r := range batch.Receipts {
		key := deliveryEnvelopeTargetKey(r)
		if !seen[key] {
			seen[key] = true
			out = append(out, ResolvedDeliveryTarget{BindingID: r.BindingID, Target: r.Target})
		}
	}
	return out
}

func (d Deps) sendK12FinalReplyFallback(ctx context.Context, source k12.DeliveryBatch, full DeliveryMessage, targets []ResolvedDeliveryTarget) (k12.DeliveryBatch, bool, error) {
	plan, err := d.buildPreparedMessageBatch(ctx, source.AgentName, source.ObjectKind, source.ObjectID, full, targets)
	if err != nil {
		return source, false, err
	}
	batch, created, err := d.Records.PrepareDeliveryPDFFallback(ctx, source.BatchID, plan)
	if err != nil {
		return source, false, err
	}
	if !created {
		queried, e := d.QueryDeliveryBatch(ctx, batch.AgentName, batch.BatchID)
		return queried, false, e
	}
	batch, err = d.sendDeliveryBatch(ctx, batch)
	return batch, true, err
}
