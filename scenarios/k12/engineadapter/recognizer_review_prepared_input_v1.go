package engineadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

type recognitionReviewPreparedInputKeyV1 struct{}

type recognitionReviewPreparedScopeV1 struct {
	header, owner, parent, job, page, plan, planContent string
}

type recognitionReviewPreparedInputV1 struct {
	image   []byte
	prompt  string
	request k12.RecognitionLayoutReviewBatchAuthorizationRequestV1
}

// 授权阶段单线程填充，派发前封存；双槽执行只读，生命周期限定于本次识题。
type recognitionReviewPreparedInputsV1 struct {
	scope  recognitionReviewPreparedScopeV1
	inputs map[k12.RecognitionPhysicalUnit]recognitionReviewPreparedInputV1
	sealed bool
}

func recognitionReviewPreparedScopeForV1(pagePNG []byte, plan k12.RecognitionLayoutPlanV2, runtime k12.RecognitionLayoutPlanRuntimeV2) (recognitionReviewPreparedScopeV1, error) {
	encodedPlan, err := json.Marshal(plan)
	if err != nil || runtime.Header.PageDigest != plan.PageDigest || recognitionInitialReadDigestV1(pagePNG) != plan.PageDigest {
		return recognitionReviewPreparedScopeV1{}, fmt.Errorf("%w: prepared review source drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	return recognitionReviewPreparedScopeV1{
		header: runtime.HeaderDigest, owner: runtime.Header.AgentName, parent: runtime.Header.ParentInvocationID,
		job: runtime.Header.JobID, page: plan.PageDigest, plan: plan.AuthorizedPlanDigest,
		planContent: recognitionInitialReadDigestV1(encodedPlan),
	}, nil
}

func withRecognitionReviewPreparedInputsV1(ctx context.Context, pagePNG []byte, plan k12.RecognitionLayoutPlanV2, runtime k12.RecognitionLayoutPlanRuntimeV2) (context.Context, error) {
	if !k12.RecognitionCompactDAGExecutionPolicy(runtime.Header.ExecutionPolicy) {
		return ctx, nil
	}
	scope, err := recognitionReviewPreparedScopeForV1(pagePNG, plan, runtime)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, recognitionReviewPreparedInputKeyV1{}, &recognitionReviewPreparedInputsV1{
		scope: scope, inputs: make(map[k12.RecognitionPhysicalUnit]recognitionReviewPreparedInputV1),
	}), nil
}

func rememberRecognitionReviewPreparedInputV1(ctx context.Context, request k12.RecognitionLayoutReviewBatchAuthorizationRequestV1, image []byte, prompt string) error {
	prepared, _ := ctx.Value(recognitionReviewPreparedInputKeyV1{}).(*recognitionReviewPreparedInputsV1)
	if prepared == nil {
		return nil
	}
	if prepared.sealed || request.PlanDigest != prepared.scope.plan {
		return fmt.Errorf("%w: prepared review input scope is immutable", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	if _, duplicate := prepared.inputs[request.PhysicalUnit]; duplicate {
		return fmt.Errorf("%w: prepared review input already exists", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	request.Members = append([]k12.RecognitionLayoutReviewMemberAuthorizationV1(nil), request.Members...)
	prepared.inputs[request.PhysicalUnit] = recognitionReviewPreparedInputV1{image: append([]byte(nil), image...), prompt: prompt, request: request}
	return nil
}

func sealRecognitionReviewPreparedInputsV1(ctx context.Context) {
	if prepared, _ := ctx.Value(recognitionReviewPreparedInputKeyV1{}).(*recognitionReviewPreparedInputsV1); prepared != nil {
		prepared.sealed = true
	}
}

func recognitionReviewExecutionInputV1(ctx context.Context, pagePNG []byte, plan k12.RecognitionLayoutPlanV2, runtime k12.RecognitionLayoutPlanRuntimeV2, request k12.RecognitionLayoutReviewBatchAuthorizationRequestV1) ([]byte, string, k12.RecognitionLayoutReviewBatchAuthorizationRequestV1, bool, error) {
	prepared, _ := ctx.Value(recognitionReviewPreparedInputKeyV1{}).(*recognitionReviewPreparedInputsV1)
	if (!k12.RecognitionCompactDAGExecutionPolicy(runtime.Header.ExecutionPolicy)) || prepared == nil {
		image, prompt, actual, err := recognitionReviewRequestV1(pagePNG, plan, request)
		return image, prompt, actual, false, err
	}
	scope, err := recognitionReviewPreparedScopeForV1(pagePNG, plan, runtime)
	if err != nil || scope != prepared.scope || !prepared.sealed {
		return nil, "", request, false, fmt.Errorf("%w: prepared review input does not match its frozen source", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	input, exists := prepared.inputs[request.PhysicalUnit]
	if !exists {
		// 恢复时已有持久授权，但内存中没有本次构图结果，继续使用原重建证明。
		image, prompt, actual, err := recognitionReviewRequestV1(pagePNG, plan, request)
		return image, prompt, actual, false, err
	}
	inputDigest, err := k12.RecognitionLayoutReviewBatchInputDigestV1(input.request)
	if err != nil || !reflect.DeepEqual(input.request, request) || inputDigest != request.InputDigest ||
		recognitionInitialReadDigestV1(input.image) != request.ImageDigest ||
		recognitionInitialReadDigestV1([]byte(input.prompt)) != request.PromptDigest {
		return nil, "", request, false, fmt.Errorf("%w: prepared review input digest drifted", k12.ErrRecognitionLayoutPlanV2Unauthorized)
	}
	actual := input.request
	actual.Members = append([]k12.RecognitionLayoutReviewMemberAuthorizationV1(nil), actual.Members...)
	return append([]byte(nil), input.image...), input.prompt, actual, true, nil
}
