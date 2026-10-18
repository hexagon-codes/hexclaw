package engineadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
)

// 仅控制物理调用与持久结算边界，题目解析、来源合并和最终投影使用实际实现。
type sourceScheduleExecutorV1 struct {
	*initialReadAdapterExecutorV1
	controlMu     sync.Mutex
	release       map[k12.RecognitionPhysicalUnit]chan struct{}
	started       map[k12.RecognitionPhysicalUnit]int
	failures      map[k12.RecognitionPhysicalUnit]error
	reviewSettled map[string]bool
	adjudicated   map[string]bool
	adjudication  map[string]k12.RecognitionPhysicalUnit
	active, peak  int
	settlementErr error
}

type sourceScheduleCallKeyV1 struct{}

func (e *sourceScheduleExecutorV1) ExecuteRecognitionPhysicalCall(ctx context.Context, call k12.RecognitionPhysicalCall, send func(context.Context) (string, error)) (k12.RecognitionPhysicalCallResult, error) {
	e.controlMu.Lock()
	e.started[call.Unit]++
	e.active++
	e.peak = max(e.peak, e.active)
	gate, exists := e.release[call.Unit]
	failure := e.failures[call.Unit]
	e.controlMu.Unlock()
	defer func() {
		e.controlMu.Lock()
		e.active--
		e.controlMu.Unlock()
	}()
	if !exists {
		return k12.RecognitionPhysicalCallResult{}, fmt.Errorf("unexpected physical unit %q", call.Unit)
	}
	select {
	case <-gate:
	case <-ctx.Done():
		return k12.RecognitionPhysicalCallResult{}, ctx.Err()
	}
	if failure != nil {
		return k12.RecognitionPhysicalCallResult{}, failure
	}
	return e.initialReadAdapterExecutorV1.ExecuteRecognitionPhysicalCall(ctx, call, func(sendCtx context.Context) (string, error) {
		return send(context.WithValue(sendCtx, sourceScheduleCallKeyV1{}, call))
	})
}

func (e *sourceScheduleExecutorV1) SettleRecognitionLayoutReviewBatchV1(ctx context.Context, source k12.RecognitionPhysicalCallResult, settlement k12.RecognitionLayoutReviewBatchSettlementV1) (k12.RecognitionLayoutReviewBatchSettlementResultV1, bool, error) {
	result, created, err := e.initialReadAdapterExecutorV1.SettleRecognitionLayoutReviewBatchV1(ctx, source, settlement)
	if err != nil {
		e.controlMu.Lock()
		e.settlementErr = err
		e.controlMu.Unlock()
	}
	if err == nil {
		e.controlMu.Lock()
		for _, candidate := range settlement.Candidates {
			e.reviewSettled[candidate.CandidateID] = true
		}
		e.controlMu.Unlock()
	}
	return result, created, err
}

func (e *sourceScheduleExecutorV1) AuthorizeRecognitionLayoutAdjudicationV2(_ context.Context, request k12.RecognitionLayoutAdjudicationRequestV2) (k12.RecognitionLayoutAdjudicationAuthorizationV2, bool, error) {
	e.controlMu.Lock()
	defer e.controlMu.Unlock()
	if !e.reviewSettled[request.CandidateID] {
		return k12.RecognitionLayoutAdjudicationAuthorizationV2{}, false, errors.New("adjudication preceded its review settlement")
	}
	unit := e.adjudication[request.CandidateID]
	return k12.RecognitionLayoutAdjudicationAuthorizationV2{
		AuthorizationID: "authorization-" + request.CandidateID, AuthorizationDigest: recognitionLayoutV2TestDigest(request.CandidateID),
		CandidateID: request.CandidateID, PhysicalUnit: unit,
	}, true, nil
}

func (e *sourceScheduleExecutorV1) SettleRecognitionLayoutAdjudicationV2(_ context.Context, source k12.RecognitionPhysicalCallResult, settlement k12.RecognitionLayoutAdjudicationSettlementV2) (k12.RecognitionLayoutAdjudicationSettlementResultV2, bool, error) {
	result := k12.RecognitionLayoutAdjudicationSettlementResultV2{SettlementDigest: recognitionLayoutV2TestDigest("adjudication-settlement-" + settlement.CandidateID), Adopted: settlement.Adopted}
	if settlement.Adopted {
		e.mu.Lock()
		original := append(json.RawMessage(nil), e.results[settlement.CandidateID].ResultJSON...)
		frozen, err := e.freeze(settlement.CandidateID, settlement.ResultKind, settlement.ResultJSON, source, settlement.SourcePhysicalUnit)
		if err == nil {
			candidate := e.results[settlement.CandidateID]
			candidate.OriginalCandidateJSON = original
			candidate.Adjudication = &k12.RecognitionLayoutAdjudicationReceiptV2{
				AuthorizationID: settlement.AuthorizationID, SettlementDigest: result.SettlementDigest,
				MatchedQuestionPrior: settlement.MatchedQuestionPrior, MatchedAnswerPrior: settlement.MatchedAnswerPrior,
			}
			e.results[settlement.CandidateID] = candidate
		}
		e.mu.Unlock()
		if err != nil {
			return result, false, err
		}
		result.FrozenResult = &frozen
	}
	e.controlMu.Lock()
	e.adjudicated[settlement.CandidateID] = settlement.Adopted
	e.controlMu.Unlock()
	return result, true, nil
}

func sourceSchedulePayloadV1(answer string, adjudication bool) string {
	payload := map[string]any{"items": []any{map[string]any{
		"target_id": "t1", "kind": "question", "recognition": map[string]any{
			"question": "5/7-1/5=", "subject": "数学", "answer_state": "present", "student_answer": answer,
			"recognition_confidence": 0.99, "ocr_signals": []string{},
			"answer_bbox": map[string]int{"x": 20, "y": 20, "width": 12, "height": 12},
		},
	}}}
	if adjudication {
		payload["verification"] = map[string]bool{"target_ownership_confirmed": true, "active_answer_complete": true}
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

func sourceScheduleFixtureV1(t *testing.T) (context.Context, []byte, k12.RecognitionLayoutPlanV2, k12.RecognitionLayoutPlanRuntimeV2, []k12.RecognitionLayoutReviewBatchAuthorizationV1, map[string]recognitionLayoutBatchOutcomeV2, *sourceScheduleExecutorV1, *RecognizerAdapter) {
	t.Helper()
	page, seed := recognitionLayoutV2DispatchPlan(t, 3)
	targets := make([]k12.RecognitionLayoutManifestTargetV2, len(seed.Targets))
	for i, target := range seed.Targets {
		targets[i] = k12.RecognitionLayoutManifestTargetV2{ManifestRef: fmt.Sprintf("manifest_%04d", i+1), ManifestOrder: i + 1,
			SourceNumberPath: target.SourceNumberPath, DisplayLabel: target.DisplayLabel, Region: target.Region}
	}
	plan, err := k12.BuildRecognitionLayoutPlanV2(k12.RecognitionLayoutPlanInputV2{
		PagePNG: page, Manifest: k12.RecognitionLayoutManifestSuccessV2{InvocationID: seed.ManifestInvocationID, ResultDigest: seed.ManifestResultDigest},
		Targets: targets, RecognitionFormat: k12.RecognitionLayoutCompactV4,
		InitialReadMode: k12.RecognitionLayoutManifestWithContentV1, EnableSourceAdjudication: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	header := recognitionLayoutV2TestDigest("source-schedule-header")
	runtime := recognitionLayoutV2RuntimeFixture(header, plan, 2, time.Now().UnixMilli(), time.Now().Add(time.Minute).UnixMilli())
	runtime.Header.InitialReadMode = k12.RecognitionLayoutManifestWithContentV1
	runtime.Header.ExecutionPolicy = k12.RecognitionExecutionPolicyCompactDAGV1
	e := &sourceScheduleExecutorV1{
		initialReadAdapterExecutorV1: &initialReadAdapterExecutorV1{runtime: runtime, results: make(map[string]k12.RecognitionLayoutCandidateFinalResultV2)},
		release:                      make(map[k12.RecognitionPhysicalUnit]chan struct{}), started: make(map[k12.RecognitionPhysicalUnit]int),
		failures: make(map[k12.RecognitionPhysicalUnit]error), reviewSettled: make(map[string]bool),
		adjudicated: make(map[string]bool), adjudication: make(map[string]k12.RecognitionPhysicalUnit),
	}
	e.physical = []k12.RecognitionLayoutPhysicalResultEvidenceV2{{PhysicalInvocationID: plan.ManifestInvocationID, PhysicalUnit: k12.RecognitionPhysicalUnitWholePage,
		ResultDigest: plan.ManifestResultDigest, PlanDigest: header, Attempt: 1}}
	ctx := k12.WithRecognitionPhysicalCallExecutor(k12.WithRecognitionLayoutInitialReadMode(k12.WithRecognitionLayoutPlanV2(t.Context(), header), k12.RecognitionLayoutManifestWithContentV1), e)
	initial := make(map[string]recognitionLayoutBatchOutcomeV2)
	var batches []k12.RecognitionLayoutReviewBatchAuthorizationV1
	for i, target := range plan.Targets {
		unit, err := k12.RecognitionLayoutReviewUnitV1(i + 1)
		if err != nil {
			t.Fatal(err)
		}
		request := k12.RecognitionLayoutReviewBatchAuthorizationRequestV1{PlanDigest: plan.AuthorizedPlanDigest, PhysicalUnit: unit, RecognitionFormat: plan.RecognitionFormat,
			Members: []k12.RecognitionLayoutReviewMemberAuthorizationV1{{AuthorizationID: "member-" + target.TargetID, AuthorizationDigest: recognitionLayoutV2TestDigest(target.TargetID), CandidateID: target.TargetID, ReviewRound: 1}}}
		_, _, request, err = recognitionReviewRequestV1(page, plan, request)
		if err != nil {
			t.Fatal(err)
		}
		auth, _, err := e.AuthorizeRecognitionLayoutReviewBatchV1(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		batches = append(batches, auth)
		e.release[unit] = make(chan struct{})
		adjudication := k12.RecognitionPhysicalUnit(fmt.Sprintf("layout_adjudicate_%04d", i+1))
		e.adjudication[target.TargetID] = adjudication
		e.release[adjudication] = make(chan struct{})
		raw := sourceSchedulePayloadV1("14/35", false)
		_, outcome := classifyRecognitionLayoutRepairV2(raw, target, plan.RecognitionFormat)
		if outcome == nil || outcome.question == nil {
			t.Fatal("initial source fixture did not parse")
		}
		outcome.source = k12.RecognitionPhysicalCallResult{InvocationID: seed.ManifestInvocationID, ResultDigest: recognitionLayoutV2TestDigest(raw), Payload: raw}
		initial[target.TargetID] = *outcome
	}
	runtime = e.runtime
	adapter := NewRecognizerAdapter(func(ctx context.Context, _ []byte, _ string) (string, error) {
		call, ok := ctx.Value(sourceScheduleCallKeyV1{}).(k12.RecognitionPhysicalCall)
		if !ok || len(call.TargetIDs) != 1 {
			return "", errors.New("physical call identity is missing")
		}
		return sourceSchedulePayloadV1("18/35", call.Unit == e.adjudication[call.TargetIDs[0]]), nil
	})
	return ctx, page, plan, runtime, batches, initial, e, adapter
}

type sourceScheduleResultV1 struct {
	outcomes map[string]recognitionLayoutBatchOutcomeV2
	err      error
}

func sourceScheduleExpectStartedV1(t *testing.T, e *sourceScheduleExecutorV1, units ...k12.RecognitionPhysicalUnit) {
	t.Helper()
	synctest.Wait()
	e.controlMu.Lock()
	defer e.controlMu.Unlock()
	if len(e.started) != len(units) || e.peak > 2 {
		t.Fatalf("physical calls or shared capacity drifted: calls=%v peak=%d want=%v settlement_error=%v", e.started, e.peak, units, e.settlementErr)
	}
	for _, unit := range units {
		if e.started[unit] != 1 {
			t.Fatalf("physical unit %s sent %d times", unit, e.started[unit])
		}
	}
}

func TestRecognitionSourceScheduleV1DependencyReadyAndSharedTwoSlots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, page, plan, runtime, batches, initial, executor, adapter := sourceScheduleFixtureV1(t)
		done := make(chan sourceScheduleResultV1, 1)
		go func() {
			outcomes, err := adapter.recognizeReviewAndAdjudicateV1(ctx, page, plan, runtime, batches, initial)
			done <- sourceScheduleResultV1{outcomes: outcomes, err: err}
		}()
		r1, r2, r3 := batches[0].PhysicalUnit, batches[1].PhysicalUnit, batches[2].PhysicalUnit
		a1, a2, a3 := executor.adjudication[plan.Targets[0].TargetID], executor.adjudication[plan.Targets[1].TargetID], executor.adjudication[plan.Targets[2].TargetID]
		sourceScheduleExpectStartedV1(t, executor, r1, r2)
		close(executor.release[r1])
		sourceScheduleExpectStartedV1(t, executor, r1, r2, a1)
		if executor.reviewSettled[plan.Targets[1].TargetID] {
			t.Fatal("unrelated slow review unexpectedly settled")
		}
		close(executor.release[a1])
		sourceScheduleExpectStartedV1(t, executor, r1, r2, a1, r3)
		close(executor.release[r3])
		sourceScheduleExpectStartedV1(t, executor, r1, r2, a1, r3, a3)
		select {
		case <-done:
			t.Fatal("page completed while one source was still in flight")
		default:
		}
		close(executor.release[r2])
		sourceScheduleExpectStartedV1(t, executor, r1, r2, a1, r3, a3, a2)
		if executor.active != 2 || len(executor.adjudicated) != 1 || len(executor.reviewSettled) != 3 {
			t.Fatal("independent adjudications did not occupy the two shared slots")
		}
		close(executor.release[a3])
		synctest.Wait()
		close(executor.release[a2])
		synctest.Wait()
		result := <-done
		if result.err != nil || len(result.outcomes) != 3 || executor.peak != 2 || len(executor.adjudicated) != 3 {
			t.Fatalf("complete source result lost: count=%d peak=%d adjudications=%v err=%v", len(result.outcomes), executor.peak, executor.adjudicated, result.err)
		}
		for _, target := range plan.Targets {
			if result.outcomes[target.TargetID].targetID != target.TargetID || !executor.adjudicated[target.TargetID] {
				t.Fatalf("target %s was lost or adjudicated against the wrong source", target.TargetID)
			}
		}
		finalization, _, err := executor.FinalizeRecognitionLayoutPlanV2(ctx)
		if err != nil {
			t.Fatal(err)
		}
		questions, err := RecognizedQuestionsFromLayoutFinalizationV2(finalization, plan)
		if err != nil || len(questions) != 3 {
			t.Fatalf("out-of-order completion failed exact-set projection: count=%d err=%v", len(questions), err)
		}
		for i, question := range questions {
			if question.DisplayLabel != fmt.Sprintf("%d.", i+1) || question.AnswerRawTranscription != "18/35" {
				t.Fatalf("printed order or independent reading changed at %d: %+v", i, question)
			}
		}
	})
}

func TestRecognitionSourceScheduleV1StopsNewWorkAndSettlesInflight(t *testing.T) {
	for _, scenario := range []struct {
		name string
		err  error
	}{
		{"unknown", usecase.ErrRecognitionPhysicalCallOutcomeUnknown},
		{"local technical failure", k12.ErrRecognitionPhysicalCallBeforeSend},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, page, plan, runtime, batches, initial, executor, adapter := sourceScheduleFixtureV1(t)
				r1, r2 := batches[0].PhysicalUnit, batches[1].PhysicalUnit
				executor.failures[r1] = scenario.err
				done := make(chan sourceScheduleResultV1, 1)
				go func() {
					outcomes, err := adapter.recognizeReviewAndAdjudicateV1(ctx, page, plan, runtime, batches, initial)
					done <- sourceScheduleResultV1{outcomes: outcomes, err: err}
				}()
				sourceScheduleExpectStartedV1(t, executor, r1, r2)
				close(executor.release[r1])
				sourceScheduleExpectStartedV1(t, executor, r1, r2)
				select {
				case <-done:
					t.Fatal("failed request cancelled or abandoned another in-flight source")
				default:
				}
				close(executor.release[r2])
				sourceScheduleExpectStartedV1(t, executor, r1, r2)
				result := <-done
				if !errors.Is(result.err, scenario.err) || result.outcomes != nil {
					t.Fatalf("failure was masked as a complete page: %+v", result)
				}
				if !executor.reviewSettled[plan.Targets[1].TargetID] || len(executor.results) != 1 || len(executor.physical) != 2 {
					t.Fatalf("successful in-flight receipt lost: settlements=%v results=%d physical=%d", executor.reviewSettled, len(executor.results), len(executor.physical))
				}
				if executor.physical[1].PhysicalUnit != r2 || len(executor.adjudicated) != 0 || executor.active != 0 {
					t.Fatal("failure triggered later work or left an in-flight physical call")
				}
			})
		})
	}
}
