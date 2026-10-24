package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/hexclaw/skill/hub"
	"github.com/hexagon-codes/hexclaw/skill/lifecycle"
)

// SkillInstallerSkill allows the LLM to search, install, remove, and list Skills from Hub.
type SkillInstallerSkill struct {
	hub       *hub.Hub
	lifecycle *lifecycle.Service
}

// NewSkillInstallerSkill creates a new SkillInstallerSkill.
func NewSkillInstallerSkill(h *hub.Hub) *SkillInstallerSkill {
	return &SkillInstallerSkill{hub: h}
}

// NewSkillLifecycleSkill 让会话工具与现有 HTTP API 使用同一安装和热加载用例。
func NewSkillLifecycleSkill(service *lifecycle.Service) *SkillInstallerSkill {
	return &SkillInstallerSkill{hub: service.Hub(), lifecycle: service}
}

func skillLifecycleResult(message string, value any) (*skill.Result, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &skill.Result{Content: message + "\n" + string(data), Data: value}, nil
}

func (s *SkillInstallerSkill) Name() string { return "manage_skill" }
func (s *SkillInstallerSkill) Description() string {
	return "Search, install, or remove skills from HexClaw Hub"
}
func (s *SkillInstallerSkill) Match(_ string) bool { return false }

func (s *SkillInstallerSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("manage_skill",
		"Discover installed skills or catalog skill packages. Install, enable, disable, remove or refresh through the same application lifecycle. resources lists exact installed package files; load reads SKILL.md without executing the skill. Use view=catalog for installable entries or view=installed for current state.",
		&llm.Schema{
			Type: "object",
			Properties: map[string]*llm.Schema{
				"action":  {Type: "string", Enum: []any{"search", "install", "remove", "list", "enable", "disable", "refresh", "resources", "load"}},
				"keyword": {Type: "string", Description: "Search keyword or skill name (required for search/install/remove)"},
				"view":    {Type: "string", Enum: []any{"installed", "catalog"}},
			},
			Required: []string{"action"},
		})
}

func (s *SkillInstallerSkill) Execute(ctx context.Context, args map[string]any) (*skill.Result, error) {
	action, _ := args["action"].(string)
	keyword, _ := args["keyword"].(string)
	if s.lifecycle != nil {
		view, _ := args["view"].(string)
		switch action {
		case "search":
			if keyword == "" {
				return nil, fmt.Errorf("keyword is required for search")
			}
			var found []hub.SkillMeta
			for _, entry := range s.lifecycle.Catalog() {
				if strings.Contains(strings.ToLower(entry.Name+" "+entry.Description+" "+strings.Join(entry.Tags, " ")), strings.ToLower(keyword)) {
					found = append(found, entry)
				}
			}
			return &skill.Result{Content: formatSkillSearchResults(found)}, nil
		case "list":
			if view != "catalog" {
				entries := s.lifecycle.List()
				return skillLifecycleResult(fmt.Sprintf("Installed skills (%d)", len(entries)), entries)
			}
			return &skill.Result{Content: formatSkillSearchResults(s.lifecycle.Catalog())}, nil
		case "install":
			if strings.TrimSpace(keyword) == "" {
				return nil, fmt.Errorf("keyword is required for install")
			}
			installed, err := s.lifecycle.InstallFromCatalog(ctx, keyword)
			if err != nil {
				return nil, err
			}
			state, err := s.lifecycle.State(keyword)
			if err != nil {
				return nil, err
			}
			return skillLifecycleResult("Skill installed and runtime state read back", map[string]any{"source": installed.Source, "state": state})
		case "remove":
			if err := s.lifecycle.Uninstall(ctx, keyword); err != nil {
				return nil, err
			}
			return &skill.Result{Content: "Skill removed and runtime refreshed", Data: map[string]any{"name": keyword, "installed": false}}, nil
		case "enable", "disable":
			state, err := s.lifecycle.SetEnabled(ctx, keyword, action == "enable")
			if err != nil {
				return nil, err
			}
			return skillLifecycleResult("Skill status persisted and runtime refreshed", state)
		case "refresh":
			if err := s.lifecycle.Refresh(ctx); err != nil {
				return nil, err
			}
			entries := s.lifecycle.List()
			return skillLifecycleResult("Installed skill state refreshed", entries)
		case "resources":
			resources, err := s.lifecycle.Resources(keyword)
			if err != nil {
				return nil, err
			}
			return skillLifecycleResult("Installed package resources", resources)
		case "load":
			resource, err := s.lifecycle.ReadResource(keyword, "SKILL.md")
			if err != nil {
				return nil, err
			}
			return &skill.Result{Content: string(resource.Content), Data: resource}, nil
		}
	}
	if s.hub == nil {
		return nil, fmt.Errorf("skill Hub is unavailable")
	}
	s.hub.EnsureCatalog()

	switch action {
	case "search":
		if keyword == "" {
			return nil, fmt.Errorf("keyword is required for search")
		}
		var results []hub.SkillMeta
		for _, entry := range s.hub.Search(keyword) {
			if !strings.EqualFold(entry.Type, "mcp") {
				results = append(results, entry)
			}
		}
		if len(results) == 0 {
			return &skill.Result{Content: fmt.Sprintf("No skills found for '%s'", keyword)}, nil
		}
		return &skill.Result{Content: formatSkillSearchResults(results)}, nil

	case "install":
		if keyword == "" {
			return nil, fmt.Errorf("keyword (skill name) is required for install")
		}
		if err := s.hub.Install(ctx, keyword); err != nil {
			return nil, fmt.Errorf("failed to install skill '%s': %w", keyword, err)
		}
		return &skill.Result{Content: fmt.Sprintf("Skill '%s' installed. Restart to load.", keyword)}, nil

	case "remove":
		if keyword == "" {
			return nil, fmt.Errorf("keyword (skill name) is required for remove")
		}
		if err := s.hub.Uninstall(keyword); err != nil {
			return nil, fmt.Errorf("failed to remove skill '%s': %w", keyword, err)
		}
		return &skill.Result{Content: fmt.Sprintf("Skill '%s' removed.", keyword)}, nil

	case "list":
		catalog := s.hub.GetCatalog()
		if catalog == nil || len(catalog.Skills) == 0 {
			return &skill.Result{Content: "No skills in catalog. Run search to discover available skills."}, nil
		}
		var entries []hub.SkillMeta
		for _, entry := range catalog.Skills {
			if !strings.EqualFold(entry.Type, "mcp") {
				entries = append(entries, entry)
			}
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Available skills (%d):\n", len(entries)))
		for i, sk := range entries {
			if i >= 20 {
				sb.WriteString(fmt.Sprintf("... and %d more\n", len(entries)-20))
				break
			}
			sb.WriteString(fmt.Sprintf("  %d. %s — %s\n", i+1, sk.Name, sk.Description))
		}
		return &skill.Result{Content: sb.String()}, nil

	default:
		return nil, fmt.Errorf("unknown action %q: use search, install, remove, or list", action)
	}
}

func formatSkillSearchResults(results []hub.SkillMeta) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d skill(s):\n", len(results)))
	for i, r := range results {
		if i >= 10 {
			sb.WriteString(fmt.Sprintf("... and %d more\n", len(results)-10))
			break
		}
		sb.WriteString(fmt.Sprintf("  %d. %s — %s", i+1, r.Name, r.Description))
		if r.Source != "" {
			sb.WriteString(" (source: " + r.Source + ")")
		}
		if r.Category != "" {
			sb.WriteString(fmt.Sprintf(" [%s]", r.Category))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
