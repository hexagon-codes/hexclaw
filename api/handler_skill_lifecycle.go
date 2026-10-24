package api

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/hexagon-codes/hexclaw/skill/lifecycle"
	"github.com/hexagon-codes/hexclaw/skill/marketplace"
)

// SkillLifecycle 返回 API 与 Agent 工具共同使用的生命周期用例。
func (s *Server) SkillLifecycle() *lifecycle.Service { return s.skillLifecycle }

func (s *Server) installSkillSource(ctx context.Context, source string) (*marketplace.MarkdownSkill, error) {
	if s.skillLifecycle != nil {
		return s.skillLifecycle.InstallFile(ctx, source)
	}
	return s.mp.Install(source)
}

func (s *Server) writeSkillInstallResponse(w http.ResponseWriter, sk *marketplace.MarkdownSkill, message string, sources ...string) {
	enabled := s.mp.IsEnabled(sk.Name())
	effective, restart, stateMessage := s.skillEffectiveState(sk.Name(), enabled)
	registered := false
	if runtime, ok := s.engine.(skillRuntimeController); ok {
		_, registered = runtime.SkillEnabled(sk.Name())
	}
	if restart || !registered {
		message = stateMessage
	}
	result := map[string]any{"name": sk.Name(), "description": sk.Description(), "version": sk.Meta.Version, "message": message, "enabled": enabled, "effective_enabled": effective, "requires_restart": restart, "runtime_registered": registered}
	if len(sources) > 0 {
		result["source"] = sources[0]
	}
	writeJSON(w, http.StatusOK, result)
}

// handleRefreshSkills 只刷新原有安装及启用状态，不自动开启用户关闭项。
func (s *Server) handleRefreshSkills(w http.ResponseWriter, r *http.Request) {
	if s.skillLifecycle == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Skill lifecycle is unavailable"})
		return
	}
	if err := s.skillLifecycle.Refresh(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": s.skillLifecycle.List()})
}

// handleSkillResources 列出已安装资源包的精确清单，不展开内容。
func (s *Server) handleSkillResources(w http.ResponseWriter, r *http.Request) {
	if s.skillLifecycle == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Skill lifecycle is unavailable"})
		return
	}
	files, err := s.skillLifecycle.Resources(r.PathValue("name"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, marketplace.ErrSkillNotInstalled) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": r.PathValue("name"), "resources": files})
}

// handleSkillResource 沿当前认证 API 返回声明的原资源字节与摘要，不复制为媒体任务。
func (s *Server) handleSkillResource(w http.ResponseWriter, r *http.Request) {
	if s.skillLifecycle == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Skill lifecycle is unavailable"})
		return
	}
	resource, err := s.skillLifecycle.ReadResource(r.PathValue("name"), r.PathValue("path"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, marketplace.ErrSkillNotInstalled) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	typ := mime.TypeByExtension(filepath.Ext(resource.Path))
	if typ == "" {
		typ = "application/octet-stream"
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Content-Length", strconv.Itoa(len(resource.Content)))
	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", resource.SHA256))
	w.Header().Set("X-Skill-Version", resource.Version)
	w.Header().Set("X-Content-SHA256", resource.SHA256)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(resource.Content)
}
