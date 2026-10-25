package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/render"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

const reportResilienceProblem = "每盒有2支笔，买了2盒，一共有多少支笔？"
const reportResilienceSolution = "每盒2支，2盒共有2×2=4支。\n\n答案：4支。"

// 外部边界只返回固定解答；任务、采用、调用回执和结算使用真实SQLite。
type reportResilienceSolver struct {
	calls    int
	solution string
}

func (s *reportResilienceSolver) UsesGradingPhysicalCalls() bool { return true }
func (s *reportResilienceSolver) Solve(context.Context, string, string, string) (SolveResult, error) {
	s.calls++
	return SolveResult{Solution: s.solution, Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}}, nil
}
func (*reportResilienceSolver) FreezeReportSolveTeachingContext(context.Context, string, string) (string, []skill.ContextReceipt, error) {
	return "preserve the complete verified answer", nil, nil
}

type reportResilienceResources struct{}

func (reportResilienceResources) ReadResource(name, path string) (marketplace.Resource, error) {
	fs := k12.BundledSkillsFS()
	manifestRaw, err := fs.ReadFile("skill-bundles/" + name + "/bundle.json")
	if err != nil {
		return marketplace.Resource{}, err
	}
	var manifest marketplace.BundleManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		return marketplace.Resource{}, err
	}
	raw, err := fs.ReadFile("skill-bundles/" + name + "/" + path)
	return marketplace.Resource{SkillID: name, Version: manifest.Version, Path: path, Content: raw, Size: int64(len(raw)), SHA256: teachingReportResourceDigest(raw)}, err
}

type reportResilienceTeaching struct {
	generationErr error
	auditErr      error
	invalid       bool
	validContent  bool
	generated     int
	audited       int
	afterAudit    func()
}

func (g *reportResilienceTeaching) GenerateParentTeachingGuide(context.Context, ParentTeachingGuideRequest) (ParentTeachingGuide, error) {
	g.generated++
	if g.generationErr != nil {
		return ParentTeachingGuide{}, g.generationErr
	}
	answer, explanation := "999支。", "未审核候选：999支。"
	if g.validContent {
		answer, explanation = "4支。", "每盒2支，2盒共有2×2=4支。"
	}
	return ParentTeachingGuide{Answer: "4支。", FullSolutionSteps: []string{"2×2=4支。"}, GradeLevelMethod: "按盒数相乘。", LikelyMistakes: []string{"不能把盒数当作支数。"}, ParentTeachingSequence: []string{"先数每盒，再算两盒。"}, FollowUpQuestions: []string{"每盒有几支？2支。"}, CheckingMethod: "2+2=4支。",
		ReportContent: &TeachingReportContentDraft{Version: TeachingReportContentContractVersion, Items: []TeachingReportDraftItem{{ProblemID: "current", ShortTitle: "笔的总数", Answers: []TeachingReportDraftAnswer{{ID: "answer", Text: answer}}}}, Sections: []TeachingReportDraftSection{{ID: "reason", Title: "数量关系", ProblemIDs: []string{"current"}, Blocks: []TeachingReportDraftBlock{{ID: "reason-block", ProblemIDs: []string{"current"}, Text: explanation}}}}}}, nil
}

func (g *reportResilienceTeaching) AuditParentTeachingGuide(_ context.Context, req ParentTeachingGuideAuditRequest) (ParentTeachingGuideAudit, error) {
	g.audited++
	if g.afterAudit != nil {
		g.afterAudit()
	}
	if g.auditErr != nil {
		return ParentTeachingGuideAudit{}, g.auditErr
	}
	verdict := "VALID"
	if g.invalid {
		verdict = "INVALID"
	}
	return ParentTeachingGuideAudit{Verdict: verdict, SourceSolutionDigest: req.SourceSolutionDigest, Output: "fixture audit"}, nil
}

func reportResilienceDeps(t *testing.T) (Deps, *reportResilienceSolver, k12.ProblemAssetVersion) {
	t.Helper()
	return reportResilienceDepsForProblem(t, reportResilienceProblem, reportResilienceSolution)
}

func reportResilienceDepsForProblem(t *testing.T, problem, solution string) (Deps, *reportResilienceSolver, k12.ProblemAssetVersion) {
	t.Helper()
	o := newParallelAnchorOrchestrator(t, &countingRecognizer{}, nil, WithGradingRunDir(t.TempDir()))
	d := o.deps
	solver := &reportResilienceSolver{solution: solution}
	d.Solver = solver
	d.TextSolveRoute = func(context.Context, string) (k12.GradingModelSnapshot, error) {
		return k12.GradingModelSnapshot{Provider: "fixture", Model: "fixture", Route: "fixture"}, nil
	}
	ctx := context.Background()
	seed, err := d.SolveTextProblem(ctx, k12.TextSolveOrigin{OwnerID: DefaultLocalOwnerScope, RunID: "seed", InputRevision: 1}, GradeRequest{AgentName: "mingming", Subject: "数学", Grade: "五年级上", Problem: problem})
	if err != nil || seed.Finalization == nil {
		t.Fatalf("freeze source solution: final=%v err=%v", seed.Finalization, err)
	}
	asset, err := d.ResumeTextSolvePublication(ctx, DefaultLocalOwnerScope, "mingming", "seed")
	if err != nil || asset == nil {
		t.Fatalf("publish verified source: asset=%v err=%v", asset, err)
	}
	d.TeachingReportResources = reportResilienceResources{}
	return d, solver, *asset
}

func TestTeachingReportFailurePreservesVerifiedResultAndReceipts(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		generationErr, auditErr error
		invalid                 bool
		wantStatus              k12.ModelInvocationStatus
	}{
		{name: "generation failure", generationErr: fmt.Errorf("invalid provider JSON: %w", egress.ErrProviderResponseProcessed), wantStatus: k12.ModelInvocationFailed},
		{name: "audit rejection", invalid: true, wantStatus: k12.ModelInvocationSucceeded},
		{name: "audit unknown", auditErr: context.DeadlineExceeded, wantStatus: k12.ModelInvocationOutcomeUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, solver, _ := reportResilienceDeps(t)
			teaching := &reportResilienceTeaching{generationErr: tc.generationErr, auditErr: tc.auditErr, invalid: tc.invalid}
			d.ParentTeachingGuide, d.ParentTeachingGuideAudit = teaching, teaching
			origin := k12.TextSolveOrigin{OwnerID: DefaultLocalOwnerScope, RunID: "target", InputRevision: 1}
			req := GradeRequest{AgentName: "mingming", Subject: "数学", Grade: "五年级上", Problem: reportResilienceProblem}
			got, err := d.SolveTextProblem(t.Context(), origin, req)
			if err != nil || got.Finalization == nil || got.TeachingReport == nil {
				t.Fatalf("verified answer lost after optional teaching failure: final=%v report=%v err=%v", got.Finalization, got.TeachingReport, err)
			}
			if got.Solution != reportResilienceSolution || got.Generation != nil || got.ReportContent == nil || got.Evidence.Verdict != VerdictAgree {
				t.Fatalf("degradation changed solution or kept an unaudited guide: %+v", got)
			}
			raw, _ := json.Marshal(got)
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			if len(fields["report_degradation_reason"]) == 0 || strings.Contains(string(raw), "999") {
				t.Fatalf("missing durable degradation or leaked rejected content: %s", raw)
			}
			markdown, err := TeachingReportMarkdown(*got.TeachingReport)
			if err != nil || !strings.Contains(markdown, "4支") || !strings.Contains(markdown, "2×2=4支") {
				t.Fatalf("complete answer or reasoning missing: %q %v", markdown, err)
			}
			before, err := d.Records.ListTextSolveInvocations(t.Context(), origin.OwnerID, req.AgentName, origin.RunID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, call := range before {
				found = found || call.Operation == k12.GradingItemOperationParentGuide && call.Status == tc.wantStatus
			}
			if !found {
				t.Fatalf("original teaching receipt was lost: %+v", before)
			}
			generated, audited := teaching.generated, teaching.audited
			replayed, err := d.SolveTextProblem(t.Context(), origin, req)
			if err != nil || replayed.Finalization == nil || replayed.Finalization.ResultDigest != got.Finalization.ResultDigest || teaching.generated != generated || teaching.audited != audited || solver.calls != 1 {
				t.Fatalf("frozen degradation repeated generation or changed result: generated=%d audited=%d solver=%d err=%v", teaching.generated, teaching.audited, solver.calls, err)
			}
			after, err := d.Records.ListTextSolveInvocations(t.Context(), origin.OwnerID, req.AgentName, origin.RunID)
			if err != nil || modelInvocationResultDigest(before) != modelInvocationResultDigest(after) {
				t.Fatal("replay changed physical receipts")
			}
		})
	}
}

type reportResilienceRenderer struct {
	calls int
	err   error
}

func (r *reportResilienceRenderer) Render(context.Context, string, string) ([]byte, string, error) {
	return nil, "", errors.New("unexpected generic renderer")
}
func (r *reportResilienceRenderer) RenderTeachingReport(context.Context, render.ReportBundle, render.ReportRenderInput) ([]byte, string, error) {
	r.calls++
	if r.err != nil {
		return nil, "", r.err
	}
	return []byte("%PDF-1.7\nfixed historical report bytes"), "application/pdf", nil
}

func TestTextReportHistorySurvivesArchivedAnswerAndFirstPrint(t *testing.T) {
	for _, change := range []string{"archive", "revision advanced"} {
		t.Run(change, func(t *testing.T) {
			d, solver, asset := reportResilienceDeps(t)
			teaching := &reportResilienceTeaching{validContent: true}
			d.ParentTeachingGuide, d.ParentTeachingGuideAudit = teaching, teaching
			renderer := &reportResilienceRenderer{}
			d.Renderer = renderer
			origin := k12.TextSolveOrigin{OwnerID: DefaultLocalOwnerScope, RunID: "history", InputRevision: 1}
			req := GradeRequest{AgentName: "mingming", Subject: "数学", Grade: "五年级上", Problem: reportResilienceProblem}
			got, err := d.SolveTextProblem(t.Context(), origin, req)
			if err != nil || got.Finalization == nil || got.AnswerSource == nil || got.AnswerSource.Kind != k12.ProblemAnswerAsset {
				t.Fatalf("adopt historical answer: %+v %v", got, err)
			}
			if change == "archive" {
				if err = d.Records.ArchiveProblemAsset(t.Context(), asset.OwnerID, asset.AssetID, asset.Revision); err != nil {
					t.Fatal(err)
				}
			} else {
				// 受控推进当前修订，保留历史版本与原采用证据；本组不验证资产编辑入口。
				if _, err = d.Records.DB().Exec(`UPDATE k12_problem_assets SET revision=revision+1 WHERE owner_id=? AND asset_id=?`, asset.OwnerID, asset.AssetID); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.Records.ValidateProblemAssetAdoption(t.Context(), asset.OwnerID, got.AnswerSource.AdoptionID); !errors.Is(err, k12storage.ErrProblemAssetUnavailable) {
				t.Fatalf("fixture still has current adoption: %v", err)
			}
			renderer.err = errors.New("fixture PDF compilation failed")
			if _, _, err := d.PrepareTextSolveTeachingReportPDF(t.Context(), origin.OwnerID, req.AgentName, origin.RunID, got.Finalization.FinalizationID, got.Finalization.ResultDigest, ""); err == nil {
				t.Fatal("PDF failure was hidden")
			}
			content, err := d.TextSolveTeachingReportContent(t.Context(), origin.OwnerID, req.AgentName, origin.RunID, got.Finalization.FinalizationID, got.Finalization.ResultDigest)
			if err != nil || !strings.Contains(content, "4支") || !strings.Contains(content, reportResilienceProblem) || renderer.calls != 1 {
				t.Fatalf("read-only body failed or rendered after PDF failure: content=%q calls=%d err=%v", content, renderer.calls, err)
			}
			if content, err := d.TextSolveTeachingReportContent(t.Context(), origin.OwnerID, req.AgentName, origin.RunID, got.Finalization.FinalizationID, "different-result"); err == nil || content != "" || renderer.calls != 1 {
				t.Fatal("read-only body accepted another finalization identity or rendered")
			}
			renderer.err = nil
			replayed, err := d.SolveTextProblem(t.Context(), origin, req)
			if err != nil || replayed.Finalization == nil || replayed.Finalization.ResultDigest != got.Finalization.ResultDigest || solver.calls != 1 {
				t.Fatalf("historical frozen answer became unreadable: %v", err)
			}
			printed, reused, err := d.PrepareTextSolveTeachingReportPDF(t.Context(), origin.OwnerID, req.AgentName, origin.RunID, got.Finalization.FinalizationID, got.Finalization.ResultDigest, "")
			if err != nil || reused || printed.Artifact.ArtifactID == "" || renderer.calls != 2 {
				t.Fatalf("first historical print rejected archived source: reused=%t calls=%d err=%v", reused, renderer.calls, err)
			}
			read, err := d.GetPrintableArtifactPDF(t.Context(), req.AgentName, printed.Artifact.ArtifactID)
			if err != nil || string(read.Render.Payload) != string(printed.Render.Payload) {
				t.Fatalf("frozen historical print unreadable: %v", err)
			}
			if solver.calls != 1 || teaching.generated != 1 || teaching.audited != 1 {
				t.Fatal("historical read/print repeated a model action")
			}
		})
	}
}

func TestTeachingReportDegradationCannotCommitRevokedAdoption(t *testing.T) {
	d, _, asset := reportResilienceDeps(t)
	teaching := &reportResilienceTeaching{invalid: true, afterAudit: func() {
		if err := d.Records.ArchiveProblemAsset(t.Context(), asset.OwnerID, asset.AssetID, asset.Revision); err != nil {
			t.Fatal(err)
		}
	}}
	d.ParentTeachingGuide, d.ParentTeachingGuideAudit = teaching, teaching
	got, err := d.SolveTextProblem(t.Context(), k12.TextSolveOrigin{OwnerID: DefaultLocalOwnerScope, RunID: "revoked", InputRevision: 1}, GradeRequest{AgentName: "mingming", Subject: "数学", Grade: "五年级上", Problem: reportResilienceProblem})
	if !errors.Is(err, k12storage.ErrProblemAssetUnavailable) || got.Finalization != nil {
		t.Fatalf("first finalization did not enforce current adoption: final=%v err=%v", got.Finalization, err)
	}
}

func TestTeachingReportFailureDoesNotInventMissingOrUnverifiedAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, solution string
		evidence       SolveEvidence
	}{
		{"missing explicit answer", "题目需要补充条件。", SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}},
		{"empty answer", "每盒2支。\n\n答案：", SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}},
		{"unverified answer", reportResilienceSolution, SolveEvidence{Verdict: VerdictUnverifiable, EvidenceType: EvidenceNone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			teaching := &reportResilienceTeaching{generationErr: errors.New("teaching unavailable")}
			d := Deps{ParentTeachingGuide: teaching, ParentTeachingGuideAudit: teaching}
			got, err := d.adaptReusedReportTeaching(t.Context(), GradeRequest{SolveOutputVersion: SolveOutputWithTeachingReportV2}, SolveHomeworkResult{Solution: tc.solution, Evidence: tc.evidence})
			if err == nil || got.ReportContent != nil || got.ReportDegradationReason != "" {
				t.Fatalf("missing or unverified answer became a complete report: %+v %v", got, err)
			}
		})
	}
}

func TestImageReportTeachingFailureFreezesWithoutSecondProviderCall(t *testing.T) {
	knownFailure := fmt.Errorf("invalid teaching JSON: %w", egress.ErrProviderResponseProcessed)
	for _, tc := range []struct {
		name, problem, solution, answer, step string
		failure                               error
		diagramFailure                        bool
	}{
		{"generation failed", reportResilienceProblem, reportResilienceSolution, "4支", "2×2=4支", knownFailure, false},
		{"generation unknown", reportResilienceProblem, reportResilienceSolution, "4支", "2×2=4支", context.DeadlineExceeded, false},
		{"optional diagram storage failed", "一个硬盘，已用空间占总容量的2/5，正好用了4.8 TB。这个硬盘总容量是多少？", "4.8÷2×5=12 TB。\n\n答案：12 TB。", "12 TB", "4.8÷2×5=12 TB", knownFailure, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
			d, solver, _ := reportResilienceDepsForProblem(t, tc.problem, tc.solution)
			teaching := &reportResilienceTeaching{generationErr: tc.failure}
			d.ParentTeachingGuide, d.ParentTeachingGuideAudit = teaching, teaching
			d.TextbookOwnerID = DefaultLocalOwnerScope
			d.GradingBudgetSnapshot = orchestratorTestBudget()
			confidence := .99
			recognizer := &countingRecognizer{questions: []RecognizedQuestion{{Question: tc.problem, RawTranscription: tc.problem, CanonicalMarkdown: tc.problem, EvidenceTranscriptions: []string{tc.problem, tc.problem}, RecognitionConfidence: &confidence, Subject: "数学", AnswerState: AnswerStateBlank, SourceWidth: 16, SourceHeight: 16}}}
			d.Recognizer = recognizer
			req := orchestratorPhotoRequest()
			req.Subject, req.TaskIntent = "数学", PhotoTaskBlankWorksheet
			var source bytes.Buffer
			if err := png.Encode(&source, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
				t.Fatal(err)
			}
			req.Image = source.Bytes()
			d.PhotoAnnotator = coveragePNGAnnotator{data: source.Bytes()}
			o := trackGradingOrchestrator(t, NewGradingOrchestrator(d, orchestratorSnapshotResolver, WithGradingRunDir(t.TempDir())))
			repository := &PageAssetRepository{Records: d.Records}
			ready, err := repository.Persist(t.Context(), DefaultLocalOwnerScope, "mingming", source.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			recognizer.questions[0].PageAssetID = ready.Metadata.PageAssetID
			if tc.diagramFailure {
				if _, err := d.Records.DB().Exec(`CREATE TRIGGER reject_optional_report_diagram BEFORE INSERT ON k12_teaching_diagram_assets BEGIN SELECT RAISE(FAIL, 'fixture optional diagram storage unavailable'); END`); err != nil {
					t.Fatal(err)
				}
			}
			coordinator := &ImageTaskCoordinator{Records: d.Records, PageAssets: repository, Classifier: &imageTaskClassifierStub{}, ResolveRoute: imageTaskRouteForTest, Now: d.Now}
			input := testCreateImageTaskInput()
			input.OwnerScope, input.SourceAssetRefs = DefaultLocalOwnerScope, []string{ready.Metadata.PageAssetID}
			prepared, _, err := coordinator.Create(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			dispatch, target, err := d.Records.CommitImageTaskRouting(t.Context(), "mingming", prepared.Dispatch.DispatchID, prepared.Dispatch.Version, k12storage.ImageTaskRoutingDecision{Intent: k12.ImageTaskIntentBlankWorksheet, Confidence: 1, Evidence: []string{"blank worksheet"}, InvocationResultDigest: "sha256:fixture-classification"})
			if err != nil {
				t.Fatal(err)
			}
			req.SourcePageAssetID, req.SourceSession = ready.Metadata.PageAssetID, input.SourceSessionID
			job, _, err := o.StartPhotoGradingJob(t.Context(), StartPhotoGradingInput{Photo: req, SourceKind: "image_task", SourceKey: dispatch.DispatchID, BudgetSnapshot: d.GradingBudgetSnapshot, ParentAutomaticAttemptID: dispatch.DispatchID + ":fixture", ParentAutomaticDeadlineAt: d.now() + 300})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.Records.BindHomeworkSubmissionGradingJob(t.Context(), "mingming", target.HomeworkSubmission.SubmissionID, job.Record.RecordID, target.HomeworkSubmission.Version); err != nil {
				t.Fatal(err)
			}
			if _, err = o.RunGradingJob(t.Context(), job.Record.RecordID); err != nil {
				t.Fatal(err)
			}
			waitGradingView(t, o, job.Record.RecordID, func(v GradingJobView) bool {
				return v.Fields.AnchorState == k12.GradingAnchorLocated || v.Fields.AnchorState == k12.GradingAnchorDegraded
			})
			completed := waitGradingView(t, o, job.Record.RecordID, func(v GradingJobView) bool {
				return v.Record.Status == k12.GradingStageCompleted || v.Record.Status == k12.GradingStageFailedRetryable || v.Record.Status == k12.GradingStageOutcomeUnknown
			})
			if completed.Record == nil || completed.Record.Status != k12.GradingStageCompleted {
				t.Fatalf("image report did not complete with its verified answer: %+v", completed)
			}
			artifact, err := d.Records.GetGradingFinalArtifactByJob(t.Context(), "mingming", job.Record.RecordID)
			if err != nil || artifact.TeachingReport == nil || !strings.Contains(artifact.CanonicalMarkdown, tc.answer) || !strings.Contains(artifact.CanonicalMarkdown, tc.step) {
				t.Fatalf("missing frozen image report: markdown=%q err=%v", artifact.CanonicalMarkdown, err)
			}
			if tc.diagramFailure {
				var frozen TeachingReportFrozenSnapshot
				if err := json.Unmarshal([]byte(artifact.ReportSnapshotJSON), &frozen); err != nil {
					t.Fatal(err)
				}
				const reason = "Optional teaching diagram storage unavailable: "
				if !strings.Contains(strings.Join(artifact.TeachingReport.Warnings, "\n"), reason) || !strings.Contains(strings.Join(frozen.Report.Warnings, "\n"), reason) {
					t.Fatalf("optional failure not frozen/read back: warnings=%v frozen=%v", artifact.TeachingReport.Warnings, frozen.Report.Warnings)
				}
				for _, section := range frozen.Report.DeepSections {
					if len(section.Diagrams) != 0 {
						t.Fatal("failed optional diagram was marked ready")
					}
				}
			}
			if teaching.generated != 1 || teaching.audited != 0 || solver.calls != 1 {
				t.Fatalf("image completion repeated standard solve or teaching: %d/%d/%d", solver.calls, teaching.generated, teaching.audited)
			}
			items, err := d.Records.ListGradingAssessmentItems(t.Context(), "mingming", job.Record.RecordID)
			if err != nil || len(items) != 1 || !strings.Contains(items[0].ResultJSON, "report_degradation_reason") {
				t.Fatalf("degradation did not reach immutable assessment: %+v %v", items, err)
			}
			if _, err = o.RunGradingJob(t.Context(), job.Record.RecordID); err != nil {
				t.Fatal(err)
			}
			if teaching.generated != 1 || solver.calls != 1 {
				t.Fatal("image replay resent teaching or solve")
			}
		})
	}
}

func TestTextReportDegradationResumesFinalizationWithoutUnknownResend(t *testing.T) {
	d, solver, _ := reportResilienceDeps(t)
	teaching := &reportResilienceTeaching{auditErr: context.DeadlineExceeded}
	d.ParentTeachingGuide, d.ParentTeachingGuideAudit = teaching, teaching
	if _, err := d.Records.DB().Exec(`CREATE TRIGGER interrupt_report_finalize BEFORE INSERT ON k12_text_solve_finalizations WHEN NEW.solve_run_id='recover' AND NEW.data!='{}' BEGIN SELECT RAISE(FAIL, 'fixture interrupted finalization'); END`); err != nil {
		t.Fatal(err)
	}
	origin := k12.TextSolveOrigin{OwnerID: DefaultLocalOwnerScope, RunID: "recover", InputRevision: 1}
	req := GradeRequest{AgentName: "mingming", Subject: "数学", Grade: "五年级上", Problem: reportResilienceProblem}
	first, err := d.SolveTextProblem(t.Context(), origin, req)
	if err == nil || first.Finalization != nil || !strings.Contains(err.Error(), "fixture interrupted finalization") {
		t.Fatalf("commit interruption was not reached: %v", err)
	}
	before, err := d.Records.ListTextSolveInvocations(t.Context(), origin.OwnerID, req.AgentName, origin.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Records.DB().Exec(`DROP TRIGGER interrupt_report_finalize`); err != nil {
		t.Fatal(err)
	}
	recovered, err := d.SolveTextProblem(t.Context(), origin, req)
	if err != nil || recovered.Finalization == nil || recovered.TeachingReport == nil || teaching.generated != 1 || teaching.audited != 1 || solver.calls != 1 {
		t.Fatalf("local degradation could not resume without unknown resend: final=%v calls=%d/%d/%d err=%v", recovered.Finalization, solver.calls, teaching.generated, teaching.audited, err)
	}
	after, err := d.Records.ListTextSolveInvocations(t.Context(), origin.OwnerID, req.AgentName, origin.RunID)
	if err != nil || modelInvocationResultDigest(before) != modelInvocationResultDigest(after) {
		t.Fatal("recovering the local commit changed physical receipts")
	}
}

func TestTeachingReportCurrentAdaptationDropsPriorDegradation(t *testing.T) {
	teaching := &reportResilienceTeaching{validContent: true}
	d := Deps{ParentTeachingGuide: teaching, ParentTeachingGuideAudit: teaching}
	prior := SolveHomeworkResult{Solution: reportResilienceSolution, Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}, ReportDegradationReason: "teaching_generation_failed", ReportContent: &TeachingReportContentDraft{Version: "prior-task"}}
	got, err := d.adaptReusedReportTeaching(t.Context(), GradeRequest{SolveOutputVersion: SolveOutputWithTeachingReportV2, Problem: reportResilienceProblem}, prior)
	if err != nil || got.ReportDegradationReason != "" || got.ReportContent != nil || got.Generation == nil || got.Generation.GuideAudit != "VALID" || got.Generation.ParentGuideCandidate.ReportContent.Version != TeachingReportContentContractVersion {
		t.Fatalf("current teaching retained a previous task's degraded projection: %+v %v", got, err)
	}
}

func TestTeachingReportDegradationPreservesEveryExplicitAnswer(t *testing.T) {
	source := "## 每份容量\n4.8÷2=2.4 TB。\n\n答案：每份2.4 TB。\n\n## 总容量\n2.4×5=12 TB。\n\n答案：总容量12 TB；以总容量为单位1时，已用占2/5。"
	got, ok := degradeVerifiedReportTeaching(SolveHomeworkResult{Solution: source, Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}}, "teaching_generation_failed")
	if !ok || got.ReportContent == nil {
		t.Fatal("complete verified source was lost")
	}
	answers := got.ReportContent.Items[0].Answers
	if len(answers) != 2 || answers[0].Text != "每份2.4 TB。" || answers[1].Text != "总容量12 TB；以总容量为单位1时，已用占2/5。" {
		t.Fatalf("degradation dropped a requested quantity or condition: %+v", answers)
	}
	if got.Solution != source {
		t.Fatal("degradation changed frozen source wording")
	}
	if _, ok := degradeVerifiedReportTeaching(SolveHomeworkResult{Solution: strings.Replace(source, "答案：每份2.4 TB。", "答案：", 1), Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}}, "teaching_generation_failed"); ok {
		t.Fatal("an unanswered requested quantity was hidden by the final answer")
	}
}
