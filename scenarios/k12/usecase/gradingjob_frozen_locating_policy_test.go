package usecase

import (
	"context"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type frozenPolicyAnchorer struct {
	policy k12.ModelRequestPolicySnapshot
	calls  int
}

func (a *frozenPolicyAnchorer) AnchorAnswers(ctx context.Context, _ []byte, questions []RecognizedQuestion) ([]RecognizedQuestion, error) {
	a.calls++
	a.policy, _ = k12.GradingModelRequestPolicyFromContext(ctx)
	out := cloneRecognizedQuestions(questions)
	out[0].BBox = &BBox{X: 0.2, Y: 0.3, W: 0.1, H: 0.05}
	return out, nil
}

func (a *frozenPolicyAnchorer) AnchorAnswerGeometry(ctx context.Context, image []byte, questions []RecognizedQuestion) ([]RecognizedQuestion, error) {
	return a.AnchorAnswers(ctx, image, questions)
}

func TestGradingLocatingUsesExplicitFrozenPolicyForAnyConfiguredModel(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		policy      k12.ModelRequestPolicySnapshot
	}{
		{"named_model_zero", k12.RecognizingPolicyModel, k12.ModelRequestPolicySnapshot{}},
		{"custom_configured", "school-reader", k12.ConfiguredRecognizingRequestPolicy()},
		{"explicit_legacy", k12.RecognizingPolicyModel, k12.ApprovedRecognizingRequestPolicy()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			deps, _ := newPipeline(t, fakeSolver{}, fakeGrader{}, nil)
			anchor := &frozenPolicyAnchorer{}
			deps.AnswerAnchorer = anchor
			snapshot := k12.GradingModelSnapshot{Provider: "school-provider", Model: tc.model, Route: "school-provider/" + tc.model, Capability: "vision", RecognizingRequestPolicy: tc.policy}
			o := trackGradingOrchestrator(t, NewGradingOrchestrator(deps, func(k12.GradingModelSnapshot) (k12.GradingModelSnapshot, error) { return snapshot, nil }))
			job, created, err := o.StartPhotoGradingJob(ctx, StartPhotoGradingInput{Photo: orchestratorPhotoRequest(), SourceKind: "desktop", SourceKey: "frozen-locating"})
			if err != nil || !created {
				t.Fatalf("start: created=%v err=%v", created, err)
			}
			questions := []RecognizedQuestion{{Question: "1+1=", StudentAnswer: "3", AnswerState: AnswerStatePresent, Subject: "数学"}}
			_, state, artifact, unknown := o.executeAnchor(job.Record.RecordID, job.Record.AgentName, []byte("image"), questions, job.Fields.ModelSnapshot)
			if state != k12.GradingAnchorLocated || unknown || anchor.calls != 1 {
				t.Fatalf("locating state=%s artifact=%s unknown=%v calls=%d", state, artifact, unknown, anchor.calls)
			}
			want := k12.ModelRequestPolicySnapshot{}
			if !tc.policy.IsZero() {
				want = k12.ApprovedLocatingRequestPolicy()
			}
			if anchor.policy != want {
				t.Fatalf("locating context changed frozen policy: got=%+v want=%+v", anchor.policy, want)
			}
			invocations, err := deps.Records.ListModelInvocations(ctx, job.Record.AgentName, job.Record.RecordID)
			if err != nil || len(invocations) != 1 || invocations[0].Stage != k12.GradingStageLocating || invocations[0].RequestPolicySnapshot != want {
				t.Fatalf("locating ledger lost policy: invocations=%+v err=%v", invocations, err)
			}
		})
	}
}
