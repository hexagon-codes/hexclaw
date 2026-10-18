package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hexagon-codes/hexclaw/messagecontent"
	"github.com/hexagon-codes/hexclaw/render"
)

// WorkflowGeneratedFile 只持久化确定性转换状态与生成文件的内容寻址引用。
type WorkflowGeneratedFile struct {
	ID          string    `json:"id"`
	NodeID      string    `json:"node_id"`
	Format      string    `json:"format"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	FilePath    string    `json:"file_path,omitempty"`
	DownloadURL string    `json:"download_url,omitempty"`
	Digest      string    `json:"digest,omitempty"`
	Size        int64     `json:"size,omitempty"`
	MIME        string    `json:"mime,omitempty"`
	Error       string    `json:"error,omitempty"`
	ErrorCode   string    `json:"error_code,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	active      bool
}

func workflowOutputNodeContent(run *WorkflowRun, nodeID string) (string, *messagecontent.MessageContent, bool) {
	for _, result := range run.NodeResults {
		if result.NodeID == nodeID {
			return result.Output, result.MessageContent, result.Status == nodeStatusCompleted
		}
	}
	return run.Output, run.MessageContent, true
}

func workflowOutputNodeConfig(node map[string]any) map[string]any {
	config := resourceMap(node["data"])
	if len(config) == 0 {
		config = resourceMap(node["config"])
	}
	return config
}

func workflowOutputFormats(value any) []string {
	var values []string
	switch raw := value.(type) {
	case []any:
		for _, item := range raw {
			values = append(values, stringValue(item))
		}
	case []string:
		values = raw
	}
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

// 转换计划与计算结果同次冻结，恢复时不从后来修改的定义补造产物。
func prepareWorkflowGeneratedFiles(run *WorkflowRun) []WorkflowGeneratedFile {
	if run == nil || run.Status != "completed" || run.Definition == nil {
		return nil
	}
	var files []WorkflowGeneratedFile
	for _, raw := range run.Definition.Nodes {
		node, ok := raw.(map[string]any)
		if !ok || stringValue(node["type"]) != "output" {
			continue
		}
		nodeID := stringValue(node["id"])
		if _, _, active := workflowOutputNodeContent(run, nodeID); !active {
			continue
		}
		outputs := resourceMap(workflowOutputNodeConfig(node)["outputs"])
		for _, format := range workflowOutputFormats(outputs["formats"]) {
			now := time.Now()
			file := WorkflowGeneratedFile{ID: "wfo-" + workflowDeliveryIdentity(run.ID, nodeID, format), NodeID: nodeID,
				Format: format, Name: workflowOutputFileName(run, nodeID, format), Status: "pending", CreatedAt: now, UpdatedAt: now}
			if format != "pdf" && format != "md" && format != "html" {
				file.Status, file.ErrorCode, file.Error = "failed", "OUTPUT_FORMAT_UNSUPPORTED", "Workflow output format is unsupported"
			}
			files = append(files, file)
		}
	}
	return files
}

func workflowOutputFileName(run *WorkflowRun, nodeID, format string) string {
	stamp := run.FinishedAt
	if stamp.IsZero() {
		stamp = run.StartedAt
	}
	name := "workflow"
	if run.Definition != nil {
		name = firstNonEmpty(run.Definition.Name, name)
	}
	return workflowOutputSafeName(name) + "-" + stamp.UTC().Format("20060102-150405") + "-" + workflowOutputSafeName(run.ID) + "-" + workflowOutputSafeName(nodeID) + "." + workflowOutputSafeName(format)
}

func workflowOutputSafeName(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/\\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, value)
	value = strings.Trim(value, " .")
	if value == "" {
		return "workflow"
	}
	return value
}

func (s *Server) workflowGeneratedFilesSnapshot(runID string) *WorkflowRun {
	s.workflowStore.mu.RLock()
	defer s.workflowStore.mu.RUnlock()
	run := s.workflowStore.runs[runID]
	if run == nil {
		return nil
	}
	return cloneWorkflowRun(run)
}

func (s *Server) updateWorkflowGeneratedFile(runID, fileID string, change func(*WorkflowGeneratedFile, *WorkflowRun) error) error {
	ws := s.workflowStore
	ws.mu.Lock()
	defer ws.mu.Unlock()
	run := ws.runs[runID]
	if run == nil {
		return errors.New("Workflow run was not found")
	}
	for i := range run.GeneratedFiles {
		if run.GeneratedFiles[i].ID != fileID {
			continue
		}
		previous := run.GeneratedFiles[i]
		previousSnapshots := make(map[string]WorkflowArtifactSnapshot, len(run.ArtifactSnapshots))
		for id, snapshot := range run.ArtifactSnapshots {
			previousSnapshots[id] = snapshot
		}
		if err := change(&run.GeneratedFiles[i], run); err != nil {
			return err
		}
		run.GeneratedFiles[i].UpdatedAt = time.Now()
		if err := ws.persistRuns(); err != nil {
			run.GeneratedFiles[i], run.ArtifactSnapshots = previous, previousSnapshots
			return err
		}
		return nil
	}
	return errors.New("Workflow output file was not found")
}

func (s *Server) generateWorkflowOutputFiles(ctx context.Context, runID, nodeID string, retry bool) {
	run := s.workflowGeneratedFilesSnapshot(runID)
	if run == nil || run.Status != "completed" {
		return
	}
	for _, file := range run.GeneratedFiles {
		if nodeID != "" && file.NodeID != nodeID {
			continue
		}
		if file.Status != "pending" && !(retry && file.Status == "failed") {
			continue
		}
		if err := s.updateWorkflowGeneratedFile(runID, file.ID, func(current *WorkflowGeneratedFile, _ *WorkflowRun) error {
			if current.active || (current.Status != "pending" && !(retry && current.Status == "failed")) {
				return errors.New("Workflow output file is already active")
			}
			current.active = true
			current.Status, current.Error, current.ErrorCode = "rendering", "", ""
			return nil
		}); err != nil {
			continue
		}
		path, digest, size, code, problem := s.renderWorkflowOutputFile(ctx, run, file)
		persistErr := s.updateWorkflowGeneratedFile(runID, file.ID, func(current *WorkflowGeneratedFile, currentRun *WorkflowRun) error {
			current.active = false
			current.Status, current.ErrorCode, current.Error = "ready", code, problem
			if problem != "" {
				current.Status = "failed"
				return nil
			}
			current.FilePath, current.DownloadURL, current.Digest, current.Size = path, "/api/v1/files/generated/"+filepath.ToSlash(path), digest, size
			current.MIME = render.Format(current.Format).MIME()
			if currentRun.ArtifactSnapshots == nil {
				currentRun.ArtifactSnapshots = map[string]WorkflowArtifactSnapshot{}
			}
			ref := workflowGeneratedFileRef(*current)
			currentRun.ArtifactSnapshots[ref.AssetID] = WorkflowArtifactSnapshot{Ref: ref, FilePath: path}
			return nil
		})
		if persistErr != nil {
			// 写盘失败不进入投递边界，解除进程内领取后保留可重试的失败状态。
			s.workflowStore.mu.Lock()
			if currentRun := s.workflowStore.runs[runID]; currentRun != nil {
				for i := range currentRun.GeneratedFiles {
					if currentRun.GeneratedFiles[i].ID == file.ID {
						currentRun.GeneratedFiles[i].active = false
						currentRun.GeneratedFiles[i].Status, currentRun.GeneratedFiles[i].ErrorCode, currentRun.GeneratedFiles[i].Error = "failed", "OUTPUT_PERSIST_FAILED", "Workflow output status could not be persisted"
					}
				}
			}
			s.workflowStore.mu.Unlock()
		}
	}
}

func (s *Server) renderWorkflowOutputFile(ctx context.Context, run *WorkflowRun, file WorkflowGeneratedFile) (string, string, int64, string, string) {
	if file.Format != "md" && file.Format != "pdf" && file.Format != "html" {
		return "", "", 0, "OUTPUT_FORMAT_UNSUPPORTED", "Workflow output format is unsupported"
	}
	if s.renderSvc == nil || s.genStore == nil {
		return "", "", 0, "OUTPUT_RENDER_UNAVAILABLE", "Workflow document rendering is unavailable"
	}
	content, canonical, _ := workflowOutputNodeContent(run, file.NodeID)
	locale := "zh-CN"
	if canonical != nil {
		content, locale = canonical.Markdown, canonical.Locale
	}
	if strings.TrimSpace(content) == "" {
		return "", "", 0, "OUTPUT_CONTENT_REQUIRED", "Workflow output has no text to convert; use its existing files instead"
	}
	result, err := s.renderSvc.Render(ctx, content, render.Format(file.Format), render.RenderOptions{Locale: locale})
	if err != nil {
		code := "OUTPUT_RENDER_FAILED"
		var renderError *render.RenderError
		if errors.As(err, &renderError) {
			code = renderError.Code
		}
		return "", "", 0, code, "Workflow output file could not be rendered"
	}
	// 缓存文件由服务管理；临时渲染结果落入genStore后才清理。
	if !result.CacheHit {
		defer os.Remove(result.Path)
	}
	input, err := os.Open(result.Path)
	if err != nil {
		return "", "", 0, "OUTPUT_PERSIST_FAILED", "Workflow output file could not be opened"
	}
	defer input.Close()
	hash := sha256.New()
	path, err := s.genStore.SaveStream(ctx, io.TeeReader(input, hash), file.Format)
	if err != nil {
		return "", "", 0, "OUTPUT_PERSIST_FAILED", "Workflow output file could not be persisted"
	}
	stat, err := input.Stat()
	if err != nil || stat.Size() == 0 {
		return "", "", 0, "OUTPUT_PERSIST_FAILED", "Workflow output file is empty or unavailable"
	}
	return path, "sha256:" + hex.EncodeToString(hash.Sum(nil)), stat.Size(), "", ""
}

func workflowGeneratedFileRef(file WorkflowGeneratedFile) messagecontent.AttachmentRef {
	// 任务ID标识一次转换；规范附件身份由内容摘要确定，供所有投递渠道复用。
	return messagecontent.AttachmentRef{AssetID: "attachment:" + strings.TrimPrefix(file.Digest, "sha256:"), Name: file.Name, MIME: file.MIME, Digest: file.Digest, AltText: file.Name}
}

// 完整生成本输出节点要求的格式后才投递，原始模型产物始终保留。
func (s *Server) attachWorkflowGeneratedFiles(runID, deliveryID string) error {
	run, delivery := s.workflowDeliverySnapshot(runID, deliveryID)
	if delivery == nil {
		return errors.New("Workflow delivery was not found")
	}
	validateMissingProjection := func(current *WorkflowDelivery) error {
		if current.Kind == "dingtalk" && current.ContentMode != "" && len(current.Parts) == 0 {
			return prepareWorkflowDingTalkProjection(current, true)
		}
		return nil
	}
	if delivery.Kind == "dingtalk" && delivery.ContentMode != "" && delivery.ContentMode != "files" && delivery.ContentMode != "both" {
		if len(delivery.Parts) > 0 {
			return nil
		}
		return s.updateWorkflowDelivery(runID, deliveryID, validateMissingProjection)
	}
	var generated []messagecontent.AttachmentRef
	for _, file := range run.GeneratedFiles {
		if file.NodeID != delivery.NodeID {
			continue
		}
		// 钉钉的新增格式合同仅附PDF；原始附件不在此过滤。
		if delivery.Kind == "dingtalk" && file.Format != "pdf" {
			continue
		}
		if file.Status != "ready" {
			return errors.New("Required workflow output files are not ready")
		}
		generated = append(generated, workflowGeneratedFileRef(file))
	}
	if len(generated) == 0 {
		if delivery.Kind == "dingtalk" && delivery.ContentMode != "" && len(delivery.Parts) == 0 {
			return s.updateWorkflowDelivery(runID, deliveryID, validateMissingProjection)
		}
		return nil
	}
	return s.updateWorkflowDelivery(runID, deliveryID, func(current *WorkflowDelivery) error {
		refs := []messagecontent.AttachmentRef{}
		seen := map[string]bool{}
		markdown, locale := current.Content, "und"
		if current.MessageContent != nil {
			markdown, locale = current.MessageContent.Markdown, current.MessageContent.Locale
			refs = append(refs, current.MessageContent.Attachments...)
		}
		for _, ref := range refs {
			seen[ref.AssetID] = true
		}
		changed := false
		for _, ref := range generated {
			if !seen[ref.AssetID] {
				refs = append(refs, ref)
				seen[ref.AssetID], changed = true, true
			}
		}
		if !changed {
			return validateMissingProjection(current)
		}
		canonical, err := messagecontent.New(messagecontent.ProducerWorkflow, locale, markdown, refs)
		if err != nil {
			return err
		}
		current.MessageContent = &canonical
		if current.Kind == "dingtalk" {
			return prepareWorkflowDingTalkProjection(current, true)
		}
		return nil
	})
}

func (s *Server) handleRetryWorkflowOutputFile(w http.ResponseWriter, r *http.Request) {
	run := s.workflowGeneratedFilesSnapshot(r.PathValue("id"))
	if !workflowRunOwnedByRequest(run, r) {
		writeAPIError(w, 404, "NOT_FOUND", "Workflow output file was not found")
		return
	}
	for _, file := range run.GeneratedFiles {
		if file.ID != r.PathValue("file") {
			continue
		}
		if file.Status != "failed" {
			writeAPIError(w, 409, "OUTPUT_NOT_RETRYABLE", "Workflow output file is active or already generated")
			return
		}
		if err := s.updateWorkflowGeneratedFile(run.ID, file.ID, func(current *WorkflowGeneratedFile, _ *WorkflowRun) error {
			if current.Status != "failed" || current.active {
				return errors.New("Workflow output file is already active")
			}
			current.Status, current.ErrorCode, current.Error = "pending", "", ""
			return nil
		}); err != nil {
			writeAPIError(w, 500, "OUTPUT_PERSIST_FAILED", "Workflow output retry could not be persisted")
			return
		}
		go func() {
			ctx := context.Background()
			s.generateWorkflowOutputFiles(ctx, run.ID, file.NodeID, false)
			// 只续行尚未发送的待处理目标；失败和未知目标仍需各自恢复。
			current := s.workflowGeneratedFilesSnapshot(run.ID)
			if current == nil {
				return
			}
			var transport workflowDeliveryTransport
			if s.instanceMgr != nil {
				transport = s.instanceMgr
			}
			for _, target := range current.Deliveries {
				if target.NodeID == file.NodeID && target.Status == "pending" {
					s.dispatchWorkflowDeliveryWithTransport(ctx, run.ID, target.ID, false, transport)
				}
			}
		}()
		writeJSON(w, http.StatusAccepted, workflowRunPublic(s.workflowGeneratedFilesSnapshot(run.ID)))
		return
	}
	writeAPIError(w, 404, "NOT_FOUND", "Workflow output file was not found")
}

func workflowRunOwnedByRequest(run *WorkflowRun, r *http.Request) bool {
	return workflowRunOwnerMatches(run, r) && !run.HistoryDeleted
}

func workflowRunOwnerMatches(run *WorkflowRun, r *http.Request) bool {
	return run != nil && (run.Request == nil || run.Request.UserID == "" || run.Request.UserID == sessionUserIDFromRequest(r))
}

// WorkflowDirectoryTarget区分服务机器路径与桌面客户端持有的目录引用。
type WorkflowDirectoryTarget struct {
	Location     string `json:"location"`
	Path         string `json:"path,omitempty"`
	DirectoryRef string `json:"directory_ref,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
}

type WorkflowDeliveryFile struct {
	ArtifactID  string `json:"artifact_id"`
	Name        string `json:"name"`
	Digest      string `json:"digest"`
	MIME        string `json:"mime,omitempty"`
	Size        int64  `json:"size,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
	Status      string `json:"status"`
	Path        string `json:"path,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (s *Server) workflowDirectoryFiles(run *WorkflowRun, delivery *WorkflowDelivery) ([]WorkflowDeliveryFile, error) {
	if delivery.MessageContent == nil || len(delivery.MessageContent.Attachments) == 0 {
		return nil, errors.New("Workflow directory delivery requires at least one output file")
	}
	var files []WorkflowDeliveryFile
	for _, ref := range delivery.MessageContent.Attachments {
		snapshot, ok := run.ArtifactSnapshots[ref.AssetID]
		if !ok || snapshot.Error != "" || snapshot.FilePath == "" || s.genStore == nil {
			return nil, errors.New("Workflow output file is unavailable")
		}
		file, err := s.genStore.Open(snapshot.FilePath)
		if err != nil {
			return nil, errors.New("Workflow output file could not be opened")
		}
		hash := sha256.New()
		size, readErr := io.Copy(hash, file)
		_ = file.Close()
		if readErr != nil || ref.Digest != "sha256:"+hex.EncodeToString(hash.Sum(nil)) {
			return nil, errors.New("Workflow output file no longer matches its frozen digest")
		}
		name := workflowOutputSafeName(ref.Name)
		generated := false
		for _, output := range run.GeneratedFiles {
			if workflowGeneratedFileRef(output).AssetID == ref.AssetID {
				generated = true
				break
			}
		}
		if !generated {
			name = workflowOutputSafeName(run.ID) + "-" + workflowOutputSafeName(delivery.NodeID) + "-" + workflowOutputSafeName(ref.AssetID) + "-" + name
		}
		files = append(files, WorkflowDeliveryFile{ArtifactID: ref.AssetID, Name: name, Digest: ref.Digest, MIME: ref.MIME,
			Size: size, DownloadURL: "/api/v1/files/generated/" + filepath.ToSlash(snapshot.FilePath), Status: "pending"})
	}
	return files, nil
}

func (s *Server) deliverWorkflowDirectory(ctx context.Context, runID, deliveryID string) {
	run, delivery := s.workflowDeliverySnapshot(runID, deliveryID)
	if delivery == nil || delivery.Directory == nil {
		return
	}
	files, err := s.workflowDirectoryFiles(run, delivery)
	if err != nil {
		code := "DELIVERY_DIRECTORY_FAILED"
		if delivery.MessageContent == nil || len(delivery.MessageContent.Attachments) == 0 {
			code = "OUTPUT_FILES_REQUIRED"
		}
		_ = s.updateWorkflowDelivery(runID, deliveryID, func(current *WorkflowDelivery) error {
			current.Status, current.ErrorCode, current.Error = "failed", code, err.Error()
			return nil
		})
		return
	}
	if err = s.updateWorkflowDelivery(runID, deliveryID, func(current *WorkflowDelivery) error {
		if len(current.Files) == 0 {
			current.Files = files
		}
		current.Status, current.Error, current.ErrorCode = "pending", "", ""
		if current.Directory.Location == "service" {
			current.Status = "sending"
		}
		return nil
	}); err != nil {
		return
	}
	if delivery.Directory.Location == "desktop" {
		return
	}
	_, delivery = s.workflowDeliverySnapshot(runID, deliveryID)
	if delivery.Directory.Location != "service" || !filepath.IsAbs(delivery.Directory.Path) {
		s.failWorkflowDirectory(runID, deliveryID, "DELIVERY_DIRECTORY_INVALID", "An absolute service directory is required")
		return
	}
	if err = os.MkdirAll(delivery.Directory.Path, 0o755); err != nil {
		s.failWorkflowDirectory(runID, deliveryID, "DELIVERY_DIRECTORY_FAILED", "Workflow output directory could not be created")
		return
	}
	for _, file := range delivery.Files {
		if file.Status == "delivered" {
			continue
		}
		if ctx.Err() != nil {
			s.failWorkflowDirectory(runID, deliveryID, "DELIVERY_DIRECTORY_FAILED", "Workflow directory delivery was interrupted")
			return
		}
		problem, code := s.saveWorkflowDirectoryFile(ctx, run, delivery.Directory.Path, file)
		if err = s.updateWorkflowDelivery(runID, deliveryID, func(current *WorkflowDelivery) error {
			for i := range current.Files {
				if current.Files[i].ArtifactID == file.ArtifactID {
					current.Files[i].Status, current.Files[i].Error = "delivered", problem
					current.Files[i].Path = file.Name
					if problem != "" {
						current.Files[i].Status, current.Status, current.Error, current.ErrorCode = "failed", "failed", problem, code
					}
				}
			}
			return nil
		}); err != nil || problem != "" {
			return
		}
	}
	_ = s.updateWorkflowDelivery(runID, deliveryID, func(current *WorkflowDelivery) error {
		current.Status, current.Error, current.ErrorCode = "delivered", "", ""
		return nil
	})
}

func (s *Server) failWorkflowDirectory(runID, deliveryID, code, problem string) {
	_ = s.updateWorkflowDelivery(runID, deliveryID, func(current *WorkflowDelivery) error {
		current.Status, current.ErrorCode, current.Error = "failed", code, problem
		return nil
	})
}

func (s *Server) saveWorkflowDirectoryFile(ctx context.Context, run *WorkflowRun, directory string, file WorkflowDeliveryFile) (string, string) {
	target := filepath.Join(directory, file.Name)
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.Open(target)
		if readErr != nil {
			return "Workflow output destination already exists and could not be checked", "DELIVERY_DIRECTORY_CONFLICT"
		}
		hash := sha256.New()
		_, readErr = io.Copy(hash, existing)
		_ = existing.Close()
		if readErr == nil && file.Digest == "sha256:"+hex.EncodeToString(hash.Sum(nil)) {
			return "", ""
		}
		return "Workflow output destination contains different content and was not overwritten", "DELIVERY_DIRECTORY_CONFLICT"
	}
	if err != nil {
		return "Workflow output file could not be created in the selected directory", "DELIVERY_DIRECTORY_FAILED"
	}
	complete := false
	defer func() {
		_ = out.Close()
		if !complete {
			_ = os.Remove(target)
		}
	}()
	snapshot := run.ArtifactSnapshots[file.ArtifactID]
	input, err := s.genStore.Open(snapshot.FilePath)
	if err != nil {
		return "Workflow output file could not be opened", "DELIVERY_DIRECTORY_FAILED"
	}
	defer input.Close()
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, hash), input)
	if err != nil || ctx.Err() != nil || file.Digest != "sha256:"+hex.EncodeToString(hash.Sum(nil)) {
		return "Workflow output file could not be saved with its frozen digest", "DELIVERY_DIRECTORY_FAILED"
	}
	if err = out.Sync(); err != nil {
		return "Workflow output file could not be flushed to disk", "DELIVERY_DIRECTORY_FAILED"
	}
	if err = out.Close(); err != nil {
		return "Workflow output file could not be closed", "DELIVERY_DIRECTORY_FAILED"
	}
	complete = true
	return "", ""
}

func (s *Server) handlePendingWorkflowDirectoryDeliveries(w http.ResponseWriter, r *http.Request) {
	clientID := r.URL.Query().Get("client_id")
	if clientID == "" {
		writeAPIError(w, 400, "DELIVERY_DIRECTORY_INVALID", "A desktop client identity is required")
		return
	}
	type pendingDelivery struct {
		RunID        string                 `json:"run_id"`
		DeliveryID   string                 `json:"delivery_id"`
		ClientID     string                 `json:"client_id"`
		DirectoryRef string                 `json:"directory_ref"`
		Files        []WorkflowDeliveryFile `json:"files"`
	}
	items := []pendingDelivery{}
	s.workflowStore.mu.RLock()
	defer s.workflowStore.mu.RUnlock()
	for _, run := range s.workflowStore.runs {
		if !workflowRunOwnerMatches(run, r) {
			continue
		}
		for _, delivery := range run.Deliveries {
			if delivery.Kind == "directory" && delivery.Status == "pending" && delivery.Directory != nil && delivery.Directory.Location == "desktop" && delivery.Directory.ClientID == clientID && len(delivery.Files) > 0 {
				items = append(items, pendingDelivery{RunID: run.ID, DeliveryID: delivery.ID, ClientID: clientID, DirectoryRef: delivery.Directory.DirectoryRef, Files: delivery.Files})
			}
		}
	}
	writeJSON(w, 200, map[string]any{"deliveries": items})
}

func (s *Server) handleAcknowledgeWorkflowDirectoryDelivery(w http.ResponseWriter, r *http.Request) {
	run, delivery := s.workflowDeliverySnapshot(r.PathValue("id"), r.PathValue("delivery"))
	if !workflowRunOwnerMatches(run, r) || delivery == nil || delivery.Kind != "directory" || delivery.Directory == nil || delivery.Directory.Location != "desktop" {
		writeAPIError(w, 404, "NOT_FOUND", "Workflow desktop delivery was not found")
		return
	}
	var req struct {
		ClientID     string                 `json:"client_id"`
		DirectoryRef string                 `json:"directory_ref"`
		Status       string                 `json:"status"`
		Files        []WorkflowDeliveryFile `json:"files"`
		Error        string                 `json:"error,omitempty"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.ClientID != delivery.Directory.ClientID || req.DirectoryRef != delivery.Directory.DirectoryRef || (req.Status != "delivered" && req.Status != "failed") {
		writeAPIError(w, 400, "DELIVERY_DESKTOP_ACK_INVALID", "Workflow desktop acknowledgement does not match its target")
		return
	}
	if req.Status == "delivered" && !workflowDirectoryAckMatches(delivery.Files, req.Files, delivery.Status == "delivered") {
		writeAPIError(w, 409, "DELIVERY_DESKTOP_ACK_INVALID", "Workflow desktop acknowledgement does not match the output files")
		return
	}
	if delivery.Status == "delivered" {
		if req.Status != "delivered" {
			writeAPIError(w, 409, "DELIVERY_DESKTOP_ACK_INVALID", "Workflow desktop delivery is already saved")
			return
		}
		writeJSON(w, 200, workflowRunPublic(run))
		return
	}
	if delivery.Status != "pending" {
		writeAPIError(w, 409, "DELIVERY_DESKTOP_ACK_INVALID", "Workflow desktop delivery is not pending")
		return
	}
	if err := s.updateWorkflowDelivery(run.ID, delivery.ID, func(current *WorkflowDelivery) error {
		if current.Status == "delivered" && req.Status == "delivered" && workflowDirectoryAckMatches(current.Files, req.Files, true) {
			return nil
		}
		if current.Status != "pending" {
			return errors.New("Workflow desktop delivery is not pending")
		}
		current.Status, current.Error, current.ErrorCode = req.Status, "", ""
		if req.Status == "failed" {
			current.Error, current.ErrorCode = "Workflow output files could not be saved on the desktop", "DELIVERY_DIRECTORY_FAILED"
		} else {
			for i := range current.Files {
				for _, received := range req.Files {
					if received.ArtifactID == current.Files[i].ArtifactID {
						current.Files[i].Path, current.Files[i].Status, current.Files[i].Error = received.Path, "delivered", ""
					}
				}
			}
		}
		return nil
	}); err != nil {
		writeAPIError(w, 500, "DELIVERY_DIRECTORY_FAILED", "Workflow desktop acknowledgement could not be persisted")
		return
	}
	writeJSON(w, 200, workflowRunPublic(s.workflowGeneratedFilesSnapshot(run.ID)))
}

func workflowDirectoryAckMatches(expected, received []WorkflowDeliveryFile, requireSamePath bool) bool {
	if len(expected) == 0 || len(expected) != len(received) {
		return false
	}
	seen := map[string]bool{}
	for _, file := range received {
		if seen[file.ArtifactID] || file.Path == "" || filepath.Base(file.Path) != file.Path || strings.ContainsAny(file.Path, `/\\`) || file.Path == "." || file.Path == ".." {
			return false
		}
		seen[file.ArtifactID] = true
		matched := false
		for _, prior := range expected {
			if prior.ArtifactID == file.ArtifactID && prior.Name == file.Name && prior.Digest == file.Digest && (!requireSamePath || prior.Path == file.Path) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
