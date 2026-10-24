package api

import (
	"net/http"

	"github.com/hexagon-codes/hexclaw/internal/agentcatalog"
)

// handleAgentTemplates 返回当前后端的真实模板源；专属场景模板仍由原场景注册表负责。
func (s *Server) handleAgentTemplates(w http.ResponseWriter, r *http.Request) {
	locale := r.URL.Query().Get("locale")
	if key := r.URL.Query().Get("key"); key != "" {
		snapshot, err := agentcatalog.Get(key, locale)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	catalog, err := agentcatalog.Read()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	items, err := agentcatalog.List(locale)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema_version": catalog.SchemaVersion, "catalog_source": "backend", "catalog_version": catalog.Version, "templates": items, "total": len(items)})
}
