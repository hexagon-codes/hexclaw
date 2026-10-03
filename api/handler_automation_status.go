package api

import "net/http"

type automationCapabilityStatus struct {
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
}

// handleAutomationStatus 区分未启用和初始化失败；零任务由就绪组件的列表响应表达。
func (s *Server) handleAutomationStatus(w http.ResponseWriter, r *http.Request) {
	s.cfgMu.RLock()
	cronEnabled, webhookEnabled := s.cfg.Cron.Enabled, s.cfg.Webhook.Enabled
	s.cfgMu.RUnlock()
	status := func(enabled, ready bool) automationCapabilityStatus {
		state := "disabled"
		if ready {
			state = "ready"
		} else if enabled {
			state = "unavailable"
		}
		return automationCapabilityStatus{Enabled: enabled, State: state}
	}
	writeJSON(w, http.StatusOK, map[string]automationCapabilityStatus{
		"cron":    status(cronEnabled, s.scheduler != nil),
		"webhook": status(webhookEnabled, s.webhookMgr != nil),
	})
}
