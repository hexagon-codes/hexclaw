package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// ReexportPrintableArtifact 复用原结果和原图，仅采用当前模板建立新的冻结文件。
func (d Deps) ReexportPrintableArtifact(ctx context.Context, agent, artifactID, revisionKey string) (PrintableArtifactView, bool, error) {
	if revisionKey == "" || len(revisionKey) > 256 || strings.TrimSpace(revisionKey) != revisionKey {
		return PrintableArtifactView{}, false, fmt.Errorf("document revision key is invalid")
	}
	original, err := d.GetPrintableArtifactPDF(ctx, agent, artifactID)
	if err != nil {
		return PrintableArtifactView{}, false, err
	}
	raw, err := d.Records.GetPrintRenderRevision(ctx, agent, artifactID, revisionKey)
	if errors.Is(err, records.ErrNotFound) {
		req, prepareErr := d.printRenderRevisionRequest(ctx, original.Artifact, revisionKey)
		if prepareErr != nil {
			return PrintableArtifactView{}, false, prepareErr
		}
		encoded, encodeErr := json.Marshal(req)
		if encodeErr != nil {
			return PrintableArtifactView{}, false, encodeErr
		}
		raw, err = d.Records.FreezePrintRenderRevision(ctx, agent, artifactID, revisionKey, string(encoded))
	}
	if err != nil {
		return PrintableArtifactView{}, false, err
	}
	var req PreparePrintableArtifactRequest
	if json.Unmarshal([]byte(raw), &req) != nil || req.AgentName != agent || req.SourceKind != original.Artifact.SourceKind || req.CanonicalMarkdown != original.Artifact.CanonicalMarkdown {
		return PrintableArtifactView{}, false, fmt.Errorf("frozen document revision source mismatch")
	}
	return d.PreparePrintableArtifact(ctx, req)
}

func (d Deps) printRenderRevisionRequest(ctx context.Context, original k12.PrintArtifact, key string) (PreparePrintableArtifactRequest, error) {
	req := PreparePrintableArtifactRequest{AgentName: original.AgentName, SourceKind: original.SourceKind, SourceRef: original.SourceRef, Title: original.Title, CanonicalMarkdown: original.CanonicalMarkdown}
	var err error
	if strings.Contains(original.SourceRef, ":teaching-report:") {
		base, _, _ := strings.Cut(original.SourceRef, ":teaching-report:")
		switch original.SourceKind {
		case k12.PrintSourceGradingFinalArtifact:
			id, _, _ := strings.Cut(strings.TrimPrefix(base, "final_artifact:"), ":")
			final, getErr := d.Records.GetGradingFinalArtifact(ctx, original.AgentName, id)
			if getErr != nil {
				return req, getErr
			}
			req, err = d.gradingTeachingReportPrintRequest(ctx, final, original.Title)
			if err != nil {
				return req, err
			}
			if !strings.HasPrefix(req.SourceRef, base+":teaching-report:") {
				return req, fmt.Errorf("report revision final source mismatch")
			}
		case k12.PrintSourceTextSolveFinalization:
			final, getErr := d.Records.TextSolvePrintArtifactSource(ctx, original)
			if getErr != nil {
				return req, getErr
			}
			var gradeRefs []k12.TextGradeReportReference
			if _, suffix, ok := strings.Cut(original.SourceRef, ":grade:"); ok {
				index := strings.LastIndex(suffix, ":sha256:")
				if index <= 0 {
					return req, fmt.Errorf("report revision grade source mismatch")
				}
				gradeRefs = []k12.TextGradeReportReference{{InvocationID: suffix[:index], ResultDigest: suffix[index+1:]}}
			}
			report, reportErr := d.textSolveTeachingReport(ctx, final, gradeRefs...)
			if reportErr != nil {
				return req, reportErr
			}
			if report == nil {
				return req, ErrTeachingReportContentRequired
			}
			req.TeachingReport = &TeachingReportPrintInput{Snapshot: TeachingReportFrozenSnapshot{Report: *report}}
		default:
			return req, fmt.Errorf("report revision source kind is unsupported")
		}
		bundle, bundleErr := FreezeTeachingReportBundle(ctx, d.TeachingReportResources)
		if bundleErr != nil {
			return req, bundleErr
		}
		req.TeachingReport.Snapshot.Bundle = bundle
		req.TeachingReport.Spec = nil
		req.SourceRef = base + ":teaching-report:" + bundle.Reference.Digest
		canonical, canonicalErr := TeachingReportMarkdown(req.TeachingReport.Snapshot.Report)
		if canonicalErr != nil || canonical != original.CanonicalMarkdown {
			return req, fmt.Errorf("report revision changed original content")
		}
	} else if strings.Contains(original.CanonicalMarkdown, "![") || strings.Contains(original.CanonicalMarkdown, "<img") {
		return req, fmt.Errorf("document revision requires its frozen image source")
	}
	sum := sha256.Sum256([]byte(key))
	req.SourceRef += ":render-revision:" + hex.EncodeToString(sum[:])
	if req.TeachingReport == nil {
		req.PDFStyleDigest = d.currentPDFStyleDigest()
	}
	if _, grade, ok := strings.Cut(original.SourceRef, ":grade:"); ok {
		req.SourceRef += ":grade:" + grade
	}
	if req.TeachingReport != nil {
		if renderer, ok := d.Renderer.(TeachingReportSpecRenderer); ok {
			bundle, resolveErr := req.TeachingReport.Snapshot.Bundle.Resolve()
			if resolveErr != nil {
				return req, resolveErr
			}
			spec, resolveErr := renderer.ResolveTeachingReportSpec(ctx, bundle)
			if resolveErr != nil {
				return req, resolveErr
			}
			req.RenderSpecDigest, req.RenderSpecJSON, err = spec.Identity()
			if err != nil {
				return req, err
			}
			req.TeachingReport.Spec = &spec
		}
	}
	return req, nil
}
