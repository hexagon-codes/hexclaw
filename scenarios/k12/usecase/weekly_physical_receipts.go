package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/hexagon-codes/hexclaw/egress"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// weeklyPhysicalReceipt 保存一次物理调用的完整返回；汇总解析不覆盖原始证据。
type weeklyPhysicalReceipt struct {
	Operation         k12.GradingItemOperation `json:"operation"`
	RequestDigest     string                   `json:"request_digest"`
	Status            string                   `json:"status"`
	Payload           string                   `json:"payload,omitempty"`
	Error             string                   `json:"error,omitempty"`
	ReuseSourceDigest string                   `json:"reuse_source_digest,omitempty"`
}

type weeklySolveInterpretation struct {
	IdempotencyKey       string      `json:"idempotency_key"`
	RequestDigest        string      `json:"request_digest"`
	InputDigest          string      `json:"input_digest"`
	ReceiptsDigest       string      `json:"receipts_digest"`
	CreatedAt            int64       `json:"created_at"`
	Result               SolveResult `json:"result"`
	SourcePlanRevision   int         `json:"source_plan_revision,omitempty"`
	ExpectedPlanRevision int         `json:"expected_plan_revision,omitempty"`
}

type weeklyPhysicalExecutor struct {
	mu         sync.Mutex
	call       *weeklyCandidateCall
	save       func() error
	replayOnly bool
	used       map[string]bool
	err        error
}

// GradingPhysicalCallsReplayOnly 让适配器在只读回执模式下拒绝未受拦截的调用。
func GradingPhysicalCallsReplayOnly(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	executor, ok := ctx.Value(gradingPhysicalCallContextKey{}).(*weeklyPhysicalExecutor)
	return ok && executor.replayOnly
}

func (e *weeklyPhysicalExecutor) ExecuteGradingPhysicalCall(ctx context.Context, spec GradingPhysicalCallSpec, next func(context.Context) (string, error)) (GradingPhysicalCallResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fail := func(err error) (GradingPhysicalCallResult, error) {
		e.err = err
		return GradingPhysicalCallResult{}, gradingPhysicalNoRetryError{cause: err}
	}
	if e.err != nil {
		return GradingPhysicalCallResult{}, gradingPhysicalNoRetryError{cause: e.err}
	}
	if spec.Operation != k12.GradingItemOperationSolveGenerate && spec.Operation != k12.GradingItemOperationSolveVerify {
		return fail(fmt.Errorf("weekly physical operation unavailable"))
	}
	key := string(spec.Operation) + ":" + spec.RequestDigest
	for _, receipt := range e.call.PhysicalCalls {
		if receipt.Operation != spec.Operation || receipt.RequestDigest != spec.RequestDigest {
			continue
		}
		switch receipt.Status {
		case "succeeded":
			if !json.Valid([]byte(receipt.Payload)) {
				return fail(fmt.Errorf("weekly physical response missing"))
			}
			if e.used != nil {
				e.used[key] = true
			}
			return GradingPhysicalCallResult{Payload: receipt.Payload}, nil
		case "sent", "outcome_unknown":
			return fail(ErrModelInvocationRequiresReconciliation)
		default:
			return fail(fmt.Errorf("weekly physical invocation failed: %s", receipt.Error))
		}
	}
	if e.replayOnly {
		return fail(fmt.Errorf("weekly physical replay request does not match saved receipt"))
	}
	receipt := weeklyPhysicalReceipt{Operation: spec.Operation, RequestDigest: spec.RequestDigest, Status: "sent"}
	e.call.PhysicalCalls = append(e.call.PhysicalCalls, receipt)
	index := len(e.call.PhysicalCalls) - 1
	if err := e.save(); err != nil {
		return fail(err)
	}
	payload, err := next(ctx)
	receipt.Payload = payload
	if err != nil {
		receipt.Status, receipt.Error = "failed", err.Error()
		if !errors.Is(err, egress.ErrProviderNotSent) && sentProviderOutcomeUnknown(err, ctx.Err()) {
			receipt.Status = "outcome_unknown"
		}
	} else if !json.Valid([]byte(payload)) {
		receipt.Status, receipt.Error = "failed", "weekly physical response is not valid JSON"
		err = errors.New(receipt.Error)
	} else {
		receipt.Status = "succeeded"
	}
	e.call.PhysicalCalls[index] = receipt
	if saveErr := e.save(); saveErr != nil {
		return fail(saveErr)
	}
	if receipt.Status == "outcome_unknown" {
		return fail(ErrModelInvocationRequiresReconciliation)
	}
	if err != nil {
		return fail(err)
	}
	return GradingPhysicalCallResult{Payload: payload}, nil
}

func (d Deps) weeklySolveInputDigest(ctx context.Context, request WeeklyPracticeCandidateRequest, index int, question string) string {
	target := request.Targets[request.Generation.Items[index].Target]
	return digestValue(struct {
		Question, Grade, Constraint, Textbook string
		ProfileRevision                       int
		Target                                WeeklyPracticeTarget
		Route                                 k12.GradingModelSnapshot
		Progress                              k12.CurriculumProgress
	}{question, request.GradeTerm, d.constraintFor(ctx, request.GradeTerm), request.Textbook, request.ProfileRevision, target, request.Generation.Route, request.Progress})
}

func weeklyLatestSolve(step *weeklyCandidateStep) *weeklyCandidateCall {
	if n := len(step.SolveRecoveryAttempts); n > 0 {
		return &step.SolveRecoveryAttempts[n-1].Call
	}
	return &step.Solve
}

func weeklySuccessfulSolveResult(call *weeklyCandidateCall) *SolveResult {
	if n := len(call.Interpretations); n > 0 {
		return &call.Interpretations[n-1].Result
	}
	return call.Result
}

func weeklyPhysicalReplayReady(call *weeklyCandidateCall) bool {
	if call.Status != "succeeded" || call.Result == nil || strings.TrimSpace(call.InputDigest) == "" {
		return false
	}
	generate, verify := false, false
	for _, receipt := range call.PhysicalCalls {
		if receipt.Status != "succeeded" || !json.Valid([]byte(receipt.Payload)) {
			return false
		}
		generate = generate || receipt.Operation == k12.GradingItemOperationSolveGenerate
		verify = verify || receipt.Operation == k12.GradingItemOperationSolveVerify
	}
	return generate && verify
}
