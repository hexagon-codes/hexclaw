package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

const writingUnreadableMarker = "[无法识别]"

func classificationOCREvidence(classified ImageTaskClassification) *k12.CreativeWorkIntakeOCREvidence {
	if classified.Intent != k12.ImageTaskIntentWriting || classified.WritingOCR == nil {
		return nil
	}
	ocr := classified.WritingOCR
	canonical := strings.TrimSpace(ocr.CanonicalContent)
	if strings.TrimSpace(ocr.Raw) == "" || canonical == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(canonical))
	evidence := &k12.CreativeWorkIntakeOCREvidence{
		Raw: strings.TrimSpace(ocr.Raw), CanonicalContent: canonical,
		CanonicalVersion: 1, CanonicalDigest: "sha256:" + hex.EncodeToString(sum[:]),
		Confidence: ocr.Confidence, RiskSegments: append([]k12.CreativeWorkIntakeOCRRisk(nil), ocr.RiskSegments...),
	}
	if len(evidence.RiskSegments) == 0 && (ocr.Confidence < .95 || strings.Contains(canonical, writingUnreadableMarker)) {
		evidence.RiskSegments = []k12.CreativeWorkIntakeOCRRisk{{SegmentID: "document", RawText: canonical, Reasons: []string{"document_unreadable"}}}
	}
	return evidence
}

// 唯一定位是局部复核和排除的前提；整篇/关键歧义不能用部分文字冒充完整原稿。
func writingRisksLocatable(e k12.CreativeWorkIntakeOCREvidence) bool {
	seen := make(map[string]bool)
	var ranges [][2]int
	for _, risk := range e.RiskSegments {
		if risk.SegmentID == "document" || seen[risk.SegmentID] || strings.TrimSpace(risk.RawText) == "" ||
			strings.Count(e.CanonicalContent, risk.RawText) != 1 {
			return false
		}
		for _, reason := range risk.Reasons {
			if reason == "critical_content_unreadable" || reason == "document_unreadable" {
				return false
			}
		}
		seen[risk.SegmentID] = true
		start := strings.Index(e.CanonicalContent, risk.RawText)
		end := start + len(risk.RawText)
		for _, prior := range ranges {
			if start < prior[1] && end > prior[0] {
				return false
			}
		}
		ranges = append(ranges, [2]int{start, end})
	}
	return len(e.RiskSegments) > 0
}

func boundedWritingEvidence(e k12.CreativeWorkIntakeOCREvidence, review []k12.CreativeWorkOCRReviewSegment) k12.CreativeWorkIntakeOCREvidence {
	e.OriginalCanonical = e.CanonicalContent
	e.Review = review
	e.CanonicalVersion++
	e.Outcome = "unreadable"
	e.UnresolvedSegments = nil
	if writingRisksLocatable(e) {
		resolved := make(map[string]string)
		for _, segment := range review {
			if segment.Readable && strings.TrimSpace(segment.Text) != "" && strings.TrimSpace(segment.VisualEvidence) != "" {
				resolved[segment.SegmentID] = segment.Text
			}
		}
		// 同时替换原文中的独立区间，避免先替换的读法被后一个片段再次匹配。
		type span struct {
			start, end int
			text       string
		}
		spans := make([]span, 0, len(e.RiskSegments))
		for _, risk := range e.RiskSegments {
			start := strings.Index(e.OriginalCanonical, risk.RawText)
			replacement, ok := resolved[risk.SegmentID]
			if !ok {
				replacement = writingUnreadableMarker
				e.UnresolvedSegments = append(e.UnresolvedSegments, risk)
			}
			spans = append(spans, span{start, start + len(risk.RawText), replacement})
		}
		var b strings.Builder
		valid := true
		for offset := 0; offset < len(e.OriginalCanonical); {
			next := -1
			for index, s := range spans {
				if s.start >= offset && (next < 0 || s.start < spans[next].start) {
					next = index
				}
			}
			if next < 0 {
				b.WriteString(e.OriginalCanonical[offset:])
				break
			}
			s := spans[next]
			for index, other := range spans {
				if index != next && other.start < s.end && other.end > s.start {
					valid = false
				}
			}
			b.WriteString(e.OriginalCanonical[offset:s.start])
			b.WriteString(s.text)
			offset = s.end
		}
		if valid {
			e.CanonicalContent = b.String()
			reliable := strings.ReplaceAll(e.CanonicalContent, writingUnreadableMarker, "")
			if strings.IndexFunc(reliable, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) >= 0 {
				e.Outcome = "complete"
				if len(e.UnresolvedSegments) > 0 {
					e.Outcome = "partial"
				}
			}
		}
	}
	if e.Outcome == "unreadable" {
		e.CanonicalContent = ""
		e.UnresolvedSegments = append([]k12.CreativeWorkIntakeOCRRisk(nil), e.RiskSegments...)
	}
	sum := sha256.Sum256([]byte(e.CanonicalContent))
	e.CanonicalDigest = "sha256:" + hex.EncodeToString(sum[:])
	return e
}

func (c *ImageTaskCoordinator) reviewWritingOCR(ctx context.Context, dispatch k12.ImageTaskDispatch, intake k12.CreativeWorkIntake, prior k12.ImageTaskInvocation, images [][]byte) (k12.CreativeWorkIntake, error) {
	if intake.OCREvidence == nil {
		return intake, fmt.Errorf("writing review requires persisted OCR evidence")
	}
	evidence := *intake.OCREvidence
	reviewer, supported := c.WritingOCR.(ImageTaskWritingOCRReviewer)
	reviewKey := "intake:" + intake.IntakeID + ":writing-ocr-review"
	invocation := prior
	var review []k12.CreativeWorkOCRReviewSegment
	if supported && writingRisksLocatable(evidence) && (dispatch.AutomaticDeadlineAt == 0 || dispatch.AutomaticDeadlineAt > c.now()) {
		if prior.OperationKey != reviewKey {
			route := prior.RouteSnapshot
			route.PromptVersion = "creative-work-writing-local-review-v1"
			prepared, _, err := c.Records.PrepareImageTaskInvocation(ctx, k12.ImageTaskInvocation{
				InvocationID: c.id("writing_review"), AgentName: intake.AgentName, IntakeID: intake.IntakeID,
				Operation: k12.ImageTaskOperationWritingOCR, OperationKey: reviewKey,
				RequestDigest: digestJSON(struct {
					Source string
					Risks  []k12.CreativeWorkIntakeOCRRisk
				}{intake.SourceDigest, evidence.RiskSegments}),
				RouteSnapshot: route, Status: k12.ImageTaskInvocationPrepared, Attempt: prior.Attempt + 1,
				DeadlineAt: dispatch.AutomaticDeadlineAt, CreatedAt: c.now(), UpdatedAt: c.now(),
			})
			if err != nil {
				return intake, err
			}
			invocation = prepared
		}
		if invocation.Status != k12.ImageTaskInvocationPrepared {
			return intake, nil
		}
		if len(images) == 0 {
			return intake, fmt.Errorf("writing review requires immutable source image")
		}
		claimed, sent, err := c.Records.ClaimImageTaskInvocationSend(ctx, intake.AgentName, invocation.InvocationID, "image-task:"+dispatch.DispatchID+":writing-ocr-review", c.now())
		if err != nil {
			return intake, err
		}
		if !sent {
			return c.Records.GetCreativeWorkIntake(ctx, intake.AgentName, intake.IntakeID)
		}
		invocation = claimed
		automaticCtx, cancelAutomatic := withImageTaskAutomaticWindow(ctx, dispatch.DispatchID, invocation.DeadlineAt, c.now())
		providerCtx, cancelProvider := imageTaskProviderContext(automaticCtx, invocation.RouteSnapshot)
		started := time.Now()
		review, err = reviewer.ReviewImageTaskWriting(providerCtx, images[0], evidence.RiskSegments)
		contextErr := providerCtx.Err()
		cancelProvider()
		cancelAutomatic()
		slog.Info("K12 writing local review finished", "dispatch_id", dispatch.DispatchID, "intake_id", intake.IntakeID, "invocation_id", invocation.InvocationID,
			"provider", invocation.RouteSnapshot.Provider, "model", invocation.RouteSnapshot.Model, "elapsed_ms", time.Since(started).Milliseconds(),
			"risk_segments", len(evidence.RiskSegments), "review_segments", len(review), "context_error", contextErr, "error", err)
		if err != nil {
			unknown := sentProviderOutcomeUnknown(err, contextErr)
			kind := "writing_ocr_review_failed"
			if unknown {
				kind = "writing_ocr_review_outcome_unknown"
			}
			if errors.Is(err, k12.ErrModelCapabilityUnverified) {
				kind = "model_capability_unverified"
			}
			persistErr := c.Records.FailImageTaskInvocation(context.WithoutCancel(ctx), intake.AgentName, invocation.InvocationID, kind, unknown, !unknown && kind != "model_capability_unverified")
			if persistErr != nil {
				return intake, errors.Join(err, persistErr)
			}
			return intake, err
		}
	} else if prior.OperationKey == reviewKey && prior.Status != k12.ImageTaskInvocationPrepared && prior.Status != k12.ImageTaskInvocationSucceeded {
		// 预算耗尽也不能把已发送未知调用的技术状态转成内容结果。
		return intake, nil
	}
	evidence = boundedWritingEvidence(evidence, review)
	evidence.FrozenAt = c.now()
	return c.Records.FreezeCreativeWorkIntakeOCR(context.WithoutCancel(ctx), intake.AgentName, intake.IntakeID, intake.Version, invocation.InvocationID, evidence, k12.CreativeWorkEvidenceBoundedReview)
}
