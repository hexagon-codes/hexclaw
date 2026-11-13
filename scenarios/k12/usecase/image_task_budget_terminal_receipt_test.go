package usecase

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestGradingItemReceiptProjectsResultDigestAlgorithm(t *testing.T) {
	const rawDigest = "aa650f1d4d0b3385eaf6fdd531ab8a3051d285b00133dd240628b47a3906ce28"
	for _, tc := range []struct{ name, stored, public string }{
		{"failed_result", rawDigest, "sha256:" + rawDigest},
		{"prefixed", "sha256:" + rawDigest, "sha256:" + rawDigest},
		{"empty", "", ""},
		{"invalid", strings.Repeat("z", 64), strings.Repeat("z", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			invocation := k12.GradingItemInvocation{ResultDigest: tc.stored}
			got := gradingItemInvocationReceipt(invocation, "source")
			if got.ResultDigest != tc.public || invocation.ResultDigest != tc.stored {
				t.Fatalf("public digest or stored identity changed: public=%q stored=%q", got.ResultDigest, invocation.ResultDigest)
			}
		})
	}
}

func TestImageTaskBudgetPreflightFailureConvergesToDurableSolveReceipt(t *testing.T) {
	classifier := &imageTaskClassifierStub{result: ImageTaskClassification{
		Intent:         k12.ImageTaskIntentCompletedHomework,
		IntentEvidence: []string{"visible handwritten answers"},
		Confidence:     0.99,
	}}
	coordinator, grading := newImageTaskCoordinatorForTest(t, classifier)
	grading.startErr = fmt.Errorf(
		"%w: %w: public image_task requires a frozen grading budget policy",
		ErrInvalidInput,
		errGradingBudgetMissing,
	)

	view, created, runErr := createAndRunImageTask(
		t,
		coordinator,
		testCreateImageTaskInput(),
	)
	if !created || !errors.Is(runErr, ErrInvalidInput) {
		t.Fatalf("expected accepted dispatch and deterministic preflight failure: created=%v err=%v", created, runErr)
	}
	if grading.starts != 1 || grading.async != 0 {
		t.Fatalf("preflight must not launch async grading: starts=%d async=%d", grading.starts, grading.async)
	}

	result, err := coordinator.Result(
		context.Background(),
		"mingming",
		view.Dispatch.DispatchID,
	)
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if result.Dispatch.Status != k12.ImageTaskStatusFailed ||
		result.Dispatch.FailureKind != "grading_budget_missing" ||
		!result.Dispatch.RetrySafe {
		t.Fatalf("dispatch did not converge to retry-safe failed: %+v", result.Dispatch)
	}
	if len(result.OperationReceipts) != 2 {
		t.Fatalf("expected classification and terminal solve receipts: %+v", result.OperationReceipts)
	}
	var receipt ImageTaskOperationReceipt
	for _, candidate := range result.OperationReceipts {
		if candidate.Operation == "solve" {
			receipt = candidate
		}
	}
	if receipt.InvocationID == "" ||
		receipt.Operation != "solve" ||
		receipt.CanonicalInputDigest != view.Dispatch.SourceDigest ||
		receipt.Status != string(k12.ImageTaskInvocationFailed) ||
		receipt.Attempt != 1 ||
		receipt.Provider != "hexclaw-gpt" ||
		receipt.Model != "gpt-5.6-sol" ||
		receipt.ResultDigest != "" {
		t.Fatalf("unexpected terminal solve receipt: %+v", receipt)
	}

	submission, err := coordinator.Records.GetHomeworkSubmission(
		context.Background(),
		"mingming",
		result.Dispatch.TargetObjectID,
	)
	if err != nil {
		t.Fatalf("GetHomeworkSubmission: %v", err)
	}
	if submission.Status != k12.HomeworkSubmissionFailed ||
		submission.GradingJobID != "" {
		t.Fatalf("homework did not fail before GradingJob creation: %+v", submission)
	}

	replayed, err := coordinator.Result(
		context.Background(),
		"mingming",
		view.Dispatch.DispatchID,
	)
	if err != nil {
		t.Fatalf("replayed Result: %v", err)
	}
	if !reflect.DeepEqual(replayed.OperationReceipts, result.OperationReceipts) {
		t.Fatalf(
			"terminal receipt identity drifted: first=%+v replay=%+v",
			result.OperationReceipts,
			replayed.OperationReceipts,
		)
	}
}
