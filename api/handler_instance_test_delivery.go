package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/instances"
)

// boundInstanceTestTargets 为所有智能体统一合并当前账号真实会话与有效显式绑定。
func (s *Server) boundInstanceTestTargets(ctx context.Context, inst *instances.Instance) ([]string, error) {
	targets, err := s.instanceMgr.KnownConversationTargets(ctx, inst)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, chatID := range targets {
		seen[chatID] = true
	}
	if s.agentRouter == nil {
		return targets, nil
	}
	for _, rule := range s.agentRouter.ListRules() {
		chatID := strings.TrimSpace(rule.ChatID)
		if chatID == "" || strings.TrimSpace(rule.Platform) != inst.Provider {
			continue
		}
		if _, ok := s.agentRouter.GetAgent(strings.TrimSpace(rule.AgentName)); !ok {
			continue
		}
		ref := strings.TrimSpace(rule.InstanceID)
		if ref == "" {
			id, err := s.instanceMgr.ResolveRunningInstanceID(inst.Provider, ref)
			if err != nil || id != inst.ID {
				continue
			}
		} else if ref != inst.ID && ref != inst.Name {
			continue
		}
		if !seen[chatID] {
			seen[chatID] = true
			targets = append(targets, chatID)
		}
	}
	sort.Strings(targets)
	return targets, nil
}

func (s *Server) handleBoundInstanceTest(w http.ResponseWriter, r *http.Request, inst *instances.Instance, requestID, content string) {
	// 回放先读取冻结结果，绑定或连接变化不能造成同一请求再次发送。
	previous, err := s.instanceMgr.ReadTestDelivery(r.Context(), inst.ID, requestID)
	if err == nil {
		writeJSON(w, http.StatusOK, previous)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	report, err := s.instanceMgr.Health(ctx, inst.Name)
	if err != nil || !report.Healthy {
		writeJSON(w, http.StatusOK, map[string]any{"success": false, "pending": false, "message": "Connection is not healthy; no test message was sent."})
		return
	}
	targets, err := s.boundInstanceTestTargets(ctx, inst)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "pending": false, "error": err.Error()})
		return
	}
	result, err := s.instanceMgr.SendBoundTest(ctx, inst.ID, requestID, targets, content)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "pending": true, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
