package memory

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
)

// Scope 限定同一用户记忆空间内的适用范围；空值继承既有全局语义。
type Scope struct {
	Agent     string   `json:"agent,omitempty"`
	Skills    []string `json:"skills,omitempty"`
	SubjectID string   `json:"subject_id,omitempty"`
	ProjectID string   `json:"project_id,omitempty"`
}

func (s Scope) IsZero() bool {
	return s.Agent == "" && len(s.Skills) == 0 && s.SubjectID == "" && s.ProjectID == ""
}

// Normalized 返回独立的规范值，避免条目与当前上下文共享可变技能切片。
func (s Scope) Normalized() Scope {
	s.Agent = strings.TrimSpace(s.Agent)
	s.SubjectID = strings.TrimSpace(s.SubjectID)
	s.ProjectID = strings.TrimSpace(s.ProjectID)
	var skills []string
	for _, name := range s.Skills {
		if name = strings.TrimSpace(name); name != "" {
			skills = append(skills, name)
		}
	}
	slices.Sort(skills)
	s.Skills = slices.Compact(skills)
	return s
}

// Matches 先核对归属，再由召回内核判断相关性；置顶不绕过此范围。
func (s Scope) Matches(current Scope) bool {
	s, current = s.Normalized(), current.Normalized()
	if s.Agent != "" && s.Agent != current.Agent || s.SubjectID != "" && s.SubjectID != current.SubjectID || s.ProjectID != "" && s.ProjectID != current.ProjectID {
		return false
	}
	if len(s.Skills) == 0 {
		return true
	}
	for _, name := range s.Skills {
		if slices.Contains(current.Skills, name) {
			return true
		}
	}
	return false
}

func (s Scope) Equal(other Scope) bool {
	s, other = s.Normalized(), other.Normalized()
	return s.Agent == other.Agent && s.SubjectID == other.SubjectID && s.ProjectID == other.ProjectID && slices.Equal(s.Skills, other.Skills)
}

// Key 供同一存储内的整理分组使用，空范围维持空键兼容。
func (s Scope) Key() string {
	s = s.Normalized()
	if s.IsZero() {
		return ""
	}
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

// EntriesForScope 只筛选适用范围，不改变条目正文、身份和有效期。
func EntriesForScope(entries []MemoryEntry, current Scope) []MemoryEntry {
	selected := make([]MemoryEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Scope.Matches(current) {
			selected = append(selected, entry)
		}
	}
	return selected
}

type scopeKey struct{}

func WithScope(ctx context.Context, scope Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope.Normalized())
}

func ScopeFromContext(ctx context.Context) Scope {
	scope, _ := ctx.Value(scopeKey{}).(Scope)
	return scope
}
