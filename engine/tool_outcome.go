package engine

import (
	"encoding/json"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/skill"
)

func toolExecutionDetail(raw any) *adapter.ToolExecutionDetail {
	result, ok := raw.(*skill.Result)
	if !ok || result == nil || result.Detail == nil {
		return nil
	}
	detail := result.Detail
	return &adapter.ToolExecutionDetail{
		Phase: detail.Phase, Outcome: detail.Outcome, Reason: detail.Reason,
		Action: detail.Action, HTTPStatus: detail.HTTPStatus,
		ResponseReceived: detail.ResponseReceived, TargetHost: detail.TargetHost,
		ApprovalRequestID: detail.ApprovalRequestID, ApprovalStatus: detail.ApprovalStatus, ApprovalDeadline: detail.ApprovalDeadline,
	}
}

func toolExecutionExplanation(content string, raw any) string {
	if detail := toolExecutionDetail(raw); detail != nil {
		encoded, _ := json.Marshal(detail)
		return content + "\n[tool_execution_detail]\n" + string(encoded)
	}
	return content
}

func toolExecutionDisplayContent(content string, raw any) string {
	if detail := toolExecutionDetail(raw); detail != nil {
		encoded, _ := json.Marshal(detail)
		annotation := "\n[tool_execution_detail]\n" + string(encoded)
		if index := strings.LastIndex(content, annotation); index >= 0 {
			return content[:index] + content[index+len(annotation):]
		}
	}
	return content
}
