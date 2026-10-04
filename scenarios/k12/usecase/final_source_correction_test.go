package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type completedSourceReviewerFixture struct {
	calls    int
	fail     bool
	verified bool
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

func prepareCompletedSourceFixture(t *testing.T) completedSourceFixture {
	t.Helper()
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	ctx := context.Background()
	deps, store := newPipeline(t, nil, nil, nil)
	reviewer := &completedSourceReviewerFixture{verified: true}
	annotator := &completedSourceAnnotatorFixture{raw: validPNGFixture(t, "corrected-final")}
	deps.Recognizer = reviewer
	deps.PhotoAnnotator = annotator
	o := &GradingOrchestrator{deps: deps, runDir: t.TempDir()}
	repo := &PageAssetRepository{Records: store}
	original, err := repo.Persist(ctx, "guardian-final", "mingming", validPNGFixture(t, "original-final"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &ImageTaskCoordinator{Records: store, PageAssets: repo, Classifier: &imageTaskClassifierStub{result: ImageTaskClassification{Intent: k12.ImageTaskIntentCompletedHomework, IntentEvidence: []string{"writing"}, Confidence: 1}}, ResolveRoute: imageTaskRouteForTest}
	in := testCreateImageTaskInput()
	in.OwnerScope = "guardian-final"
	in.SourceRef = "source-correction"
	in.SourceAssetRefs = []string{original.Metadata.PageAssetID}
	view, _, err := coordinator.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	dispatchID := view.Dispatch.DispatchID
	// 临时真实库冻结一份已交付的双题任务；模型仅在独立复核边界使用替身。
	jobRecord, err := k12.NewGradingJobRecord("mingming", "session-final", k12.GradingJobFields{SubmissionID: "submission-final", SourceKind: "image_task", IdempotencyKey: k12.BuildGradingIdempotencyKey("image_task", dispatchID, 1), ConfirmedVersion: 1, ConfirmationState: k12.GradingConfirmationConfirmed, AnchorState: k12.GradingAnchorLocated, ModelSnapshot: k12.GradingModelSnapshot{Provider: "provider-a", Model: "model-a", Route: "provider-a/model-a"}})
	if err != nil {
		t.Fatal(err)
	}
	jobRecord.Status = k12.GradingStageCompleted
	if _, err = store.Put(ctx, jobRecord); err != nil {
		t.Fatal(err)
	}
	assetRefs, _ := json.Marshal(in.SourceAssetRefs)
	_, err = store.DB().Exec(`INSERT INTO k12_homework_submissions(submission_id,dispatch_id,agent_name,learner_id,source_kind,source_ref,source_asset_refs_json,task_intent,status,grading_job_id,idempotency_key,created_at,updated_at) VALUES('submission-final',?,'mingming','mingming','desktop','source-correction',?,'completed_homework','completed',?,'homework-final',100,100)`, dispatchID, string(assetRefs), jobRecord.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DB().Exec(`UPDATE k12_image_task_dispatches SET task_intent='completed_homework',target_object_type='homework_submission',target_object_id='submission-final' WHERE dispatch_id=?`, dispatchID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := k12.ProblemAttemptSnapshot{}
	for _, id := range []string{"target", "other"} {
		answer := "unclear"
		if id == "other" {
			answer = "blank"
		}
		snapshot.Problems = append(snapshot.Problems, k12.Problem{ProblemID: id, AgentName: "mingming", SubmissionID: "submission-final", PageAssetID: original.Metadata.PageAssetID, Ordinal: len(snapshot.Problems), ProblemKind: k12.ProblemKindStandalone, SourceNumberPath: []string{id}, DisplayLabel: id, Subject: "数学", StemRaw: "1+1=", StemMarkdown: "1+1=", CanonicalVersion: 1, CreatedAt: 100, UpdatedAt: 100})
		snapshot.Attempts = append(snapshot.Attempts, k12.Attempt{AttemptID: "attempt-" + id, AgentName: "mingming", SubmissionID: "submission-final", ProblemID: id, AnswerState: answer, ConfirmedVersion: 1, InputDigest: "input-" + id, CreatedAt: 100, UpdatedAt: 100})
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
		status := PhotoAnswerUnclear
		receiptStatus := k12.GradingAssessmentAnswerUnclear
		if q.ProblemID == "other" {
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
	run := &gradingRun{questions: questions, result: &PhotoGradeResult{AnnotatedImage: &RenderedPhoto{Data: oldPNG, MIME: "image/png"}}}
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

func TestCompletedSourceCorrection_ReconcileOnlyKnownPreflightFailure(t *testing.T) {
	for _, failure := range []string{"original source region unavailable", "recognizing invocation policy does not match frozen route policy", "context deadline exceeded"} {
		t.Run(failure, func(t *testing.T) {
			f := prepareCompletedSourceFixture(t)
			// 历史终稿在后续时点纠正，新产物不能继承早于创建时间的旧更新时间。
			f.o.deps.Now = func() int64 { return 2000 }
			ctx := context.Background()
			id := "source-correction-" + modelInvocationDigest([]byte("mingming"), []byte(f.dispatch), []byte(f.request.IdempotencyKey))
			r := k12storage.FinalSourceCorrectionRequest{CorrectionID: id, AgentName: "mingming", JobID: f.job.Record.RecordID, DispatchID: f.dispatch, OwnerScope: "guardian-final", SubmissionID: "submission-final", ProblemID: "target", InputRevision: 1, StructureVersion: 1, InputDigest: f.request.InputDigest, OriginalArtifactID: f.old.ArtifactID, OriginalArtifactDigest: f.old.ArtifactDigest, SourceDigest: f.old.OriginalSourceDigest}
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
			f := prepareCompletedSourceFixture(t)
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
			restarted := &GradingOrchestrator{deps: f.o.deps, runDir: f.o.runDir}
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
				qs, e := f.o.deps.loadCurrentConfirmedQuestions(ctx, "mingming", "submission-final")
				if e != nil {
					t.Fatal(e)
				}
				for _, q := range qs {
					if q.ProblemID == "target" && (q.AnswerState != AnswerStateBlank || q.ConfirmedVersion != 2 || q.StudentAnswer != "") {
						t.Fatalf("stale source: %+v", q)
					}
				}
				var attempts int
				if e = f.store.DB().QueryRow(`SELECT count(*) FROM k12_attempts WHERE agent_name='mingming' AND submission_id='submission-final'`).Scan(&attempts); e != nil || attempts != 2 {
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
