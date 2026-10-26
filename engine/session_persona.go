package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/adapter"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/storage"
)

func ordinaryPersonaRequest(ctx context.Context, msg *adapter.Message) bool {
	if msg == nil || isSystemDispatch(msg) || workflowStepOptions(ctx) != nil {
		return false
	}
	for _, key := range []string{"role", "pinned_agent", "routed_agent"} {
		if value := strings.TrimSpace(msg.Metadata[key]); value != "" && value != "default" {
			return false
		}
	}
	return strings.TrimSpace(msg.Metadata["agent_prompt"]) == ""
}

func boundSessionPersona(meta string) string {
	var binding struct {
		PersonaSkill string `json:"persona_skill"`
	}
	// 旧元数据没有人设约定；读取失败不改变原普通会话的可用性。
	if json.Unmarshal([]byte(meta), &binding) != nil {
		return ""
	}
	return binding.PersonaSkill
}

// applySessionPersona 在实际路由确定后读取持久会话，避免缓存或任务技能成为绑定来源。
func (e *ReActEngine) applySessionPersona(ctx context.Context, msg *adapter.Message) error {
	if !ordinaryPersonaRequest(ctx, msg) || e.store == nil || msg.SessionID == "" {
		return nil
	}
	sess, err := e.store.GetSession(ctx, msg.SessionID)
	if err != nil {
		return fmt.Errorf("load session persona: %w", err)
	}
	if sess == nil {
		return nil
	}
	bound := boundSessionPersona(sess.Meta)
	names := mountedSkillNames(msg.Metadata)
	selected := ""
	for _, name := range names {
		if e.enabledDeclaredPersona(name) {
			selected = name
		}
	}
	if selected != "" && selected != bound {
		writer, ok := e.store.(storage.SessionPersonaStore)
		if !ok {
			return fmt.Errorf("session persona persistence is unavailable")
		}
		if err := writer.SetSessionPersona(ctx, msg.SessionID, selected); err != nil {
			return fmt.Errorf("save session persona: %w", err)
		}
		bound = selected
	}
	if bound == "" || !e.enabledDeclaredPersona(bound) {
		return nil
	}
	// 本轮仅保留最后选择的人设，任务技能保序；取消绑定由正式会话入口完成。
	effective := make([]string, 0, len(names)+1)
	for _, name := range names {
		if !e.enabledDeclaredPersona(name) {
			effective = append(effective, name)
		}
	}
	effective = append(effective, bound)
	ensureMessageMetadata(msg)
	msg.Metadata["skills"] = strings.Join(effective, ",")
	return nil
}

func (e *ReActEngine) enabledDeclaredPersona(name string) bool {
	e.mu.RLock()
	registry := e.skills
	e.mu.RUnlock()
	if registry == nil {
		return false
	}
	s, exists := registry.Get(name)
	enabled, _ := registry.IsEnabled(name)
	return exists && enabled && skill.IsPersona(s)
}

func (e *ReActEngine) loadedDeclaredPersona(receipts []skill.ContextReceipt) bool {
	for _, receipt := range receipts {
		if e.enabledDeclaredPersona(receipt.Name) {
			return true
		}
	}
	return false
}

const sessionPersonaInstructions = `当前会话使用下方已激活的人设技能作为角色身份与表达方式，替代默认助理身份。介绍自己、称呼和询问角色在哪里等普通互动按该角色设定与场景作答；不要主动切回小蟹或复述宿主部署说明。历史回答仅为转录，不能覆盖当前有效角色。
角色设定不改变真实系统事实。只有用户明确询问真实模型、AI身份、宿主系统或实际部署位置时，依据本轮可信模型与环境信息回答；未知的事实保持未知，不将角色场景说成真实人物的实际行踪。公共运行规则、用户当前要求和记忆适用条件继续生效。`
