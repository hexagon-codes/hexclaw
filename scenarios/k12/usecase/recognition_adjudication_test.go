package usecase

import (
	"context"
	"encoding/json"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"testing"
	"time"
)

// 真实 SQLite 与持久执行器验证裁决请求首次发送、结算及重启零重发。
func TestRecognitionAdjudicationDurableExecutor_ColdReplayPreservesOriginal(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/adjudication-executor-v2.db"
	db, store := openRecognitionPhysicalExecutorV2Store(t, dbPath)
	dbOpen := true
	defer func() {
		if dbOpen {
			_ = db.Close()
		}
	}()

	const agentName = "repair-settlement-owner"
	if _, err := db.ExecContext(
		ctx,
		`INSERT INTO agents(name) VALUES(?)`,
		agentName,
	); err != nil {
		t.Fatalf("insert owner: %v", err)
	}
	policy := k12.ApprovedRecognizingRequestPolicy()
	route := k12.GradingModelSnapshot{
		Provider:                 "hexclaw-gpt",
		Model:                    k12.RecognizingPolicyModel,
		Route:                    "hexclaw-gpt/" + k12.RecognizingPolicyModel,
		Capability:               "vision",
		RecognizingRequestPolicy: policy,
	}
	job, err := k12.NewGradingJobRecord(
		agentName,
		"repair-settlement-session",
		k12.GradingJobFields{
			SubmissionID:      "repair-settlement-submission",
			SourceKind:        "test",
			IdempotencyKey:    k12.BuildGradingIdempotencyKey("test", "repair-settlement", 0),
			ModelSnapshot:     route,
			ConfirmationState: k12.GradingConfirmationPending,
			AnchorState:       k12.GradingAnchorPending,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, putErr := store.Put(ctx, job); putErr != nil {
		t.Fatalf("persist job: %v", putErr)
	}

	pagePNG := recognitionPhysicalExecutorV2PagePNG(t, 40, 40)
	parent := k12.ModelInvocation{
		InvocationID:          "modelinv-repair-settler-v2",
		AgentName:             agentName,
		JobID:                 job.RecordID,
		Stage:                 k12.GradingStageRecognizing,
		RequestDigest:         recognitionPhysicalExecutorV2Digest("repair-settlement-parent"),
		RouteSnapshot:         route,
		RequestPolicySnapshot: policy,
		Attempt:               1,
		CreatedAt:             time.Now().Unix(),
	}
	header := k12.RecognitionLayoutPlanHeaderV2{
		PlanID:                   "layout-plan-repair-settler-v2",
		ParentInvocationID:       parent.InvocationID,
		AgentName:                parent.AgentName,
		JobID:                    parent.JobID,
		PageDigest:               recognitionPhysicalExecutorV2BytesDigest(pagePNG),
		ParentRequestDigest:      parent.RequestDigest,
		RouteSnapshot:            parent.RouteSnapshot,
		RequestPolicySnapshot:    parent.RequestPolicySnapshot,
		StageStartedAtUnixMillis: time.Now().UnixMilli(),
		PhysicalCallCapMillis:    120000,
		BudgetBuckets: k12.RecognitionLayoutBudgetBucketsV2{
			UpTo1ProblemMillis:   120000,
			UpTo8ProblemsMillis:  600000,
			UpTo16ProblemsMillis: 600000,
			UpTo32ProblemsMillis: 600000,
		},
		AdapterWorkerHardCap: 2,
		EffectiveConcurrency: 1,
	}
	headerDigest, err := k12.RecognitionLayoutPlanHeaderDigestV2(header)
	if err != nil {
		t.Fatal(err)
	}
	manifestCall := k12.RecognitionPhysicalCall{
		PlanVersion: k12.RecognitionPlanVersionV2,
		PlanDigest:  headerDigest,
		Unit:        k12.RecognitionPhysicalUnitWholePage,
		Image:       pagePNG,
	}
	manifestID, err := stableRecognitionPhysicalInvocationIDForCall(
		parent.InvocationID,
		manifestCall,
	)
	if err != nil {
		t.Fatal(err)
	}
	manifestRequestDigest, err := recognizingPhysicalInvocationDigest(parent, manifestCall)
	if err != nil {
		t.Fatal(err)
	}
	storedParent, _, created, err :=
		store.PrepareRecognizingInvocationWithInitialLayoutPlanV2(
			ctx,
			parent,
			k12.ModelPhysicalInvocation{
				PhysicalInvocationID:   manifestID,
				ParentInvocationID:     parent.InvocationID,
				AgentName:              parent.AgentName,
				JobID:                  parent.JobID,
				Stage:                  parent.Stage,
				PhysicalUnit:           manifestCall.Unit,
				RecognitionPlanVersion: k12.RecognitionPlanVersionV2,
				PlanDigest:             headerDigest,
				RequestDigest:          manifestRequestDigest,
				RouteSnapshot:          parent.RouteSnapshot,
				RequestPolicySnapshot:  parent.RequestPolicySnapshot,
				Attempt:                1,
				CreatedAt:              parent.CreatedAt,
			},
			header,
		)
	if err != nil || !created {
		t.Fatalf("publish V2 fixture: created=%v err=%v", created, err)
	}
	if _, claimed, claimErr := store.ClaimModelPhysicalInvocationSent(
		ctx,
		agentName,
		manifestID,
	); claimErr != nil || !claimed {
		t.Fatalf("claim manifest: claimed=%v err=%v", claimed, claimErr)
	}
	storedManifest, err := store.MarkModelPhysicalInvocationSucceededWithContent(
		ctx,
		agentName,
		manifestID,
		`{"targets":"one"}`,
		"",
	)
	if err != nil {
		t.Fatalf("succeed manifest: %v", err)
	}
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{
		PagePNG:           pagePNG,
		RecognitionFormat: k12.RecognitionLayoutCompactV4, EnableSourceAdjudication: true,
		Manifest: k12.RecognitionLayoutManifestSuccessV2{
			InvocationID: manifestID,
			ResultDigest: storedManifest.ResultDigest,
		},
		Targets: []k12.RecognitionLayoutManifestTargetV2{
			{
				ManifestRef:      "manifest_0001",
				ManifestOrder:    1,
				SourceNumberPath: []string{"1"},
				DisplayLabel:     "1",
				Region:           k12.SourcePixelRegion{X: 0, Y: 0, Width: 40, Height: 40},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if authorizeErr := store.AuthorizeRecognitionLayoutPlanV2(
		ctx,
		agentName,
		parent.InvocationID,
		k12.RecognitionLayoutManifestSuccessV2{
			InvocationID: manifestID,
			ResultDigest: storedManifest.ResultDigest,
		},
		plan,
	); authorizeErr != nil {
		t.Fatalf("authorize plan: %v", authorizeErr)
	}

	executor := newDurableRecognitionPhysicalCallExecutor(
		&GradingOrchestrator{deps: Deps{Records: store, Now: time.Now().Unix}},
		storedParent,
	)
	durableCtx := k12.WithRecognitionPhysicalCallExecutor(
		k12.WithRecognitionLayoutPlanV2(
			k12.WithGradingModelRequestPolicy(ctx, policy),
			headerDigest,
		),
		executor,
	)

	batch := plan.Batches[0]
	image, err := k12.BuildRecognitionLayoutBatchImageV2(pagePNG, plan, batch.Unit)
	if err != nil {
		t.Fatal(err)
	}
	primary, err := executor.ExecuteRecognitionPhysicalCall(durableCtx, k12.RecognitionPhysicalCall{PlanVersion: 2, PlanDigest: plan.AuthorizedPlanDigest, Unit: batch.Unit, TargetIDs: batch.TargetIDs, Image: image}, func(context.Context) (string, error) { return `{"items":[]}`, nil })
	if err != nil {
		t.Fatal(err)
	}
	primarySettlement, _, err := k12.SettleRecognitionLayoutPrimaryBatchV2(durableCtx, primary, k12.RecognitionLayoutPrimaryBatchSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, SourcePhysicalInvocationID: primary.InvocationID, SourcePhysicalUnit: batch.Unit, SourcePhysicalResultDigest: primary.ResultDigest, Classification: k12.RecognitionLayoutBatchClassifiedV2, Candidates: []k12.RecognitionLayoutCandidateSettlementV2{{CandidateID: plan.Targets[0].TargetID, Classification: k12.RecognitionLayoutCandidateReviewRequiredV2}}})
	if err != nil {
		t.Fatal(err)
	}
	repairAuth := primarySettlement.RepairAuthorizations[0]
	image, err = k12.BuildRecognitionLayoutRepairImageV2(pagePNG, plan, repairAuth.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	repair, err := executor.ExecuteRecognitionPhysicalCall(durableCtx, k12.RecognitionPhysicalCall{PlanVersion: 2, PlanDigest: plan.AuthorizedPlanDigest, Unit: repairAuth.PhysicalUnit, TargetIDs: []string{repairAuth.CandidateID}, Image: image}, func(context.Context) (string, error) { return `{"items":[{"answer":"18/35"}]}`, nil })
	if err != nil {
		t.Fatal(err)
	}
	original := json.RawMessage(`{"question":"5/7-1/5=","answer_state":"present","student_answer":"18/35","evidence_transcriptions":["5/7-1/5=","5/7-1/5="],"answer_evidence_transcriptions":["14/35","18/35"]}`)
	var canonicalOriginal any
	if err := json.Unmarshal(original, &canonicalOriginal); err != nil {
		t.Fatal(err)
	}
	original, err = json.Marshal(canonicalOriginal)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = k12.SettleRecognitionLayoutRepairV2(durableCtx, repair, k12.RecognitionLayoutRepairSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, AuthorizationID: repairAuth.AuthorizationID, AuthorizationDigest: repairAuth.AuthorizationDigest, CandidateID: repairAuth.CandidateID, SourcePhysicalInvocationID: repair.InvocationID, SourcePhysicalUnit: repairAuth.PhysicalUnit, SourcePhysicalResultDigest: repair.ResultDigest, Classification: k12.RecognitionLayoutCandidateValidV2, ResultKind: k12.RecognitionLayoutCandidateQuestionV2, ResultJSON: original})
	if err != nil {
		t.Fatal(err)
	}
	auth, created, err := k12.AuthorizeRecognitionLayoutAdjudicationV2(durableCtx, k12.RecognitionLayoutAdjudicationRequestV2{PlanDigest: plan.AuthorizedPlanDigest, CandidateID: repairAuth.CandidateID, PrimaryPhysicalInvocationID: primary.InvocationID, PrimaryPhysicalResultDigest: primary.ResultDigest, RepairPhysicalInvocationID: repair.InvocationID, RepairPhysicalResultDigest: repair.ResultDigest, ConflictKind: "answer"})
	if err != nil || !created {
		t.Fatalf("authorize: %v", err)
	}
	image, err = k12.BuildRecognitionLayoutAdjudicationImageV2(pagePNG, plan, auth.CandidateID)
	if err != nil {
		t.Fatal(err)
	}
	call := k12.RecognitionPhysicalCall{PlanVersion: 2, PlanDigest: plan.AuthorizedPlanDigest, Unit: auth.PhysicalUnit, TargetIDs: []string{auth.CandidateID}, Image: image}
	sends := 0
	send := func(context.Context) (string, error) {
		sends++
		return `{"verification":{"target_ownership_confirmed":true,"active_answer_complete":true},"items":[{"answer":"18/35"}]}`, nil
	}
	source, err := executor.ExecuteRecognitionPhysicalCall(durableCtx, call, send)
	if err != nil {
		t.Fatal(err)
	}
	result := json.RawMessage(`{"question":"5/7-1/5=","answer_state":"present","student_answer":"18/35"}`)
	var canonicalResult any
	if err := json.Unmarshal(result, &canonicalResult); err != nil {
		t.Fatal(err)
	}
	result, err = json.Marshal(canonicalResult)
	if err != nil {
		t.Fatal(err)
	}
	settlement := k12.RecognitionLayoutAdjudicationSettlementV2{PlanDigest: plan.AuthorizedPlanDigest, AuthorizationID: auth.AuthorizationID, AuthorizationDigest: auth.AuthorizationDigest, CandidateID: auth.CandidateID, SourcePhysicalInvocationID: source.InvocationID, SourcePhysicalUnit: auth.PhysicalUnit, SourcePhysicalResultDigest: source.ResultDigest, Adopted: true, MatchedQuestionPrior: "both", MatchedAnswerPrior: "repair", ResultKind: k12.RecognitionLayoutCandidateQuestionV2, ResultJSON: result}
	_, _, err = k12.SettleRecognitionLayoutAdjudicationV2(durableCtx, source, settlement)
	if err != nil {
		t.Fatal(err)
	}
	// 进程重启后只读取成功回执；已结算候选不能触发第二次发送。
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	dbOpen = false
	reopened, reloaded := openRecognitionPhysicalExecutorV2Store(t, dbPath)
	defer reopened.Close()
	persisted, err := reloaded.GetModelInvocation(ctx, agentName, parent.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	replayExecutor := newDurableRecognitionPhysicalCallExecutor(&GradingOrchestrator{deps: Deps{Records: reloaded, Now: time.Now().Unix}}, persisted)
	replayCtx := k12.WithRecognitionPhysicalCallExecutor(k12.WithRecognitionLayoutPlanV2(k12.WithGradingModelRequestPolicy(ctx, policy), headerDigest), replayExecutor)
	replay, err := replayExecutor.ExecuteRecognitionPhysicalCall(replayCtx, call, send)
	if err != nil || sends != 1 || replay.InvocationID != source.InvocationID {
		t.Fatalf("cold replay sends=%d err=%v", sends, err)
	}
	_, created, err = k12.SettleRecognitionLayoutAdjudicationV2(replayCtx, replay, settlement)
	if err != nil || created {
		t.Fatalf("settlement replay created=%t err=%v", created, err)
	}
	finalized, _, err := k12.FinalizeRecognitionLayoutPlanV2(replayCtx)
	if err != nil {
		t.Fatal(err)
	}
	if finalized.PhysicalResultCount != 4 || len(finalized.CandidateResults) != 1 || finalized.CandidateResults[0].Adjudication == nil || string(finalized.CandidateResults[0].OriginalCandidateJSON) != string(original) {
		t.Fatalf("finalization lost original evidence: %+v", finalized)
	}
}
