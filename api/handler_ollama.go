package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	goruntime "runtime"
	"strings"
	"time"
)

const defaultOllamaBaseURL = "http://localhost:11434"

// SetOllamaBaseURL overrides the native Ollama management endpoint. It is
// deliberately loopback-only: model pull/delete endpoints are side-effecting,
// so a test seam must not become an SSRF or accidental remote-management seam.
// Call it during server construction, before serving requests.
func (s *Server) SetOllamaBaseURL(rawURL string) error {
	normalized, err := normalizeNativeOllamaBaseURL(rawURL)
	if err != nil {
		return err
	}
	s.ollamaBaseURL = normalized
	return nil
}

// ValidateNativeOllamaBaseURL applies the same loopback-only endpoint contract
// as SetOllamaBaseURL without mutating a Server. Startup capability discovery
// uses it so probing and automatic installation cannot drift from the native
// management boundary.
func ValidateNativeOllamaBaseURL(rawURL string) error {
	_, err := normalizeNativeOllamaBaseURL(rawURL)
	return err
}

func normalizeNativeOllamaBaseURL(rawURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid Ollama base URL %q", rawURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("Ollama base URL scheme must be http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("Ollama base URL must not contain credentials, query, or fragment")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", fmt.Errorf("Ollama base URL must use a loopback host")
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	if path != "" && path != "/v1" {
		return "", fmt.Errorf("Ollama base URL path must be empty or /v1")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.ForceQuery = false
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

// SetOllamaModelInstalledCallback installs a post-pull lifecycle hook. It is
// invoked only after Ollama emits a successful terminal event, allowing
// runtime capabilities (such as a semantic-index catalog) to refresh without
// coupling the generic model-management handler to those domains.
func (s *Server) SetOllamaModelInstalledCallback(callback func(context.Context, string)) {
	s.onOllamaModelInstalled = callback
}

func (s *Server) ollamaEndpoint(path string) string { return s.ollamaTarget().endpoint(path) }
func (s *Server) ollamaHTTPClient(totalTimeout, responseHeaderTimeout time.Duration) *http.Client {
	return s.ollamaTarget().client(totalTimeout, responseHeaderTimeout)
}

func (s *Server) ollamaLifecycleContext() context.Context {
	if s.serviceLifecycleCtx != nil {
		return s.serviceLifecycleCtx
	}
	return context.Background()
}

// OllamaStatus Ollama 运行时状态 (14.15 本地 LLM 管理)
type OllamaStatus struct {
	TargetID        string        `json:"target_id"`
	TargetRevision  uint64        `json:"target_revision"`
	ResolvedBaseURL string        `json:"resolved_base_url"`
	CanRestart      bool          `json:"can_restart"`
	Reachable       bool          `json:"reachable"`
	Error           string        `json:"error,omitempty"`
	Running         bool          `json:"running"`           // Ollama 服务是否在运行
	Version         string        `json:"version,omitempty"` // Ollama 版本号
	Models          []OllamaModel `json:"models,omitempty"`  // 已下载的模型列表
	Associated      bool          `json:"associated"`        // 是否已关联为 LLM Provider
	ModelCount      int           `json:"model_count"`       // 模型数量
}

// OllamaModel Ollama 已下载的模型
type OllamaModel struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Family   string `json:"family,omitempty"`
	Params   string `json:"parameter_size,omitempty"`
	Quant    string `json:"quantization_level,omitempty"`
	// Capabilities 模型真实能力（BUG-20260704）：直接透出 Ollama /api/tags 上报的
	// capabilities（如 completion / vision / tools / thinking），由前端映射为模态徽章。
	// 此前前端只按模型名查静态表猜能力 → qwen3.5:9b 等视觉模型被误判为纯文本。
	Capabilities []string `json:"capabilities,omitempty"`
}

// ollamaTagsResponse 是 Ollama GET /api/tags 的响应结构（含 capabilities，新版 Ollama 已上报）。
type ollamaTagsResponse struct {
	Models []struct {
		Name         string   `json:"name"`
		Size         int64    `json:"size"`
		ModifiedAt   string   `json:"modified_at"`
		Capabilities []string `json:"capabilities"`
		Details      struct {
			Family            string `json:"family"`
			ParameterSize     string `json:"parameter_size"`
			QuantizationLevel string `json:"quantization_level"`
		} `json:"details"`
	} `json:"models"`
}

// parseOllamaTags 把 /api/tags 响应体解析为 OllamaModel 列表（含真实 capabilities）。
// 抽成纯函数便于单测；解析失败返回 nil（调用方保持列表为空，不 panic）。
func parseOllamaTags(body []byte) []OllamaModel {
	var result ollamaTagsResponse
	if json.Unmarshal(body, &result) != nil || result.Models == nil {
		return nil
	}
	models := make([]OllamaModel, 0, len(result.Models))
	for _, m := range result.Models {
		models = append(models, OllamaModel{
			Name:         m.Name,
			Size:         m.Size,
			Modified:     m.ModifiedAt,
			Family:       m.Details.Family,
			Params:       m.Details.ParameterSize,
			Quant:        m.Details.QuantizationLevel,
			Capabilities: m.Capabilities,
		})
	}
	return models
}

// handleOllamaStatus 探测本地 Ollama 服务状态 + 模型列表 + 版本 + 关联状态
//
// 前端状态机：
//
//	detecting → not_installed / installed_not_running / running_not_associated / associated / updatable
func (s *Server) handleOllamaStatus(w http.ResponseWriter, r *http.Request) {
	target := s.ollamaTarget()
	client := target.client(3*time.Second, 3*time.Second)
	status := OllamaStatus{TargetID: target.TargetID, TargetRevision: target.TargetRevision, ResolvedBaseURL: target.ResolvedBaseURL, CanRestart: target.CanRestart, Models: []OllamaModel{}}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, target.endpoint("/api/tags"), nil)
	response, err := client.Do(req)
	if err != nil {
		status.Error = "Ollama tags request failed"
	} else {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if response.StatusCode == http.StatusOK && readErr == nil {
			models := parseOllamaTags(body)
			if models != nil {
				status.Models = models
				status.Running = true
				status.Reachable = true
				status.ModelCount = len(models)
			} else {
				status.Error = "Invalid Ollama tags response"
			}
		} else {
			status.Error = "Ollama tags request failed"
		}
	}
	if status.Running {
		req, _ = http.NewRequestWithContext(r.Context(), http.MethodGet, target.endpoint("/api/version"), nil)
		if response, err = client.Do(req); err == nil {
			if response.StatusCode == http.StatusOK {
				var version struct {
					Version string `json:"version"`
				}
				if json.NewDecoder(response.Body).Decode(&version) == nil {
					status.Version = version.Version
				}
			}
			response.Body.Close()
		}
	}
	status.Associated = len(target.AssociatedProviderInstanceIDs) > 0
	writeJSON(w, http.StatusOK, status)
}

// handleOllamaRunning 获取当前加载到内存的模型列表
//
// GET /api/v1/ollama/running
func (s *Server) handleOllamaRunning(w http.ResponseWriter, r *http.Request) {
	target := s.ollamaTarget()
	client := target.client(3*time.Second, 3*time.Second)
	resp, err := client.Get(target.endpoint("/api/ps"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("Ollama 连接失败: %v", err)})
		return
	}
	defer resp.Body.Close()

	var result struct {
		Models []struct {
			Name          string `json:"name"`
			Size          int64  `json:"size"`
			SizeVRAM      int64  `json:"size_vram"`
			ExpiresAt     string `json:"expires_at"`
			ContextLength int    `json:"context_length"`
			Details       struct {
				Family            string `json:"family"`
				ParameterSize     string `json:"parameter_size"`
				QuantizationLevel string `json:"quantization_level"`
			} `json:"details"`
		} `json:"models"`
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK || readErr != nil || json.Unmarshal(body, &result) != nil || result.Models == nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Invalid Ollama running models response"})
		return
	}

	type runningModel struct {
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		SizeVRAM  int64  `json:"size_vram"`
		ExpiresAt string `json:"expires_at"`
		Params    string `json:"parameter_size,omitempty"`
		Quant     string `json:"quantization_level,omitempty"`
		Context   int    `json:"context_length"`
	}
	models := make([]runningModel, 0, len(result.Models))
	for _, m := range result.Models {
		models = append(models, runningModel{
			Name: m.Name, Size: m.Size, SizeVRAM: m.SizeVRAM,
			ExpiresAt: m.ExpiresAt, Context: m.ContextLength,
			Params: m.Details.ParameterSize, Quant: m.Details.QuantizationLevel,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models": models, "target_id": target.TargetID, "target_revision": target.TargetRevision,
	})
}

// handleOllamaUnload 从内存中卸载模型
//
// POST /api/v1/ollama/unload  Body: {"model": "qwen3:8b"}
func (s *Server) handleOllamaUnload(w http.ResponseWriter, r *http.Request) {
	target := s.ollamaTarget()
	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model is required"})
		return
	}
	// keep_alive=0 让 Ollama 立即卸载模型
	unloadBody, _ := json.Marshal(map[string]any{"model": req.Model, "keep_alive": 0})
	client := target.client(10*time.Second, 10*time.Second)
	resp, err := client.Post(target.endpoint("/api/generate"), "application/json", bytes.NewReader(unloadBody))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("卸载失败: %v", err)})
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) // drain
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		writeJSON(w, resp.StatusCode, map[string]string{"error": "Ollama 卸载失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "unloaded"})
}

// defaultWarmupKeepAlive 预热请求默认驻留时长——与 ai-core 请求级默认（defaultKeepAlive="30m"）
// 一致：预热 5m 而真实对话 30m 会让预热模型在 5~30min 空窗期提前卸载，首条消息仍走冷路径。
const defaultWarmupKeepAlive = "30m"

// warmupNumCtxTiers 镜像 ai-core llm/ollama 的 numCtxTiers（该表未导出）。真实对话经
// ai-core 自动分档：标准桌面聊天（SOUL + 操作手册 + 工具 schema ≈8k token）稳态落在 8192
// 档（真机取证 ctx 4096→8192）。
var warmupNumCtxTiers = []int{4096, 8192, 16384, 32768}

// defaultWarmupNumCtx 预热请求默认 num_ctx：与真实对话稳态一致。AP-206 核心不变量——
// 预热请求的 runner-affecting 参数（num_ctx）必须与真实对话一致，否则首条消息触发 runner
// 重载、预热白做。
const defaultWarmupNumCtx = 8192

// resolveWarmupNumCtx 请求显式 num_ctx 优先（>0 原样生效，供前端下发与真实对话一致的档位），
// 否则回落到稳态默认档 8192（ai-core 分档表中的档位之一）。
func resolveWarmupNumCtx(reqNumCtx int) int {
	if reqNumCtx > 0 {
		return reqNumCtx
	}
	return defaultWarmupNumCtx
}

// resolveOllamaNumCtx 预热 num_ctx 解析：Ollama provider 配了 num_ctx 上限则**恒用它**（预热与
// 真实请求必须同档，否则 Ollama 载了大 runner 后小请求不降载、内存照样撑爆——BUG-20260712），
// 未配置时回落 resolveWarmupNumCtx（请求显式 > 8192 稳态默认）。
func (s *Server) resolveOllamaNumCtx(reqNumCtx int) int {
	if s.cfg != nil {
		for name, p := range s.persistedLLMConfig().Providers {
			lower := strings.ToLower(name)
			if lower == "ollama" || strings.Contains(strings.ToLower(p.BaseURL), "localhost:11434") {
				if p.NumCtx > 0 {
					return p.NumCtx
				}
			}
		}
	}
	return resolveWarmupNumCtx(reqNumCtx)
}

// resolveOllamaKeepAlive 取 Ollama provider 配置的 keep_alive（与真实对话下发值一致），
// 未配置时回落到与 ai-core 请求级默认一致的 30m。
func (s *Server) resolveOllamaKeepAlive() string {
	if s.cfg != nil {
		for name, p := range s.persistedLLMConfig().Providers {
			lower := strings.ToLower(name)
			if lower == "ollama" || strings.Contains(strings.ToLower(p.BaseURL), "localhost:11434") {
				if ka := strings.TrimSpace(p.KeepAlive); ka != "" {
					return ka
				}
			}
		}
	}
	return defaultWarmupKeepAlive
}

// buildOllamaLoadBody 构造 /api/generate 预热请求体。
//
// AP-206 核心不变量：预热请求的 runner-affecting 参数（num_ctx）必须与真实对话一致，
// 否则真实对话经 ai-core 带分档 num_ctx（如 8192）时 Ollama 重载 runner，预热白做。
func buildOllamaLoadBody(model string, numCtx int, keepAlive string) []byte {
	payload := map[string]any{
		"model":      model,
		"prompt":     "",
		"stream":     false,
		"keep_alive": keepAlive,
	}
	if numCtx > 0 {
		payload["options"] = map[string]any{"num_ctx": numCtx}
	}
	b, _ := json.Marshal(payload)
	return b
}

// handleOllamaLoad 预热模型到内存
//
// POST /api/v1/ollama/load  Body: {"model": "qwen3:8b", "num_ctx": 8192}
// num_ctx 可选：前端可下发与真实对话一致的档位；缺省时回落稳态默认档。
func (s *Server) handleOllamaLoad(w http.ResponseWriter, r *http.Request) {
	target := s.ollamaTarget()
	var req struct {
		Model  string `json:"model"`
		NumCtx int    `json:"num_ctx,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model is required"})
		return
	}
	numCtx, keepAlive := target.warmupParameters(req.Model, req.NumCtx)
	loadBody := buildOllamaLoadBody(req.Model, numCtx, keepAlive)
	client := target.client(30*time.Second, 30*time.Second)
	resp, err := client.Post(target.endpoint("/api/generate"), "application/json", bytes.NewReader(loadBody))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("预热失败: %v", err)})
		return
	}
	defer resp.Body.Close()
	var result struct {
		Done  bool   `json:"done"`
		Error string `json:"error"`
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result) != nil || !result.Done || result.Error != "" {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "Ollama model warmup was not confirmed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "loaded"})
}

// handleOllamaDelete 删除已下载的模型
//
// DELETE /api/v1/ollama/models/:name
func ollamaModelBase(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	colon := strings.LastIndex(normalized, ":")
	slash := strings.LastIndex(normalized, "/")
	if colon > slash {
		return normalized[:colon]
	}
	return normalized
}

func canonicalOllamaModelTag(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if strings.LastIndex(normalized, ":") <= strings.LastIndex(normalized, "/") {
		return normalized + ":latest"
	}
	return normalized
}

func (s *Server) ollamaModelAbsent(r *http.Request, targetSnapshot ollamaTargetSnapshot, name string) (bool, error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetSnapshot.endpoint("/api/tags"), nil)
	if err != nil {
		return false, err
	}
	resp, err := targetSnapshot.client(10*time.Second, 10*time.Second).Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		io.Copy(io.Discard, resp.Body)
		return false, fmt.Errorf("Ollama tags status %d", resp.StatusCode)
	}
	var payload struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return false, err
	}
	if payload.Models == nil {
		return false, fmt.Errorf("Invalid Ollama tags response")
	}
	target := canonicalOllamaModelTag(name)
	for _, model := range payload.Models {
		candidate := model.Name
		if candidate == "" {
			candidate = model.Model
		}
		if canonicalOllamaModelTag(candidate) == target {
			return false, nil
		}
	}
	return true, nil
}

// disableEmbeddingAutoInstallForDeletedModel 记住用户的删除意图。
// 否则当前知识库嵌入模型会在下次 sidecar 启动时被静默重新下载。
func (s *Server) disableEmbeddingAutoInstallForDeletedModel(name string) error {
	info := s.kbEmbedding
	if info == nil || !info.Local || ollamaModelBase(info.Model) != ollamaModelBase(name) {
		return nil
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if s.cfg == nil {
		return fmt.Errorf("配置未初始化")
	}
	if s.cfg.Knowledge.Embedding.DisableAutoInstall {
		return nil
	}

	nextCfg := *s.cfg
	nextKnowledge := s.cfg.Knowledge
	nextEmbedding := nextKnowledge.Embedding
	nextEmbedding.DisableAutoInstall = true
	nextKnowledge.Embedding = nextEmbedding
	nextCfg.Knowledge = nextKnowledge
	if err := s.saveRuntimeConfig(&nextCfg); err != nil {
		return fmt.Errorf("保存知识库嵌入模型自动安装设置: %w", err)
	}
	s.cfg.Knowledge = nextKnowledge
	return nil
}

func (s *Server) disableEmbeddingAutoInstallForTarget(target ollamaTargetSnapshot, name string) error {
	if strings.TrimRight(target.ResolvedBaseURL, "/") != strings.TrimRight(s.ollamaBaseURL, "/") {
		return nil
	}
	return s.disableEmbeddingAutoInstallForDeletedModel(name)
}

func (s *Server) handleOllamaDelete(w http.ResponseWriter, r *http.Request) {
	target := s.ollamaTarget()
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model name required"})
		return
	}
	if err := s.disableEmbeddingAutoInstallForTarget(target, name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "关闭模型自动重装失败: " + err.Error(),
		})
		return
	}
	delBody, _ := json.Marshal(map[string]string{"model": name})
	req2, _ := http.NewRequestWithContext(r.Context(), "DELETE", target.endpoint("/api/delete"), bytes.NewReader(delBody))
	req2.Header.Set("Content-Type", "application/json")
	client := target.client(10*time.Second, 10*time.Second)
	resp, err := client.Do(req2)
	if err != nil {
		if absent, reconcileErr := s.ollamaModelAbsent(r, target, name); reconcileErr == nil && absent {
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("删除失败: %v", err)})
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if absent, reconcileErr := s.ollamaModelAbsent(r, target, name); reconcileErr == nil && absent {
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
			return
		}
		writeJSON(w, resp.StatusCode, map[string]string{"error": "Ollama 删除失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleOllamaRestart 重启 Ollama 服务
//
// POST /api/v1/ollama/restart
// macOS: open -a Ollama（桌面应用自带 serve）
// Linux: systemctl restart ollama 或 ollama serve
func (s *Server) handleOllamaRestart(w http.ResponseWriter, r *http.Request) {
	target := s.ollamaTarget()
	if !target.CanRestart {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Ollama process is not managed by this backend"})
		return
	}
	// 先检测当前是否在运行
	client := target.client(2*time.Second, 2*time.Second)
	wasRunning := false
	if resp, err := client.Get(target.endpoint("/api/version")); err == nil {
		resp.Body.Close()
		wasRunning = true
	}

	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "darwin":
		// macOS: 通过桌面应用启动（最可靠）
		if wasRunning {
			// 先关再开
			exec.Command("osascript", "-e", `quit app "Ollama"`).Run()
			time.Sleep(2 * time.Second)
		}
		cmd = exec.Command("open", "-a", "Ollama")
	case "linux":
		if wasRunning {
			exec.Command("systemctl", "restart", "ollama").Run()
			writeJSON(w, http.StatusOK, map[string]string{"status": "restarting"})
			return
		}
		cmd = exec.Command("ollama", "serve")
		cmd.SysProcAttr = sysProcAttr()
	default:
		// Windows: start ollama
		cmd = exec.Command("ollama", "serve")
	}

	if err := cmd.Start(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("启动失败: %v", err)})
		return
	}

	// 等待 Ollama 启动（最多 10 秒）
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		if resp, err := client.Get(target.endpoint("/api/version")); err == nil {
			resp.Body.Close()
			writeJSON(w, http.StatusOK, map[string]string{"status": "running"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "starting"})
}
