package usecase

import (
	"context"
	"fmt"

	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func (d Deps) renderTeachingReport(ctx context.Context, input TeachingReportPrintInput) ([]byte, string, error) {
	renderer, ok := d.Renderer.(TeachingReportRenderer)
	if !ok {
		return nil, "", ErrRenderUnavailable
	}
	bundle, err := input.Snapshot.Bundle.Resolve()
	if err != nil {
		return nil, "", err
	}
	logo := render.PDFBrandImage()
	logoDigest := teachingReportResourceDigest(logo)
	assets := make(map[string]TeachingReportAsset, len(input.Assets)+1)
	for id, asset := range input.Assets {
		assets[id] = asset
	}
	assets["pdf-brand"] = TeachingReportAsset{Data: logo, MIME: "image/png"}
	branding := TeachingReportBranding{
		Name: "HexClaw 河蟹 AI", Tagline: "本地云端均可部署 · 数据自主掌控 · 软件开源免费",
		WebsiteURL: "https://hexclaw.net/", WebsiteLabel: "hexclaw.net", WeChatID: "hexclaw",
		GitHubURL: "https://github.com/hexagon-codes/hexclaw-desktop", GitHubLabel: "github.com/hexagon-codes/hexclaw-desktop",
		LogoAssetID: "pdf-brand", LogoAssetDigest: logoDigest, LogoMIME: "image/png",
	}
	project := ProjectTeachingReportPDF
	if bundle.InputSchema() == "homework-report-render-v2" {
		project = ProjectTeachingReportPDFV2
	}
	projection, err := project(input.Snapshot.Report, branding, assets)
	if err != nil {
		return nil, "", err
	}
	return renderer.RenderTeachingReport(ctx, bundle, render.ReportRenderInput{Document: projection.Document, Formulas: projection.Formulas, Resources: projection.Resources})
}

// prepareTeachingReportPrintRequest 只用实际来源与冻结内容建立既有PrintArtifact。
func prepareTeachingReportPrintRequest(agent, sourceRef, title string, snapshot TeachingReportFrozenSnapshot, assets map[string]TeachingReportAsset) (PreparePrintableArtifactRequest, error) {
	report, err := k12.NewTeachingReport(snapshot.Report)
	if err != nil {
		return PreparePrintableArtifactRequest{}, err
	}
	snapshot.Report = report
	if _, err := snapshot.Bundle.Resolve(); err != nil {
		return PreparePrintableArtifactRequest{}, err
	}
	canonical, err := TeachingReportMarkdown(report)
	if err != nil {
		return PreparePrintableArtifactRequest{}, err
	}
	kind := k12.PrintSourceGradingFinalArtifact
	if report.Source.Kind == k12.TeachingReportSourceTextSolve {
		kind = k12.PrintSourceTextSolveFinalization
	}
	if title == "" {
		title = report.Metadata.Title + " · " + report.Metadata.ReportDate
	}
	if sourceRef == "" {
		return PreparePrintableArtifactRequest{}, fmt.Errorf("report source reference is required")
	}
	return PreparePrintableArtifactRequest{AgentName: agent, SourceKind: kind,
		SourceRef: sourceRef + ":teaching-report:" + snapshot.Bundle.Reference.Digest,
		Title:     title, CanonicalMarkdown: canonical, TeachingReport: &TeachingReportPrintInput{Snapshot: snapshot, Assets: assets}}, nil
}
