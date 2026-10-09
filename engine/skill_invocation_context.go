package engine

import (
	"context"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/skill"
)

// 两条引擎路径共用请求身份桥；场景输入只是提示，领域用例仍核验真实归属和来源。
func withSkillInvocationContext(ctx context.Context, msg *adapter.Message, sessionID, provider, model string) context.Context {
	requestID := strings.TrimSpace(messageRequestID(msg))
	if requestID == "" {
		requestID = strings.TrimSpace(msg.ID)
	}
	messageID := strings.TrimSpace(msg.Metadata["source_message_id"])
	if messageID == "" {
		messageID = requestID
	}
	if messageID == "" {
		messageID = strings.TrimSpace(msg.ID)
	}
	if requestID == "" {
		requestID = messageID
	}
	input := make(map[string]string)
	for _, key := range []string{"k12_task_intent", "k12_active_material", "k12_provided_materials", "documents", "provider", "model"} {
		if value := msg.Metadata[key]; value != "" {
			input[key] = value
		}
	}
	input["provider"], input["model"] = provider, model
	return skill.WithInvocationContext(ctx, skill.InvocationContext{
		AuthenticatedUserID: skill.AuthenticatedUserID(ctx),
		RoutedAgentName:     skill.RoutedAgentName(ctx), RequestID: requestID,
		SourceMessageID: messageID, SessionID: sessionID,
		OriginalUserText: skill.OriginalUserText(ctx), ScenarioInput: input,
	})
}
