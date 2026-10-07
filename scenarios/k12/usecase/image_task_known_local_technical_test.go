package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/assetstore"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type knownTechnicalHTTPSource struct {
	url    string
	client *http.Client
}

func (*knownTechnicalHTTPSource) UsesGradingPhysicalCalls() bool { return true }

func (*knownTechnicalHTTPSource) FailedVerificationPayload(payload string) error {
	var value SolveResult
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return err
	}
	if value.Solution == "N/A" {
		return errors.New("stored verifier execution failed")
	}
	return nil
}

func (s *knownTechnicalHTTPSource) Solve(ctx context.Context, problem, _, _ string) (SolveResult, error) {
	var value SolveResult
	for _, operation := range []k12.GradingItemOperation{k12.GradingItemOperationSolveGenerate, k12.GradingItemOperationSolveVerify} {
		spec := GradingPhysicalCallSpec{Operation: operation, RequestDigest: "known-technical:" + string(operation) + ":" + problem}
		if operation == k12.GradingItemOperationSolveVerify {
			spec.KnownExecutionFailure = s.FailedVerificationPayload
		}
		result, err := ExecuteGradingPhysicalCall(ctx, spec, func(callCtx context.Context) (string, error) {
			req, err := http.NewRequestWithContext(callCtx, http.MethodPost, s.url+"/"+string(operation), nil)
			if err != nil {
				return "", err
			}
			response, err := s.client.Do(req)
			if err != nil {
				return "", err
			}
			defer response.Body.Close()
			raw, err := io.ReadAll(response.Body)
			return string(raw), err
		})
		if err != nil {
			return SolveResult{}, err
		}
		if err := json.Unmarshal([]byte(result.Payload), &value); err != nil {
			return SolveResult{}, err
		}
	}
	return value, nil
}

type knownTechnicalFixture struct {
	o                          *GradingOrchestrator
	c                          *ImageTaskCoordinator
	dispatch                   k12.ImageTaskDispatch
	job                        GradingJobView
	original                   k12.GradingAssessmentItem
	before                     []k12.GradingItemInvocation
	generateCalls, verifyCalls *atomic.Int32
}

// 真实 SQLite 保存历史与当前代次；只有 Provider HTTP 边界为本次隔离服务。
func newKnownTechnicalFixture(t *testing.T) knownTechnicalFixture {
	t.Helper()
	ctx := context.Background()
	generateCalls, verifyCalls := &atomic.Int32{}, &atomic.Int32{}
	payload, _ := json.Marshal(SolveResult{Solution: "2", Evidence: SolveEvidence{
		Verdict: VerdictAgree, EvidenceType: EvidenceHeterogeneousModel, SolverModel: "solver-a", VerifierModel: "verifier-b"}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/solve_generate":
			generateCalls.Add(1)
		case "/solve_verify":
			verifyCalls.Add(1)
		default:
			t.Errorf("unexpected provider operation: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	solver := &knownTechnicalHTTPSource{url: server.URL, client: server.Client()}
	t.Setenv("HEXCLAW_ASSET_ROOT", t.TempDir())
	image, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	assetID, err := assetstore.Save("mingming", image)
	if err != nil {
		t.Fatal(err)
	}
	o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{{Question: "1+1=", Subject: "数学", AnswerState: AnswerStateBlank}},
		&itemResumeSolver{calls: map[string]int{}}, &itemResumeGrader{calls: map[string]int{}})
	o.deps.Solver = solver
	request := orchestratorPhotoRequest()
	request.Image, request.SourcePageAssetID = image, assetID
	started, _, err := o.StartPhotoGradingJob(ctx, StartPhotoGradingInput{Photo: request, SourceKind: "im", SourceKey: "known-technical-original"})
	if err != nil {
		t.Fatal(err)
	}
	jobID := started.Record.RecordID
	freezeItemResumeBudget(t, o, jobID)
	if _, err := o.RunGradingJob(ctx, jobID); err != nil {
		t.Fatal(err)
	}
	waitGradingView(t, o, jobID, func(v GradingJobView) bool {
		return v.Fields.AnchorState == k12.GradingAnchorLocated || v.Fields.AnchorState == k12.GradingAnchorDegraded
	})
	run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
	c := &ImageTaskCoordinator{Records: o.deps.Records, Grading: o, Classifier: &imageTaskClassifierStub{},
		ReadAsset: func(string, string) ([]byte, error) { return run.req.Image, nil },
		ResolveRoute: func(route k12.ImageTaskRouteSnapshot) (k12.ImageTaskRouteSnapshot, error) {
			route.Route, route.Capability = route.Provider+"/"+route.Model, "vision"
			route.PolicyVersion, route.PromptVersion = "image-task-routing-v1", "image-task-classifier-v1"
			return route, nil
		}}
	created, _, err := c.Create(ctx, CreateImageTaskInput{
		OwnerScope: DefaultLocalOwnerScope, AgentName: "mingming", LearnerID: "mingming",
		SourceKind: k12.ImageTaskSourceDesktop, SourceRef: "known-technical-image", SourceSessionID: "known-technical-session",
		SourceAssetRefs: []string{assetID}, AttemptGeneration: 1,
		RouteRequest: k12.ImageTaskRouteSnapshot{Provider: job.Fields.ModelSnapshot.Provider, Model: job.Fields.ModelSnapshot.Model},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatch, target, err := o.deps.Records.CommitImageTaskRouting(ctx, "mingming", created.Dispatch.DispatchID, created.Dispatch.Version,
		k12storage.ImageTaskRoutingDecision{Intent: k12.ImageTaskIntentCompletedHomework, Confidence: .99,
			Evidence: []string{"worksheet"}, InvocationResultDigest: "sha256:known-technical-classification"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.deps.Records.BindHomeworkSubmissionGradingJob(ctx, "mingming", target.HomeworkSubmission.SubmissionID, jobID, target.HomeworkSubmission.Version); err != nil {
		t.Fatal(err)
	}
	job.Fields.SourceKind = "image_task"
	job.Fields.IdempotencyKey = k12.BuildGradingIdempotencyKey("image_task", dispatch.DispatchID, job.Fields.ConfirmedVersion)
	job, err = o.deps.saveGradingJob(ctx, job, k12.GradingStageAssessing)
	if err != nil {
		t.Fatal(err)
	}
	question := run.questions[0]
	executor := newDurableGradingPhysicalCallExecutor(o, job, question)
	physicalCtx := withGradingPhysicalCallExecutor(ctx, executor)
	for _, operation := range []k12.GradingItemOperation{k12.GradingItemOperationSolveGenerate, k12.GradingItemOperationSolveVerify} {
		raw := string(payload)
		if operation == k12.GradingItemOperationSolveVerify {
			legacy, _ := json.Marshal(SolveResult{Solution: "N/A", Evidence: SolveEvidence{Verdict: VerdictAgree, EvidenceType: EvidenceNumericExec}})
			raw = string(legacy)
		}
		if _, err := ExecuteGradingPhysicalCall(physicalCtx, GradingPhysicalCallSpec{Operation: operation,
			RequestDigest: "known-technical:" + string(operation) + ":" + question.Question}, func(context.Context) (string, error) { return raw, nil }); err != nil {
			t.Fatal(err)
		}
	}
	oldItem := photoItemWithPracticeReference(run.req, question)
	oldItem.Status, oldItem.Solve.Solution = PhotoBlankSolved, "N/A"
	guide, guideID, err := executeGradingItemOperation(ctx, o, job, question, k12.GradingItemOperationParentGuide,
		map[string]string{"fixture": "legacy-parent-guide"}, func(callCtx context.Context) (ParentTeachingGuide, error) {
			return (&parentTeachingGuideSpy{}).GenerateParentTeachingGuide(callCtx, ParentTeachingGuideRequest{})
		})
	if err != nil {
		t.Fatal(err)
	}
	oldItem.ParentGuide = &guide
	if _, err := commitGradingAssessmentItem(ctx, o.deps, job, question, oldItem,
		executor.lastInvocation(k12.GradingItemOperationSolveVerify), "", guideID, k12storage.GradingAssessmentEffects{}); err != nil {
		t.Fatal(err)
	}
	original, err := o.deps.Records.GetGradingAssessmentItem(ctx, "mingming", jobID, question.ProblemID)
	if err != nil {
		t.Fatal(err)
	}
	job.Fields.AttemptCount = 2
	job, err = o.deps.saveGradingJob(ctx, job, k12.GradingStageAssessing)
	if err != nil {
		t.Fatal(err)
	}
	executor = newDurableGradingPhysicalCallExecutor(o, job, question)
	_, err = ExecuteGradingPhysicalCall(withGradingPhysicalCallExecutor(ctx, executor), GradingPhysicalCallSpec{
		Operation: k12.GradingItemOperationSolveVerify, RequestDigest: "known-technical:" + string(k12.GradingItemOperationSolveVerify) + ":" + question.Question,
		KnownExecutionFailure: solver.FailedVerificationPayload,
	}, func(context.Context) (string, error) {
		return "", errors.Join(egress.ErrProviderResponseProcessed, context.DeadlineExceeded)
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("seed current known failure: %v", err)
	}
	job, err = o.deps.AdvanceGradingStage(ctx, "mingming", jobID, AdvanceGradingInput{Outcome: GradingOutcomeFailed, FailureKind: "item_assessment_failed", Retryable: true})
	if err != nil || job.Record.Status != k12.GradingStageFailedTerminal || job.Fields.AttemptCount != 3 {
		t.Fatalf("terminal fixture: %+v %v", job, err)
	}
	before, err := o.deps.Records.ListGradingItemInvocations(ctx, "mingming", jobID)
	if err != nil {
		t.Fatal(err)
	}
	return knownTechnicalFixture{o, c, dispatch, job, original, before, generateCalls, verifyCalls}
}

func TestKnownLocalTechnicalRuntimeReplaysSuccessAndAppendsCorrection(t *testing.T) {
	f := newKnownTechnicalFixture(t)
	ctx := context.Background()
	if _, err := f.c.Retry(ctx, "mingming", f.dispatch.DispatchID, f.dispatch.Version); !errors.Is(err, k12storage.ErrImageTaskInvalidState) {
		t.Fatalf("ordinary terminal retry: %v", err)
	}
	// 现有并发信号量暂留 worker，以直接核对提交后的原计数和重复请求零调用。
	for range cap(f.o.sem) {
		f.o.sem <- struct{}{}
	}
	defer func() {
		for len(f.o.sem) > 0 {
			<-f.o.sem
		}
	}()
	if _, err := f.c.RetryWithIntent(ctx, "mingming", f.dispatch.DispatchID, f.dispatch.Version, DefaultLocalOwnerScope, ImageTaskRetryIntentKnownLocalTechnical); err != nil {
		t.Fatal(err)
	}
	queued, err := f.o.deps.GetGradingJob(ctx, "mingming", f.job.Record.RecordID)
	if err != nil || queued.Record.Status != k12.GradingStageQueued || queued.Fields.AttemptCount != 3 || queued.Fields.FailedStage != k12.GradingStageAssessing {
		t.Fatalf("queued recovery: %+v %v", queued, err)
	}
	if _, err := f.c.RetryWithIntent(ctx, "mingming", f.dispatch.DispatchID, f.dispatch.Version, DefaultLocalOwnerScope, ImageTaskRetryIntentKnownLocalTechnical); !errors.Is(err, k12storage.ErrImageTaskVersionConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	if f.generateCalls.Load() != 0 || f.verifyCalls.Load() != 0 {
		t.Fatal("provider called before worker release")
	}
	for range cap(f.o.sem) {
		<-f.o.sem
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := f.o.WaitForIdle(waitCtx); err != nil {
		t.Fatal(err)
	}
	finished, err := f.o.deps.GetGradingJob(ctx, "mingming", f.job.Record.RecordID)
	if err != nil || finished.Record.Status != k12.GradingStageCompleted {
		t.Fatalf("recovered final stage=%s failure=%s err=%v", finished.Record.Status, finished.Fields.FailureKind, err)
	}
	if finished.Fields.SubmissionID != f.job.Fields.SubmissionID || finished.Fields.IdempotencyKey != f.job.Fields.IdempotencyKey ||
		!reflect.DeepEqual(finished.Fields.ModelSnapshot, f.job.Fields.ModelSnapshot) || !reflect.DeepEqual(finished.Fields.BudgetSnapshot, f.job.Fields.BudgetSnapshot) {
		t.Fatal("recovery replaced original frozen job identity/model/budget")
	}
	if f.generateCalls.Load() != 0 || f.verifyCalls.Load() != 1 {
		t.Fatalf("physical sends generate=%d verify=%d", f.generateCalls.Load(), f.verifyCalls.Load())
	}
	effective, err := f.o.deps.Records.GetEffectiveGradingAssessment(ctx, "mingming", f.job.Record.RecordID, f.original.ProblemID)
	if err != nil || effective.Correction == nil || !reflect.DeepEqual(effective.Original, f.original) {
		t.Fatalf("append correction: %+v %v", effective, err)
	}
	var item PhotoGradeItem
	if err := json.Unmarshal([]byte(effective.Current.ResultJSON), &item); err != nil || item.Solve.Solution != "2" {
		t.Fatalf("corrected answer: %+v %v", item, err)
	}
	rows, err := f.o.deps.Records.ListGradingItemInvocations(ctx, "mingming", f.job.Record.RecordID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]k12.GradingItemInvocation{}
	for _, row := range rows {
		byID[row.InvocationID] = row
	}
	for _, old := range f.before {
		oldRaw, _ := json.Marshal(old)
		currentRaw, _ := json.Marshal(byID[old.InvocationID])
		oldSHA, currentSHA := sha256.Sum256(oldRaw), sha256.Sum256(currentRaw)
		if oldSHA != currentSHA {
			t.Fatalf("old receipt changed: %s old=%s current=%s", old.InvocationID, hex.EncodeToString(oldSHA[:]), hex.EncodeToString(currentSHA[:]))
		}
	}
	newVerify := byID[effective.Current.SolveInvocationID]
	if newVerify.OperationAttempt != 4001 || newVerify.JobID != f.job.Record.RecordID {
		t.Fatalf("same-job generation4: %+v", newVerify)
	}
}

func TestKnownLocalTechnicalRuntimeUnavailableRejectsWithoutWrites(t *testing.T) {
	for _, name := range []string{"sealed", "missing_runtime"} {
		t.Run(name, func(t *testing.T) {
			f := newKnownTechnicalFixture(t)
			if name == "sealed" {
				if err := f.o.Shutdown(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				f.o.mu.Lock()
				delete(f.o.runs, f.job.Record.RecordID)
				f.o.mu.Unlock()
				f.o.runDir = ""
			}
			if _, err := f.c.RetryWithIntent(context.Background(), "mingming", f.dispatch.DispatchID, f.dispatch.Version, DefaultLocalOwnerScope, ImageTaskRetryIntentKnownLocalTechnical); err == nil {
				t.Fatal("unavailable runtime accepted")
			}
			dispatch, err := f.o.deps.Records.GetImageTaskDispatch(context.Background(), "mingming", f.dispatch.DispatchID)
			if err != nil || !reflect.DeepEqual(dispatch, f.dispatch) {
				t.Fatalf("rejected dispatch changed: %+v %v", dispatch, err)
			}
			job, err := f.o.deps.GetGradingJob(context.Background(), "mingming", f.job.Record.RecordID)
			if err != nil || !reflect.DeepEqual(job.Record, f.job.Record) {
				t.Fatalf("rejected job changed: %+v %v", job, err)
			}
			if f.generateCalls.Load() != 0 || f.verifyCalls.Load() != 0 {
				t.Fatal("rejected recovery sent provider call")
			}
		})
	}
}
