package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/internal/mathtext"
	"github.com/hexagon-codes/hexclaw/messagecontent"
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

// K12FinalReplyReadableCharacters 只数用户可见字符，不把链接目标、图片字节或排版语法算作正文。
func K12FinalReplyReadableCharacters(markdown string) int {
	return utf8.RuneCountInString(mathtext.PlainMarkdownText(markdown))
}

func k12FinalReplyUsesPDF(artifact k12.GradingFinalArtifact, intent k12.ImageTaskIntent) bool {
	if intent != k12.ImageTaskIntentBlankWorksheet && intent != k12.ImageTaskIntentCompletedHomework {
		return false
	}
	return FinalReplyUsesPDF(artifact.CanonicalMarkdown, intent == k12.ImageTaskIntentBlankWorksheet && artifact.TotalCount > 1)
}

// K12FinalArtifactPDFTitle 由冻结标题与冻结日期派生文件名，不使用内部任务身份。
func K12FinalArtifactPDFTitle(artifact k12.GradingFinalArtifact) string {
	if artifact.TeachingReport != nil {
		return artifact.TeachingReport.Metadata.Title + " · " + artifact.TeachingReport.Metadata.ReportDate
	}
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
	return EffectiveImageTaskHomeworkIntent(dispatch), nil
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
		if strings.HasSuffix(existing.ObjectID, ":"+k12.DeliveryProjectionVersionIMFinalPDFV1) {
			return d.resumeFinalReplyBatch(ctx, existing)
		}
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
	locale, localeErr := d.finalReplyFrozenLocale(ctx, artifact)
	if localeErr != nil {
		return k12.DeliveryBatch{}, false, localeErr
	}
	if locale != "" {
		disclosure, err := d.finalReplyExecutionSummary(ctx, artifact)
		if err != nil {
			return k12.DeliveryBatch{}, false, err
		}
		req := FinalReplyDeliveryRequest{AgentName: agentName, ObjectKind: objectKind, ObjectID: objectID, SourceKind: k12.PrintSourceGradingFinalArtifact, SourceRef: artifact.ArtifactID + ":" + artifact.ArtifactDigest, Title: K12FinalArtifactPDFTitle(artifact), Message: full, Producer: messagecontent.ProducerK12, Locale: locale, Document: intent == k12.ImageTaskIntentBlankWorksheet && artifact.TotalCount > 1, Summary: k12FinalReplySummary(artifact), DingTalkSuffix: disclosure}
		hasDing := false
		for _, target := range targets {
			hasDing = hasDing || target.Target.Platform == "dingtalk"
		}
		if hasDing && FinalReplyUsesPDF(full.Content, req.Document) {
			pdf, _, prepareErr := d.PrepareGradingFinalArtifactPDFExact(ctx, agentName, finalID, expectedDigest, req.Title)
			if prepareErr == nil {
				req.PDF = &pdf
				req.GradingImageInPDF = intent == k12.ImageTaskIntentCompletedHomework
			}
			// 原图PDF准备已知失败时直接冻结全文，不能另用无原图的通用PDF冒充。
			if prepareErr != nil {
				req.Document = false
				req.Message.Source = nil
				return d.prepareImageFinalFullReply(ctx, req, targets)
			}
		}
		return d.PrepareAndSendFinalReply(ctx, req, targets)
	}
	allDing := len(targets) > 0
	for _, target := range targets {
		if target.Target.Platform != "dingtalk" {
			allDing = false
		}
	}
	if allDing {
		disclosure, disclosureErr := d.finalReplyExecutionSummary(ctx, artifact)
		if disclosureErr != nil {
			return k12.DeliveryBatch{}, false, disclosureErr
		}
		if disclosure != "" {
			full.Content += "\n\n" + disclosure
		}
	}
	message := full
	if allDing && k12FinalReplyUsesPDF(artifact, intent) {
		objectID += ":" + imageFinalPDFRenderContractVersion
		pdf, _, prepareErr := d.PrepareGradingFinalArtifactPDFExact(ctx, agentName, finalID, expectedDigest, K12FinalArtifactPDFTitle(artifact))
		if prepareErr == nil && len(pdf.Render.Payload) <= messagecontent.DingTalkMaxOutboundPDFBytes {
			message.Content = k12FinalReplySummary(artifact)
			if suffix := strings.TrimSpace(strings.TrimPrefix(full.Content, artifact.CanonicalMarkdown)); suffix != "" {
				message.Content += "\n\n" + suffix
			}
			_, filename := render.SanitizeFilename(strings.ReplaceAll(pdf.Artifact.Title, "/", "-"), "pdf")
			message.Attachments = append([]DeliveryAttachment(nil), full.Attachments...)
			if intent == k12.ImageTaskIntentCompletedHomework {
				message.Attachments = nil
			}
			message.Attachments = append(message.Attachments, DeliveryAttachment{Name: filename, MIME: "application/pdf", Data: pdf.Render.Payload})
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

func (d Deps) finalReplyFrozenLocale(ctx context.Context, artifact k12.GradingFinalArtifact) (string, error) {
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
	return dispatch.OperationRouteRequest.MessageLocale, nil
}

// 同源图片PDF准备失败只回退完整正文和必需原图，不转成缺图文档。
func (d Deps) prepareImageFinalFullReply(ctx context.Context, req FinalReplyDeliveryRequest, targets []ResolvedDeliveryTarget) (k12.DeliveryBatch, bool, error) {
	full := req.Message
	var err error
	full.Source, err = finalReplySource(req, full)
	if err != nil {
		return k12.DeliveryBatch{}, false, err
	}
	if req.DingTalkSuffix != "" {
		projected := full
		projected.Content += "\n\n" + req.DingTalkSuffix
		full.Projections = map[string]DeliveryMessage{"dingtalk": projected}
	}
	return d.PrepareAndSendMessageBatchForTargets(ctx, req.AgentName, req.ObjectKind, req.ObjectID+":"+k12.DeliveryProjectionVersionIMFinalPDFV1, full, targets)
}

// finalReplyExecutionSummary 只消费同一终稿的领域回执；历史缺少语言快照时保持原投影。
func (d Deps) finalReplyExecutionSummary(ctx context.Context, artifact k12.GradingFinalArtifact) (string, error) {
	if d.ImageTaskResult == nil {
		return "", nil
	}
	job, err := d.GetGradingJob(ctx, artifact.AgentName, artifact.JobID)
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
	if dispatch.OperationRouteRequest.MessageLocale == "" {
		return "", nil
	}
	result, err := d.ImageTaskResult(ctx, artifact.AgentName, id)
	if err != nil {
		return "", err
	}
	if result.FinalArtifact == nil || result.FinalArtifact.ArtifactID != artifact.ArtifactID || result.FinalArtifact.ArtifactDigest != artifact.ArtifactDigest {
		return "", errors.New("image task execution summary source mismatch")
	}
	facts := messagecontent.ExecutionSummary{}
	steps := make(map[string]bool)
	solved := false
	for _, receipt := range result.OperationReceipts {
		if receipt.Status != "succeeded" || receipt.InvocationID == "" || receipt.ResultDigest == "" {
			continue
		}
		stage := ""
		switch receipt.Operation {
		case "classification", "writing_ocr", "recognizing", "recognize", "recognize_repair":
			stage = "image_recognition"
		case "solve", "solve_generate", "solve_verify":
			stage = "question_solving"
			solved = true
		case "grade":
			stage = "grading"
		case "parent_guide":
			stage = "parent_guide"
		case "annotation":
			stage = "image_annotation"
		case "work_feedback":
			stage = "answer_generation"
		}
		if stage != "" {
			steps[stage] = true
		}
		// 聚合阶段的声明路由不是物理模型调用；只列独立操作或已关联的真实调用单元。
		if receipt.ExecutionKind == string(k12.GradingExecutionProvider) && receipt.Model != "" && (stage != "" || receipt.PhysicalUnit != "") {
			model := messagecontent.ExecutionSummaryModel{Stage: stage, Provider: receipt.Provider, Model: receipt.Model}
			if receipt.Provider == dispatch.OperationRouteRequest.Provider {
				model.ProviderDisplayName = dispatch.OperationRouteRequest.ProviderDisplayName
				model.ProviderInstanceID = dispatch.OperationRouteRequest.ProviderInstanceID
			}
			facts.Models = append(facts.Models, model)
		}
	}
	if solved {
		facts.Tools = append(facts.Tools, messagecontent.ExecutionSummaryTool{Kind: "skill", Name: "solve", Status: "success", DisplayNames: map[string]string{"zh-CN": "解题", "en": "Solve homework", "ug-CN": "مەسىلە يېشىش"}})
	}
	if result.Photo != nil {
		for _, item := range result.Photo.Items {
			if item.ParentGuide != nil {
				steps["parent_guide"] = true
			}
		}
	}
	if d.finalReplyHasCurrentNumericExecution(ctx, artifact, result) {
		steps["numeric_verification"] = true
	}
	if steps["numeric_verification"] {
		facts.Tools = append(facts.Tools, messagecontent.ExecutionSummaryTool{Kind: "tool", Name: "code_exec", Status: "success", DisplayNames: map[string]string{"zh-CN": "沙箱计算", "en": "Sandbox calculation", "ug-CN": "قۇم ساندۇقىدا ھېسابلاش"}})
	}
	for _, key := range []string{"image_recognition", "question_solving", "numeric_verification", "grading", "parent_guide", "image_annotation"} {
		if steps[key] {
			facts.Steps = append(facts.Steps, messagecontent.ExecutionSummaryStep{Key: key, Status: "success"})
		}
	}
	return adapter.DingTalkExecutionSummaryFooter(dispatch.OperationRouteRequest.MessageLocale, facts), nil
}

// 旧资产的验算证明继续用于答案资格；当前工具披露只采用本题本任务的实际执行回执。
func (d Deps) finalReplyHasCurrentNumericExecution(ctx context.Context, artifact k12.GradingFinalArtifact, result ImageTaskResult) bool {
	if d.Records == nil || result.Photo == nil {
		return false
	}
	assessments, err := d.Records.ListGradingAssessmentItems(ctx, artifact.AgentName, artifact.JobID)
	if err != nil {
		slog.Warn("K12 current execution disclosure is unavailable", "agent_id", artifact.AgentName, "job_id", artifact.JobID, "error", err)
		return false
	}
	invocations, err := d.Records.ListGradingItemInvocations(ctx, artifact.AgentName, artifact.JobID)
	if err != nil {
		slog.Warn("K12 current execution disclosure is unavailable", "agent_id", artifact.AgentName, "job_id", artifact.JobID, "error", err)
		return false
	}
	assessmentByProblem := make(map[string]k12.GradingAssessmentItem, len(assessments))
	for _, assessment := range assessments {
		assessmentByProblem[assessment.ProblemID] = assessment
	}
	invocationByID := make(map[string]k12.GradingItemInvocation, len(invocations))
	for _, invocation := range invocations {
		invocationByID[invocation.InvocationID] = invocation
	}
	for _, item := range result.Photo.Items {
		q, evidence := item.Recognized, item.Solve.Evidence
		if item.ResultKind == PhotoItemAssessment {
			evidence = item.Grade.Evidence
		}
		if evidence.EvidenceType != EvidenceNumericExec || evidence.Verdict != VerdictAgree || evidence.VerificationRunID == "" || evidence.VerificationInputDigest == "" {
			continue
		}
		assessment, ok := assessmentByProblem[q.ProblemID]
		if !ok || assessment.AgentName != artifact.AgentName || assessment.JobID != artifact.JobID ||
			assessment.AttemptID != q.AttemptID || assessment.ConfirmedVersion != q.ConfirmedVersion ||
			assessment.InputDigest != q.InputDigest || assessment.SolveInvocationID == "" {
			continue
		}
		current := func(invocation k12.GradingItemInvocation) bool {
			return invocation.AgentName == artifact.AgentName && invocation.JobID == artifact.JobID &&
				invocation.ProblemID == q.ProblemID && invocation.AttemptID == q.AttemptID &&
				invocation.InputRevision == q.ConfirmedVersion && invocation.InputDigest == q.InputDigest &&
				invocation.Status == k12.ModelInvocationSucceeded && invocation.ResultJSON != "" &&
				invocation.ResultDigest == modelInvocationDigest([]byte(invocation.ResultJSON))
		}
		solved, ok := invocationByID[assessment.SolveInvocationID]
		if !ok || !current(solved) {
			continue
		}
		if solved.Operation == k12.GradingItemOperationSolveVerify && finalReplyNumericReceiptMatches(solved, evidence) {
			return true
		}
		if solved.Operation != k12.GradingItemOperationSolve || solved.ExecutionKind != k12.GradingExecutionLocalDeterministic {
			continue
		}
		payload, _, _, decodeErr := decodeGroundedPhysicalPayload(solved.ResultJSON, nil)
		var stored SolveHomeworkResult
		if decodeErr != nil || json.Unmarshal([]byte(payload), &stored) != nil || stored.Evidence != evidence {
			continue
		}
		for _, invocation := range invocations {
			if current(invocation) && invocation.Operation == k12.GradingItemOperationSolveVerify && finalReplyNumericReceiptMatches(invocation, evidence) {
				return true
			}
		}
	}
	return false
}

func finalReplyNumericReceiptMatches(invocation k12.GradingItemInvocation, evidence SolveEvidence) bool {
	payload, _, _, err := decodeGroundedPhysicalPayload(invocation.ResultJSON, nil)
	if err != nil {
		return false
	}
	// 本地程序账本保存返回的JSON字符串；Provider账本直接保存结构化子任务结果。
	var encoded string
	if json.Unmarshal([]byte(payload), &encoded) == nil {
		payload = encoded
	}
	var result struct {
		Receipt *struct {
			InputDigest     string `json:"input_digest"`
			RunID           string `json:"run_id"`
			Status          string `json:"status"`
			ExitCode        int    `json:"exit_code"`
			Timeout         bool   `json:"timeout"`
			RuntimeMissing  bool   `json:"runtime_missing"`
			Error           string `json:"error"`
			StdoutBytes     int64  `json:"stdout_bytes"`
			Stdout          string `json:"stdout"`
			StdoutTruncated bool   `json:"stdout_truncated"`
			StderrTruncated bool   `json:"stderr_truncated"`
			Truncated       bool   `json:"truncated"`
		} `json:"execution_receipt"`
	}
	if json.Unmarshal([]byte(payload), &result) != nil || result.Receipt == nil {
		return false
	}
	receipt := result.Receipt
	return receipt.RunID == evidence.VerificationRunID && receipt.InputDigest == evidence.VerificationInputDigest &&
		receipt.Status == "success" && receipt.ExitCode == 0 && !receipt.Timeout && !receipt.RuntimeMissing &&
		receipt.Error == "" && !receipt.Truncated && !receipt.StdoutTruncated && !receipt.StderrTruncated &&
		receipt.StdoutBytes > 0 && int64(len(receipt.Stdout)) == receipt.StdoutBytes
}

// GetK12FinalReplyBatch 不读取或重新生成媒体；旧对象身份始终优先于新投影。
func (d Deps) GetK12FinalReplyBatch(ctx context.Context, agentName, objectKind, objectID string) (k12.DeliveryBatch, error) {
	batch, err := d.Records.GetLatestDeliveryBatchForObject(ctx, agentName, objectKind, objectID)
	if errors.Is(err, records.ErrNotFound) {
		batch, err = d.Records.GetLatestDeliveryBatchForObject(ctx, agentName, objectKind, objectID+":"+imageFinalPDFRenderContractVersion)
		if errors.Is(err, records.ErrNotFound) {
			return d.Records.GetLatestDeliveryBatchForObject(ctx, agentName, objectKind, objectID+":"+k12.DeliveryProjectionVersionIMFinalPDFV1)
		}
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
		if queried.BatchID == "" {
			queried = batch
		}
		return queried, false, e
	}
	result, sendErr := d.sendDeliveryBatch(ctx, batch)
	if result.BatchID == "" {
		result = batch
	}
	return result, true, sendErr
}
