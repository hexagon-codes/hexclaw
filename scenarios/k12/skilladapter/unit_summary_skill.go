package skilladapter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
	"github.com/hexagon-codes/hexclaw/scenarios/k12/usecase"
	"github.com/hexagon-codes/hexclaw/skill"
)

type UnitSummarySkill struct {
	coordinator *usecase.UnitSummaryCoordinator
}

func NewUnitSummarySkill(coordinator *usecase.UnitSummaryCoordinator) *UnitSummarySkill {
	return &UnitSummarySkill{coordinator: coordinator}
}
func (*UnitSummarySkill) Name() string      { return "k12_unit_summary" }
func (*UnitSummarySkill) Match(string) bool { return false }
func (*UnitSummarySkill) Description() string {
	return "Prepare or retrieve one grounded unit study material for the routed child, automatically saving the same-version PDF artifact."
}
func (*UnitSummarySkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("k12_unit_summary", "Use for a parent's request to summarize/revise a learning unit, open a saved unit material, or answer a material follow-up. Six subjects share one task. The original user text, routed child, request identity, current opened material and available source documents are resolved by the application. Do not pass owner/agent/session/source facts. Call intent=resume only when the current user is answering a previous material clarification; unrelated new tasks are not clarification answers. Do not call for an ordinary homework question. Saving/PDF happens automatically only on actual successful generation; never add confirmation or knowledge ingestion.",
		&llm.Schema{Type: "object", Properties: map[string]*llm.Schema{
			"subject": {Type: "string", Description: "Optional subject hint: math/chinese/english/science/information_technology/art. Explicit final text wins."},
			"intent":  {Type: "string", Description: "Optional semantic action: generate/revise/reuse/open/follow_up/resume/none; none means the final user request is not a unit-material task. Do not invent an update for ordinary questions."},
		}})
}

func (s *UnitSummarySkill) Execute(ctx context.Context, args map[string]any) (*skill.Result, error) {
	if s.coordinator == nil {
		return nil, fmt.Errorf("unit summary coordinator is unavailable")
	}
	inv := skill.CurrentInvocation(ctx)
	if inv.RoutedAgentName == "" || inv.SessionID == "" || inv.SourceMessageID == "" || inv.RequestID == "" || skill.SystemDispatchSource(ctx) != "" {
		return nil, fmt.Errorf("unit summary requires a routed interactive request")
	}
	owner := s.coordinator.Deps.TextbookOwnerID
	if owner == "" {
		owner = usecase.DefaultLocalOwnerScope
	}
	// 持久用户消息是最终正文依据，通用RAG包装和模型参数不能改写本轮任务。
	finalText, err := s.coordinator.Deps.Records.ReadUnitSummaryFollowup(ctx, inv.SessionID, inv.SourceMessageID)
	if err != nil {
		return nil, err
	}
	var hint struct {
		Subject string `json:"subject"`
		Model   string `json:"model"`
	}
	_ = json.Unmarshal([]byte(inv.ScenarioInput["k12_task_intent"]), &hint)
	subject, _ := args["subject"].(string)
	if subject == "" {
		subject = hint.Subject
	}
	intent, _ := args["intent"].(string)
	var active struct {
		RevisionID string `json:"revision_id"`
	}
	_ = json.Unmarshal([]byte(inv.ScenarioInput["k12_active_material"]), &active)
	var view usecase.UnitSummaryView
	err = nil
	if intent == "resume" {
		pending, e := s.coordinator.Deps.Records.ListUnitSummaryAttempts(ctx, owner, inv.RoutedAgentName, inv.SessionID, true)
		if e != nil {
			return nil, e
		}
		var waiting []k12.UnitSummaryAttempt
		for _, a := range pending {
			if a.State == "needs_input" {
				waiting = append(waiting, a)
			}
		}
		if len(waiting) != 1 {
			return nil, fmt.Errorf("unit summary clarification does not identify one pending task")
		}
		view, _, err = s.coordinator.ResumeAndWait(ctx, owner, inv.RoutedAgentName, waiting[0].AttemptID, usecase.UnitSummaryResumeRequest{ExpectedRevision: waiting[0].Revision, IdempotencyKey: inv.RequestID + ":unit-summary-resume", FollowupMessageID: inv.SourceMessageID})
	} else {
		view, _, err = s.coordinator.StartAndWait(ctx, usecase.UnitSummaryRequest{
			OwnerID: owner, AgentName: inv.RoutedAgentName, SessionID: inv.SessionID, SourceMessageID: inv.SourceMessageID,
			IdempotencyKey: inv.RequestID + ":unit-summary", FinalUserText: finalText,
			PromptInvocation: json.RawMessage(inv.ScenarioInput["k12_task_intent"]), ActiveRevisionID: active.RevisionID,
			Subject: subject, Intent: intent, ModelSnapshot: k12.GradingModelSnapshot{Provider: inv.ScenarioInput["provider"], Model: inv.ScenarioInput["model"]},
		})
	}
	if err != nil {
		return nil, err
	}
	if view.Job.State == "ignored" {
		// 非单元任务不附加产物身份；普通会话直接处理最终正文，不把解析审计显示为待生成资料。
		body, _ := json.Marshal(map[string]any{
			"handled": false, "intent": "none", "state": "ignored",
			"instruction": "The final user request is not a unit-material task. Continue the ordinary conversation and answer the actual request. Do not ask for a unit or learning material, or claim a unit document or PDF was created.",
		})
		return &skill.Result{Content: string(body)}, nil
	}
	ref := map[string]any{"kind": "unit_summary", "attempt_id": view.Job.AttemptID, "state": view.Job.State}
	if view.Document != nil {
		ref["document_id"] = view.Document.DocumentID
	}
	if view.Material != nil {
		ref["revision_id"] = view.Material.RevisionID
		ref["artifact_id"] = view.Material.Artifact.ArtifactID
		ref["content_digest"] = view.Material.ContentDigest
	}
	references, _ := json.Marshal([]map[string]any{ref})
	result := map[string]any{"state": view.Job.State, "delivery_complete": view.Delivery.Complete, "material_reference": ref}
	if view.Material != nil {
		result["title"] = view.Material.Title
		result["filename"] = view.Material.Filename
	}
	if view.Job.Clarification != "" {
		result["clarification"] = view.Job.Clarification
	}
	if view.Job.FailureDetail != "" {
		result["error"] = view.Job.FailureDetail
	}
	if !view.Delivery.Complete {
		result["instruction"] = "Report the actual pending, clarification or failed state; do not claim the material or PDF is saved."
	} else {
		// 保存回执不包含实测页数，目标版式不能被主聊天冒称为已核验的一页结果。
		result["instruction"] = "Confirm the actual saved file identity and delivery state. This receipt does not contain a measured PDF page count. Do not infer or assert one page or any specific page count from the requested layout, brief format, filename or render contract. Describe the compact layout as a goal unless actual measured page-count evidence is available."
	}
	body, _ := json.Marshal(result)
	return &skill.Result{Content: strings.TrimSpace(string(body)), Metadata: map[string]string{"artifacts": string(references)}}, nil
}
