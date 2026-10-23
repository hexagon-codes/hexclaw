package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/ai-core/llm"
	imagegen "github.com/hexagon-codes/ai-core/media/image"
	videogen "github.com/hexagon-codes/ai-core/media/video"
	"github.com/hexagon-codes/ai-core/media/voice"
	"github.com/hexagon-codes/ai-core/media/voicechat"
	"github.com/hexagon-codes/hexclaw/internal/mediaflow"
	"github.com/hexagon-codes/hexclaw/skill"
)

func NewMediaFlowGenerateSkill(flow *mediaflow.Service) *MediaGenerateSkill {
	return &MediaGenerateSkill{flow: flow}
}

func (s *MediaGenerateSkill) mediaFlowDefinition() llm.ToolDefinition {
	var kinds, actions []any
	var routes []string
	seenKinds, seenActions := map[string]bool{}, map[string]bool{}
	if snapshot := s.flow.Router().Current(); snapshot != nil {
		for _, route := range snapshot.Routes {
			if route.Model != "" {
				routes = append(routes, fmt.Sprintf("%s/%s: provider=%q, model=%q", route.Kind, route.Action, route.ProviderKey, route.Model))
			} else {
				routes = append(routes, fmt.Sprintf("%s/%s: use the configured voice service", route.Kind, route.Action))
			}
			if !seenKinds[route.Kind] {
				kinds = append(kinds, route.Kind)
				seenKinds[route.Kind] = true
			}
			if !seenActions[route.Action] {
				actions = append(actions, route.Action)
				seenActions[route.Action] = true
			}
		}
	}
	actions = append(actions, "read", "resend", "resume", "cancel")
	return llm.NewToolDefinition("media_generate", "Use configured media models to generate actual images, videos or speech. Read, resume or resend a previous operation by its operation_id; resending does not generate again. Returned assets are durable files, not a prompt description. Available routes: "+strings.Join(routes, "; "), &llm.Schema{Type: "object", Properties: map[string]*llm.Schema{
		"kind": {Type: "string", Enum: kinds}, "action": {Type: "string", Enum: actions}, "prompt": {Type: "string", Description: "Content to generate or speak"},
		"provider": {Type: "string", Description: "Exact configured provider instance or key"}, "model": {Type: "string"}, "operation_id": {Type: "string", Description: "Existing owned operation for read/resend/resume/cancel"},
		"size": {Type: "string"}, "n": {Type: "integer"}, "style": {Type: "string"}, "quality": {Type: "string"}, "with_audio": {Type: "boolean"}, "duration": {Type: "integer"}, "voice": {Type: "string"}, "format": {Type: "string"},
	}})
}

func (s *MediaGenerateSkill) executeMediaFlow(ctx context.Context, args map[string]any) (*skill.Result, error) {
	snapshot := s.flow.Router().Current()
	if snapshot == nil || !snapshot.ToolEnabled {
		return nil, fmt.Errorf("media generation tool is disabled")
	}
	inv := skill.CurrentInvocation(ctx)
	if inv.AuthenticatedUserID == "" {
		return nil, fmt.Errorf("authenticated media origin is required")
	}
	action := firstStringArg(args, "action")
	id := firstStringArg(args, "operation_id")
	if action == "read" || action == "resend" || action == "resume" || action == "cancel" {
		if id == "" {
			return nil, fmt.Errorf("operation_id is required")
		}
		var op *mediaflow.Operation
		var err error
		restoredAssets := false
		switch action {
		case "resume":
			if err := mediaflow.RetainRequest(ctx); err != nil {
				return nil, err
			}
			op, err = s.flow.Read(ctx, inv.AuthenticatedUserID, id)
			if err != nil {
				return nil, err
			}
			restoredAssets = op.SaveState != "ready"
			op, err = s.flow.Resume(ctx, inv.AuthenticatedUserID, id)
			if err == nil {
				op, err = s.flow.Wait(ctx, inv.AuthenticatedUserID, id, false)
			}
		case "cancel":
			op, err = s.flow.Cancel(ctx, inv.AuthenticatedUserID, id)
		default:
			op, err = s.flow.Read(ctx, inv.AuthenticatedUserID, id)
		}
		if err != nil {
			return mediaFlowSkillFailure(op, err)
		}
		if action == "resume" && !op.CancelRequested && op.SaveState != "ready" {
			return mediaFlowSkillFailure(op, fmt.Errorf("media operation %s: recovery remains incomplete (%s)", op.ID, op.ErrorCode))
		}
		result := mediaFlowSkillResult(op, action == "resend" || restoredAssets && !op.CancelRequested)
		deliveries, err := s.flow.DeliveryStatuses(ctx, inv.AuthenticatedUserID, id)
		if err != nil {
			return result, fmt.Errorf("media delivery state is unavailable")
		}
		view := result.Data.(map[string]any)
		view["deliveries"] = deliveries
		encodedView, _ := json.Marshal(view)
		result.Content = string(encodedView)
		if action == "resend" {
			encoded, _ := json.Marshal([]string{op.ID})
			result.Metadata["media_resend_operations"] = string(encoded)
		}
		return result, nil
	}
	kind := firstStringArg(args, "kind")
	if kind == "" {
		kind = "image"
	}
	originKind := "conversation"
	ordinal := inv.OperationOrdinal
	if ordinal == "" {
		ordinal = inv.ToolCallID
	}
	if dispatch := skill.SystemDispatchSource(ctx); dispatch != "" {
		originKind = dispatch
	}
	req := mediaflow.Request{Origin: mediaflow.Origin{Owner: inv.AuthenticatedUserID, Kind: originKind, RequestID: inv.RequestID, Ordinal: ordinal, ToolCallID: inv.ToolCallID, SessionID: inv.SessionID, RequestDigest: inv.RequestDigest, AgentName: inv.RoutedAgentName, Platform: inv.Platform, InstanceID: inv.PlatformInstanceID, ChatID: inv.ChatID}, Kind: kind, Action: action, Provider: firstStringArg(args, "provider"), Model: firstStringArg(args, "model"), PreferredProvider: inv.ScenarioInput["provider"]}
	prompt := firstStringArg(args, "prompt")
	if kind == "image" || kind == "video" {
		req.Selection = s.flow.Router().UserSelection(inv.OriginalUserText, kind)
	}
	switch kind {
	case "image":
		req.Image = &imagegen.Request{Prompt: prompt, Size: firstStringArg(args, "size"), N: intArg(args, "n", 1), Style: firstStringArg(args, "style"), Quality: firstStringArg(args, "quality")}
	case "video":
		withAudio, _ := args["with_audio"].(bool)
		req.Video = &videogen.Request{Prompt: prompt, Size: firstStringArg(args, "size"), Duration: intArg(args, "duration", 0), WithAudio: withAudio}
	case "audio":
		if action == "chat" {
			req.Chat = &voicechat.Request{Text: prompt, Voice: firstStringArg(args, "voice"), Format: firstStringArg(args, "format")}
		} else {
			req.Speech = &mediaflow.SpeechRequest{Text: prompt, Options: voice.SynthesizeOptions{Voice: firstStringArg(args, "voice"), Format: voice.AudioFormat(firstStringArg(args, "format"))}}
		}
	default:
		return nil, fmt.Errorf("unsupported media kind")
	}
	if err := mediaflow.RetainRequest(ctx); err != nil {
		return nil, err
	}
	op, err := s.flow.Begin(ctx, req)
	if err != nil {
		return mediaFlowSkillFailure(op, err)
	}
	op, err = s.flow.Wait(ctx, inv.AuthenticatedUserID, op.ID, false)
	if err != nil {
		return mediaFlowSkillResult(op, true), err
	}
	if op.CancelRequested {
		mediaflow.CancelRequest(ctx)
		return mediaFlowSkillResult(op, false), context.Canceled
	}
	result := mediaFlowSkillResult(op, true)
	if op.SaveState != "ready" {
		return mediaFlowSkillFailure(op, fmt.Errorf("media operation %s: %s", op.ID, op.ErrorCode))
	}
	return result, nil
}

func mediaFlowSkillFailure(op *mediaflow.Operation, err error) (*skill.Result, error) {
	if op == nil {
		return nil, err
	}
	result := mediaFlowSkillResult(op, true)
	outcome := "failed"
	if op.GenerationState == "unknown" {
		outcome = "unknown"
	}
	detail := skill.ToolExecutionDetail{Phase: "media", Action: op.Request.Action, Outcome: outcome, Reason: op.ErrorCode}
	result.Detail = &detail
	return result, &skill.ExecutionError{Detail: detail, Err: err}
}

func mediaFlowSkillResult(op *mediaflow.Operation, includeAssets bool) *skill.Result {
	if op == nil {
		return &skill.Result{Content: "Media result is not available"}
	}
	view := map[string]any{"operation_id": op.ID, "provider_instance_id": op.Route.ProviderID, "model": op.Route.Model, "generation_state": op.GenerationState, "save_state": op.SaveState, "error_code": op.ErrorCode, "error": op.Error}
	view["actual_provider_instance_id"] = op.ActualProviderID
	view["attempts"] = op.Attempts
	view["selection"] = op.Request.Selection
	metadata := map[string]string{}
	if includeAssets {
		encoded, _ := json.Marshal([]string{op.ID})
		metadata["media_operations"] = string(encoded)
	}
	if includeAssets && len(op.Assets) > 0 {
		view["assets"] = op.Assets
		encoded, _ := json.Marshal(op.Assets)
		metadata["artifacts"] = string(encoded)
	}
	encoded, _ := json.Marshal(view)
	return &skill.Result{Content: string(encoded), Data: view, Metadata: metadata}
}
