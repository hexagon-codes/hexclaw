package usecase

import (
	"context"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type zeroPolicyUnknownRecognitionProbe struct {
	*recognitionLayoutInitialV2Probe
	unknown bool
}

func (p *zeroPolicyUnknownRecognitionProbe) Recognize(ctx context.Context, image []byte) ([]RecognizedQuestion, error) {
	_, err := p.recognitionLayoutInitialV2Probe.Recognize(ctx, image)
	if p.unknown && p.entryErr == nil {
		if _, claimed, claimErr := p.records.ClaimModelPhysicalInvocationSent(ctx, p.agentName, p.manifest.PhysicalInvocationID); claimErr != nil || !claimed {
			return nil, claimErr
		}
		if _, markErr := p.records.MarkModelPhysicalInvocationOutcomeUnknown(ctx, p.agentName, p.manifest.PhysicalInvocationID, "controlled_transport_unknown"); markErr != nil {
			return nil, markErr
		}
		return nil, context.DeadlineExceeded
	}
	return nil, err
}

func TestRecognitionZeroPolicyKeepsLegacyAndUnknownPhysicalBoundaries(t *testing.T) {
	for _, version := range []int{k12.RecognitionPlanVersionV1, k12.RecognitionPlanVersionV2} {
		t.Run(map[int]string{1: "legacy v1", 2: "v2 unknown child"}[version], func(t *testing.T) {
			probe := &zeroPolicyUnknownRecognitionProbe{recognitionLayoutInitialV2Probe: &recognitionLayoutInitialV2Probe{}, unknown: version == k12.RecognitionPlanVersionV2}
			d := recoveryDeps(t, probe, nil, nil)
			probe.records = d.Records
			if version == k12.RecognitionPlanVersionV2 {
				d.GradingBudgetSnapshot = recognitionLayoutInitialV2Budget()
			}
			d.GradingBudgetSnapshot.RecognitionPlanVersion = version
			o := newRecoverableOrchestrator(t, d, t.TempDir())
			photo := orchestratorPhotoRequest()
			photo.Image = recognitionLayoutInitialV2PagePNG(t)
			job, _, err := o.StartPhotoGradingJob(t.Context(), StartPhotoGradingInput{Photo: photo, SourceKind: "desktop", SourceKey: "zero-policy"})
			if err != nil {
				t.Fatal(err)
			}
			probe.agentName, probe.jobID = job.Record.AgentName, job.Record.RecordID
			_, _ = o.RunGradingJob(t.Context(), probe.jobID)
			if probe.parent.InvocationID == "" || !probe.parent.RequestPolicySnapshot.IsZero() {
				t.Fatal("fixture must retain the actual zero-policy parent")
			}
			before, err := d.Records.ListModelPhysicalInvocations(t.Context(), probe.agentName, probe.jobID)
			if err != nil {
				t.Fatal(err)
			}
			_, receiptErr := o.recognitionPhysicalSuccessSet(t.Context(), probe.parent, photo.Image)
			if version == k12.RecognitionPlanVersionV1 {
				if receiptErr != nil {
					t.Fatalf("legacy zero-policy path changed: %v", receiptErr)
				}
				return
			}
			if probe.entryErr != nil || len(before) != 1 || before[0].Status != k12.ModelInvocationOutcomeUnknown || receiptErr == nil {
				t.Fatalf("V2 unknown must remain explicit instead of bypassing its physical ledger: entry=%v children=%+v err=%v", probe.entryErr, before, receiptErr)
			}
			current, err := d.GetGradingJob(t.Context(), probe.agentName, probe.jobID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Record.Status != k12.GradingStageOutcomeUnknown {
				if _, err := d.AdvanceGradingStage(t.Context(), probe.agentName, probe.jobID, AdvanceGradingInput{Outcome: GradingOutcomeUnknown, FailureKind: "controlled_transport_unknown"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := o.RecoverGradingJobs(t.Context(), []string{probe.agentName}); err != nil {
				t.Fatal(err)
			}
			after, err := d.Records.ListModelPhysicalInvocations(t.Context(), probe.agentName, probe.jobID)
			current, jobErr := d.GetGradingJob(t.Context(), probe.agentName, probe.jobID)
			if err != nil || jobErr != nil || !reflect.DeepEqual(before, after) || probe.calls != 1 || current.Record.Status != k12.GradingStageOutcomeUnknown {
				t.Fatalf("recovery changed or resent an unknown physical request: calls=%d stage=%s err=%v/%v", probe.calls, current.Record.Status, err, jobErr)
			}
		})
	}
}
