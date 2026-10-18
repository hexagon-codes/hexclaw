package engineadapter

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

func preparedReviewFixtureV1(t *testing.T) (context.Context, []byte, k12.RecognitionLayoutPlanV2, k12.RecognitionLayoutPlanRuntimeV2, k12.RecognitionLayoutReviewBatchAuthorizationRequestV1) {
	t.Helper()
	ctx, page, plan, runtime, batches, _, executor, _ := sourceScheduleFixtureV1(t)
	var members []k12.RecognitionLayoutReviewMemberAuthorizationV1
	for _, batch := range batches {
		members = append(members, batch.Members...)
	}
	runtime.ReviewBatches, executor.runtime.ReviewBatches = nil, nil
	ctx, err := withRecognitionReviewPreparedInputsV1(ctx, page, plan, runtime)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := authorizeRecognitionReviewGroupsV1(ctx, page, plan, runtime, members)
	if err != nil || len(groups) != 1 {
		t.Fatalf("prepare review group: groups=%d err=%v", len(groups), err)
	}
	sealRecognitionReviewPreparedInputsV1(ctx)
	return ctx, page, plan, runtime, groups[0].RecognitionLayoutReviewBatchAuthorizationRequestV1
}

func TestRecognitionReviewPreparedInputV1ReusesExactAuthorizedBytes(t *testing.T) {
	ctx, page, plan, runtime, request := preparedReviewFixtureV1(t)
	wantImage, wantPrompt, wantRequest, err := recognitionReviewRequestV1(page, plan, request)
	if err != nil {
		t.Fatal(err)
	}
	image, prompt, actual, reused, err := recognitionReviewExecutionInputV1(ctx, page, plan, runtime, request)
	if err != nil || !reused || !bytes.Equal(image, wantImage) || prompt != wantPrompt || !reflect.DeepEqual(actual, wantRequest) {
		t.Fatalf("authorized input was not reused exactly: reused=%v err=%v", reused, err)
	}
	image[0] ^= 0xff
	actual.Members[0].CandidateID = "changed-consumer-copy"
	image, prompt, actual, reused, err = recognitionReviewExecutionInputV1(ctx, page, plan, runtime, request)
	if err != nil || !reused || !bytes.Equal(image, wantImage) || prompt != wantPrompt || !reflect.DeepEqual(actual, wantRequest) {
		t.Fatal("consumer mutation changed another read of the frozen input")
	}
	if err := rememberRecognitionReviewPreparedInputV1(ctx, request, wantImage, wantPrompt); !errors.Is(err, k12.ErrRecognitionLayoutPlanV2Unauthorized) {
		t.Fatal("dispatch-stage input map remained writable")
	}
}

func TestRecognitionReviewPreparedInputV1RejectsSourceAndInputDrift(t *testing.T) {
	for _, scenario := range []string{"page", "plan target", "request target", "owner", "parent", "image bytes", "prompt", "input digest"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, page, plan, runtime, request := preparedReviewFixtureV1(t)
			prepared := ctx.Value(recognitionReviewPreparedInputKeyV1{}).(*recognitionReviewPreparedInputsV1)
			switch scenario {
			case "page":
				page = append([]byte(nil), page...)
				page[len(page)-1] ^= 0xff
			case "plan target":
				plan.Targets = append([]k12.RecognitionLayoutTargetV2(nil), plan.Targets...)
				plan.Targets[0].Region.X++
			case "request target":
				request.Members = append([]k12.RecognitionLayoutReviewMemberAuthorizationV1(nil), request.Members...)
				request.Members[0].CandidateID = plan.Targets[1].TargetID
			case "owner":
				runtime.Header.AgentName = "another-owner"
			case "parent":
				runtime.Header.ParentInvocationID = "another-task"
			case "image bytes":
				input := prepared.inputs[request.PhysicalUnit]
				input.image[0] ^= 0xff
			case "prompt":
				input := prepared.inputs[request.PhysicalUnit]
				input.prompt += "changed"
				prepared.inputs[request.PhysicalUnit] = input
			case "input digest":
				request.InputDigest = recognitionInitialReadDigestV1([]byte("changed-input"))
			}
			image, prompt, _, reused, err := recognitionReviewExecutionInputV1(ctx, page, plan, runtime, request)
			if !errors.Is(err, k12.ErrRecognitionLayoutPlanV2Unauthorized) || reused || len(image) != 0 || prompt != "" {
				t.Fatalf("changed source was reused or silently rebuilt: reused=%v err=%v", reused, err)
			}
		})
	}
}

func TestRecognitionReviewPreparedInputV1KeepsLegacyAndRecoveryConstruction(t *testing.T) {
	ctx, page, plan, runtime, request := preparedReviewFixtureV1(t)
	wantImage, wantPrompt, wantRequest, err := recognitionReviewRequestV1(page, plan, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"legacy", "recovery without memory", "recovery with existing authorization"} {
		t.Run(scenario, func(t *testing.T) {
			current, currentRuntime := ctx, runtime
			switch scenario {
			case "legacy":
				currentRuntime.Header.ExecutionPolicy = ""
			case "recovery without memory":
				current = context.Background()
			case "recovery with existing authorization":
				current, err = withRecognitionReviewPreparedInputsV1(context.Background(), page, plan, runtime)
				if err != nil {
					t.Fatal(err)
				}
				sealRecognitionReviewPreparedInputsV1(current)
			}
			image, prompt, actual, reused, err := recognitionReviewExecutionInputV1(current, page, plan, currentRuntime, request)
			if err != nil || reused || !bytes.Equal(image, wantImage) || prompt != wantPrompt || !reflect.DeepEqual(actual, wantRequest) {
				t.Fatalf("original construction changed: reused=%v err=%v", reused, err)
			}
		})
	}
}
