package usecase

import (
	"context"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type policyRecordingClassifier struct {
	imageTaskClassifierStub
	policy k12.ModelRequestPolicySnapshot
}

func (c *policyRecordingClassifier) ClassifyImageTask(ctx context.Context, in ImageTaskClassificationInput) (ImageTaskClassification, error) {
	c.policy, _ = k12.GradingModelRequestPolicyFromContext(ctx)
	return c.imageTaskClassifierStub.ClassifyImageTask(ctx, in)
}

func TestImageTaskConfiguredPolicyPersistsAndReplaysWithoutCallingProvider(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "legacy zero policy"
		policy := k12.ModelRequestPolicySnapshot{}
		if configured {
			name = "configured policy"
			policy = k12.ConfiguredRecognizingRequestPolicy()
		}
		t.Run(name, func(t *testing.T) {
			original, grading := newImageTaskCoordinatorForTest(t, &imageTaskClassifierStub{})
			original.ResolveRoute = func(requested k12.ImageTaskRouteSnapshot) (k12.ImageTaskRouteSnapshot, error) {
				route, err := imageTaskRouteForTest(requested)
				route.RecognizingRequestPolicy = policy
				return route, err
			}
			input := testCreateImageTaskInput()
			input.RouteRequest.Provider, input.RouteRequest.Model = "cloud-gpt", "gpt-6-sol"
			prepared, created, err := original.Create(context.Background(), input)
			if err != nil || !created {
				t.Fatalf("prepare: created=%v err=%v", created, err)
			}
			classifier := &policyRecordingClassifier{imageTaskClassifierStub: imageTaskClassifierStub{result: ImageTaskClassification{
				Intent: k12.ImageTaskIntentCompletedHomework, IntentEvidence: []string{"handwritten answers"}, Confidence: 1,
			}}}
			restarted := restartImageTaskCoordinator(original, classifier)
			restarted.ResolveRoute = func(k12.ImageTaskRouteSnapshot) (k12.ImageTaskRouteSnapshot, error) {
				t.Fatal("recovery must not resolve today's route")
				return k12.ImageTaskRouteSnapshot{}, nil
			}
			stored, err := restarted.Get(context.Background(), input.AgentName, prepared.Dispatch.DispatchID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Dispatch.ClassificationRouteSnapshot.RecognizingRequestPolicy != policy {
				t.Fatal("SQLite route lost frozen policy")
			}
			if _, err := restarted.Run(context.Background(), input.AgentName, prepared.Dispatch.DispatchID); err != nil {
				t.Fatal(err)
			}
			if classifier.policy != policy || grading.input.ModelSnapshot.RecognizingRequestPolicy != policy {
				t.Fatalf("classification or grading changed frozen policy: %+v %+v", classifier.policy, grading.input.ModelSnapshot)
			}
			if _, err := restarted.Run(context.Background(), input.AgentName, prepared.Dispatch.DispatchID); err != nil {
				t.Fatal(err)
			}
			if classifier.calls != 1 {
				t.Fatalf("successful checkpoint replayed %d times", classifier.calls)
			}
		})
	}
}

func TestImageTaskLegacyPolicyUnknownRemainsParkedAfterUpgrade(t *testing.T) {
	original, _ := newImageTaskCoordinatorForTest(t, &imageTaskClassifierStub{})
	input := testCreateImageTaskInput()
	input.RouteRequest.Provider, input.RouteRequest.Model = "cloud-gpt", "gpt-6-sol"
	prepared, _, err := original.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := original.Records.ClaimImageTaskInvocationSend(context.Background(), input.AgentName,
		prepared.Dispatch.ClassificationInvocationID, "original-request", 1000); err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	classifier := &policyRecordingClassifier{}
	restarted := restartImageTaskCoordinator(original, classifier)
	restarted.ResolveRoute = func(k12.ImageTaskRouteSnapshot) (k12.ImageTaskRouteSnapshot, error) {
		t.Fatal("sent checkpoint must not acquire today's policy")
		return k12.ImageTaskRouteSnapshot{}, nil
	}
	if recovered, err := restarted.Recover(context.Background(), []string{input.AgentName}); err != nil || recovered != 0 {
		t.Fatalf("unknown checkpoint resumed: count=%d err=%v", recovered, err)
	}
	stored, err := restarted.Get(context.Background(), input.AgentName, prepared.Dispatch.DispatchID)
	if err != nil {
		t.Fatal(err)
	}
	if classifier.calls != 0 || !stored.Dispatch.ClassificationRouteSnapshot.RecognizingRequestPolicy.IsZero() {
		t.Fatal("unknown checkpoint was resent or backfilled")
	}
}
