package usecase

import (
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func TestREGBUGK12ImageTaskRecognizingPolicy001ModelNameKeepsZeroPolicy(t *testing.T) {
	got := gradingSnapshotFromImageRoute(k12.ImageTaskRouteSnapshot{
		Provider: "hexclaw-gpt",
		Model:    k12.RecognizingPolicyModel,
	})
	if !got.RecognizingRequestPolicy.IsZero() {
		t.Fatalf("model name added an unfrozen recognizing policy: %+v", got)
	}
	if err := k12.ValidateGradingRecognizingRequestPolicy(got); err != nil {
		t.Fatalf("Sol ImageTask route cannot enter GradingJob: %v", err)
	}
}

func TestREGBUGK12ImageTaskRecognizingPolicy001OtherModelKeepsZeroPolicy(t *testing.T) {
	for _, model := range []string{"gpt-5.6-terra", "gpt-5.6-luna"} {
		t.Run(model, func(t *testing.T) {
			got := gradingSnapshotFromImageRoute(k12.ImageTaskRouteSnapshot{
				Provider: "hexclaw-gpt",
				Model:    model,
			})
			if !got.RecognizingRequestPolicy.IsZero() {
				t.Fatalf("non-Sol ImageTask route inherited Sol recognizing policy: %+v", got)
			}
			if err := k12.ValidateGradingRecognizingRequestPolicy(got); err != nil {
				t.Fatalf("non-Sol ImageTask route is invalid: %v", err)
			}
		})
	}
}

func TestREGBUGK12ImageTaskRecognizingPolicy001CoordinatorPassesPolicyToGradingJob(t *testing.T) {
	classifier := &imageTaskClassifierStub{result: ImageTaskClassification{
		Intent:         k12.ImageTaskIntentCompletedHomework,
		IntentEvidence: []string{"visible handwritten answers"},
		Confidence:     0.99,
	}}
	coordinator, grading := newImageTaskCoordinatorForTest(t, classifier)
	coordinator.ResolveRoute = func(requested k12.ImageTaskRouteSnapshot) (k12.ImageTaskRouteSnapshot, error) {
		route, err := imageTaskRouteForTest(requested)
		route.RecognizingRequestPolicy = k12.ApprovedRecognizingRequestPolicy()
		return route, err
	}
	view, created, err := createAndRunImageTask(t, coordinator, testCreateImageTaskInput())
	if err != nil || !created || view.Homework == nil || grading.starts != 1 {
		t.Fatalf("run completed-homework ImageTask: created=%v starts=%d homework=%t err=%v",
			created, grading.starts, view.Homework != nil, err)
	}
	got := grading.input.ModelSnapshot
	if got.Provider != "hexclaw-gpt" || got.Model != k12.RecognizingPolicyModel ||
		got.RecognizingRequestPolicy != k12.ApprovedRecognizingRequestPolicy() {
		t.Fatalf("ImageTask grading job lost frozen route/policy: %+v", got)
	}
	if err := k12.ValidateGradingRecognizingRequestPolicy(got); err != nil {
		t.Fatalf("ImageTask grading job cannot enter recognizing: %v", err)
	}
}
