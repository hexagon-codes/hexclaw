package engineadapter

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type recognitionSourceWorkV1 struct {
	reviewIndex int
	target      *k12.RecognitionLayoutTargetV2
	reviewed    recognitionLayoutBatchOutcomeV2
}

type recognitionSourceCompletionV1 struct {
	work     recognitionSourceWorkV1
	outcomes []recognitionLayoutBatchOutcomeV2
	err      error
}

// 同题两份来源齐备后即可裁决；复核与裁决共用冻结的两个槽位。
// 停止派发不会取消已经发送的请求，所有在途结算完成后才返回错误。
func (a *RecognizerAdapter) recognizeReviewAndAdjudicateV1(ctx context.Context, pagePNG []byte, plan k12.RecognitionLayoutPlanV2, runtime k12.RecognitionLayoutPlanRuntimeV2, batches []k12.RecognitionLayoutReviewBatchAuthorizationV1, initial map[string]recognitionLayoutBatchOutcomeV2) (map[string]recognitionLayoutBatchOutcomeV2, error) {
	if (!k12.RecognitionCompactDAGExecutionPolicy(runtime.Header.ExecutionPolicy)) || runtime.Header.AdapterWorkerHardCap != 2 || runtime.Header.EffectiveConcurrency < 1 || runtime.Header.PhysicalCallCapMillis != 120000 {
		return nil, fmt.Errorf("%w: source scheduling parameters drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	result := make(map[string]recognitionLayoutBatchOutcomeV2)
	queue := make([]recognitionSourceWorkV1, len(batches))
	for i := range batches {
		queue[i].reviewIndex = i
	}
	slots := min(2, runtime.Header.EffectiveConcurrency)
	completed := make(chan recognitionSourceCompletionV1, slots)
	var gate sync.Mutex
	var stopped error
	stop := func(err error) {
		if err == nil {
			return
		}
		gate.Lock()
		if stopped == nil {
			stopped = err
		}
		gate.Unlock()
	}
	inFlight := 0
	dispatch := func(work recognitionSourceWorkV1) bool {
		gate.Lock()
		defer gate.Unlock()
		if stopped == nil {
			stopped = ctx.Err()
		}
		if stopped != nil {
			return false
		}
		inFlight++
		go func() {
			finished := recognitionSourceCompletionV1{work: work}
			if work.target == nil {
				read := a.recognizeReviewBatchV1(ctx, pagePNG, plan, runtime, batches[work.reviewIndex], initial, work.reviewIndex)
				finished.outcomes, finished.err = read.outcomes, read.err
			} else {
				finished.err = a.recognizeLayoutAdjudicationV2(ctx, pagePNG, plan, runtime, *work.target, initial[work.target.TargetID], work.reviewed)
			}
			stop(finished.err)
			completed <- finished
		}()
		return true
	}
	for len(queue) > 0 || inFlight > 0 {
		for len(queue) > 0 && inFlight < slots {
			if !dispatch(queue[0]) {
				queue = nil
				break
			}
			queue = queue[1:]
		}
		if inFlight == 0 {
			break
		}
		finished := <-completed
		inFlight--
		if finished.err != nil || finished.work.target != nil {
			continue
		}
		ready := make([]recognitionSourceWorkV1, 0, len(finished.outcomes))
		for _, outcome := range finished.outcomes {
			if _, duplicate := result[outcome.targetID]; duplicate {
				stop(fmt.Errorf("%w: review target appears in multiple batches", k12.ErrRecognitionLayoutPlanV2Unauthorized))
				break
			}
			result[outcome.targetID] = outcome
			if !plan.SourceAdjudication || !recognitionNeedsAdjudicationV2(initial[outcome.targetID], outcome, plan.InitialReadMode, runtime.Header.ExecutionPolicy) {
				continue
			}
			singleton, err := recognitionReviewSingletonPayloadV1(outcome.source.Payload, outcome.targetID, batches)
			if err != nil {
				stop(err)
				break
			}
			target, err := recognitionLayoutTargetV2(plan, outcome.targetID)
			if err != nil {
				stop(err)
				break
			}
			projected := outcome
			projected.source.Payload = singleton
			ready = append(ready, recognitionSourceWorkV1{target: &target, reviewed: projected})
		}
		// 已就绪的冲突优先释放，最终题序仍由冻结计划决定。
		queue = append(ready, queue...)
	}
	gate.Lock()
	err := stopped
	gate.Unlock()
	if err != nil {
		return nil, err
	}
	return result, nil
}

func logRecognitionPhaseV1(ctx context.Context, headerDigest, phase string, started time.Time) {
	slog.InfoContext(ctx, "K12 recognition phase completed", "header_digest", headerDigest, "phase", phase, "elapsed_ms", time.Since(started).Milliseconds())
}
