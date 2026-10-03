package apihttp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

type answerFeedbackHTTPSolver struct{ calls int }

func (*answerFeedbackHTTPSolver) UsesGradingPhysicalCalls() bool { return true }

func (s *answerFeedbackHTTPSolver) Solve(context.Context, string, string, string) (usecase.SolveResult, error) {
	s.calls++
	return usecase.SolveResult{Solution: "4", Evidence: usecase.SolveEvidence{Verdict: usecase.VerdictAgree, EvidenceType: usecase.EvidenceNumericExec}}, nil
}

type answerFeedbackHTTPGrader struct{ calls int }

func (g *answerFeedbackHTTPGrader) Grade(context.Context, string, string, string) (usecase.GradeOutcome, error) {
	g.calls++
	correct := true
	return usecase.GradeOutcome{Verdict: usecase.VerdictAgree, FinalAnswerCorrect: &correct}, nil
}

// 公共入口受理真实持久事实，经 Outbox 和逐题回执完成后再次从公共查询读取。
func TestProblemAssetAnswerFeedbackHTTP(t *testing.T) {
	ctx := context.Background()
	seed := seedProblemSourceActionHTTPWithSnapshot(t, func(asset string) (k12.ProblemAttemptSnapshot, string) {
		return k12.ProblemAttemptSnapshot{Problems: []k12.Problem{{ProblemID: "feedback-problem", AgentName: "mingming", SubmissionID: "submission-source-action", PageAssetID: asset, Ordinal: 0, ProblemKind: k12.ProblemKindStandalone, Subject: "数学", StemRaw: "2+2=", StemMarkdown: "2+2=", CanonicalVersion: 1}}, Attempts: []k12.Attempt{{AttemptID: "feedback-attempt", AgentName: "mingming", SubmissionID: "submission-source-action", ProblemID: "feedback-problem", AnswerState: "present", AnswerRaw: "4", AnswerMarkdown: "4", ConfirmedVersion: 1, InputDigest: "sha256:feedback-input"}}}, "feedback-problem"
	})
	store := seed.fixture.coordinator.Records
	if _, err := seed.fixture.db.Exec(`INSERT INTO k12_image_task_owner_scopes(dispatch_id,owner_scope,agent_name,created_at) VALUES(?,?,?,?)`, seed.dispatchID, usecase.DefaultLocalOwnerScope, "mingming", 100); err != nil {
		t.Fatal(err)
	}
	deps := usecase.Deps{Records: store, TextbookOwnerID: usecase.DefaultLocalOwnerScope}
	job, err := deps.GetGradingJob(ctx, "mingming", seed.jobID)
	if err != nil {
		t.Fatal(err)
	}
	proof := func(id string, operation k12.GradingItemOperation, kind k12.GradingExecutionKind, raw string) string {
		t.Helper()
		inv := k12.GradingItemInvocation{InvocationID: id, AgentName: "mingming", JobID: seed.jobID, ProblemID: seed.problemID, AttemptID: "feedback-attempt", Operation: operation, ExecutionKind: kind, OperationAttempt: 1, RequestDigest: "sha256:" + id, InputRevision: 1, InputDigest: "sha256:feedback-input", RouteSnapshot: job.Fields.ModelSnapshot}
		if _, _, err := store.PrepareGradingItemInvocation(ctx, inv); err != nil {
			t.Fatal(err)
		}
		if _, err := store.MarkGradingItemInvocationSent(ctx, "mingming", id); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(append([]byte{0}, []byte(raw)...))
		digest := "sha256:" + hex.EncodeToString(sum[:])
		if _, err := store.MarkGradingItemInvocationSucceeded(ctx, "mingming", id, digest, raw); err != nil {
			t.Fatal(err)
		}
		return digest
	}
	raw := `{"Solution":"4","Evidence":{"Verdict":"agree","EvidenceType":"numeric_exec"}}`
	solveDigest := proof("feedback-original-solve", k12.GradingItemOperationSolve, k12.GradingExecutionLocalDeterministic, raw)
	facts := k12.ProblemAssetFacts{Subject: "数学", Stem: "2+2=", AnswerContext: map[string]string{"grade_term": "五年级上"}}
	identity, err := facts.ExactIdentity(usecase.DefaultLocalOwnerScope)
	if err != nil {
		t.Fatal(err)
	}
	asset, _, err := store.PublishProblemAsset(ctx, k12.ProblemAssetPublication{OwnerID: usecase.DefaultLocalOwnerScope, PublicationID: "feedback-publication", Facts: facts, Answer: "4", AnswerResultJSON: raw, Verification: k12.ProblemAssetVerification{AgentName: "mingming", InvocationID: "feedback-original-solve", InputDigest: "sha256:feedback-input", ResultDigest: solveDigest, FactsDigest: identity.FactsDigest, Kind: k12.ProblemAnswerDeterministic, Policy: "local-deterministic-v1"}})
	if err != nil {
		t.Fatal(err)
	}
	adoption, _, err := store.AdoptProblemAsset(ctx, k12.ProblemAssetAdoption{OwnerID: asset.OwnerID, JobID: seed.jobID, ProblemID: seed.problemID, InputRevision: 1, InputDigest: "sha256:feedback-input", AssetID: asset.AssetID, AssetVersion: asset.Version, AssetRevision: asset.Revision, FactsDigest: asset.FactsDigest})
	if err != nil {
		t.Fatal(err)
	}
	proof("feedback-original-grade", k12.GradingItemOperationGrade, k12.GradingExecutionProvider, `{"Verdict":"agree","FinalAnswerCorrect":true}`)
	result, _ := json.Marshal(usecase.PhotoGradeItem{Status: usecase.PhotoCorrect})
	original, _, err := store.CommitGradingAssessmentItem(ctx, k12.GradingAssessmentItem{AgentName: "mingming", JobID: seed.jobID, ProblemID: seed.problemID, AttemptID: "feedback-attempt", ConfirmedVersion: 1, InputDigest: "sha256:feedback-input", Status: k12.GradingAssessmentCorrect, ResultJSON: string(result), ResultDigest: "sha256:feedback-original", GradeInvocationID: "feedback-original-grade", AnswerSource: &k12.ProblemAnswerSource{Kind: k12.ProblemAnswerAsset, AssetID: asset.AssetID, AssetVersion: asset.Version, AssetRevision: asset.Revision, FactsDigest: asset.FactsDigest, AdoptionID: adoption.AdoptionID}, ProjectionStatus: k12.GradingProjectionCommitted}, k12storage.GradingAssessmentEffects{})
	if err != nil {
		t.Fatal(err)
	}
	solver, grader := &answerFeedbackHTTPSolver{}, &answerFeedbackHTTPGrader{}
	deps.Solver, deps.Grader = solver, grader
	orchestrator := usecase.NewGradingOrchestrator(deps, func(snapshot k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) { return snapshot, nil })
	worker := usecase.NewProblemAssetFeedbackConsumer(store)
	worker.SetGrading(orchestrator)
	dispatcher := k12storage.NewDispatcher(store, worker)
	path := "/image-tasks/" + seed.dispatchID + "/problems/" + seed.problemID + "/answer-feedback"
	contextResponse, _ := do(t, seed.fixture.handler, http.MethodGet, path, "")
	var anchor struct {
		JobID         string                         `json:"job_id"`
		InputRevision int                            `json:"input_revision"`
		ResultDigest  string                         `json:"result_digest"`
		Assessment    k12.EffectiveGradingAssessment `json:"assessment"`
	}
	if err = json.Unmarshal(contextResponse.Body.Bytes(), &anchor); err != nil || contextResponse.Code != http.StatusOK || anchor.JobID != seed.jobID || anchor.InputRevision != 1 || anchor.ResultDigest != original.ResultDigest || anchor.Assessment.Correction != nil {
		t.Fatalf("feedback context: %d %s err=%v", contextResponse.Code, contextResponse.Body.String(), err)
	}
	body, _ := json.Marshal(map[string]any{"kind": "answer_error", "job_id": anchor.JobID, "input_revision": anchor.InputRevision, "result_digest": anchor.ResultDigest, "reason": "Please verify this adopted answer."})
	post := func(data []byte, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		seed.fixture.handler.ServeHTTP(w, r)
		return w
	}
	bad := post([]byte(`{"kind":"dislike"}`), "bad")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("implicit dislike accepted: %d", bad.Code)
	}
	w := post(body, "feedback-http-1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	var accepted struct {
		Created  bool                            `json:"created"`
		Feedback k12storage.ProblemAssetFeedback `json:"feedback"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &accepted); err != nil || !accepted.Created || accepted.Feedback.Status != "pending" {
		t.Fatalf("accepted=%+v %v", accepted, err)
	}
	if err = dispatcher.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	w, _ = do(t, seed.fixture.handler, http.MethodGet, path+"/"+accepted.Feedback.FeedbackID, "")
	var completed k12storage.ProblemAssetFeedback
	if err = json.Unmarshal(w.Body.Bytes(), &completed); err != nil || w.Code != http.StatusOK || completed.Status != "completed" || len(completed.Outcomes) != 1 || completed.Outcomes[0].CorrectionID == "" {
		t.Fatalf("completed HTTP: %d %s err=%v", w.Code, w.Body.String(), err)
	}
	if solver.calls != 1 || grader.calls != 1 {
		t.Fatalf("correction calls solve=%d grade=%d", solver.calls, grader.calls)
	}
	contextResponse, _ = do(t, seed.fixture.handler, http.MethodGet, path, "")
	if err = json.Unmarshal(contextResponse.Body.Bytes(), &anchor); err != nil || contextResponse.Code != http.StatusOK || anchor.ResultDigest != original.ResultDigest || anchor.Assessment.Correction == nil || anchor.Assessment.Correction.CorrectionID != completed.Outcomes[0].CorrectionID {
		t.Fatalf("effective feedback context: %d %s err=%v", contextResponse.Code, contextResponse.Body.String(), err)
	}
	w = post(body, "feedback-http-1")
	if w.Code != http.StatusAccepted {
		t.Fatalf("replay: %d %s", w.Code, w.Body.String())
	}
	if err = dispatcher.ProcessPending(ctx); err != nil {
		t.Fatal(err)
	}
	if solver.calls != 1 || grader.calls != 1 {
		t.Fatal("HTTP replay repeated model operations")
	}
	stored, err := store.GetGradingAssessmentItem(ctx, "mingming", seed.jobID, seed.problemID)
	if err != nil || stored.ResultDigest != original.ResultDigest || stored.GradeInvocationID != original.GradeInvocationID {
		t.Fatalf("original changed: %+v %v", stored, err)
	}
}
