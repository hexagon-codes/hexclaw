package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hexagon-codes/toolkit/util/idgen"
	"github.com/hexagon-codes/toolkit/util/logger"
)

// 运行代际属于进程，不随数据库、配置或服务实例身份持久化。
var processInstanceID = idgen.NanoID()

type serviceHealthResponse struct {
	Status            string `json:"status"`
	Error             string `json:"error,omitempty"`
	ProcessInstanceID string `json:"process_instance_id"`
	RestartSupported  bool   `json:"restart_supported"`
}

// SetManagedRestart 接入已由部署 supervisor 管理的优雅退出回调。
// 必须在 Start 前设置；nil 表示部署未提供进程恢复能力。
// 回调只取消原服务生命周期，资源清理与新进程启动沿原退出链及 supervisor 执行。
func (s *Server) SetManagedRestart(restart func()) {
	s.serviceRestartMu.Lock()
	defer s.serviceRestartMu.Unlock()
	s.serviceRestart = restart
}

func (s *Server) managedRestartSupported() bool {
	s.serviceRestartMu.Lock()
	defer s.serviceRestartMu.Unlock()
	return s.serviceRestart != nil
}

type serviceRestartRequest struct {
	ExpectedProcessInstanceID string `json:"expected_process_instance_id"`
	RequestID                 string `json:"request_id"`
}

// handleRestartService 仅受理目标运行代际的一次退出，旧代际请求不能重启恢复后的进程。
// 返回 202 只表示已受理；客户端须查询新代际健康状态，结果未知时不重发请求。
func (s *Server) handleRestartService(w http.ResponseWriter, r *http.Request) {
	var req serviceRestartRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid service restart request"})
		return
	}
	req.ExpectedProcessInstanceID = strings.TrimSpace(req.ExpectedProcessInstanceID)
	req.RequestID = strings.TrimSpace(req.RequestID)
	if req.ExpectedProcessInstanceID == "" || req.RequestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected_process_instance_id and request_id are required"})
		return
	}

	s.serviceRestartMu.Lock()
	var rejection string
	switch {
	case req.ExpectedProcessInstanceID != processInstanceID:
		rejection = "Process instance has changed"
	case s.serviceRestart == nil:
		rejection = "Managed service restart is not configured"
	case s.serviceRestartRequestID != "" && s.serviceRestartRequestID != req.RequestID:
		rejection = "Service restart is already in progress"
	}
	if rejection != "" {
		s.serviceRestartMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": rejection, "process_instance_id": processInstanceID,
		})
		return
	}
	firstRequest := s.serviceRestartRequestID == ""
	s.serviceRestartRequestID = req.RequestID
	restart := s.serviceRestart
	s.serviceRestartMu.Unlock()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status": "restarting", "process_instance_id": processInstanceID, "request_id": req.RequestID,
	})
	if !firstRequest {
		return
	}
	// 先写出受理响应，再关闭监听；写出失败亦保持同次操作，不允许重复退出。
	if err := http.NewResponseController(w).Flush(); err != nil {
		logger.Warn("Failed to flush service restart acknowledgement", "process_instance_id", processInstanceID)
	}
	logger.Info("Managed service restart accepted", "process_instance_id", processInstanceID, "request_id", req.RequestID)
	restart()
}
