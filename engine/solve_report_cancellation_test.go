package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

type durableSolveProbeError struct{ cause error }

func (e durableSolveProbeError) Error() string                { return e.cause.Error() }
func (e durableSolveProbeError) Unwrap() error                { return e.cause }
func (e durableSolveProbeError) SubAgentRetryable() bool      { return false }
func (e durableSolveProbeError) SolveExecutionMustStop() bool { return true }

func TestDurableTeachingReportKeepsCallerCancellation(t *testing.T) {
	for _, mode := range []string{"no_deadline", "explicit_deadline", "caller_cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			cancel := func() {}
			if mode == "explicit_deadline" {
				ctx, cancel = context.WithTimeout(ctx, time.Minute)
			} else if mode == "caller_cancel" {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			expectedDeadline, expectedBounded := ctx.Deadline()
			ctx = WithCallerOwnedExecution(ctx)
			ctx = WithSubAgentCallInterceptor(ctx, SubAgentCallInterceptorFunc(
				func(ctx context.Context, spec SubAgentSpec, next SubAgentExecFunc) (SubAgentResult, error) {
					return next(ctx, spec)
				}))
			probe := errors.New("stop after observing caller context")
			calls := 0
			solver := NewSolveSkill(func(child context.Context, _ SubAgentSpec) (SubAgentResult, error) {
				calls++
				deadline, bounded := child.Deadline()
				if bounded != expectedBounded || (bounded && !deadline.Equal(expectedDeadline)) {
					t.Errorf("child deadline=%v bounded=%v, caller deadline=%v bounded=%v", deadline, bounded, expectedDeadline, expectedBounded)
				}
				if mode == "caller_cancel" {
					cancel()
					if !errors.Is(child.Err(), context.Canceled) {
						t.Error("caller cancellation did not reach the durable report")
					}
				}
				return SubAgentResult{}, durableSolveProbeError{cause: probe}
			}, NewSubAgentRegistry(""))
			_, err := solver.Execute(ctx, map[string]any{
				"problem":          "根据图中条件求阴影部分面积，并解释各步依据。",
				"self_consistency": 1,
			})
			if !errors.Is(err, probe) || calls != 1 {
				t.Fatalf("calls=%d err=%v, want one observed call", calls, err)
			}
		})
	}
}

func TestDurableSolveUnknownStopsRemainingCandidates(t *testing.T) {
	unknown := errors.New("durable provider outcome requires reconciliation")
	calls := 0
	solver := NewSolveSkill(func(context.Context, SubAgentSpec) (SubAgentResult, error) {
		calls++
		return SubAgentResult{}, durableSolveProbeError{cause: unknown}
	}, NewSubAgentRegistry(""))
	_, err := solver.Execute(context.Background(), map[string]any{
		"problem":          "根据图中条件求阴影部分面积，并用不同方法核对。",
		"self_consistency": 2, "method_diversity": true,
	})
	if !errors.Is(err, unknown) || calls != 1 {
		t.Fatalf("calls=%d err=%v, want original unknown outcome without a second candidate", calls, err)
	}
}
