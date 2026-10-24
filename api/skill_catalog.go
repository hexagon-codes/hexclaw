package api

import (
	"strings"

	"github.com/hexagon-codes/hexclaw/skill/hub"
)

// skillCatalog 保持同一可发现目录，Skill 含出厂包，MCP 仍由原 Hub 类型目录提供。
func (s *Server) skillCatalog() []hub.SkillMeta {
	var entries []hub.SkillMeta
	if s.skillLifecycle != nil {
		entries = append(entries, s.skillLifecycle.Catalog()...)
	}
	if s.skillHub != nil {
		s.skillHub.EnsureCatalog()
		if catalog := s.skillHub.GetCatalog(); catalog != nil {
			for _, entry := range catalog.Skills {
				if s.skillLifecycle == nil || strings.EqualFold(entry.Type, "mcp") {
					entries = append(entries, entry)
				}
			}
		}
	}
	return entries
}
