package usecase_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/engine"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/engineadapter"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

type textGradeResources struct{}

func (textGradeResources) ReadResource(name, path string) (marketplace.Resource, error) {
	fs := k12.BundledSkillsFS()
	raw, err := fs.ReadFile("skill-bundles/" + name + "/bundle.json")
	if err != nil {
		return marketplace.Resource{}, err
	}
	var manifest marketplace.BundleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return marketplace.Resource{}, err
	}
	raw, err = fs.ReadFile("skill-bundles/" + name + "/" + path)
	digest := sha256.Sum256(raw)
	return marketplace.Resource{SkillID: name, Version: manifest.Version, Path: path, Content: raw, Size: int64(len(raw)), SHA256: hex.EncodeToString(digest[:])}, err
}

type textGradeRenderer struct{ calls int }

func (*textGradeRenderer) Render(context.Context, string, string) ([]byte, string, error) {
	return nil, "", fmt.Errorf("unexpected legacy renderer")
}
func (r *textGradeRenderer) RenderTeachingReport(_ context.Context, _ render.ReportBundle, input render.ReportRenderInput) ([]byte, string, error) {
	r.calls++
	return append([]byte("%PDF-1.7\n"), input.Document...), "application/pdf", nil
}

type textGradeCounter struct {
	delegate usecase.VerifiedSolutionGrader
	calls    int
}

// 非二元评价使用评分端口的合法受控回包，不把不可解析的Provider文本当成评价。
type textGradeUntrustedBoundary struct{}

func (textGradeUntrustedBoundary) GradeVerified(context.Context, string, string, string, string) (usecase.GradeOutcome, error) {
	return usecase.GradeOutcome{Verdict: usecase.VerdictUnverifiable, ErrorCause: "原作答无法确认"}, nil
}

func (g *textGradeCounter) GradeVerified(ctx context.Context, subject, problem, answer, solved string) (usecase.GradeOutcome, error) {
	g.calls++
	return g.delegate.GradeVerified(ctx, subject, problem, answer, solved)
}

func newTextGradeDeps(t *testing.T, provider engine.SubAgentExecFunc) (usecase.Deps, *textGradeCounter, *textGradeRenderer) {
	t.Helper()
	d := newDataDeps(t)
	adapter := engineadapter.NewSolveAdapter(engine.NewSolveSkill(provider, nil))
	counter, renderer := &textGradeCounter{delegate: adapter}, &textGradeRenderer{}
	d.Solver, d.Grader, d.VerifiedGrader = adapter, adapter, counter
	d.TeachingReportResources, d.Renderer = textGradeResources{}, renderer
	d.TextSolveRoute = func(context.Context, string) (k12.GradingModelSnapshot, error) {
		return k12.GradingModelSnapshot{Provider: "fixture", Model: "fixture", Route: "fixture/fixture"}, nil
	}
	return d, counter, renderer
}

func TestTextGradeReportUsesActualAssessmentAndKeepsUnscoredHistory(t *testing.T) {
	for _, tc := range []struct {
		name, answer, providerOutput string
		status                       k12.GradingAssessmentStatus
	}{
		{"correct", "11.4", "", k12.GradingAssessmentCorrect},
		{"wrong", "12", "", k12.GradingAssessmentWrong},
		{"process issue", "3.8×3=10.4\n答：11.4", "CORRECT: no\nFINAL_ANSWER_CORRECT: yes\nWRONG_STEP: 3.8×3=10.4\nMISCONCEPTION: 中间乘积错误\nGUIDANCE: 重新计算乘积", k12.GradingAssessmentProcessIssue},
		{"untrusted", "笔迹未确认", "", k12.GradingAssessmentUntrusted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			providerCalls := 0
			d, counter, renderer := newTextGradeDeps(t, func(_ context.Context, spec engine.SubAgentSpec) (engine.SubAgentResult, error) {
				providerCalls++
				if spec.Agent != "grader" || tc.providerOutput == "" {
					return engine.SubAgentResult{}, fmt.Errorf("unexpected provider call %s", spec.Agent)
				}
				return engine.SubAgentResult{Output: tc.providerOutput}, nil
			})
			if tc.status == k12.GradingAssessmentUntrusted {
				counter.delegate = textGradeUntrustedBoundary{}
			}
			origin := k12.TextSolveOrigin{OwnerID: "desktop-user", RunID: "text-grade-" + tc.name, InputRevision: 1}
			ctx := usecase.WithAuthorizedTextSolveOrigin(t.Context(), origin, "default")
			req := usecase.GradeRequest{AgentName: "default", Subject: "数学", Grade: "六年级上", Problem: "3.8×3=?", StudentAnswer: tc.answer}
			base, err := d.SolveTextProblem(ctx, origin, req)
			if err != nil || base.Finalization == nil {
				t.Fatalf("source solve failed: %v", err)
			}
			original := *base.Finalization
			oldPDF, _, err := d.PrepareTextSolveTeachingReportPDF(ctx, origin.OwnerID, "default", origin.RunID, original.FinalizationID, original.ResultDigest, "")
			if err != nil {
				t.Fatal(err)
			}
			graded, err := d.GradeHomeworkProblem(ctx, req)
			if err != nil || graded.TeachingReport == nil || graded.GradeReportRef == nil {
				t.Fatalf("actual assessment did not reach the two-layer report: report=%v ref=%v err=%v", graded.TeachingReport, graded.GradeReportRef, err)
			}
			item := graded.TeachingReport.Items[0]
			if item.Assessment.Status != tc.status || item.OriginalAnswer.State != k12.TeachingReportAnswerPresent || item.OriginalAnswer.RawText != tc.answer || graded.TeachingReport.ProblemSources[0].AssessmentDigest != graded.GradeReportRef.ResultDigest {
				t.Fatalf("assessment or original answer changed: %+v", item)
			}
			inv, err := d.Records.GetTextSolveInvocation(ctx, origin.OwnerID, "default", graded.GradeReportRef.InvocationID)
			if err != nil || inv.Status != k12.ModelInvocationSucceeded || inv.Operation != k12.GradingItemOperationGrade || inv.ResultDigest != graded.GradeReportRef.ResultDigest || inv.RunID != origin.RunID {
				t.Fatalf("report grade reference was not an actual terminal receipt: %+v %v", inv, err)
			}
			body, err := usecase.TeachingReportMarkdown(*graded.TeachingReport)
			if err != nil || !strings.Contains(body, "一、快速解答") || !strings.Contains(body, "二、深度讲解") || !strings.Contains(body, tc.answer) {
				t.Fatalf("incomplete report body: %q %v", body, err)
			}
			frozen, err := d.Records.GetTextSolveFinalization(ctx, origin.OwnerID, "default", origin.RunID)
			if err != nil || frozen.ResultDigest != original.ResultDigest || frozen.ResultJSON != original.ResultJSON {
				t.Fatal("grading rewrote the original solve finalization")
			}
			unscored, err := d.TextSolveTeachingReportContent(ctx, origin.OwnerID, "default", origin.RunID, original.FinalizationID, original.ResultDigest)
			if err != nil || unscored != oldPDF.Artifact.CanonicalMarkdown {
				t.Fatal("a later grade reinterpreted the unscored source")
			}
			gradedBody, err := d.TextSolveTeachingReportContentWithGrade(ctx, origin.OwnerID, "default", origin.RunID, original.FinalizationID, original.ResultDigest, *graded.GradeReportRef)
			if err != nil || gradedBody != body {
				t.Fatalf("graded source did not replay exactly: %v", err)
			}
			pdf, _, err := d.PrepareTextSolveTeachingReportPDFWithGrade(ctx, origin.OwnerID, "default", origin.RunID, original.FinalizationID, original.ResultDigest, "", *graded.GradeReportRef)
			if err != nil || pdf.Artifact.ArtifactID == oldPDF.Artifact.ArtifactID || pdf.Artifact.CanonicalMarkdown != body || !strings.Contains(pdf.Artifact.SourceRef, ":grade:"+graded.GradeReportRef.InvocationID+":"+graded.GradeReportRef.ResultDigest) {
				t.Fatalf("graded PDF lost its exact assessment source: %+v %v", pdf.Artifact, err)
			}
			read, err := d.GetPrintableArtifactPDF(ctx, "default", oldPDF.Artifact.ArtifactID)
			if err != nil || !bytes.Equal(read.Render.Payload, oldPDF.Render.Payload) {
				t.Fatal("old unscored PDF bytes changed")
			}
			before, err := d.Records.ListTextSolveInvocations(ctx, origin.OwnerID, "default", origin.RunID)
			if err != nil {
				t.Fatal(err)
			}
			again, err := d.GradeHomeworkProblem(ctx, req)
			if err != nil || again.GradeReportRef == nil || *again.GradeReportRef != *graded.GradeReportRef || counter.calls != 1 {
				t.Fatalf("grade replay repeated assessment: count=%d err=%v", counter.calls, err)
			}
			after, err := d.Records.ListTextSolveInvocations(ctx, origin.OwnerID, "default", origin.RunID)
			if err != nil || len(before) != len(after) {
				t.Fatal("grade replay created another invocation")
			}
			bad := *graded.GradeReportRef
			bad.ResultDigest = "sha256:wrong"
			if _, _, err := d.PrepareTextSolveTeachingReportPDFWithGrade(ctx, origin.OwnerID, "default", origin.RunID, original.FinalizationID, original.ResultDigest, "", bad); err == nil || renderer.calls != 2 {
				t.Fatal("mismatched assessment rendered or fell back to unscored content")
			}
			var mistakes int
			if err := d.Records.DB().QueryRow(`SELECT COUNT(*) FROM k12_mistakes`).Scan(&mistakes); err != nil || mistakes != 0 || graded.RecordCreated {
				t.Fatal("standalone assessment created student records")
			}
			if tc.providerOutput == "" && providerCalls != 0 {
				t.Fatalf("local arithmetic invoked provider %d times", providerCalls)
			}
		})
	}
}

func TestTextGradeUnknownReceiptIsNotResentOrProjected(t *testing.T) {
	providerCalls := 0
	d, counter, _ := newTextGradeDeps(t, func(_ context.Context, spec engine.SubAgentSpec) (engine.SubAgentResult, error) {
		providerCalls++
		if spec.Agent != "grader" {
			return engine.SubAgentResult{}, fmt.Errorf("unexpected %s", spec.Agent)
		}
		return engine.SubAgentResult{}, context.DeadlineExceeded
	})
	origin := k12.TextSolveOrigin{OwnerID: "desktop-user", RunID: "unknown-grade", InputRevision: 1}
	ctx := usecase.WithAuthorizedTextSolveOrigin(t.Context(), origin, "default")
	req := usecase.GradeRequest{AgentName: "default", Subject: "数学", Grade: "六年级上", Problem: "3.8×3=?", StudentAnswer: "先看个位，再估计大约十二"}
	for range 2 {
		got, err := d.GradeHomeworkProblem(ctx, req)
		if err == nil || got.GradeReportRef != nil || got.TeachingReport != nil {
			t.Fatalf("unknown grading was presented as complete: %+v %v", got, err)
		}
	}
	if counter.calls != 1 || providerCalls != 1 {
		t.Fatalf("unknown grade resent: grade=%d provider=%d", counter.calls, providerCalls)
	}
	calls, err := d.Records.ListTextSolveInvocations(ctx, origin.OwnerID, "default", origin.RunID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range calls {
		found = found || call.Operation == k12.GradingItemOperationGrade && call.Status == k12.ModelInvocationOutcomeUnknown
	}
	if !found {
		t.Fatal("actual unknown grading receipt was lost")
	}
}
