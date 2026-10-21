package usecase_test

import (
	"context"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"strings"
	"testing"
)

func TestWeeklyTrackOutput_FreezesOnlyWorksheetAndReusesBytes(t *testing.T) {
	ctx := context.Background()
	d := newDataDeps(t, "xiaoming")
	d.Now = func() int64 { return 1785081600 }
	configureWeeklyBundle(t, &d, false)
	source := &manualBudgetCandidates{}
	d.WeeklyCandidates = source
	renderer := &weeklyDeliveryRenderer{payload: []byte("%PDF-1.7\nworksheet")}
	d.Renderer = renderer
	plan, _, err := d.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: "xiaoming", IdempotencyKey: "track-plan"})
	if err != nil {
		t.Fatal(err)
	}
	plan, _, err = d.PrepareWeeklyTextbookTrack(ctx, "xiaoming", plan.PlanID, plan.Revision, 5, "five")
	if err != nil {
		t.Fatal(err)
	}
	output, replay, err := d.PrepareWeeklyPracticeTrackOutput(ctx, "xiaoming", plan.PlanID, "textbook_consolidation", plan.Revision)
	if err != nil || replay || renderer.calls != 1 {
		t.Fatalf("first output: replay=%v err=%v renders=%d", replay, err, renderer.calls)
	}
	if strings.Count(output.Artifact.CanonicalMarkdown, "答：") != 5 || strings.Contains(output.Artifact.CanonicalMarkdown, "预览 ·") || strings.Contains(output.Artifact.CanonicalMarkdown, "第 1/") {
		t.Fatalf("worksheet contains wrong content: %s", output.Artifact.CanonicalMarkdown)
	}
	if !strings.Contains(output.Render.RenderContractVersion, "workbook") {
		t.Fatalf("worksheet layout contract missing: %s", output.Render.RenderContractVersion)
	}
	legacyRef := strings.TrimSuffix(output.Artifact.SourceRef, ":"+output.Render.RenderContractVersion)
	if legacyRef == output.Artifact.SourceRef {
		t.Fatal("layout version is absent from worksheet identity")
	}
	legacy, _, err := d.PreparePrintableArtifact(ctx, usecase.PreparePrintableArtifactRequest{
		AgentName: "xiaoming", SourceKind: k12.PrintSourcePracticeQuestion,
		SourceRef: legacyRef, Title: output.Artifact.Title,
		CanonicalMarkdown: output.Artifact.CanonicalMarkdown,
	})
	if err != nil || legacy.Artifact.ArtifactID == output.Artifact.ArtifactID {
		t.Fatalf("layout upgrade reused a legacy artifact: %+v %v", legacy, err)
	}
	if renderer.calls != 2 {
		t.Fatalf("expected independent frozen files: renders=%d", renderer.calls)
	}
	again, replay, err := d.PrepareWeeklyPracticeTrackOutput(ctx, "xiaoming", plan.PlanID, "textbook_consolidation", plan.Revision)
	if err != nil || !replay || again.Artifact.ArtifactID != output.Artifact.ArtifactID || again.Render.ByteDigest != output.Render.ByteDigest || renderer.calls != 2 {
		t.Fatalf("same worksheet rerendered: %v %v", replay, err)
	}
	legacyAgain, err := d.GetPrintableArtifactPDF(ctx, "xiaoming", legacy.Artifact.ArtifactID)
	if err != nil || legacyAgain.Render.ByteDigest != legacy.Render.ByteDigest || legacyAgain.Artifact.CanonicalMarkdown != legacy.Artifact.CanonicalMarkdown {
		t.Fatalf("layout upgrade changed a frozen legacy worksheet: %v", err)
	}
	current, err := d.Records.GetWeeklyPracticePlan(ctx, "xiaoming", plan.PlanID)
	if err != nil || current.Status != k12.WeeklyPlanDraft || current.Revision != plan.Revision {
		t.Fatalf("output froze or changed plan: %+v %v", current, err)
	}
	changed, _, err := d.PrepareWeeklyTextbookTrack(ctx, "xiaoming", plan.PlanID, plan.Revision, 3, "three")
	if err != nil || len(changed.Tracks[1].Items) != 3 {
		t.Fatalf("cannot generate after output: %+v %v", changed, err)
	}
	next, _, err := d.PrepareWeeklyPracticeTrackOutput(ctx, "xiaoming", plan.PlanID, "textbook_consolidation", changed.Revision)
	if err != nil || next.Artifact.ArtifactID == output.Artifact.ArtifactID {
		t.Fatalf("changed worksheet reused previous identity: %v", err)
	}
	old, err := d.GetPrintableArtifactPDF(ctx, "xiaoming", output.Artifact.ArtifactID)
	if err != nil || old.Artifact.CanonicalMarkdown != output.Artifact.CanonicalMarkdown || old.Render.ByteDigest != output.Render.ByteDigest {
		t.Fatalf("old frozen worksheet changed: %v", err)
	}
	if _, _, err := d.PrepareWeeklyPracticeTrackOutput(ctx, "xiaoming", plan.PlanID, "due_review", changed.Revision); err == nil {
		t.Fatal("accepted unsupported section")
	}
}

func TestWeeklyTrackOutput_RegenerationPreservesPreviousArithmeticBatch(t *testing.T) {
	ctx := context.Background()
	d := newDataDeps(t, "xiaoming")
	d.Now = func() int64 { return 1785081600 }
	configureWeeklyBundle(t, &d, false)
	source := &manualBudgetCandidates{}
	d.WeeklyCandidates = source
	plan, _, err := d.EnsureWeeklyPracticePlan(ctx, usecase.EnsureWeeklyPracticePlanRequest{AgentName: "xiaoming", IdempotencyKey: "arithmetic-plan"})
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "xiaoming", plan.PlanID, plan.Revision, 3, "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = d.StartWeeklyArithmeticBatch(ctx, "xiaoming", first.BatchID, "start"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "xiaoming", plan.PlanID, plan.Revision, 3, "implicit"); err == nil {
		t.Fatal("implicitly replaced active batch")
	}
	next, replay, err := d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "xiaoming", plan.PlanID, plan.Revision, 3, "replace", true)
	if err != nil || replay || next.BatchID == first.BatchID || next.Ordinal != first.Ordinal+1 {
		t.Fatalf("explicit new batch: %+v %v", next, err)
	}
	old, err := d.Records.GetWeeklyArithmeticBatch(ctx, "xiaoming", first.BatchID)
	if err != nil || old.State != k12.WeeklyArithmeticInProgress || len(old.Items) != 3 {
		t.Fatalf("previous batch lost: %+v %v", old, err)
	}
	calls := source.calls
	if _, replay, err = d.CreateWeeklyArithmeticBatchWithItemCount(ctx, "xiaoming", plan.PlanID, plan.Revision, 3, "replace", true); err != nil || !replay || source.calls != calls {
		t.Fatalf("regeneration replay called generator: %v %v", replay, err)
	}
}
