package builtin

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hexagon-codes/hexclaw/skill"
)

func (s *AppQuerySkill) querySkills(ctx context.Context, args map[string]any) (*skill.Result, error) {
	if s.skills == nil {
		return nil, fmt.Errorf("skill lifecycle is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q := QueryOptions{Domain: "skills", Action: cronArgString(args, "action"), ID: cronArgString(args, "id"), Query: cronArgString(args, "query"), View: cronArgString(args, "view"), Cursor: cronArgString(args, "cursor")}
	if q.Action == "" {
		q.Action = "list"
	}
	if q.View == "" {
		q.View = "installed"
	}
	limit := 20
	if value, exists := args["limit"]; exists {
		switch typed := value.(type) {
		case int:
			limit = typed
		case float64:
			limit = int(typed)
			if float64(limit) != typed {
				return nil, fmt.Errorf("limit must be an integer")
			}
		default:
			return nil, fmt.Errorf("limit must be an integer")
		}
		if limit < 0 {
			return nil, fmt.Errorf("limit must not be negative")
		}
		q.Limit = &limit
	}
	if q.Action != "list" && q.Action != "get" && q.Action != "search" {
		return nil, fmt.Errorf("unsupported skills query action")
	}
	if q.Action == "get" && q.ID == "" {
		return nil, fmt.Errorf("action=get requires id")
	}
	if q.Action == "search" && q.Query == "" {
		return nil, fmt.Errorf("action=search requires query")
	}
	var items []map[string]any
	switch q.View {
	case "installed":
		for _, entry := range s.skills.List() {
			items = append(items, map[string]any{"name": entry.Name, "version": entry.Version, "description": entry.Description, "enabled": entry.Enabled, "runtime_registered": entry.Registered, "effective_enabled": entry.EffectiveEnabled, "requires_restart": entry.RequiresRestart})
		}
	case "catalog":
		for _, entry := range s.skills.Catalog() {
			if strings.EqualFold(entry.Type, "mcp") {
				continue
			}
			source := entry.Source
			if source == "" {
				source = "hub"
			}
			items = append(items, map[string]any{"name": entry.Name, "version": entry.Version, "description": entry.Description, "type": "skill", "source": source})
		}
	default:
		return nil, fmt.Errorf("skills view must be installed or catalog")
	}
	sort.Slice(items, func(i, j int) bool { return items[i]["name"].(string) < items[j]["name"].(string) })
	var matched []map[string]any
	for _, entry := range items {
		name := entry["name"].(string)
		if q.Action == "get" && name != q.ID {
			continue
		}
		if q.Query != "" && !strings.Contains(strings.ToLower(name+" "+entry["description"].(string)), strings.ToLower(q.Query)) {
			continue
		}
		matched = append(matched, entry)
	}
	page := make([]map[string]any, 0)
	next := ""
	for _, entry := range matched {
		if entry["name"].(string) <= q.Cursor {
			continue
		}
		if len(page) >= limit {
			if len(page) > 0 {
				next = page[len(page)-1]["name"].(string)
			}
			break
		}
		page = append(page, entry)
	}
	return skillLifecycleResult("Skill resources", map[string]any{"domain": "skills", "view": q.View, "items": page, "total": len(matched), "next_cursor": next})
}
