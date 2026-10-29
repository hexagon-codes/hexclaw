package usecase

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assetstore"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

// 真实 SQLite 与实际编排器验证复用和副作用；受控模型只证明调用边界，不代表模型质量。
func TestPhotoGradingCacheExactResult(t *testing.T) {
	ctx := context.Background()
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	runDir := t.TempDir()
	rec := &countingRecognizer{questions: []RecognizedQuestion{{Question: "2+2=", Subject: "数学", StudentAnswer: "5", AnswerState: AnswerStatePresent}}}
	solver, grader := &assetReuseSolver{}, &assetReuseGrader{}
	o := newParallelAnchorOrchestrator(t, rec, nil, WithGradingRunDir(runDir))
	o.deps.Solver, o.deps.Grader = solver, grader
	o.deps.TextbookOwnerID, o.deps.GradingBudgetSnapshot = DefaultLocalOwnerScope, orchestratorTestBudget()
	o.deps.PageAssets = assetstore.PageStore{}
	o.deps.PhotoAnnotator = coveragePNGAnnotator{data: validPNGFixture(t, "cached-annotation")}
	input := StartPhotoGradingInput{SourceKind: "im", SourceKey: "cache-original", Photo: PhotoGradeRequest{
		AgentName: "mingming", Grade: "五年级上", SourceSession: "bound-chat", TaskIntent: PhotoTaskCompletedHomework,
		Image: validPNGFixture(t, "same-student-answer"),
	}}
	start := func(engine *GradingOrchestrator, in StartPhotoGradingInput) (GradingJobView, bool) {
		t.Helper()
		job, created, err := engine.StartPhotoGradingJob(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return job, created
	}
	seed, created := start(o, input)
	if !created {
		t.Fatal("first photo must create its original job")
	}
	finishPhotoCacheJob(t, o, seed.Record.RecordID)
	artifact, err := o.deps.Records.GetGradingFinalArtifactByJob(ctx, input.Photo.AgentName, seed.Record.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	before := photoCacheFactCounts(t, o)
	calls := [3]int{rec.calls, solver.calls, len(grader.answers)}

	restarted := trackGradingOrchestrator(t, NewGradingOrchestrator(o.deps, orchestratorSnapshotResolver, WithGradingRunDir(runDir)))
	for _, key := range []string{"cache-new-message", "cache-new-message", "cache-third-message"} {
		in := input
		in.SourceKey = key
		job, created := start(restarted, in)
		if created || job.Record.RecordID != seed.Record.RecordID || job.Record.Status != k12.GradingStageCompleted {
			t.Fatalf("same exact photo was recomputed: created=%v job=%s status=%s", created, job.Record.RecordID, job.Record.Status)
		}
		frozen, err := restarted.deps.Records.GetGradingFinalArtifactByJob(ctx, input.Photo.AgentName, job.Record.RecordID)
		if err != nil || frozen.ArtifactID != artifact.ArtifactID || frozen.ArtifactDigest != artifact.ArtifactDigest {
			t.Fatalf("did not reuse the frozen report: %v", err)
		}
	}
	if after := photoCacheFactCounts(t, restarted); after != before {
		t.Fatalf("reused photo duplicated learning facts: before=%v after=%v", before, after)
	}
	if now := [3]int{rec.calls, solver.calls, len(grader.answers)}; now != calls {
		t.Fatalf("reused photo called a model: before=%v after=%v", calls, now)
	}
	var reuses int
	if err := o.deps.Records.DB().QueryRow(`SELECT count(*) FROM k12_photo_grading_reuses`).Scan(&reuses); err != nil || reuses != 2 {
		t.Fatalf("new messages need independent idempotent provenance: count=%d err=%v", reuses, err)
	}

	for i, scenario := range []string{"student_answer", "child_profile", "method_version", "different_agent", "owner_scope", "request_scope"} {
		t.Run(scenario, func(t *testing.T) {
			in := input
			in.SourceKey = fmt.Sprintf("cache-miss-%d", i)
			switch scenario {
			case "student_answer":
				in.Photo.Image = validPNGFixture(t, "changed-student-handwriting")
			case "child_profile":
				profiles := o.deps.Profiles.(*memProfiles)
				previous := profiles.m["mingming"]
				changed := previous
				changed.ChildName = "Another child"
				profiles.m["mingming"] = changed
				defer func() { profiles.m["mingming"] = previous }()
			case "method_version":
				in.ModelSnapshot = orchestratorSnapshot()
				in.ModelSnapshot.ConfigFingerprint = "changed-processing-version"
			case "different_agent":
				in.Photo.AgentName = "eval-agent"
				o.deps.Profiles.(*memProfiles).m["eval-agent"] = k12.ChildProfile{ChildName: "Other", GradeTerm: in.Photo.Grade}
			case "owner_scope":
				previous := restarted.deps.TextbookOwnerID
				restarted.deps.TextbookOwnerID = "other-owner"
				defer func() { restarted.deps.TextbookOwnerID = previous }()
			case "request_scope":
				in.Photo.ImageScope = &k12.RecognitionImageScopeInputV1{Version: k12.RecognitionImageScopeVersionV1, Action: "grade", MessageIntent: "Only question 1"}
			}
			job, created := start(restarted, in)
			if !created || job.Record.RecordID == seed.Record.RecordID {
				t.Fatal("changed effective input reused an old grade")
			}
		})
	}
	changedReplay := input
	changedReplay.SourceKey = "cache-new-message"
	changedReplay.Photo.Image = validPNGFixture(t, "changed-source-command")
	if _, _, err := restarted.StartPhotoGradingJob(ctx, changedReplay); err == nil {
		t.Fatal("replayed source accepted different handwriting")
	}
}

func TestPhotoGradingCacheCompleteSharedQuestionIdentity(t *testing.T) {
	confidence := 1.0
	questions := []RecognizedQuestion{
		{ProblemID: "material", ProblemKind: ProblemKindCompoundParent, Question: "每盒有2支笔，买2盒。", RawTranscription: "每盒有2支笔，买2盒。", CanonicalMarkdown: "每盒有2支笔，买2盒。", Subject: "数学", CanonicalVersion: 1, AnswerState: AnswerStateBlank, RecognitionConfidence: &confidence},
		{ProblemID: "part", ParentProblemID: "material", ProblemKind: ProblemKindSubproblem, Question: "一共有多少支？", RawTranscription: "一共有多少支？", CanonicalMarkdown: "一共有多少支？", Subject: "数学", CanonicalVersion: 1, ConfirmedVersion: 1, InputDigest: "frozen-part", AnswerState: AnswerStateBlank, RecognitionConfidence: &confidence},
	}
	assess := RecognizedQuestionsForAssessment(questions)
	req := GradeRequest{Subject: "数学", Grade: "五年级上", Problem: assess[0].Question}
	first, eligible := gradingProblemAssetFacts(assess[0], req)
	if !eligible {
		t.Fatalf("complete shared textual conditions should be eligible: %+v", assess[0])
	}
	questions[0].Question, questions[0].RawTranscription, questions[0].CanonicalMarkdown = "每盒有3支笔，买2盒。", "每盒有3支笔，买2盒。", "每盒有3支笔，买2盒。"
	other := RecognizedQuestionsForAssessment(questions)
	req.Problem = other[0].Question
	second, eligible := gradingProblemAssetFacts(other[0], req)
	if !eligible {
		t.Fatal("second complete question should be eligible")
	}
	one, _ := first.ExactIdentity("owner")
	two, _ := second.ExactIdentity("owner")
	if one.Key == two.Key {
		t.Fatal("different shared conditions reused an old answer")
	}
	if _, eligible := gradingProblemAssetFacts(questions[1], req); eligible {
		t.Fatal("a child without its actual shared material must not reuse an answer")
	}
	req.Problem += "请根据下图回答。"
	if _, eligible := gradingProblemAssetFacts(other[0], req); eligible {
		t.Fatal("unrepresented diagram dependencies must use the normal solver")
	}
}

func TestPhotoGradingCacheImageTaskKeepsNewMessageIdentity(t *testing.T) {
	ctx := context.Background()
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	original := validPNGFixture(t, "image-task-exact-original")
	dimensions, _, err := image.DecodeConfig(bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	confidence := 1.0
	rec := &countingRecognizer{questions: []RecognizedQuestion{
		{Question: "2+2=", Subject: "数学", SourceNumberPath: []string{"1"}, DisplayLabel: "1.", StudentAnswer: "5", AnswerState: AnswerStatePresent, RecognitionConfidence: &confidence, SourceWidth: dimensions.Width, SourceHeight: dimensions.Height},
		{Question: "2+2=", Subject: "数学", SourceNumberPath: []string{"2"}, DisplayLabel: "2.", AnswerState: AnswerStateUnclear, OCRSignals: []string{"unclear_handwriting"}, RecognitionConfidence: &confidence, SourceWidth: dimensions.Width, SourceHeight: dimensions.Height},
	}}
	o := newParallelAnchorOrchestrator(t, rec, &photoAnchorerFake{boxes: map[int]BBox{0: {X: .1, Y: .1, W: .5, H: .5}}}, WithGradingRunDir(t.TempDir()))
	o.deps.GradingBudgetSnapshot = orchestratorTestBudget()
	o.deps.GradingBudgetSnapshot.ItemConcurrency = 1
	o.deps.Solver, o.deps.Grader = &assetReuseSolver{}, &assetReuseGrader{}
	o.deps.PageAssets = assetstore.PageStore{}
	o.deps.PhotoAnnotator = coveragePNGAnnotator{data: validPNGFixture(t, "image-task-cached-annotation")}
	repo := &PageAssetRepository{Records: o.deps.Records}
	ready, err := repo.Persist(ctx, "cache-owner", "mingming", original)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := newImageTaskCoordinatorForTest(t, &imageTaskClassifierStub{})
	c.Records, c.Grading, c.PageAssets, c.NewID = o.deps.Records, o, repo, nil
	c.Now = func() int64 { return time.Now().Unix() }
	var firstJob, firstArtifact, firstDispatch string
	for i := 0; i < 3; i++ {
		input := testCreateImageTaskInput()
		input.OwnerScope, input.SourceAssetRefs = "cache-owner", []string{ready.Metadata.PageAssetID}
		input.SourceRef = fmt.Sprintf("cache-real-message-%d", i)
		if i == 2 {
			input.MessageIntent = "请输出PDF"
		}
		prepared, _, err := c.Create(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		dispatch, target, err := c.Records.CommitImageTaskRouting(ctx, "mingming", prepared.Dispatch.DispatchID, prepared.Dispatch.Version,
			k12storage.ImageTaskRoutingDecision{Intent: k12.ImageTaskIntentCompletedHomework, Confidence: 1, Evidence: []string{"observed student answer"}, InvocationResultDigest: "sha256:cache-classification"})
		if err != nil {
			t.Fatal(err)
		}
		job, created, err := o.StartPhotoGradingJob(ctx, StartPhotoGradingInput{
			Photo:      PhotoGradeRequest{AgentName: "mingming", Grade: "五年级上", SourceSession: input.SourceSessionID, Image: original, SourcePageAssetID: ready.Metadata.PageAssetID, TaskIntent: PhotoTaskCompletedHomework},
			SourceKind: "image_task", SourceKey: dispatch.DispatchID, ParentAutomaticAttemptID: dispatch.DispatchID + ":cache", ParentAutomaticDeadlineAt: time.Now().Unix() + 600,
			BudgetSnapshot: o.deps.GradingBudgetSnapshot,
		})
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			if !created || job.Record.RecordID == firstJob {
				t.Fatal("changed delivery intent reused the old frozen message")
			}
			continue
		}
		if _, err := c.Records.BindHomeworkSubmissionGradingJob(ctx, "mingming", target.HomeworkSubmission.SubmissionID, job.Record.RecordID, target.HomeworkSubmission.Version); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if !created {
				t.Fatal("first message must be computed")
			}
			firstJob, firstDispatch = job.Record.RecordID, dispatch.DispatchID
			finishPhotoCacheJob(t, o, firstJob)
		} else if created || job.Record.RecordID != firstJob || dispatch.DispatchID == firstDispatch {
			t.Fatal("cache hit lost new message identity or recomputed its grading")
		}
		result, err := c.Result(ctx, "mingming", dispatch.DispatchID)
		if err != nil || result.FinalArtifact == nil || result.Photo == nil {
			t.Fatalf("new message cannot resolve frozen result: %v", err)
		}
		if result.Photo.AnnotatedImage == nil || !bytes.Equal(result.Photo.AnnotatedImage.Data, validPNGFixture(t, "image-task-cached-annotation")) {
			t.Fatal("new message lost the frozen annotated image")
		}
		if i == 0 {
			firstArtifact = result.FinalArtifact.ArtifactID
		} else if result.FinalArtifact.ArtifactID != firstArtifact {
			t.Fatal("new message did not reuse the same report")
		}
		completed, err := c.Records.HasCompletedImageTaskHomework(ctx, "mingming", dispatch.DispatchID)
		if err != nil || !completed {
			t.Fatalf("cached message was left waiting: %v", err)
		}
	}
	if rec.calls != 1 {
		t.Fatalf("cached message repeated recognition: %d", rec.calls)
	}
}

func TestPhotoGradingCacheKeepsKnownUntrustedConclusion(t *testing.T) {
	ctx := context.Background()
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	rec := &countingRecognizer{questions: []RecognizedQuestion{{Question: "2+2=", Subject: "数学", StudentAnswer: "5", AnswerState: AnswerStatePresent}}}
	solver := &assetReuseSolver{}
	grader := &itemResumeGrader{calls: map[string]int{}, outcomes: map[string]GradeOutcome{"2+2=": {Verdict: VerdictUnverifiable}}}
	o := newParallelAnchorOrchestrator(t, rec, nil, WithGradingRunDir(t.TempDir()))
	o.deps.Solver, o.deps.Grader = solver, grader
	o.deps.TextbookOwnerID, o.deps.GradingBudgetSnapshot = DefaultLocalOwnerScope, orchestratorTestBudget()
	o.deps.PageAssets = assetstore.PageStore{}
	in := StartPhotoGradingInput{SourceKind: "im", SourceKey: "uncertain-original", Photo: PhotoGradeRequest{
		AgentName: "mingming", Grade: "五年级上", SourceSession: "same-chat", TaskIntent: PhotoTaskCompletedHomework, Image: validPNGFixture(t, "known-uncertain-result"),
	}}
	job, _, err := o.StartPhotoGradingJob(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	finishPhotoCacheJob(t, o, job.Record.RecordID)
	items, err := o.deps.Records.ListGradingAssessmentItems(ctx, "mingming", job.Record.RecordID)
	if err != nil || len(items) != 1 || items[0].Status != k12.GradingAssessmentUntrusted {
		t.Fatalf("original uncertainty was not preserved: items=%+v err=%v", items, err)
	}
	before := photoCacheFactCounts(t, o)
	in.SourceKey = "uncertain-new-message"
	reused, created, err := o.StartPhotoGradingJob(ctx, in)
	if err != nil || created || reused.Record.RecordID != job.Record.RecordID {
		t.Fatalf("known uncertain terminal outcome was recomputed: %v", err)
	}
	if after := photoCacheFactCounts(t, o); after != before {
		t.Fatal("uncertain result created duplicate learning evidence")
	}
	if rec.calls != 1 || solver.calls != 1 || grader.calls["2+2="] != 1 {
		t.Fatal("known uncertainty repeated model work")
	}
}

func finishPhotoCacheJob(t *testing.T, o *GradingOrchestrator, id string) {
	t.Helper()
	job, err := o.RunGradingJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if job.Record.Status == k12.GradingStageAwaitingConfirmation {
		join, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := o.WaitForIdle(join); err != nil {
			t.Fatal(err)
		}
		settled, err := o.deps.GetGradingJob(context.Background(), "mingming", id)
		if err != nil {
			t.Fatal(err)
		}
		if settled.Fields.AnchorState == k12.GradingAnchorPending {
			t.Fatalf("anchor did not settle: status=%s failure=%s anchor=%s", settled.Record.Status, settled.Fields.FailureKind, settled.Fields.AnchorState)
		}
		job = settled
		if job.Record.Status == k12.GradingStageAwaitingConfirmation && job.Fields.ConfirmationState == k12.GradingConfirmationPending {
			job, err = o.ConfirmAndRun(context.Background(), id, nil)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if job.Record == nil || job.Record.Status != k12.GradingStageCompleted {
		t.Fatalf("photo grading did not complete: job=%+v", job.Record)
	}
}

func photoCacheFactCounts(t *testing.T, o *GradingOrchestrator) [4]int {
	t.Helper()
	var out [4]int
	for i, table := range []string{"k12_grading_jobs", "k12_grading_assessment_items", "k12_mistakes", "outbox_events"} {
		if err := o.deps.Records.DB().QueryRow("SELECT count(*) FROM " + table).Scan(&out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestPhotoGradingCacheDifferentPhotoStillGradesCurrentAnswer(t *testing.T) {
	ctx := context.Background()
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	solver, grader := &assetReuseSolver{}, &assetReuseGrader{}
	o := newParallelAnchorOrchestrator(t, &countingRecognizer{}, nil, WithGradingRunDir(t.TempDir()))
	o.deps.Solver, o.deps.Grader = solver, grader
	o.deps.TextbookOwnerID, o.deps.GradingBudgetSnapshot = DefaultLocalOwnerScope, orchestratorTestBudget()
	o.deps.PageAssets = assetstore.PageStore{}
	dispatcher := k12storage.NewDispatcher(o.deps.Records, ProblemAssetConsumer{Records: o.deps.Records})
	var previous string
	for i, answer := range []string{"4", "5"} {
		o.deps.Recognizer = &countingRecognizer{questions: []RecognizedQuestion{{Question: "2+2=", Subject: "数学", StudentAnswer: answer, AnswerState: AnswerStatePresent}}}
		job, created, err := o.StartPhotoGradingJob(ctx, StartPhotoGradingInput{SourceKind: "im", SourceKey: "different-photo-" + answer,
			Photo: PhotoGradeRequest{AgentName: "mingming", Grade: "五年级上", SourceSession: "bound-chat", TaskIntent: PhotoTaskCompletedHomework, Image: validPNGFixture(t, "photo-answer-"+answer)}})
		if err != nil || !created || job.Record.RecordID == previous {
			t.Fatalf("different photo did not create an independent task: %v", err)
		}
		previous = job.Record.RecordID
		finishPhotoCacheJob(t, o, previous)
		if err := dispatcher.ProcessPending(ctx); err != nil {
			t.Fatal(err)
		}
		items, err := o.deps.Records.ListGradingAssessmentItems(ctx, "mingming", previous)
		if err != nil || len(items) != 1 {
			t.Fatalf("current assessment missing: %v", err)
		}
		want := k12.GradingAssessmentCorrect
		if i == 1 {
			want = k12.GradingAssessmentWrong
		}
		if items[0].Status != want {
			t.Fatalf("current answer %s got stale grade %s", answer, items[0].Status)
		}
		if i == 1 && items[0].AnswerSource == nil {
			t.Fatal("complete matching question did not reuse the verified answer")
		}
	}
	if solver.calls != 1 || len(grader.answers) != 2 {
		t.Fatalf("wrong reuse boundary: solve=%d grade=%d", solver.calls, len(grader.answers))
	}
}
