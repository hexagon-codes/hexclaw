package usecase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	k12storage "github.com/hexagon-codes/hexclaw/scenarios/k12/storage"
)

type imageTaskClassificationResponseParser interface {
	ParseImageTaskClassificationResponse(string, bool) (ImageTaskClassification, error)
}

func (c *ImageTaskCoordinator) classificationResponseRecorder(agentName, invocationID string) func(context.Context, string) error {
	return func(ctx context.Context, raw string) error {
		return c.Records.SaveImageTaskClassificationResponse(ctx, agentName, invocationID, raw)
	}
}

// ReparseClassification 仅复验当前失败调用的已知回复，不发送模型或推进下游模型任务。
func (c *ImageTaskCoordinator) ReparseClassification(ctx context.Context, agentName, dispatchID string, expectedVersion int) (ImageTaskView, error) {
	if err := c.validate(); err != nil {
		return ImageTaskView{}, err
	}
	agentName, dispatchID = strings.TrimSpace(agentName), strings.TrimSpace(dispatchID)
	dispatch, err := c.Records.GetImageTaskDispatch(ctx, agentName, dispatchID)
	if err != nil {
		return ImageTaskView{}, err
	}
	invocation, err := c.Records.GetLatestClassificationInvocation(ctx, agentName, dispatchID)
	if err != nil {
		return ImageTaskView{}, err
	}
	if invocation.Status == k12.ImageTaskInvocationSucceeded {
		var prior k12storage.ImageTaskRoutingDecision
		if json.Unmarshal([]byte(invocation.ResultJSON), &prior) == nil && prior.OriginalParseFailure != nil &&
			prior.OriginalParseFailure.DispatchVersion == expectedVersion {
			return c.projectTarget(ctx, dispatch)
		}
	}
	if dispatch.Version != expectedVersion {
		return ImageTaskView{}, k12storage.ErrImageTaskVersionConflict
	}
	if dispatch.Status != k12.ImageTaskStatusFailed || !dispatch.RetrySafe || dispatch.TargetObjectID != "" ||
		invocation.Status != k12.ImageTaskInvocationFailed || !invocation.RetrySafe {
		return ImageTaskView{}, k12storage.ErrImageTaskInvalidState
	}
	response, present, err := k12storage.ReadImageTaskClassificationResponse(invocation)
	if err != nil {
		return ImageTaskView{}, err
	}
	if !present {
		return ImageTaskView{}, fmt.Errorf("%w: classification response is unavailable", k12storage.ErrImageTaskInvalidState)
	}
	parser, ok := c.Classifier.(imageTaskClassificationResponseParser)
	if !ok {
		return ImageTaskView{}, fmt.Errorf("classification response parser unavailable")
	}
	classified, err := parser.ParseImageTaskClassificationResponse(response.RawResponse, invocation.RouteSnapshot.PromptVersion == "creative-work-classification-ocr-v1")
	if err != nil {
		return ImageTaskView{}, err
	}
	routed, target, err := c.Records.CommitImageTaskRouting(ctx, agentName, dispatchID, expectedVersion, k12storage.ImageTaskRoutingDecision{
		Intent: classified.Intent, Evidence: classified.IntentEvidence, Confidence: classified.Confidence,
		ConfirmationCandidates: classified.ConfirmationCandidates, WorkTitleCandidate: classified.WorkTitleCandidate,
		TaskRequirementCandidate: classified.TaskRequirementCandidate, InvocationResultDigest: digestJSON(classified),
		WritingOCR: classificationOCREvidence(classified), ReparseInvocationID: invocation.InvocationID,
	})
	if err != nil {
		return ImageTaskView{}, err
	}
	return ImageTaskView{Dispatch: routed, Homework: target.HomeworkSubmission, Creative: target.CreativeIntake}, nil
}
