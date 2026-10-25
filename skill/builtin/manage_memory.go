package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/memory"
	"github.com/hexagon-codes/hexclaw/skill"
)

// ManageMemorySkill 是长期记忆的统一写入工具（增量 G：采纳 Claude Code 式「主模型随手判断」）。
//
// 双重职责：①主模型在回话中**主动**判断到值得长期记的用户事实时顺手存（替代每轮即时后台抽取，
// 零额外 LLM 调用、内容感知、按需）②用户**显式**要求记住/更新/忘掉/置顶时执行。
// 写经 memory 单写器（UpsertStructuredEntryForRole / UpdateEntry / DeleteEntry / SetPinned），
// 与画像蒸馏/反思共用同一原子落盘 + 去重/时序取代纪律；add/update 复用同一 PII 守卫（凭证不入记忆）。
// 作用域 _global（用户级跨角色）：记忆管理面向用户身份/偏好/规则；角色私有事实由反思整合。
type ManageMemorySkill struct {
	fm *memory.FileMemory
}

func NewManageMemorySkill(fm *memory.FileMemory) *ManageMemorySkill {
	return &ManageMemorySkill{fm: fm}
}

func (s *ManageMemorySkill) Name() string { return "manage_memory" }

func (s *ManageMemorySkill) Description() string {
	return "Curate the user's long-term memory: add / update / remove / pin / unpin entries."
}

// Match 始终走 LLM tool-calling，无关键字快路径。
func (s *ManageMemorySkill) Match(string) bool { return false }

func (s *ManageMemorySkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("manage_memory",
		"Persist durable facts about the user to long-term memory. Call this PROACTIVELY (without "+
			"being asked) the moment you learn a lasting fact about the user — their identity, role, "+
			"preferences, residence, timezone, allergies, or long-term conventions/constraints — and "+
			"ALSO whenever they explicitly ask to remember / update / forget / prioritize something "+
			"(e.g. \"记住我喜欢…\", \"忘掉…\", \"以后都…\"). Do NOT save transient tasks, one-off "+
			"questions, or this conversation's working details, and NEVER store passwords, API keys, or "+
			"other credentials. For changeable attributes (居住地/时区/职业…) pass `subject` so a new "+
			"value supersedes the old one. Use persona scope for a convention specific to the mounted persona, "+
			"agent scope for this assistant's working rules, and subject/project scope for their durable facts. "+
			"Global scope is for reusable user facts and preferences. Scope identities come from the current context; never invent IDs.",
		&llm.Schema{
			Type: "object",
			Properties: map[string]*llm.Schema{
				"action": {
					Type:        "string",
					Description: "Operation",
					Enum:        []any{"add", "update", "remove", "pin", "unpin"},
				},
				"content": {
					Type:        "string",
					Description: "Memory text (required for add/update)",
				},
				"scope": {
					Type: "string", Enum: []any{"global", "agent", "persona", "subject", "project"},
					Description: "Applicability of a new memory; defaults to global. Other scopes bind to the current context.",
				},
				"type": {
					Type:        "string",
					Description: "Memory type for add (default fact). identity/preference/instruction/rule are kept resident.",
					Enum:        []any{"identity", "preference", "instruction", "rule", "fact", "context"},
				},
				"subject": {
					Type:        "string",
					Description: "Attribute name for changeable facts (e.g. 居住地, 时区) — enables time-aware supersede on add",
				},
				"target": {
					Type:        "string",
					Description: "For update/remove/pin/unpin: an entry id (m-N) or a distinctive substring of the entry's content",
				},
				"attribute": {Type: "string", Description: "For a named person's current fact: grade, current_residence, or interest. Leave unset when uncertain."},
				"value":     {Type: "string", Description: "The explicit current attribute value; interests are multiple independent values."},
			},
			Required: []string{"action"},
		})
}

func (s *ManageMemorySkill) Execute(ctx context.Context, args map[string]any) (*skill.Result, error) {
	if s.fm == nil {
		return nil, fmt.Errorf("file memory is not enabled")
	}
	action := strings.ToLower(strings.TrimSpace(memArgString(args, "action")))
	switch action {
	case "add":
		return s.add(ctx, args)
	case "update":
		return s.update(ctx, args)
	case "remove":
		return s.remove(ctx, args)
	case "pin":
		return s.setPin(ctx, args, true)
	case "unpin":
		return s.setPin(ctx, args, false)
	default:
		return nil, fmt.Errorf("unsupported action %q (available: add/update/remove/pin/unpin)", action)
	}
}

func (s *ManageMemorySkill) add(ctx context.Context, args map[string]any) (*skill.Result, error) {
	content := strings.TrimSpace(memArgString(args, "content"))
	if content == "" {
		return nil, fmt.Errorf("add requires content")
	}
	if memory.LooksSensitive(content) {
		return &skill.Result{Content: "出于安全考虑未记忆：内容疑似含密码 / 密钥等凭证（凭证请走连接中心，不进记忆）。"}, nil
	}
	memType := strings.TrimSpace(memArgString(args, "type"))
	if memType == "" {
		memType = "fact"
	}
	subject := strings.TrimSpace(memArgString(args, "subject"))
	current := memory.ScopeFromContext(ctx)
	var scope memory.Scope
	switch strings.TrimSpace(memArgString(args, "scope")) {
	case "", "global":
	case "agent":
		if current.Agent == "" {
			return nil, fmt.Errorf("Current agent identity is unavailable")
		}
		scope.Agent = current.Agent
	case "persona":
		if len(current.Skills) == 0 {
			return nil, fmt.Errorf("Current persona identity is unavailable")
		}
		scope.Skills = current.Skills
	case "subject":
		if current.SubjectID == "" {
			return nil, fmt.Errorf("Current subject identity is unavailable")
		}
		scope.SubjectID = current.SubjectID
	case "project":
		if current.ProjectID == "" {
			return nil, fmt.Errorf("Current project identity is unavailable")
		}
		scope.ProjectID = current.ProjectID
	default:
		return nil, fmt.Errorf("Unsupported memory scope")
	}
	act, err := s.fm.UpsertScopedFactForRole(content, memType, "ai_managed", "", subject, memArgString(args, "attribute"), memArgString(args, "value"), scope)
	if err != nil {
		return nil, err
	}
	verb := map[string]string{"insert": "已记住", "update": "已更新", "supersede": "已更新（取代旧值）", "discard": "已存在，无需重复记"}[act]
	if verb == "" {
		verb = "已处理"
	}
	return &skill.Result{Content: verb + "：" + content, Metadata: map[string]string{"action": act}}, nil
}

func (s *ManageMemorySkill) update(ctx context.Context, args map[string]any) (*skill.Result, error) {
	content := strings.TrimSpace(memArgString(args, "content"))
	if content == "" {
		return nil, fmt.Errorf("update requires content")
	}
	if memory.LooksSensitive(content) {
		return &skill.Result{Content: "出于安全考虑未更新：新内容疑似含凭证。"}, nil
	}
	id, found, err := s.resolveTarget(ctx, args)
	if err != nil {
		return nil, err
	}
	if !found {
		return &skill.Result{Content: "未找到要更新的记忆条目（请提供更准确的内容片段或条目 id）。"}, nil
	}
	if err := s.fm.UpdateEntry(id, content); err != nil {
		return nil, err
	}
	return &skill.Result{Content: "已更新记忆：" + content, Metadata: map[string]string{"id": id}}, nil
}

func (s *ManageMemorySkill) remove(ctx context.Context, args map[string]any) (*skill.Result, error) {
	id, found, err := s.resolveTarget(ctx, args)
	if err != nil {
		return nil, err
	}
	if !found {
		return &skill.Result{Content: "未找到要删除的记忆条目。"}, nil
	}
	if err := s.fm.DeleteEntry(id); err != nil {
		return nil, err
	}
	return &skill.Result{Content: "已删除该条记忆。", Metadata: map[string]string{"id": id}}, nil
}

func (s *ManageMemorySkill) setPin(ctx context.Context, args map[string]any, pinned bool) (*skill.Result, error) {
	id, found, err := s.resolveTarget(ctx, args)
	if err != nil {
		return nil, err
	}
	if !found {
		return &skill.Result{Content: "未找到要置顶/取消置顶的记忆条目。"}, nil
	}
	if err := s.fm.SetPinned(id, pinned); err != nil {
		return nil, err
	}
	if pinned {
		return &skill.Result{Content: "已置顶该条记忆（强制常驻）。", Metadata: map[string]string{"id": id}}, nil
	}
	return &skill.Result{Content: "已取消置顶。", Metadata: map[string]string{"id": id}}, nil
}

// resolveTarget 把 target 参数解析为条目 id：精确 id（m-N）直用，否则按内容子串唯一匹配活跃条目。
func (s *ManageMemorySkill) resolveTarget(ctx context.Context, args map[string]any) (id string, found bool, err error) {
	target := strings.TrimSpace(memArgString(args, "target"))
	if target == "" {
		return "", false, fmt.Errorf("this action requires `target` (entry id or content substring)")
	}
	entries := memory.EntriesForScope(s.fm.ParseEntries(), memory.ScopeFromContext(ctx))
	// 1) 精确 id 命中。
	for _, e := range entries {
		if e.ID == target {
			return e.ID, true, nil
		}
	}
	// 2) 内容子串匹配（唯一才接受，避免误删）。
	var matchID string
	hits := 0
	for _, e := range entries {
		if strings.Contains(e.Content, target) {
			matchID = e.ID
			hits++
		}
	}
	if hits == 1 {
		return matchID, true, nil
	}
	if hits > 1 {
		return "", false, fmt.Errorf("target %q 匹配到多条记忆，请提供更精确的片段或条目 id", target)
	}
	return "", false, nil
}

// memArgString 从 args 取字符串参数（容错非字符串值）。
func memArgString(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
		if v != nil {
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}
