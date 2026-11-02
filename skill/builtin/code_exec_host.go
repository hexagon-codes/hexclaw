package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hexagon-codes/hexclaw/skill"
)

// executeHost 仅由运行时全权限进入；保留代码执行的输入与回执合同，进程执行复用 Shell。
func (s *CodeExecSkill) executeHost(ctx context.Context, req codeExecRequest, input map[string]any) (*skill.Result, error) {
	s.mu.RLock()
	cfg := cloneCodeExecPolicyState(s.policy).cfg
	base := cfg.Workspace
	execute := s.hostExecutor
	s.mu.RUnlock()
	if strings.TrimSpace(base) == "" {
		return nil, errors.New("code execution workspace is required")
	}
	if execute == nil {
		execute = NewShellSkill().Execute
	}
	runID := newCodeExecRunID()
	root := filepath.Join(base, "runs", runID)
	work := filepath.Join(root, "work")
	artifactDir := filepath.Join(root, "artifacts")
	for _, dir := range []string{work, artifactDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("create host code execution directory: %w", err)
		}
	}
	cwd := base
	if req.ProjectRoot != "" {
		var err error
		cwd, err = resolveToolPath(ctx, base, req.ProjectRoot)
		if err != nil {
			return nil, err
		}
	}
	var command []string
	var err error
	switch req.Mode {
	case "snippet":
		cwd = work
		command, err = prepareCodeExecCommand(ctx, req, codeExecRun{Base: base, Workspace: work})
	case "module":
		cwd = work
		if len(req.Command) > 0 {
			for _, file := range req.Files {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				relative, pathErr := cleanRunRelativePath(file.Path)
				if pathErr != nil {
					return nil, pathErr
				}
				destination := filepath.Join(work, relative)
				if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
					return nil, fmt.Errorf("create module directory: %w", err)
				}
				if err := os.WriteFile(destination, []byte(file.Content), 0600); err != nil {
					return nil, fmt.Errorf("write module file: %w", err)
				}
			}
			command = normalizeCommandRuntime(req.Command)
		} else {
			command, err = prepareModuleCommand(ctx, req, codeExecRun{Base: base, Workspace: work})
		}
	case "file":
		if len(req.Command) > 0 {
			command = normalizeCommandRuntime(req.Command)
			break
		}
		entry := req.EntryPoint
		if entry == "" {
			entry, err = inferRunnableFile(ctx, cwd, req.Language)
		} else {
			entry, err = resolveToolPath(ctx, cwd, entry)
		}
		if err != nil {
			return nil, err
		}
		if entry == "" {
			return nil, errors.New("entrypoint or command is required for mode=file")
		}
		lang := req.Language
		if lang == "" {
			lang = languageFromPath(entry)
		}
		_, program, arguments, runnerErr := runnerForLanguage(lang)
		if runnerErr != nil {
			return nil, runnerErr
		}
		command = append([]string{program}, append(arguments, entry)...)
		if req.ProjectRoot == "" {
			cwd = filepath.Dir(entry)
		}
	case "project":
		command = normalizeCommandRuntime(req.Command)
		if len(command) == 0 {
			command, err = defaultProjectCommand(ctx, cwd)
		}
	}
	if err != nil {
		return nil, err
	}
	if len(command) == 0 {
		return nil, errors.New("host code execution command is required")
	}
	env := map[string]any{}
	if original, ok := input["env"].(map[string]any); ok {
		for key, value := range original {
			env[key] = value
		}
	}
	env["HEXCLAW_RUN_ID"] = runID
	env["HEXCLAW_ARTIFACT_DIR"] = artifactDir
	args := map[string]any{"executable": command[0], "args": command[1:], "cwd": cwd, "env": env}
	for _, key := range []string{"stdin", "stdin_file", "timeout_seconds"} {
		if value, ok := input[key]; ok {
			args[key] = value
		}
	}
	if req.Timeout > 0 {
		args["timeout_seconds"] = req.Timeout
	}
	policy := skill.CurrentExecutionPolicy(ctx)
	if policy.Profile != "full_access" {
		return nil, errors.New("execution profile changed before host dispatch; use the current execution profile")
	}
	result, executionErr := execute(ctx, args)
	if result == nil {
		return nil, executionErr
	}
	encoded, marshalErr := json.Marshal(result.Data)
	if marshalErr != nil {
		return result, errors.Join(executionErr, fmt.Errorf("encode host execution receipt: %w", marshalErr))
	}
	var report map[string]any
	if err := json.Unmarshal(encoded, &report); err != nil || report == nil {
		return result, errors.Join(executionErr, errors.New("host execution receipt is unavailable"))
	}
	status := "failed"
	switch report["outcome"] {
	case "success":
		status = "success"
	case "unknown", "cancelled", "timeout":
		status = "unknown"
	}
	report["run_id"], report["mode"], report["language"] = runID, req.Mode, req.Language
	report["status"], report["timeout"] = status, report["outcome"] == "timeout"
	report["filesystem_isolation"] = "host"
	report["policy_profile"], report["policy_revision"] = policy.Profile, policy.Revision
	report["paths"] = map[string]string{"run_root": root, "workspace": work, "cwd": cwd, "artifacts": artifactDir, "manifest": filepath.Join(root, "manifest.json")}
	stdout, _ := report["stdout"].(string)
	stderr, _ := report["stderr"].(string)
	stdoutBytes, _ := report["stdout_bytes"].(float64)
	stderrBytes, _ := report["stderr_bytes"].(float64)
	report["truncated"] = stdoutBytes > float64(len(stdout)) || stderrBytes > float64(len(stderr))
	if req.Artifacts && ctx.Err() == nil {
		budget := codeExecApplicationBudgetFor(cfg)
		artifacts, artifactErr := collectCodeExecArtifactsObserved(ctx, root, artifactDir, budget.MaxArtifactBytes, budget.MaxWorkspaceBytes, nil)
		report["artifacts"] = artifacts
		if artifactErr != nil {
			executionErr = errors.Join(executionErr, fmt.Errorf("collect host execution artifacts: %w", artifactErr))
			report["status"] = "failed"
		}
	}
	manifest, manifestErr := json.MarshalIndent(report, "", "  ")
	if manifestErr == nil {
		manifestErr = os.WriteFile(filepath.Join(root, "manifest.json"), manifest, 0600)
	}
	if manifestErr != nil {
		executionErr = errors.Join(executionErr, fmt.Errorf("persist host execution receipt: %w", manifestErr))
		report["status"] = "failed"
	}
	result.Data = report
	result.Content = stdout
	if stderr != "" {
		result.Content += "\n[stderr]\n" + stderr
	}
	result.Content += "\n[hexclaw_host_result]\n" + string(manifest)
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	result.Metadata["run_id"], result.Metadata["status"], result.Metadata["mode"] = runID, fmt.Sprint(report["status"]), req.Mode
	return result, executionErr
}
