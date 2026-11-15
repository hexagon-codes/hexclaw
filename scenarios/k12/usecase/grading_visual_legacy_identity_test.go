package usecase

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

func TestGradingVisualRequestDoesNotResendStoredTextInvocation(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "known_text_success"
		if unknown {
			name = "unknown_text_outcome"
		}
		t.Run(name, func(t *testing.T) {
			o := newItemResumeOrchestrator(t, t.TempDir(), []RecognizedQuestion{{Question: "q1", Subject: "数学", StudentAnswer: "1", AnswerState: AnswerStatePresent}}, &itemResumeSolver{calls: map[string]int{}}, &itemResumeGrader{calls: map[string]int{}})
			jobID := runItemResumeJobToAssessing(t, o, "legacy-visual-source")
			run, job := confirmItemResumeJobWithoutRun(t, o, jobID)
			question := run.questions[0]
			executor := newDurableGradingPhysicalCallExecutor(o, job, question)
			calls := 0
			textSpec := GradingPhysicalCallSpec{Operation: k12.GradingItemOperationSolveGenerate, RequestDigest: "old-text-request"}
			original, originalErr := executor.ExecuteGradingPhysicalCall(t.Context(), textSpec, func(context.Context) (string, error) {
				calls++
				if unknown {
					return "", context.DeadlineExceeded
				}
				return `{"Output":"old response without pixels"}`, nil
			})
			if unknown != errors.Is(originalErr, ErrGradingPhysicalCallOutcomeUnknown) || (!unknown && originalErr != nil) {
				t.Fatalf("seed text invocation: result=%+v err=%v", original, originalErr)
			}
			before, err := o.deps.Records.ListGradingItemInvocations(t.Context(), job.Record.AgentName, jobID)
			if err != nil || len(before) != 1 {
				t.Fatalf("text ledger=%+v err=%v", before, err)
			}
			executor = newDurableGradingPhysicalCallExecutor(o, job, question)
			visualSpec := GradingPhysicalCallSpec{Operation: textSpec.Operation, RequestDigest: "new-request-with-source-pixels", LegacyRequestDigest: textSpec.RequestDigest}
			_, err = executor.ExecuteGradingPhysicalCall(t.Context(), visualSpec, func(context.Context) (string, error) {
				calls++
				return `{"Output":"unexpected second provider call"}`, nil
			})
			var stopped interface{ SolveExecutionMustStop() bool }
			if err == nil || calls != 1 || !errors.As(err, &stopped) || !stopped.SolveExecutionMustStop() {
				t.Fatalf("old text request became a new visual call: sends=%d err=%v", calls, err)
			}
			if unknown {
				if !errors.Is(err, ErrModelInvocationRequiresReconciliation) {
					t.Fatalf("unknown text result lost reconciliation: %v", err)
				}
			} else if !errors.Is(err, ErrInvalidInput) || !errors.Is(err, k12storage.ErrGradingItemInvocationConflict) || errors.Is(err, ErrModelInvocationRequiresReconciliation) {
				t.Fatalf("known text result was relabelled unknown or reused: %v", err)
			}
			after, listErr := o.deps.Records.ListGradingItemInvocations(t.Context(), job.Record.AgentName, jobID)
			if listErr != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("legacy result changed after visual attempt: before=%+v after=%+v err=%v", before, after, listErr)
			}
		})
	}
}
