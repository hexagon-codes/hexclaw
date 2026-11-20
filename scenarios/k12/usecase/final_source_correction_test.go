package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/records"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type completedSourceReviewerFixture struct {
	calls    int
	fail     bool
	verified bool
	seen     []RecognizedQuestion
}

type completedSourceReparserFixture struct {
	*completedSourceReviewerFixture
	response   CompletedSourceReview
	parseCalls int
}

func (s *completedSourceReparserFixture) ParseCompletedSourceReview(raw string, _ RecognizedQuestion) (CompletedSourceReview, error) {
	s.parseCalls++
	if raw != s.response.Raw {
		return CompletedSourceReview{}, errors.New("unexpected source receipt")
	}
	parsed := s.response
	parsed.Verified = true
	return parsed, nil
}

func TestCompletedSourceCorrectionReparsesSucceededReceiptWithoutResending(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "succeeded receipt", true: "unknown receipt"}[unknown], func(t *testing.T) {
			deps, _, orchestrator, original, _ := originalTeachingReportFixtureWithRegion(t, &k12.SourcePixelRegion{Width: 8, Height: 4})
			ctx := t.Context()
			job, err := deps.GetGradingJob(ctx, original.AgentName, original.JobID)
			if err != nil {
				t.Fatal(err)
			}
			dispatch, err := gradingFinalImageTaskDispatchID(job)
			if err != nil {
				t.Fatal(err)
			}
			questions, err := deps.loadCurrentConfirmedQuestions(ctx, original.AgentName, job.Fields.SubmissionID)
			if err != nil || len(questions) != 1 {
				t.Fatalf("questions=%d err=%v", len(questions), err)
			}
			q := questions[0]
			request := FinalSourceCorrectionInput{Agent: original.AgentName, IdempotencyKey: "reparse-source", ArtifactID: original.ArtifactID, ArtifactDigest: original.ArtifactDigest, InputRevision: q.ConfirmedVersion, InputDigest: q.InputDigest}
			reviewer := &completedSourceReviewerFixture{fail: unknown}
			orchestrator.deps.Recognizer = reviewer
			first, err := orchestrator.CorrectCompletedSource(ctx, DefaultLocalOwnerScope, dispatch, q.ProblemID, request)
			if err == nil || reviewer.calls != 1 {
				t.Fatalf("initial unverified source became complete: result=%+v err=%v calls=%d", first, err, reviewer.calls)
			}
			before, err := deps.Records.GetFinalSourceCorrection(ctx, original.AgentName, first.CorrectionID)
			if err != nil {
				t.Fatal(err)
			}
			parser := &completedSourceReparserFixture{completedSourceReviewerFixture: reviewer}
			if err := json.Unmarshal([]byte(before.ResponseJSON), &parser.response); err != nil {
				t.Fatal(err)
			}
			if before.Status != map[bool]string{false: "succeeded", true: "outcome_unknown"}[unknown] || parser.response.Verified {
				t.Fatalf("fixture lost the persisted receipt state: %+v", before)
			}
			if !unknown {
				items, err := deps.Records.ListEffectiveGradingAssessments(ctx, original.AgentName, original.JobID)
				if err != nil || len(items) != 1 {
					t.Fatalf("assessments=%d err=%v", len(items), err)
				}
				source, _ := json.Marshal(q)
				if _, err := deps.Records.CommitFinalBlankSourceCorrection(ctx, first.CorrectionID, original, source, items[0]); !errors.Is(err, k12storage.ErrGradingFinalArtifactConflict) {
					t.Fatalf("legacy commit trusted an unverified receipt: %v", err)
				}
				for _, change := range []string{"digest", "raw", "question"} {
					parsed := parser.response
					parsed.Verified = true
					digest := before.ResponseDigest
					switch change {
					case "digest":
						digest = "sha256:" + strings.Repeat("a", 64)
					case "raw":
						parsed.Raw += " "
					case "question":
						parsed.Question.Question += "changed"
					}
					payload, _ := json.Marshal(parsed)
					if _, err := deps.Records.CommitFinalBlankSourceCorrectionWithReview(ctx, first.CorrectionID, original, source, items[0], digest, payload); !errors.Is(err, k12storage.ErrGradingFinalArtifactConflict) {
						t.Fatalf("changed %s bypassed the exact receipt binding: %v", change, err)
					}
				}
			}
			restarted := trackGradingOrchestrator(t, NewGradingOrchestrator(orchestrator.deps, orchestratorSnapshotResolver, WithGradingRunDir(orchestrator.runDir)))
			restarted.deps.Recognizer = parser
			resumed, resumeErr := restarted.CorrectCompletedSource(ctx, DefaultLocalOwnerScope, dispatch, q.ProblemID, request)
			after, err := deps.Records.GetFinalSourceCorrection(ctx, original.AgentName, first.CorrectionID)
			if err != nil || before.ResponseJSON != after.ResponseJSON || before.ResponseDigest != after.ResponseDigest || reviewer.calls != 1 {
				t.Fatalf("source receipt was rewritten or resent: err=%v calls=%d", err, reviewer.calls)
			}
			if unknown {
				if !errors.Is(resumeErr, ErrModelInvocationRequiresReconciliation) || parser.parseCalls != 0 || after.Status != "outcome_unknown" {
					t.Fatalf("unknown receipt was reinterpreted: result=%+v err=%v parses=%d", resumed, resumeErr, parser.parseCalls)
				}
				return
			}
			if resumeErr != nil || resumed.Artifact == nil || resumed.Status != "completed" || after.Status != "completed" || parser.parseCalls != 1 {
				t.Fatalf("succeeded receipt did not complete by pure replay: result=%+v err=%v parses=%d", resumed, resumeErr, parser.parseCalls)
			}
			current, err := deps.loadCurrentConfirmedQuestions(ctx, original.AgentName, job.Fields.SubmissionID)
			if err != nil || len(current) != 1 || current[0].RawTranscription != q.RawTranscription || current[0].CanonicalMarkdown != q.CanonicalMarkdown || current[0].Question != q.Question {
				t.Fatalf("pure replay changed the original question: err=%v", err)
			}
			if _, err := restarted.CorrectCompletedSource(ctx, DefaultLocalOwnerScope, dispatch, q.ProblemID, request); err != nil || reviewer.calls != 1 || parser.parseCalls != 1 {
				t.Fatalf("completed replay repeated parsing or vision: err=%v calls=%d parses=%d", err, reviewer.calls, parser.parseCalls)
			}
		})
	}
}

func TestReportOnlySourceCorrectionKeepsFrozenPageAndReplay(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "unknown"}[unknown], func(t *testing.T) {
			deps, _, orchestrator, original, pixels := originalTeachingReportFixtureWithRegion(t, &k12.SourcePixelRegion{Width: 8, Height: 4})
			ctx := t.Context()
			if original.HasAnnotatedAsset() || original.TeachingReport == nil || len(original.TeachingReport.SourcePages) != 1 || orchestrator.deps.PhotoAnnotator != nil {
				t.Fatal("formal report-only fixture must have a frozen source and no annotator")
			}
			job, err := deps.GetGradingJob(ctx, original.AgentName, original.JobID)
			if err != nil {
				t.Fatal(err)
			}
			dispatch, err := gradingFinalImageTaskDispatchID(job)
			if err != nil {
				t.Fatal(err)
			}
			questions, err := deps.loadCurrentConfirmedQuestions(ctx, original.AgentName, job.Fields.SubmissionID)
			if err != nil || len(questions) != 1 {
				t.Fatalf("questions=%d err=%v", len(questions), err)
			}
			q := questions[0]
			request := FinalSourceCorrectionInput{Agent: original.AgentName, IdempotencyKey: "report-only", ArtifactID: original.ArtifactID, ArtifactDigest: original.ArtifactDigest, InputRevision: q.ConfirmedVersion, InputDigest: q.InputDigest}
			reviewer := &completedSourceReviewerFixture{verified: true, fail: unknown}
			orchestrator.deps.Recognizer = reviewer
			result, err := orchestrator.CorrectCompletedSource(ctx, DefaultLocalOwnerScope, dispatch, q.ProblemID, request)
			if unknown {
				if err == nil || result.Status != "outcome_unknown" {
					t.Fatalf("unknown source review became complete: result=%+v err=%v", result, err)
				}
			} else if err != nil || result.Artifact == nil || result.Status != "completed" {
				t.Fatalf("report-only correction failed: result=%+v err=%v", result, err)
			}
			restarted := trackGradingOrchestrator(t, NewGradingOrchestrator(orchestrator.deps, orchestratorSnapshotResolver, WithGradingRunDir(orchestrator.runDir)))
			replayed, replayErr := restarted.CorrectCompletedSource(ctx, DefaultLocalOwnerScope, dispatch, q.ProblemID, request)
			if reviewer.calls != 1 {
				t.Fatalf("source review was resent: calls=%d", reviewer.calls)
			}
			current, err := deps.Records.GetGradingFinalArtifactByJob(ctx, original.AgentName, original.JobID)
			if err != nil {
				t.Fatal(err)
			}
			if unknown {
				if !errors.Is(replayErr, ErrModelInvocationRequiresReconciliation) || current.ArtifactID != original.ArtifactID {
					t.Fatalf("unknown correction changed the original report: artifact=%s err=%v", current.ArtifactID, replayErr)
				}
				request.IdempotencyKey = "another-unauthorized-recovery"
				if _, err := restarted.CorrectCompletedSource(ctx, DefaultLocalOwnerScope, dispatch, q.ProblemID, request); err == nil || reviewer.calls != 1 {
					t.Fatal("another command bypassed an unknown source review")
				}
				return
			}
			if replayErr != nil || !replayed.Replayed || current.ArtifactID != result.Artifact.ArtifactID || current.ArtifactID == original.ArtifactID {
				t.Fatalf("report correction replay lost its revision: %+v err=%v", replayed, replayErr)
			}
			if current.HasAnnotatedAsset() || current.AnnotatedAssetOwnerScope != "" || current.OriginalSourceDigest != "" ||
				current.TeachingReport == nil || !reflect.DeepEqual(current.TeachingReport.SourcePages, original.TeachingReport.SourcePages) {
				t.Fatal("report-only correction fabricated an annotation or changed the frozen source page")
			}
			if len(current.TeachingReport.DeepSections) != 1 {
				t.Fatalf("source correction discarded the complete reference explanation: deep_sections=%d", len(current.TeachingReport.DeepSections))
			}
			assessments, err := deps.Records.ListEffectiveGradingAssessments(ctx, original.AgentName, original.JobID)
			if err != nil || len(assessments) != 1 || assessments[0].Status != k12.GradingAssessmentBlankSolved || assessments[0].ParentGuideInvocationID == "" || assessments[0].InputRevision != q.ConfirmedVersion+1 {
				t.Fatalf("blank source correction did not preserve a complete reference with its own revision: %+v err=%v", assessments, err)
			}
			old, err := deps.Records.GetGradingFinalArtifact(ctx, original.AgentName, original.ArtifactID)
			if err != nil || old.ArtifactDigest != original.ArtifactDigest || old.ReportSnapshotJSON != original.ReportSnapshotJSON {
				t.Fatalf("correction rewrote the original report: %v", err)
			}
			page, err := (&PageAssetRepository{Records: deps.Records}).OpenReady(ctx, DefaultLocalOwnerScope, original.AgentName, current.TeachingReport.SourcePages[0].AssetID)
			if err != nil || !bytes.Equal(page.Data, pixels) {
				t.Fatalf("original source pixels changed: %v", err)
			}
			command, err := deps.Records.GetFinalSourceCorrection(ctx, original.AgentName, result.CorrectionID)
			if err != nil || command.Request.SourceDigest != page.Metadata.ContentDigest || !command.Request.Model.RecognizingRequestPolicy.IsZero() {
				t.Fatalf("source identity or legacy request policy changed: %+v err=%v", command.Request, err)
			}
			if len(reviewer.seen) != 1 || reviewer.seen[0].SourceRegion == nil {
				t.Fatal("existing frozen region recovery was not used")
			}
		})
	}
}

func TestReportOnlySourceCorrectionRejectsMismatchedIdentityBeforeReview(t *testing.T) {
	deps, _, orchestrator, original, _ := originalTeachingReportFixtureWithRegion(t, &k12.SourcePixelRegion{Width: 8, Height: 4})
	ctx := t.Context()
	job, err := deps.GetGradingJob(ctx, original.AgentName, original.JobID)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := gradingFinalImageTaskDispatchID(job)
	if err != nil {
		t.Fatal(err)
	}
	questions, err := deps.loadCurrentConfirmedQuestions(ctx, original.AgentName, job.Fields.SubmissionID)
	if err != nil || len(questions) != 1 {
		t.Fatalf("questions=%d err=%v", len(questions), err)
	}
	q := questions[0]
	request := FinalSourceCorrectionInput{Agent: original.AgentName, IdempotencyKey: "invalid-report-source", ArtifactID: original.ArtifactID, ArtifactDigest: original.ArtifactDigest, InputRevision: q.ConfirmedVersion, InputDigest: q.InputDigest}
	reviewer := &completedSourceReviewerFixture{verified: true}
	orchestrator.deps.Recognizer = reviewer
	// 另一owner自己的ready图片不能替代原dispatch的归属和来源绑定。
	_, err = (&PageAssetRepository{Records: deps.Records}).Persist(ctx, "other-owner", original.AgentName, validPNGFixture(t, "other-source-owner"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Records.ResolveGradingFinalSourcePage(ctx, "other-owner", original.AgentName, dispatch, original.ArtifactID); !errors.Is(err, records.ErrNotFound) {
		t.Fatalf("source resolver crossed the original owner binding: %v", err)
	}
	for _, mutate := range []func(*FinalSourceCorrectionInput){
		func(r *FinalSourceCorrectionInput) { r.ArtifactDigest = strings.Repeat("a", 64) },
		func(r *FinalSourceCorrectionInput) { r.InputRevision++ },
		func(r *FinalSourceCorrectionInput) { r.InputDigest = "stale-input" },
	} {
		invalid := request
		mutate(&invalid)
		if _, err := orchestrator.CorrectCompletedSource(ctx, DefaultLocalOwnerScope, dispatch, q.ProblemID, invalid); !errors.Is(err, records.ErrVersionConflict) {
			t.Fatalf("stale correction identity was accepted: %v", err)
		}
	}
	if _, _, err := deps.Records.PrepareFinalSourceCorrection(ctx, k12storage.FinalSourceCorrectionRequest{
		CorrectionID: "wrong-source-digest", AgentName: original.AgentName, JobID: original.JobID, DispatchID: dispatch, OwnerScope: DefaultLocalOwnerScope,
		SubmissionID: job.Fields.SubmissionID, ProblemID: q.ProblemID, InputRevision: q.ConfirmedVersion, InputDigest: q.InputDigest,
		StructureVersion: original.StructureVersion, OriginalArtifactID: original.ArtifactID, OriginalArtifactDigest: original.ArtifactDigest,
		SourceDigest: strings.Repeat("a", 64), Model: job.Fields.ModelSnapshot,
	}); !errors.Is(err, records.ErrVersionConflict) {
		t.Fatalf("source digest mismatch was accepted: %v", err)
	}
	var commands int
	if err := deps.Records.DB().QueryRow(`SELECT count(*) FROM k12_final_source_corrections`).Scan(&commands); err != nil || commands != 0 || reviewer.calls != 0 {
		t.Fatalf("invalid source correction escaped preflight: commands=%d calls=%d err=%v", commands, reviewer.calls, err)
	}
}

func (s *completedSourceReviewerFixture) Recognize(context.Context, []byte) ([]RecognizedQuestion, error) {
	return nil, errors.New("unexpected full-page recognition")
}
func (s *completedSourceReviewerFixture) ReviewCompletedSource(ctx context.Context, _ []byte, q RecognizedQuestion) (CompletedSourceReview, error) {
	model, _ := k12.GradingModelSnapshotFromContext(ctx)
	policy, _ := k12.GradingModelRequestPolicyFromContext(ctx)
	if err := k12.ValidateModelInvocationRequestPolicy(k12.GradingStageRecognizing, model, policy); err != nil {
		return CompletedSourceReview{}, err
	}
	s.calls++
	s.seen = append(s.seen, q)
	if s.fail {
		return CompletedSourceReview{}, context.DeadlineExceeded
	}
	q.AnswerState = AnswerStateBlank
	q.StudentAnswer = ""
	confidence := 0.99
	q.RecognitionConfidence = &confidence
	return CompletedSourceReview{Question: q, Verified: s.verified, Raw: `{"independent":"blank"}`}, nil
}

type completedSourceAnnotatorFixture struct {
	raw   []byte
	fail  bool
	calls int
}

func (s *completedSourceAnnotatorFixture) Annotate(context.Context, []byte, []PhotoAnnotation) (RenderedPhoto, error) {
	s.calls++
	if s.fail {
		return RenderedPhoto{}, errors.New("renderer failed")
	}
	return RenderedPhoto{Data: s.raw, MIME: "image/png"}, nil
}

type completedSourceFixture struct {
	o         *GradingOrchestrator
	store     *k12storage.Store
	dispatch  string
	job       GradingJobView
	old       k12.GradingFinalArtifact
	request   FinalSourceCorrectionInput
	reviewer  *completedSourceReviewerFixture
	annotator *completedSourceAnnotatorFixture
	oldPNG    []byte
}

type completedSourceImageFixture struct {
	sourceKind    k12.ImageTaskSourceKind
	data          []byte
	rawReader     bool
	unannotated   bool
	frozenDigest  string
	frozenOwner   string
	messageLocale string
	compound      bool
	keepOtherMark bool
}

func prepareCompletedSourceFixture(t *testing.T) completedSourceFixture {
	return prepareCompletedSourceFixtureForIntent(t, k12.ImageTaskIntentCompletedHomework)
}

func prepareCompletedSourceFixtureForIntent(t *testing.T, intent k12.ImageTaskIntent, sourceImages ...completedSourceImageFixture) completedSourceFixture {
	t.Helper()
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	ctx := context.Background()
	deps, store := newPipeline(t, fakeSolver{solution: "1+1=2。\n\n答案：2", ev: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}}, nil, nil)
	deps.ParentTeachingGuide = &grading20260726RestartGuide{}
	reviewer := &completedSourceReviewerFixture{verified: true}
	annotator := &completedSourceAnnotatorFixture{raw: validPNGFixture(t, "corrected-final")}
	deps.Recognizer = reviewer
	deps.PhotoAnnotator = annotator
	o := trackGradingOrchestrator(t, NewGradingOrchestrator(deps, orchestratorSnapshotResolver, WithGradingRunDir(t.TempDir())))
	repo := &PageAssetRepository{Records: store}
	sourceImage := completedSourceImageFixture{data: validPNGFixture(t, "original-final")}
	if len(sourceImages) != 0 {
		sourceImage = sourceImages[0]
	}
	original, err := repo.Persist(ctx, "guardian-final", "mingming", sourceImage.data)
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &ImageTaskCoordinator{Records: store, PageAssets: repo, Classifier: &imageTaskClassifierStub{result: ImageTaskClassification{Intent: intent, IntentEvidence: []string{"source content"}, Confidence: 1}}, ResolveRoute: imageTaskRouteForTest}
	if sourceImage.rawReader {
		coordinator.PageAssets = nil
		coordinator.ReadAsset = func(string, string) ([]byte, error) {
			return append([]byte(nil), sourceImage.data...), nil
		}
	}
	in := testCreateImageTaskInput()
	if sourceImage.sourceKind != "" {
		in.SourceKind = sourceImage.sourceKind
	}
	in.OwnerScope = "guardian-final"
	in.SourceRef = "source-correction"
	in.SourceAssetRefs = []string{original.Metadata.PageAssetID}
	in.RouteRequest.MessageLocale = sourceImage.messageLocale
	view, _, err := coordinator.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if sourceImage.frozenDigest != "" || sourceImage.frozenOwner != "" {
		invocation, err := store.GetImageTaskInvocation(ctx, in.AgentName, view.Dispatch.ClassificationInvocationID)
		if err != nil {
			t.Fatal(err)
		}
		dispatch := view.Dispatch
		dispatch.DispatchID += "-source-fixture"
		dispatch.IdempotencyKey += "-source-fixture"
		dispatch.ClassificationInvocationID += "-source-fixture"
		// 克隆任务沿用实际持久分类路由，展示视图可能省略内部能力字段。
		dispatch.ClassificationRouteSnapshot = invocation.RouteSnapshot
		if sourceImage.frozenDigest != "" {
			dispatch.SourceDigest = sourceImage.frozenDigest
		}
		if sourceImage.frozenOwner != "" {
			dispatch.OwnerScope = sourceImage.frozenOwner
		}
		invocation.InvocationID = dispatch.ClassificationInvocationID
		invocation.DispatchID = dispatch.DispatchID
		invocation.OperationKey = "dispatch:" + dispatch.DispatchID + ":classification"
		view.Dispatch, _, err = store.PrepareImageTaskDispatch(ctx, dispatch, invocation)
		if err != nil {
			t.Fatal(err)
		}
	}
	dispatchID := view.Dispatch.DispatchID
	submissionID := scopedPhotoSubmissionID(original.Data, "image_task", dispatchID)
	// 临时真实库冻结一份已交付的双题任务；模型仅在独立复核边界使用替身。
	jobRecord, err := k12.NewGradingJobRecord("mingming", "session-final", k12.GradingJobFields{SubmissionID: submissionID, SourceKind: "image_task", IdempotencyKey: k12.BuildGradingIdempotencyKey("image_task", dispatchID, 1), ConfirmedVersion: 1, ConfirmationState: k12.GradingConfirmationConfirmed, AnchorState: k12.GradingAnchorLocated, ModelSnapshot: k12.GradingModelSnapshot{Provider: "provider-a", Model: "model-a", Route: "provider-a/model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	jobRecord.Status = k12.GradingStageCompleted
	if _, err = store.Put(ctx, jobRecord); err != nil {
		t.Fatal(err)
	}
	assetRefs, _ := json.Marshal(in.SourceAssetRefs)
	_, err = store.DB().Exec(`INSERT INTO k12_homework_submissions(submission_id,dispatch_id,agent_name,learner_id,source_kind,source_ref,source_asset_refs_json,task_intent,status,grading_job_id,idempotency_key,created_at,updated_at) VALUES(?,?,'mingming','mingming',?,'source-correction',?,?,'completed',?,'homework-final',100,100)`, submissionID, dispatchID, in.SourceKind, string(assetRefs), intent, jobRecord.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DB().Exec(`UPDATE k12_image_task_dispatches SET task_intent=?,target_object_type='homework_submission',target_object_id=? WHERE dispatch_id=?`, intent, submissionID, dispatchID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := k12.ProblemAttemptSnapshot{}
	for _, id := range []string{"target", "other"} {
		answer := "unclear"
		if id == "other" && !sourceImage.keepOtherMark {
			answer = "blank"
		}
		snapshot.Problems = append(snapshot.Problems, k12.Problem{ProblemID: id, AgentName: "mingming", SubmissionID: submissionID, PageAssetID: original.Metadata.PageAssetID, Ordinal: len(snapshot.Problems), ProblemKind: k12.ProblemKindStandalone, SourceNumberPath: []string{id}, DisplayLabel: id, Subject: "数学", StemRaw: "1+1=", StemMarkdown: "1+1=", CanonicalVersion: 1, CreatedAt: 100, UpdatedAt: 100})
		snapshot.Attempts = append(snapshot.Attempts, k12.Attempt{AttemptID: "attempt-" + id, AgentName: "mingming", SubmissionID: submissionID, ProblemID: id, AnswerState: answer, ConfirmedVersion: 1, InputDigest: "input-" + id, CreatedAt: 100, UpdatedAt: 100})
	}
	if sourceImage.compound {
		parent := snapshot.Problems[0]
		parent.ProblemID, parent.ProblemKind = "shared", k12.ProblemKindCompoundParent
		parent.Subject, parent.SourceNumberPath, parent.DisplayLabel = "语文", []string{"1"}, "1"
		parent.StemRaw, parent.StemMarkdown = "短文：池边的柳树是浅绿色。", "短文：池边的柳树是浅绿色。"
		child := &snapshot.Problems[0]
		child.Ordinal, child.Subject = 1, "语文"
		child.ProblemKind, child.ParentProblemID, child.SubproblemNo = k12.ProblemKindSubproblem, parent.ProblemID, "1"
		child.SourceNumberPath, child.DisplayLabel = []string{"1", "（1）"}, "（1）"
		child.StemRaw, child.StemMarkdown = "短文中的柳树是什么颜色？", "短文中的柳树是什么颜色？"
		snapshot.Problems[1].Ordinal = 2
		snapshot.Problems = append([]k12.Problem{parent}, snapshot.Problems...)
	}
	if err = store.PutProblemAttemptSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	questions, err := RecognizedQuestionsFromProblemAttemptSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// 历史当前投影缺少区域，完整回执保留原像素；恢复只补来源，不改旧作答。
	for i := range questions {
		questions[i].SourceRegion = &k12.SourcePixelRegion{Width: original.Metadata.PixelWidth, Height: original.Metadata.PixelHeight}
		questions[i].SourceWidth, questions[i].SourceHeight = original.Metadata.PixelWidth, original.Metadata.PixelHeight
	}
	legacyQuestions := cloneRecognizedQuestions(questions)
	for i := range legacyQuestions {
		legacyQuestions[i].PageAssetID = "legacy-logical-page"
	}
	recognitionReceipt := gradingRecognitionReceiptFile{JobID: jobRecord.RecordID, AgentName: "mingming", InvocationID: "recognition-original", Questions: legacyQuestions, CanonicalDigest: CanonicalRecognizedQuestionsDigest(legacyQuestions)}
	receiptRaw, _ := json.Marshal(recognitionReceipt)
	if err = os.MkdirAll(filepath.Dir(o.recognitionReceiptPath(jobRecord.RecordID)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = atomicWriteFile(o.recognitionReceiptPath(jobRecord.RecordID), receiptRaw); err != nil {
		t.Fatal(err)
	}
	for _, q := range questions {
		if q.ProblemKind == ProblemKindCompoundParent {
			continue
		}
		status := PhotoAnswerUnclear
		receiptStatus := k12.GradingAssessmentAnswerUnclear
		if q.ProblemID == "other" && !sourceImage.keepOtherMark {
			status = PhotoUnanswered
			receiptStatus = k12.GradingAssessmentUnanswered
		}
		item := gradingAssessmentCanonicalResult(PhotoGradeItem{Recognized: q, Status: status})
		raw, _ := json.Marshal(item)
		_, _, err = store.CommitGradingAssessmentItem(ctx, k12.GradingAssessmentItem{AgentName: "mingming", JobID: jobRecord.RecordID, ProblemID: q.ProblemID, AttemptID: q.AttemptID, ConfirmedVersion: 1, InputRevision: 1, StructureVersion: 1, InputDigest: q.InputDigest, Status: receiptStatus, ResultJSON: string(raw), ResultDigest: modelInvocationResultDigest(item), ProjectionStatus: k12.GradingProjectionCommitted, CreatedAt: 200}, k12storage.GradingAssessmentEffects{})
		if err != nil {
			t.Fatal(err)
		}
	}
	job, err := deps.GetGradingJob(ctx, "mingming", jobRecord.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	oldPNG := validPNGFixture(t, "old-final")
	run := &gradingRun{agentName: "mingming", req: PhotoGradeRequest{AgentName: "mingming", Grade: "五年级上", Subject: "数学", SourceSession: "session-final", Image: original.Data, SourcePageAssetID: original.Metadata.PageAssetID, TaskIntent: PhotoTaskCompletedHomework}, questions: questions, result: &PhotoGradeResult{AnnotatedImage: &RenderedPhoto{Data: oldPNG, MIME: "image/png"}}}
	if intent == k12.ImageTaskIntentBlankWorksheet {
		run.req.TaskIntent = PhotoTaskBlankWorksheet
	}
	if err := o.persistRun(jobRecord.RecordID, run); err != nil {
		t.Fatal(err)
	}
	if sourceImage.unannotated {
		run.result.AnnotatedImage = nil
	}
	artifact, err := o.finalizeGradingPage(ctx, run, job)
	if err != nil {
		t.Fatal(err)
	}
	return completedSourceFixture{o: o, store: store, dispatch: dispatchID, job: job, old: artifact, request: FinalSourceCorrectionInput{Agent: "mingming", IdempotencyKey: "once", ArtifactID: artifact.ArtifactID, ArtifactDigest: artifact.ArtifactDigest, InputRevision: 1, InputDigest: "input-target"}, reviewer: reviewer, annotator: annotator, oldPNG: oldPNG}
}

func TestCompletedSourceCorrection_MissingRegionNeverSent(t *testing.T) {
	f := prepareCompletedSourceFixture(t)
	if err := os.Remove(f.o.recognitionReceiptPath(f.job.Record.RecordID)); err != nil {
		t.Fatal(err)
	}
	_, err := f.o.CorrectCompletedSource(context.Background(), "guardian-final", f.dispatch, "target", f.request)
	var commands int
	if e := f.store.DB().QueryRow(`SELECT count(*) FROM k12_final_source_corrections`).Scan(&commands); e != nil {
		t.Fatal(e)
	}
	if err == nil || commands != 0 || f.reviewer.calls != 0 {
		t.Fatalf("missing region sent or claimed: commands=%d calls=%d err=%v", commands, f.reviewer.calls, err)
	}
}

func TestSourceReferenceUnknownKeepsOriginalFinalAndDoesNotResend(t *testing.T) {
	f := prepareCompletedSourceFixture(t)
	solver := &grading20260726PhysicalSolver{calls: map[k12.GradingItemOperation]int{}, failFirst: map[k12.GradingItemOperation]error{k12.GradingItemOperationSolveGenerate: io.ErrUnexpectedEOF}}
	f.o.deps.Solver = solver
	for i := 0; i < 2; i++ {
		if _, err := f.o.CorrectCompletedSource(t.Context(), "guardian-final", f.dispatch, "target", f.request); err == nil {
			t.Fatal("unknown reference solve was published as complete")
		}
		current, err := f.store.GetGradingFinalArtifactByJob(t.Context(), "mingming", f.job.Record.RecordID)
		if err != nil || current.ArtifactID != f.old.ArtifactID {
			t.Fatalf("unknown reference changed the original final: %v", err)
		}
	}
	if solver.calls[k12.GradingItemOperationSolveGenerate] != 1 || f.reviewer.calls != 1 {
		t.Fatalf("unknown source/reference was resent: generate=%d review=%d", solver.calls[k12.GradingItemOperationSolveGenerate], f.reviewer.calls)
	}
	questions, err := f.o.deps.loadCurrentConfirmedQuestions(t.Context(), "mingming", f.job.Fields.SubmissionID)
	if err != nil || questions[0].ConfirmedVersion != 1 {
		t.Fatalf("unknown reference published a partial input revision: %v", err)
	}
	rows, err := f.store.ListGradingItemInvocations(t.Context(), "mingming", f.job.Record.RecordID)
	if err != nil || len(rows) != 1 || rows[0].Status != k12.ModelInvocationOutcomeUnknown || rows[0].InputRevision != 2 {
		t.Fatalf("reference outcome ledger=%+v err=%v", rows, err)
	}
	id := "source-correction-" + modelInvocationDigest([]byte(f.request.Agent), []byte(f.dispatch), []byte(f.request.IdempotencyKey))
	command, err := f.store.GetFinalSourceCorrection(t.Context(), f.request.Agent, id)
	if err != nil {
		t.Fatal(err)
	}
	request := f.request
	request.IdempotencyKey = "unsafe-reference-recovery"
	request.RecoveryOf, request.RecoveryResponseDigest = id, command.ResponseDigest
	if _, err := f.o.CorrectCompletedSource(t.Context(), "guardian-final", f.dispatch, "target", request); !errors.Is(err, records.ErrVersionConflict) || solver.count(k12.GradingItemOperationSolveGenerate) != 1 {
		t.Fatalf("explicit known-failure recovery admitted an unknown solve: %v", err)
	}
}

func TestSourceReferenceKnown503RetriesOnlyItsFailedStep(t *testing.T) {
	f := prepareCompletedSourceFixture(t)
	solver := &grading20260726PhysicalSolver{calls: map[k12.GradingItemOperation]int{}, failFirst: map[k12.GradingItemOperation]error{
		k12.GradingItemOperationSolveVerify: errItemResumeProvider503,
	}}
	f.o.deps.Solver = solver
	ctx := t.Context()
	first, err := f.o.CorrectCompletedSource(ctx, "guardian-final", f.dispatch, "target", f.request)
	if err == nil {
		t.Fatal("expected a known verifier failure")
	}
	old, err := f.store.GetFinalSourceCorrection(ctx, f.request.Agent, first.CorrectionID)
	if err != nil || old.Status != "succeeded" {
		t.Fatalf("source reading did not settle: %v", err)
	}
	if _, err := f.o.CorrectCompletedSource(ctx, "guardian-final", f.dispatch, "target", f.request); !errors.Is(err, ErrGradingItemInvocationFailed) || solver.count(k12.GradingItemOperationSolveVerify) != 1 {
		t.Fatalf("original key reissued the failed verifier: %v", err)
	}
	retry := f.request
	retry.IdempotencyKey = "explicit-verifier-recovery"
	retry.RecoveryOf, retry.RecoveryResponseDigest = first.CorrectionID, old.ResponseDigest
	recovered, err := f.o.CorrectCompletedSource(ctx, "guardian-final", f.dispatch, "target", retry)
	if err != nil || recovered.Artifact == nil || recovered.Status != "completed" {
		t.Fatalf("explicit recovery: status=%s %v", recovered.Status, err)
	}
	if solver.count(k12.GradingItemOperationSolveGenerate) != 1 || solver.count(k12.GradingItemOperationSolveVerify) != 2 || f.reviewer.calls != 1 {
		t.Fatalf("wrong physical replay counts: generate=%d verify=%d source=%d", solver.count(k12.GradingItemOperationSolveGenerate), solver.count(k12.GradingItemOperationSolveVerify), f.reviewer.calls)
	}
	if after, err := f.store.GetFinalSourceCorrection(ctx, f.request.Agent, first.CorrectionID); err != nil || !reflect.DeepEqual(after, old) {
		t.Fatalf("old source correction was modified: %v", err)
	}
	replay, err := f.o.CorrectCompletedSource(ctx, "guardian-final", f.dispatch, "target", retry)
	if err != nil || !replay.Replayed || replay.Artifact == nil || replay.Artifact.ArtifactID != recovered.Artifact.ArtifactID || solver.count(k12.GradingItemOperationSolveVerify) != 2 {
		t.Fatalf("recovery key was not idempotent: %v", err)
	}
}

func TestSourceCorrectionUsesFrozenSharedStemInSolve(t *testing.T) {
	f := prepareCompletedSourceFixtureForIntent(t, k12.ImageTaskIntentCompletedHomework, completedSourceImageFixture{data: validPNGFixture(t, "compound question"), compound: true})
	solver := &captureFinalSourceSolver{}
	f.o.deps.Solver = solver
	_, err := f.o.CorrectCompletedSource(t.Context(), "guardian-final", f.dispatch, "target", f.request)
	if err == nil || !strings.Contains(solver.problem, "池边的柳树是浅绿色") || !strings.Contains(solver.problem, "短文中的柳树是什么颜色") {
		t.Fatalf("source solve lost the shared or current stem: %q %v", solver.problem, err)
	}
}

func TestSourceCorrectionDoesNotPublishEmptyAnnotation(t *testing.T) {
	original := validPNGFixture(t, "single annotated problem")
	f := prepareCompletedSourceFixtureForIntent(t, k12.ImageTaskIntentCompletedHomework, completedSourceImageFixture{data: original})
	result, err := f.o.CorrectCompletedSource(t.Context(), "guardian-final", f.dispatch, "target", f.request)
	if err != nil || result.Artifact == nil {
		t.Fatalf("source correction with no remaining marks failed: %v", err)
	}
	if result.Artifact.HasAnnotatedAsset() || !f.old.HasAnnotatedAsset() || f.annotator.calls != 0 {
		t.Fatal("the final mark was withdrawn but an annotation was still rendered")
	}
	page, err := f.store.ResolveGradingFinalSourcePage(t.Context(), "guardian-final", "mingming", f.dispatch, result.Artifact.ArtifactID)
	if err != nil || page.ContentDigest != f.old.OriginalSourceDigest {
		t.Fatalf("the corrected legacy artifact lost its original page: %v", err)
	}
	old, err := f.store.OpenGradingFinalAnnotatedAsset(t.Context(), "mingming", f.old.ArtifactID)
	if err != nil || !bytes.Equal(old.Data, f.oldPNG) {
		t.Fatalf("the previously delivered annotation was overwritten: %v", err)
	}
}

type captureFinalSourceSolver struct{ problem string }

func (s *captureFinalSourceSolver) Solve(_ context.Context, problem, _, _ string) (SolveResult, error) {
	s.problem = problem
	return SolveResult{}, errors.New("stop after observing the solve input")
}

func TestSourceReferencePhysicalNamespaceKeepsOrdinaryIdentityAndStableReplay(t *testing.T) {
	f := prepareCompletedSourceFixture(t)
	questions, err := f.o.deps.loadCurrentConfirmedQuestions(t.Context(), "mingming", f.job.Fields.SubmissionID)
	if err != nil {
		t.Fatal(err)
	}
	q := questions[0]
	spec := GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveGenerate, RequestDigest: "sha256:same-provider-request"}
	sends := 0
	send := func(context.Context) (string, error) { sends++; return `{"output":"2"}`, nil }
	ordinary := newDurableGradingPhysicalCallExecutor(f.o, f.job, q)
	old, err := ordinary.ExecuteGradingPhysicalCall(t.Context(), spec, send)
	if err != nil {
		t.Fatal(err)
	}
	q.ConfirmedVersion, q.InputDigest = 2, "source-revision-two"
	ctx := context.WithValue(t.Context(), finalSourceReferenceContextKey{}, finalSourceReferenceScope{digest: "source-command-one:source-revision-two"})
	current := newDurableGradingPhysicalCallExecutor(f.o, f.job, q)
	first, err := current.ExecuteGradingPhysicalCall(ctx, spec, send)
	if err != nil || first.InvocationID == old.InvocationID {
		t.Fatalf("source reference rebound an old physical receipt: %v", err)
	}
	restarted := newDurableGradingPhysicalCallExecutor(f.o, f.job, q)
	replayed, err := restarted.ExecuteGradingPhysicalCall(ctx, spec, send)
	if err != nil || replayed.InvocationID != first.InvocationID || sends != 2 {
		t.Fatalf("same source reference was resent: sends=%d err=%v", sends, err)
	}
	rows, err := f.store.ListGradingItemInvocations(t.Context(), "mingming", f.job.Record.RecordID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("physical namespace ledger=%+v err=%v", rows, err)
	}
	for _, row := range rows {
		if row.InvocationID == old.InvocationID && (row.RequestDigest != spec.RequestDigest || row.InputRevision != 1) {
			t.Fatal("ordinary physical request identity changed")
		}
		if row.InvocationID == first.InvocationID && (row.RequestDigest == spec.RequestDigest || row.InputRevision != 2 || row.InputDigest != q.InputDigest) {
			t.Fatal("source reference did not freeze its own input identity")
		}
	}
}

func TestCompletedSourceReferenceRecoveryUsesNewCommandAndKeepsOriginalReceipt(t *testing.T) {
	deps, _, orchestrator, original, _ := originalTeachingReportFixtureWithRegion(t, &k12.SourcePixelRegion{Width: 8, Height: 4})
	ctx := t.Context()
	job, err := deps.GetGradingJob(ctx, original.AgentName, original.JobID)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := gradingFinalImageTaskDispatchID(job)
	if err != nil {
		t.Fatal(err)
	}
	questions, err := deps.loadCurrentConfirmedQuestions(ctx, original.AgentName, job.Fields.SubmissionID)
	if err != nil || len(questions) != 1 {
		t.Fatalf("questions=%d err=%v", len(questions), err)
	}
	q := questions[0]
	q.SourceRegion = &k12.SourcePixelRegion{Width: 8, Height: 4}
	q.SourceWidth, q.SourceHeight = 8, 4
	prior, err := deps.Records.GetGradingAssessmentItem(ctx, original.AgentName, original.JobID, q.ProblemID)
	if err != nil {
		t.Fatal(err)
	}
	request := FinalSourceCorrectionInput{Agent: original.AgentName, IdempotencyKey: "legacy-incomplete-source", ArtifactID: original.ArtifactID, ArtifactDigest: original.ArtifactDigest, InputRevision: q.ConfirmedVersion, InputDigest: q.InputDigest}
	id := "source-correction-" + modelInvocationDigest([]byte(request.Agent), []byte(dispatch), []byte(request.IdempotencyKey))
	command, _, err := deps.Records.PrepareFinalSourceCorrection(ctx, k12storage.FinalSourceCorrectionRequest{CorrectionID: id, AgentName: original.AgentName, JobID: original.JobID, DispatchID: dispatch, OwnerScope: DefaultLocalOwnerScope, SubmissionID: job.Fields.SubmissionID, ProblemID: q.ProblemID, InputRevision: q.ConfirmedVersion, InputDigest: q.InputDigest, StructureVersion: prior.StructureVersion, OriginalArtifactID: original.ArtifactID, OriginalArtifactDigest: original.ArtifactDigest, SourceDigest: original.TeachingReport.SourcePages[0].SourceDigest, Model: job.Fields.ModelSnapshot})
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := deps.Records.ClaimFinalSourceCorrection(ctx, original.AgentName, id); err != nil || !claimed {
		t.Fatalf("claim=%t err=%v", claimed, err)
	}
	reviewer := &completedSourceReviewerFixture{verified: true}
	legacyReview, err := reviewer.ReviewCompletedSource(ctx, nil, q)
	if err != nil {
		t.Fatal(err)
	}
	response, _ := json.Marshal(legacyReview)
	if err := deps.Records.SettleFinalSourceCorrection(ctx, original.AgentName, id, "succeeded", string(response), ""); err != nil {
		t.Fatal(err)
	}
	q.AnswerState, q.StudentAnswer, q.AnswerRawTranscription, q.AnswerCanonicalMarkdown = AnswerStateBlank, "", "", ""
	q.ConfirmedVersion++
	q.CanonicalVersion = q.ConfirmedVersion
	q.InputDigest = k12storage.FinalSourceInputDigest(command, q.ProblemID, q.ConfirmedVersion)
	item := gradingAssessmentCanonicalResult(PhotoGradeItem{Recognized: q, Status: PhotoUnanswered})
	payload, _ := json.Marshal(item)
	assessment := k12.GradingAssessmentItem{AgentName: original.AgentName, JobID: original.JobID, ProblemID: q.ProblemID, AttemptID: q.AttemptID, InputRevision: q.ConfirmedVersion, ConfirmedVersion: q.ConfirmedVersion, InputDigest: q.InputDigest, PublishedRevision: prior.PublishedRevision + 1, StructureVersion: prior.StructureVersion, CurrentDisposition: k12.GradingAssessmentDispositionCurrent, Status: k12.GradingAssessmentUnanswered, ResultJSON: string(payload), ResultDigest: modelInvocationResultDigest(item), ProjectionStatus: k12.GradingProjectionCommitted, CreatedAt: deps.now(), UpdatedAt: deps.now()}
	ordered, _ := json.Marshal([]string{assessment.ResultDigest})
	bad := original
	bad.ArtifactID, bad.SummaryInvocationID, bad.OrderedCurrentDigestsJSON = "final-"+id, "", string(ordered)
	bad.CanonicalMarkdown = renderCanonicalFinalForTask([]gradingFinalEntry{{question: q, assessment: &assessment}}, PhotoTaskCompletedHomework, bad.CoverageStatus)
	var snapshot TeachingReportFrozenSnapshot
	if err := json.Unmarshal([]byte(original.ReportSnapshotJSON), &snapshot); err != nil {
		t.Fatal(err)
	}
	run, err := orchestrator.ensureRun(ctx, original.JobID)
	if err != nil {
		t.Fatal(err)
	}
	legacyRun := &gradingRun{req: run.req, result: &PhotoGradeResult{}, questions: []RecognizedQuestion{q}}
	if err := orchestrator.freezeGradingTeachingReport(ctx, legacyRun, job, &bad, []k12.GradingAssessmentItem{assessment}, nil, ordered, int64(snapshot.Report.Source.Revision)); err != nil {
		t.Fatal(err)
	}
	bad.ArtifactDigest = k12.ComputeGradingFinalArtifactDigest(bad)
	source, _ := json.Marshal(q)
	bad, err = deps.Records.CommitFinalBlankSourceCorrection(ctx, id, bad, source, assessment)
	if err != nil || bad.TeachingReport == nil || len(bad.TeachingReport.DeepSections) != 0 {
		t.Fatalf("historical incomplete revision was not established through its original contract: err=%v", err)
	}
	before, err := deps.Records.GetFinalSourceCorrection(ctx, original.AgentName, id)
	if err != nil {
		t.Fatal(err)
	}
	orchestrator.deps.Recognizer = reviewer
	oldReplay, err := orchestrator.CorrectCompletedSource(ctx, DefaultLocalOwnerScope, dispatch, q.ProblemID, request)
	if err != nil || oldReplay.Artifact == nil || oldReplay.Artifact.ArtifactID != bad.ArtifactID || reviewer.calls != 1 {
		t.Fatalf("completed command was silently upgraded: %+v err=%v", oldReplay, err)
	}
	recovery := FinalSourceCorrectionInput{Agent: original.AgentName, IdempotencyKey: "explicit-reference-recovery", ArtifactID: bad.ArtifactID, ArtifactDigest: bad.ArtifactDigest, InputRevision: q.ConfirmedVersion, InputDigest: q.InputDigest, RecoveryOf: id, RecoveryResponseDigest: before.ResponseDigest}
	invalid := recovery
	invalid.RecoveryResponseDigest = "sha256:" + strings.Repeat("a", 64)
	if _, err := orchestrator.CorrectCompletedSource(ctx, DefaultLocalOwnerScope, dispatch, q.ProblemID, invalid); err == nil || reviewer.calls != 1 {
		t.Fatalf("mismatched completed recovery was accepted: err=%v calls=%d", err, reviewer.calls)
	}
	recovered, err := orchestrator.CorrectCompletedSource(ctx, DefaultLocalOwnerScope, dispatch, q.ProblemID, recovery)
	if err != nil || recovered.Artifact == nil || recovered.Status != "completed" || len(recovered.Artifact.TeachingReport.DeepSections) != 1 || reviewer.calls != 1 {
		t.Fatalf("explicit reference recovery failed or resent vision: %+v err=%v calls=%d", recovered, err, reviewer.calls)
	}
	after, err := deps.Records.GetFinalSourceCorrection(ctx, original.AgentName, id)
	if err != nil || after.Status != "completed" || after.ResponseJSON != before.ResponseJSON || after.ResponseDigest != before.ResponseDigest {
		t.Fatalf("completed source receipt changed: %v", err)
	}
	child, err := deps.Records.GetFinalSourceCorrection(ctx, original.AgentName, recovered.CorrectionID)
	if err != nil || child.Request.RecoveryOf != id || child.ResponseJSON != before.ResponseJSON || child.ResponseDigest != before.ResponseDigest {
		t.Fatalf("new command lost the exact original source evidence: %+v err=%v", child, err)
	}
	current, err := deps.Records.GetGradingAssessmentItem(ctx, original.AgentName, original.JobID, q.ProblemID)
	if err != nil || current.Status != k12.GradingAssessmentBlankSolved || current.InputRevision != 3 || current.ParentGuideInvocationID == "" {
		t.Fatalf("reference recovery did not publish its own new input receipt: %+v err=%v", current, err)
	}
	storedBad, err := deps.Records.GetGradingFinalArtifact(ctx, original.AgentName, bad.ArtifactID)
	if err != nil || storedBad.ArtifactDigest != bad.ArtifactDigest || storedBad.ReportSnapshotJSON != bad.ReportSnapshotJSON {
		t.Fatalf("reference recovery rewrote the old report: %v", err)
	}
}

func TestCompletedSourceCorrectionRestoresExactFrozenContextBeforeReview(t *testing.T) {
	f := prepareCompletedSourceFixture(t)
	questions, err := f.o.deps.loadCurrentConfirmedQuestions(t.Context(), "mingming", f.job.Fields.SubmissionID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.store.ResolveGradingFinalSourcePage(t.Context(), "guardian-final", "mingming", f.dispatch, f.old.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range questions {
		questions[i].ImageSourceContext = &k12.RecognitionImageSourceContextV1{Version: k12.RecognitionImageScopeVersionV1,
			ScopeDigest: "frozen-page-material", TargetIDs: []string{questions[i].ProblemID},
			Materials: []k12.RecognitionImageScopeDependencyV1{{Text: "本页计算题只填写答案。", Region: k12.SourcePixelRegion{Width: page.PixelWidth, Height: page.PixelHeight}}}}
	}
	audit := gradingRecognitionAuditFile{JobID: f.job.Record.RecordID, AgentName: "mingming", Questions: questions, CanonicalDigest: CanonicalRecognizedQuestionsDigest(questions)}
	raw, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	auditPath := f.o.recognitionAuditPath(f.job.Record.RecordID)
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(auditPath, raw); err != nil {
		t.Fatal(err)
	}
	executionInput := questions[0]
	executionInput.ImageSourceContext = nil
	executionInput.ConfirmedVersion++
	executionInput.InputDigest = "next-source-revision"
	executionInput.AnswerState, executionInput.StudentAnswer = AnswerStateBlank, ""
	borrowed, err := f.o.finalSourceReferenceQuestion(f.job, executionInput)
	if err != nil || borrowed.ImageSourceContext == nil || len(borrowed.ImageSourceContext.Materials) != 1 || executionInput.ImageSourceContext != nil {
		t.Fatalf("source reference did not retain printed materials in its execution copy: %v", err)
	}
	changed := executionInput
	changed.RawTranscription += " changed"
	if other, err := f.o.finalSourceReferenceQuestion(f.job, changed); err != nil || other.ImageSourceContext != nil {
		t.Fatalf("changed printed source borrowed another scope: %v", err)
	}
	result, err := f.o.CorrectCompletedSource(t.Context(), "guardian-final", f.dispatch, "target", f.request)
	if err != nil || result.Status != "completed" || len(f.reviewer.seen) != 1 {
		t.Fatalf("source correction failed: %s %v", result.Status, err)
	}
	if source := f.reviewer.seen[0].ImageSourceContext; source == nil || len(source.Materials) != 1 || source.Materials[0].Text != "本页计算题只填写答案。" {
		t.Fatal("source correction lost exact frozen shared material")
	}
	current, err := f.o.deps.loadCurrentConfirmedQuestions(t.Context(), "mingming", f.job.Fields.SubmissionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range current {
		if q.ProblemID == "target" && (q.ConfirmedVersion != 2 || q.ImageSourceContext != nil) {
			t.Fatal("corrected input retained the prior context proof")
		}
		if q.ProblemID == "target" {
			f.request.InputRevision, f.request.InputDigest = q.ConfirmedVersion, q.InputDigest
		}
	}
	f.request.IdempotencyKey = "next-source-revision"
	f.request.ArtifactID, f.request.ArtifactDigest = result.Artifact.ArtifactID, result.Artifact.ArtifactDigest
	next, err := f.o.CorrectCompletedSource(t.Context(), "guardian-final", f.dispatch, "target", f.request)
	if err != nil || next.Status != "completed" || len(f.reviewer.seen) != 2 || f.reviewer.seen[1].ImageSourceContext != nil {
		t.Fatalf("next revision lost its source region or borrowed old context: %s %v", next.Status, err)
	}
}

func TestCompletedSourceCorrection_ReconcileOnlyKnownPreflightFailure(t *testing.T) {
	for _, failure := range []string{"original source region unavailable", "recognizing invocation policy does not match frozen route policy", "context deadline exceeded"} {
		t.Run(failure, func(t *testing.T) {
			f := prepareCompletedSourceFixture(t)
			// 历史终稿在后续时点纠正，新产物不能继承早于创建时间的旧更新时间。
			f.o.deps.Now = func() int64 { return 2000 }
			ctx := context.Background()
			id := "source-correction-" + modelInvocationDigest([]byte("mingming"), []byte(f.dispatch), []byte(f.request.IdempotencyKey))
			r := k12storage.FinalSourceCorrectionRequest{CorrectionID: id, AgentName: "mingming", JobID: f.job.Record.RecordID, DispatchID: f.dispatch, OwnerScope: "guardian-final", SubmissionID: f.job.Fields.SubmissionID, ProblemID: "target", InputRevision: 1, StructureVersion: 1, InputDigest: f.request.InputDigest, OriginalArtifactID: f.old.ArtifactID, OriginalArtifactDigest: f.old.ArtifactDigest, SourceDigest: f.old.OriginalSourceDigest}
			r.Model = f.job.Fields.ModelSnapshot
			if _, _, err := f.store.PrepareFinalSourceCorrection(ctx, r); err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.ClaimFinalSourceCorrection(ctx, "mingming", id); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(CompletedSourceReview{})
			if err := f.store.SettleFinalSourceCorrection(ctx, "mingming", id, "outcome_unknown", string(raw), failure); err != nil {
				t.Fatal(err)
			}
			_, err := f.o.CorrectCompletedSource(ctx, "guardian-final", f.dispatch, "target", f.request)
			v, e := f.store.GetFinalSourceCorrection(ctx, "mingming", id)
			want := "outcome_unknown"
			if failure != "context deadline exceeded" {
				want = "failed"
			}
			if err == nil || e != nil || v.Status != want || v.ResponseJSON != string(raw) || v.Failure != failure || f.reviewer.calls != 0 {
				t.Fatalf("preflight reconciliation: %+v calls=%d err=%v read=%v", v, f.reviewer.calls, err, e)
			}
		})
	}
}

func TestCompletedSourceCorrection_AtomicRevisionAndReplay(t *testing.T) {
	for _, mode := range []string{"complete", "render_failure", "unknown", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			f := prepareCompletedSourceFixtureForIntent(t, k12.ImageTaskIntentCompletedHomework, completedSourceImageFixture{data: validPNGFixture(t, "two marked problems"), keepOtherMark: true})
			ctx := context.Background()
			before, err := f.store.GetGradingAssessmentItem(ctx, "mingming", f.job.Record.RecordID, "other")
			if err != nil {
				t.Fatal(err)
			}
			f.reviewer.fail = mode == "unknown"
			f.reviewer.verified = mode != "uncertain"
			f.annotator.fail = mode == "render_failure"
			result, err := f.o.CorrectCompletedSource(ctx, "guardian-final", f.dispatch, "target", f.request)
			if mode == "complete" && err != nil {
				t.Fatal(err)
			}
			if mode != "complete" && err == nil {
				t.Fatal("incomplete correction accepted")
			}
			current, e := f.store.GetGradingFinalArtifactByJob(ctx, "mingming", f.job.Record.RecordID)
			if e != nil {
				t.Fatal(e)
			}
			if mode != "complete" && current.ArtifactID != f.old.ArtifactID {
				t.Fatal("partial or unknown correction replaced old final")
			}
			// 同一命令重放不重复外发；渲染失败只重新消费已保存成功复核。
			f.annotator.fail = false
			restarted := trackGradingOrchestrator(t, NewGradingOrchestrator(f.o.deps, orchestratorSnapshotResolver, WithGradingRunDir(f.o.runDir)))
			replay, e := restarted.CorrectCompletedSource(ctx, "guardian-final", f.dispatch, "target", f.request)
			if mode == "complete" || mode == "render_failure" {
				if e != nil || replay.Artifact == nil {
					t.Fatalf("replay: %+v %v", replay, e)
				}
				if !replay.Replayed || replay.Artifact.ArtifactID == f.old.ArtifactID {
					t.Fatal("revision/replay identity missing")
				}
				if result.Artifact != nil && result.Artifact.ArtifactID != replay.Artifact.ArtifactID {
					t.Fatal("duplicate final revision")
				}
				current, e = f.store.GetGradingFinalArtifactByJob(ctx, "mingming", f.job.Record.RecordID)
				if e != nil || current.ArtifactID != replay.Artifact.ArtifactID {
					t.Fatal("current read/export head mismatch", e)
				}
				opened, e := f.store.OpenGradingFinalAnnotatedAsset(ctx, "mingming", current.ArtifactID)
				if e != nil || !bytes.Equal(opened.Data, f.annotator.raw) {
					t.Fatal("current PNG missing", e)
				}
				qs, e := f.o.deps.loadCurrentConfirmedQuestions(ctx, "mingming", f.job.Fields.SubmissionID)
				if e != nil {
					t.Fatal(e)
				}
				for _, q := range qs {
					if q.ProblemID == "target" && (q.AnswerState != AnswerStateBlank || q.ConfirmedVersion != 2 || q.StudentAnswer != "") {
						t.Fatalf("stale source: %+v", q)
					}
				}
				var attempts int
				if e = f.store.DB().QueryRow(`SELECT count(*) FROM k12_attempts WHERE agent_name='mingming' AND submission_id=?`, f.job.Fields.SubmissionID).Scan(&attempts); e != nil || attempts != 2 {
					t.Fatal("correction invented attempts", attempts, e)
				}
			} else if e == nil {
				t.Fatal("unknown/uncertain silently became successful")
			}
			if f.reviewer.calls != 1 {
				t.Fatalf("review called %d times", f.reviewer.calls)
			}
			if mode == "unknown" {
				unknown, e := f.store.GetFinalSourceCorrection(ctx, "mingming", result.CorrectionID)
				if e != nil {
					t.Fatal(e)
				}
				recovery := f.request
				recovery.IdempotencyKey += "-recovery"
				if _, e := restarted.CorrectCompletedSource(ctx, "guardian-final", f.dispatch, "target", recovery); e == nil || f.reviewer.calls != 1 {
					t.Fatal("new key retried unknown without explicit authorization")
				}
				recovery.RecoveryOf = result.CorrectionID
				recovery.RecoveryResponseDigest = "changed-response"
				recovery.AcceptDuplicateExecution = true
				if _, e := restarted.CorrectCompletedSource(ctx, "guardian-final", f.dispatch, "target", recovery); e == nil || f.reviewer.calls != 1 {
					t.Fatal("changed unknown receipt accepted")
				}
				recovery.RecoveryResponseDigest = unknown.ResponseDigest
				f.reviewer.fail = false
				recovered, e := restarted.CorrectCompletedSource(ctx, "guardian-final", f.dispatch, "target", recovery)
				if e != nil || recovered.Status != "completed" || f.reviewer.calls != 2 {
					t.Fatalf("explicit recovery: %+v calls=%d err=%v", recovered, f.reviewer.calls, e)
				}
				again, e := restarted.CorrectCompletedSource(ctx, "guardian-final", f.dispatch, "target", recovery)
				if e != nil || !again.Replayed || again.Artifact.ArtifactID != recovered.Artifact.ArtifactID || f.reviewer.calls != 2 {
					t.Fatal("recovery replay changed artifact or repeated call", e)
				}
				preserved, e := f.store.GetFinalSourceCorrection(ctx, "mingming", result.CorrectionID)
				if e != nil || !reflect.DeepEqual(unknown, preserved) {
					t.Fatal("original unknown receipt changed", e)
				}
			}
			old, e := f.store.GetGradingFinalArtifact(ctx, "mingming", f.old.ArtifactID)
			if e != nil || !reflect.DeepEqual(old, f.old) {
				t.Fatal("old final mutated", e)
			}
			oldImage, e := f.store.OpenGradingFinalAnnotatedAsset(ctx, "mingming", old.ArtifactID)
			if e != nil || !bytes.Equal(oldImage.Data, f.oldPNG) {
				t.Fatal("old delivered PNG changed", e)
			}
			after, e := f.store.GetGradingAssessmentItem(ctx, "mingming", f.job.Record.RecordID, "other")
			if e != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("unrelated assessment changed", e)
			}
		})
	}
}
