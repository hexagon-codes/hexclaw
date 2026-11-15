package usecase

import (
	"errors"
	"fmt"
	"testing"
)

func TestGradingNoRetryPreservesKnownFailureAndUnknownOutcome(t *testing.T) {
	auditFailure := errors.New("report teaching audit did not accept the candidate")
	unknown := fmt.Errorf("%w: invocation=sample", ErrGradingPhysicalCallOutcomeUnknown)
	for _, tc := range []struct {
		name        string
		cause       error
		wantUnknown bool
		stopSolve   bool
	}{
		{name: "known audit rejection", cause: auditFailure},
		{name: "unknown provider outcome", cause: unknown, wantUnknown: true},
		{name: "known source identity conflict", cause: ErrInvalidInput, stopSolve: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := gradingPhysicalNoRetryError{cause: tc.cause, stopSolve: tc.stopSolve}
			if err.Error() != tc.cause.Error() {
				t.Fatalf("no-retry marker changed the failure meaning: %q, want %q", err.Error(), tc.cause.Error())
			}
			if !errors.Is(err, tc.cause) || errors.Is(err, ErrGradingPhysicalCallOutcomeUnknown) != tc.wantUnknown {
				t.Fatalf("no-retry marker changed the error chain: %v", err)
			}
			if err.SubAgentRetryable() {
				t.Fatal("known failure and unknown outcome must not trigger an automatic model retry")
			}
			if err.SolveExecutionMustStop() != (tc.wantUnknown || tc.stopSolve) {
				t.Fatalf("whole solve stop=%v, want=%v", err.SolveExecutionMustStop(), tc.wantUnknown || tc.stopSolve)
			}
		})
	}
}
