package usecase

import (
	"context"
	"log/slog"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 报告图只说明已由精确有理数核对的数量关系，不改变原求解或批改的证据等级。
func (d Deps) attachReportTeachingDiagram(ctx context.Context, agent string, q RecognizedQuestion, solution, sourceDigest string, guide ParentTeachingGuide) (ParentTeachingGuide, error) {
	if len(guide.TeachingDiagrams) > 0 || d.Records == nil || q.PageAssetID == "" {
		return guide, nil
	}
	quantities := []string{solution, guide.Answer}
	if guide.ReportContent != nil && len(guide.ReportContent.Items) == 1 && guide.ReportContent.Items[0].ProblemID == "current" {
		for _, answer := range guide.ReportContent.Items[0].Answers {
			quantities = append(quantities, answer.Text, answer.LaTeX)
		}
		for _, step := range guide.ReportContent.Items[0].Steps {
			quantities = append(quantities, step.Text, step.LaTeX)
		}
	}
	source, candidate, supported := teachingDiagramQuantityContext(q, strings.Join(quantities, "\n"), sourceDigest)
	if !supported {
		return guide, nil
	}
	diagram := BuildTeachingDiagram(source, candidate)
	if diagram.Status != "ready" {
		return guide, nil
	}
	owner, err := d.Records.ReadyPageAssetOwnerForAgent(ctx, agent, q.PageAssetID)
	if err != nil {
		slog.WarnContext(ctx, "Teaching report diagram omitted", "problem_id", q.ProblemID, "reason", "source_unavailable", "err", err)
		guide.TeachingDiagramWarnings = append(guide.TeachingDiagramWarnings, "Optional teaching diagram source unavailable: "+q.ProblemID)
		return guide, nil
	}
	id, err := d.Records.PutTeachingDiagramAsset(ctx, owner, agent, diagram.SVG, d.now())
	if err != nil {
		slog.WarnContext(ctx, "Teaching report diagram omitted", "problem_id", q.ProblemID, "reason", "storage_unavailable", "err", err)
		guide.TeachingDiagramWarnings = append(guide.TeachingDiagramWarnings, "Optional teaching diagram storage unavailable: "+q.ProblemID)
		return guide, nil
	}
	guide.TeachingDiagrams = []k12.TeachingDiagramAsset{{
		AssetID: id, AssetDigest: diagram.AssetDigest, SpecDigest: diagram.SpecDigest,
		RendererVersion: diagram.RendererVersion, ProblemID: source.ProblemID,
		InputRevision: source.InputRevision, ProblemInputDigest: source.ProblemInputDigest,
		SolutionDigest: source.SolutionDigest, MIME: diagram.MIME,
		Width: diagram.Width, Height: diagram.Height, AltText: diagram.AltText,
	}}
	return guide, nil
}
