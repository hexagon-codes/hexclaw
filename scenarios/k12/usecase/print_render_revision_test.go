package usecase

import (
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestTeachingReportReexportKeepsContentAndModelReceiptsSQLite(t *testing.T) {
	d, solver, _ := reportResilienceDeps(t)
	teaching := &reportResilienceTeaching{validContent: true}
	d.ParentTeachingGuide, d.ParentTeachingGuideAudit = teaching, teaching
	renderer := &reportResilienceRenderer{}
	d.Renderer = renderer
	origin := k12.TextSolveOrigin{OwnerID: DefaultLocalOwnerScope, RunID: "explicit-report-revision", InputRevision: 1}
	request := GradeRequest{AgentName: "mingming", Subject: "数学", Grade: "五年级上", Problem: reportResilienceProblem}
	solved, err := d.SolveTextProblem(t.Context(), origin, request)
	if err != nil || solved.Finalization == nil {
		t.Fatalf("source solve: %+v %v", solved, err)
	}
	original, _, err := d.PrepareTextSolveTeachingReportPDF(t.Context(), origin.OwnerID, request.AgentName, origin.RunID, solved.Finalization.FinalizationID, solved.Finalization.ResultDigest, "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := d.Records.ListTextSolveInvocations(t.Context(), origin.OwnerID, request.AgentName, origin.RunID)
	if err != nil {
		t.Fatal(err)
	}
	revision, replay, err := d.ReexportPrintableArtifact(t.Context(), request.AgentName, original.Artifact.ArtifactID, "trusted-render-1")
	if err != nil || replay || revision.Artifact.ArtifactID == original.Artifact.ArtifactID || revision.Artifact.CanonicalMarkdown != original.Artifact.CanonicalMarkdown || renderer.calls != 2 {
		t.Fatalf("new template revision: %+v replay=%t err=%v", revision, replay, err)
	}
	d.TeachingReportResources = nil
	again, replay, err := d.ReexportPrintableArtifact(t.Context(), request.AgentName, original.Artifact.ArtifactID, "trusted-render-1")
	if err != nil || !replay || again.Artifact.ArtifactID != revision.Artifact.ArtifactID || renderer.calls != 2 {
		t.Fatalf("revision replay read changed resources: %+v %v", again, err)
	}
	after, err := d.Records.ListTextSolveInvocations(t.Context(), origin.OwnerID, request.AgentName, origin.RunID)
	if err != nil || modelInvocationResultDigest(before) != modelInvocationResultDigest(after) || solver.calls != 1 || teaching.generated != 1 || teaching.audited != 1 {
		t.Fatalf("re-export reran the model: calls=%d/%d/%d err=%v", solver.calls, teaching.generated, teaching.audited, err)
	}
	old, err := d.GetPrintableArtifactPDF(t.Context(), request.AgentName, original.Artifact.ArtifactID)
	if err != nil || old.Render.ByteDigest != original.Render.ByteDigest {
		t.Fatal("old artifact overwritten")
	}
}
