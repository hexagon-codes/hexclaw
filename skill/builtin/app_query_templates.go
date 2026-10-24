package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/agentcatalog"
	"github.com/hexagon-codes/hexclaw/skill"
)

func (s *AppQuerySkill) queryAgentTemplates(ctx context.Context, args map[string]any) (*skill.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	locale := cronArgString(args, "locale")
	if locale == "" {
		locale = skill.CurrentInvocation(ctx).ScenarioInput["user_locale"]
	}
	action := cronArgString(args, "action")
	if action == "get" {
		key := cronArgString(args, "id")
		if key == "" {
			return nil, fmt.Errorf("action=get requires id")
		}
		snapshot, err := agentcatalog.Get(key, locale)
		if err != nil {
			return nil, err
		}
		return skillLifecycleResult("Agent template snapshot", snapshot)
	}
	if action != "" && action != "list" && action != "search" {
		return nil, fmt.Errorf("unsupported template query action")
	}
	list, err := agentcatalog.List(locale)
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(cronArgString(args, "query"))
	var summary []map[string]any
	for _, snapshot := range list {
		if query != "" && !strings.Contains(strings.ToLower(snapshot.Key+" "+snapshot.Name+" "+snapshot.Description), query) {
			continue
		}
		summary = append(summary, map[string]any{"template_key": snapshot.Key, "catalog_source": snapshot.Source, "catalog_version": snapshot.CatalogVersion, "template_version": snapshot.Version, "template_digest": snapshot.Digest, "locale": snapshot.Locale, "name": snapshot.Name, "description": snapshot.Description})
	}
	return skillLifecycleResult("Agent template catalog", map[string]any{"templates": summary, "total": len(summary)})
}
