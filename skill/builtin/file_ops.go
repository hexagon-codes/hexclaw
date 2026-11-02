package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/skill"
)

// FileOpsSkill 提供文件读写、精确编辑与原文件附件登记。
// 读写编辑沿用原目录合同；附件登记另可复用连接器已有的只读授权。
type FileOpsSkill struct {
	workspace      string // allowed base directory
	artifactWriter func(context.Context, string, string, io.Reader) (map[string]any, error)
	fileAccess     *FileAccessBroker
}

// NewFileOpsSkill creates a file operations skill.
// workspace is the root directory the agent may operate on (e.g. /tmp/hexclaw-workspace).
func NewFileOpsSkill(workspace string) *FileOpsSkill {
	return &FileOpsSkill{workspace: workspace}
}

// SetArtifactWriter 在启动装配阶段注入同一产物服务，不重新渲染已生成文件。
func (s *FileOpsSkill) SetArtifactWriter(writer func(context.Context, string, string, io.Reader) (map[string]any, error)) {
	s.artifactWriter = writer
}

// SetFileAccessBroker 只为 attach 复用连接器的已有只读授权，不扩大读写编辑范围。
func (s *FileOpsSkill) SetFileAccessBroker(broker *FileAccessBroker) { s.fileAccess = broker }

func (s *FileOpsSkill) Name() string        { return "file_ops" }
func (s *FileOpsSkill) Description() string { return "Read, write, edit, or attach existing files" }
func (s *FileOpsSkill) Match(_ string) bool { return false }

func (s *FileOpsSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("file_ops",
		"Read, write, edit, or attach files. Relative paths use the workspace; full_access also permits absolute paths and paths outside the workspace according to the OS account. Actions: read (with optional offset/limit), write (create/overwrite), edit (precise string replacement), attach (register an existing file as a response artifact without rendering or changing its original bytes; also accepts existing connector read authorization). Use attach for CLI-generated PDF, Office, image, archive, or other file outputs; artifact registration alone does not confirm final response delivery.",
		&llm.Schema{
			Type: "object",
			Properties: map[string]*llm.Schema{
				"action":     {Type: "string", Description: "Action: read, write, edit, attach"},
				"path":       {Type: "string", Description: "File path relative to workspace, or an OS-account-accessible path in full_access"},
				"content":    {Type: "string", Description: "File content (for write) or new_string (for edit)"},
				"old_string": {Type: "string", Description: "String to replace (for edit, must match exactly once)"},
				"offset":     {Type: "integer", Description: "Start line (for read, 1-based, default 1)"},
				"limit":      {Type: "integer", Description: "Max lines to read (default 100)"},
			},
			Required: []string{"action", "path"},
		})
}

func (s *FileOpsSkill) Execute(ctx context.Context, args map[string]any) (*skill.Result, error) {
	action, _ := args["action"].(string)
	relPath, _ := args["path"].(string)

	if relPath == "" {
		return nil, fmt.Errorf("path is required")
	}
	if action == "attach" {
		return s.attachFile(ctx, relPath)
	}

	var absPath string
	var err error
	if skill.FullAccess(ctx) {
		absPath, err = resolveToolPath(ctx, s.workspace, relPath)
	} else {
		absPath, err = s.safePath(relPath)
	}
	if err != nil {
		return nil, err
	}

	switch action {
	case "read":
		return s.doRead(absPath, args)
	case "write":
		content, _ := args["content"].(string)
		return s.doWrite(absPath, content)
	case "edit":
		oldStr, _ := args["old_string"].(string)
		newStr, _ := args["content"].(string)
		return s.doEdit(absPath, oldStr, newStr)
	default:
		return nil, fmt.Errorf("unknown action %q (use read, write, edit, attach)", action)
	}
}

func (s *FileOpsSkill) attachFile(ctx context.Context, path string) (*skill.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.artifactWriter == nil {
		return nil, fmt.Errorf("file attachment storage is not configured")
	}
	resolved, err := resolveToolPath(ctx, s.workspace, path)
	if err != nil {
		if s.fileAccess == nil {
			return nil, err
		}
		candidate := path
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(s.workspace, candidate)
		}
		resolved, err = s.fileAccess.AuthorizeExistingContext(ctx, candidate)
		if err != nil {
			return nil, err
		}
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, fmt.Errorf("open file for attachment: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect file for attachment: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("attachment path must be a regular file")
	}
	name := filepath.Base(path)
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if contentType == "" {
		header := make([]byte, 512)
		n, readErr := file.Read(header)
		if readErr != nil && readErr != io.EOF {
			return nil, fmt.Errorf("inspect attachment content type: %w", readErr)
		}
		contentType = http.DetectContentType(header[:n])
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("rewind attachment file: %w", err)
		}
	}
	ref, err := s.artifactWriter(ctx, name, contentType, file)
	if err != nil {
		detail := skill.ToolExecutionDetail{Phase: "artifact_registration", Outcome: "failure", Reason: "artifact_registration_failed", Action: "attach"}
		return &skill.Result{Detail: &detail}, &skill.ExecutionError{Detail: detail, Err: fmt.Errorf("register file attachment: %w", err)}
	}
	artifactID, _ := ref["id"].(string)
	if artifactID == "" {
		artifactID, _ = ref["artifact_id"].(string)
	}
	artifactPath, _ := ref["path"].(string)
	artifactURL, _ := ref["url"].(string)
	if artifactID == "" || artifactPath == "" && artifactURL == "" {
		return nil, fmt.Errorf("artifact storage returned no retrievable file reference")
	}
	encoded, err := json.Marshal(ref)
	if err != nil {
		return nil, fmt.Errorf("encode file attachment reference: %w", err)
	}
	detail := skill.ToolExecutionDetail{Phase: "artifact_registration", Outcome: "success", Action: "attach"}
	return &skill.Result{
		Content: "File artifact registered with its original bytes: " + string(encoded),
		Data:    ref, Detail: &detail, Metadata: map[string]string{"status": "ok", "action": "attach", "name": name, "mime": contentType},
	}, nil
}

func (s *FileOpsSkill) safePath(relPath string) (string, error) {
	// Block absolute paths and traversal
	if filepath.IsAbs(relPath) || strings.Contains(relPath, "..") {
		return "", fmt.Errorf("path must be relative and cannot contain '..'")
	}
	abs := filepath.Join(s.workspace, relPath)
	resolved, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to resolve path: %w", err)
	}
	if err == nil {
		wsResolved, _ := filepath.EvalSymlinks(s.workspace)
		// Require a separator boundary so a sibling like "/x/ws-evil" is not
		// accepted as inside "/x/ws".
		if resolved != wsResolved && !strings.HasPrefix(resolved, wsResolved+string(filepath.Separator)) {
			return "", fmt.Errorf("path escapes workspace (symlink?)")
		}
	}
	return abs, nil
}

func (s *FileOpsSkill) doRead(absPath string, args map[string]any) (*skill.Result, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read failed: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	offset := intArg(args, "offset", 1) - 1 // 1-based to 0-based
	limit := intArg(args, "limit", 100)

	if offset < 0 {
		offset = 0
	}
	if offset >= len(lines) {
		return &skill.Result{Content: "(empty: offset beyond file end)"}, nil
	}
	end := offset + limit
	if end > len(lines) {
		end = len(lines)
	}

	var sb strings.Builder
	for i := offset; i < end; i++ {
		sb.WriteString(fmt.Sprintf("%d\t%s\n", i+1, lines[i]))
	}
	return &skill.Result{
		Content: sb.String(),
		Metadata: map[string]string{
			"path":        absPath,
			"total_lines": fmt.Sprintf("%d", len(lines)),
		},
	}, nil
}

func (s *FileOpsSkill) doWrite(absPath, content string) (*skill.Result, error) {
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create directory failed: %w", err)
	}
	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("write failed: %w", err)
	}
	return &skill.Result{
		Content: fmt.Sprintf("Written %d bytes to %s", len(content), absPath),
	}, nil
}

func (s *FileOpsSkill) doEdit(absPath, oldStr, newStr string) (*skill.Result, error) {
	if oldStr == "" {
		return nil, fmt.Errorf("old_string is required for edit")
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("read failed: %w", err)
	}
	text := string(data)
	count := strings.Count(text, oldStr)
	if count == 0 {
		return nil, fmt.Errorf("old_string not found in file")
	}
	if count > 1 {
		return nil, fmt.Errorf("old_string matches %d times (must match exactly once)", count)
	}
	result := strings.Replace(text, oldStr, newStr, 1)
	if err := os.WriteFile(absPath, []byte(result), 0644); err != nil {
		return nil, fmt.Errorf("write failed: %w", err)
	}
	return &skill.Result{
		Content: fmt.Sprintf("Replaced 1 occurrence in %s", absPath),
	}, nil
}

func intArg(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return def
	}
}
