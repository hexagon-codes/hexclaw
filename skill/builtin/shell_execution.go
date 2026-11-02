package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hexagon-codes/hexclaw/skill"
	"github.com/hexagon-codes/toolkit/os/hostprocess"
)

const shellSummaryBytes = 64 * 1024

// ShellRequest 的环境覆盖和输入只用于执行，不复制到日志或结果摘要。
type ShellRequest struct {
	Action           string
	Executable       string
	Args             []string
	Command          string
	WorkingDirectory string
	Env              map[string]string
	Stdin            *string
	StdinFile        string
	Timeout          time.Duration
}

// ShellExecution 保存原始进程终态；stdout/stderr 为摘要，完整数据由产物引用追溯。
type ShellExecution struct {
	Executable                 string         `json:"executable"`
	Args                       []string       `json:"args,omitempty"`
	Cwd                        string         `json:"cwd"`
	ExecutionHost              string         `json:"execution_host"`
	PID                        int            `json:"pid,omitempty"`
	ExitCode                   int            `json:"exit_code"`
	Outcome                    string         `json:"outcome"`
	Started                    bool           `json:"started"`
	Settled                    bool           `json:"settled"`
	BackgroundProcessesRunning bool           `json:"background_processes_running"`
	Stdout                     string         `json:"stdout"`
	Stderr                     string         `json:"stderr"`
	StdoutBytes                int64          `json:"stdout_bytes"`
	StderrBytes                int64          `json:"stderr_bytes"`
	StdoutTruncated            bool           `json:"stdout_truncated"`
	StderrTruncated            bool           `json:"stderr_truncated"`
	StdoutArtifact             map[string]any `json:"stdout_artifact,omitempty"`
	StderrArtifact             map[string]any `json:"stderr_artifact,omitempty"`
	StdoutRetainedPath         string         `json:"stdout_retained_path,omitempty"`
	StderrRetainedPath         string         `json:"stderr_retained_path,omitempty"`
	OutputPersistenceError     string         `json:"output_persistence_error,omitempty"`
}

// ExternalExecutionFacts 仅投影同次进程和受控输出文件的事实，不复制命令、参数、环境或输入。
// 保留路径属于原执行 owner；本方法不授予任意文件的 HTTP 访问能力。
func (r *ShellExecution) ExternalExecutionFacts() map[string]any {
	if r == nil {
		return nil
	}
	reason := ""
	switch {
	case r.BackgroundProcessesRunning:
		reason = "background_processes"
	case !r.Started:
		reason = "not_started"
	case !r.Settled:
		reason = "process_state_unknown"
	case r.Outcome == "timeout":
		reason = "timeout"
	case r.Outcome == "cancelled":
		reason = "cancelled"
	case r.OutputPersistenceError != "":
		reason = "output_persistence"
	case r.ExitCode != 0:
		reason = "nonzero_exit"
	}
	facts := map[string]any{
		"exit_code": r.ExitCode, "outcome": r.Outcome, "reason": reason,
		"execution_host": r.ExecutionHost, "cwd": r.Cwd,
		"started": r.Started, "settled": r.Settled,
		"stdout_bytes": r.StdoutBytes, "stderr_bytes": r.StderrBytes,
		"stdout_truncated": r.StdoutTruncated, "stderr_truncated": r.StderrTruncated,
		"background_processes_running": r.BackgroundProcessesRunning,
		"cancelled":                    r.Outcome == "cancelled", "timed_out": r.Outcome == "timeout",
		"output_persistence_failed": r.OutputPersistenceError != "",
	}
	for _, output := range []struct {
		name string
		ref  map[string]any
		path string
	}{
		{"stdout", r.StdoutArtifact, r.StdoutRetainedPath},
		{"stderr", r.StderrArtifact, r.StderrRetainedPath},
	} {
		if len(output.ref) > 0 {
			ref := make(map[string]any)
			for _, key := range []string{"id", "artifact_id", "name", "display_name", "mime", "path", "digest", "size", "url"} {
				if value, ok := output.ref[key]; ok {
					ref[key] = value
				}
			}
			facts[output.name+"_artifact"] = ref
		}
		if output.path != "" {
			facts[output.name+"_retained_path"] = output.path
		}
	}
	return facts
}

func parseShellRequest(args map[string]any) (ShellRequest, error) {
	r := ShellRequest{Action: "run"}
	for name, target := range map[string]*string{
		"action": &r.Action, "executable": &r.Executable, "command": &r.Command,
		"cwd": &r.WorkingDirectory, "stdin_file": &r.StdinFile,
	} {
		if value, exists := args[name]; exists {
			text, ok := value.(string)
			if !ok || strings.ContainsRune(text, 0) {
				return r, fmt.Errorf("%s must be a string without NUL bytes", name)
			}
			*target = text
		}
	}
	if r.Action == "" {
		r.Action = "run"
	}
	if r.Action != "run" && r.Action != "discover" {
		return r, fmt.Errorf("unsupported shell action %q", r.Action)
	}
	if r.Command == "" {
		if query, ok := args["query"].(string); ok {
			r.Command = query
		}
	}
	r.Command = extractShellCommand(r.Command)
	if strings.ContainsRune(r.Command, 0) {
		return r, fmt.Errorf("command must not contain NUL bytes")
	}
	if r.Executable != "" && r.Command != "" {
		return r, fmt.Errorf("executable and command are mutually exclusive")
	}
	if r.Action == "discover" && r.Command != "" {
		return r, fmt.Errorf("discover requires executable instead of command")
	}
	switch argv := args["args"].(type) {
	case nil:
	case []string:
		r.Args = append([]string(nil), argv...)
	case []any:
		for _, value := range argv {
			arg, ok := value.(string)
			if !ok {
				return r, fmt.Errorf("args must contain only strings")
			}
			r.Args = append(r.Args, arg)
		}
	default:
		return r, fmt.Errorf("args must be an array of strings")
	}
	for _, arg := range r.Args {
		if strings.ContainsRune(arg, 0) {
			return r, fmt.Errorf("arguments must not contain NUL bytes")
		}
	}
	if r.Command != "" && len(r.Args) != 0 {
		return r, fmt.Errorf("args requires executable")
	}
	if value, exists := args["stdin"]; exists {
		text, ok := value.(string)
		if !ok {
			return r, fmt.Errorf("stdin must be a string")
		}
		r.Stdin = &text
	}
	if r.Stdin != nil && r.StdinFile != "" {
		return r, fmt.Errorf("stdin and stdin_file are mutually exclusive")
	}
	r.Env = make(map[string]string)
	switch environment := args["env"].(type) {
	case nil:
	case map[string]string:
		for key, value := range environment {
			r.Env[key] = value
		}
	case map[string]any:
		for key, value := range environment {
			text, ok := value.(string)
			if !ok {
				return r, fmt.Errorf("environment values must be strings")
			}
			r.Env[key] = text
		}
	default:
		return r, fmt.Errorf("env must be an object with string values")
	}
	for key, value := range r.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return r, fmt.Errorf("invalid environment override")
		}
	}
	if value, exists := args["timeout_seconds"]; exists {
		var seconds float64
		switch number := value.(type) {
		case float64:
			seconds = number
		case float32:
			seconds = float64(number)
		case int:
			seconds = float64(number)
		case int64:
			seconds = float64(number)
		case json.Number:
			var err error
			seconds, err = number.Float64()
			if err != nil {
				return r, fmt.Errorf("invalid timeout_seconds")
			}
		default:
			return r, fmt.Errorf("timeout_seconds must be a positive number")
		}
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds >= float64(math.MaxInt64)/float64(time.Second) {
			return r, fmt.Errorf("timeout_seconds is outside the supported positive duration range")
		}
		r.Timeout = time.Duration(seconds * float64(time.Second))
		if r.Timeout <= 0 {
			return r, fmt.Errorf("timeout_seconds must be at least one nanosecond")
		}
	}
	return r, nil
}

func (s *ShellSkill) executeRequest(ctx context.Context, request ShellRequest) (*skill.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.WorkingDirectory == "" {
		request.WorkingDirectory = "."
	}
	directory, err := filepath.Abs(request.WorkingDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("access working directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("working directory is not a directory")
	}
	environment := shellEnvironment(request.Env)
	program, argv := request.Executable, request.Args
	if request.Command != "" {
		program, argv = platformShellCommand(request.Command, environment)
	}
	resolved, err := resolveShellExecutable(program, directory, environment)
	if err != nil {
		return nil, err
	}
	host, err := os.Hostname()
	if err != nil {
		host = runtime.GOOS
	}
	result := &ShellExecution{Executable: resolved, Args: append([]string(nil), argv...), Cwd: directory, ExecutionHost: host, ExitCode: -1, Outcome: "failure"}
	if request.Action == "discover" {
		result.Outcome = "success"
		return &skill.Result{Content: "Program located: " + resolved + ". Run its documented help or task command to verify the required capability.", Data: result, Metadata: map[string]string{"status": "ok"}}, nil
	}
	if s.outputDirectory != "" {
		if err := os.MkdirAll(s.outputDirectory, 0700); err != nil {
			return nil, fmt.Errorf("create shell output root: %w", err)
		}
	}
	outputDirectory, err := os.MkdirTemp(s.outputDirectory, "hexclaw-shell-")
	if err != nil {
		return nil, fmt.Errorf("create shell output directory: %w", err)
	}
	stdout, err := os.OpenFile(filepath.Join(outputDirectory, "stdout.txt"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		os.Remove(outputDirectory)
		return nil, fmt.Errorf("create stdout file: %w", err)
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(outputDirectory, "stderr.txt"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		stdout.Close()
		os.Remove(stdout.Name())
		os.Remove(outputDirectory)
		return nil, fmt.Errorf("create stderr file: %w", err)
	}
	defer stderr.Close()
	stdin, inputErr := openShellInput(request, directory, outputDirectory)
	if inputErr != nil {
		stdout.Close()
		stderr.Close()
		os.Remove(stdout.Name())
		os.Remove(stderr.Name())
		os.Remove(outputDirectory)
		return nil, inputErr
	}
	defer stdin.Close()
	timeout := request.Timeout
	if timeout == 0 {
		timeout = s.timeout
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	process, runErr := hostprocess.Run(ctx, hostprocess.Spec{
		Executable: resolved, Args: argv, Dir: directory, Env: environment,
		Stdin: stdin, Stdout: stdout, Stderr: stderr,
	})
	result.Started, result.Settled, result.PID, result.ExitCode = process.Started, process.Settled, process.PID, process.ExitCode
	stdin.Close()
	if request.Stdin != nil {
		os.Remove(stdin.Name())
	}
	result.Outcome = "success"
	if runErr != nil {
		result.Outcome = "failure"
	}
	if errors.Is(runErr, context.DeadlineExceeded) {
		result.Outcome = "timeout"
	} else if errors.Is(runErr, context.Canceled) {
		result.Outcome = "cancelled"
	} else if process.Started && !process.Settled {
		result.Outcome = "unknown"
	}
	result.BackgroundProcessesRunning = process.BackgroundProcessesRunning
	if process.BackgroundProcessesRunning {
		result.Outcome = "unknown"
	}
	var persistenceErrors []error
	result.Stdout, result.StdoutBytes, result.StdoutTruncated, result.StdoutArtifact, result.StdoutRetainedPath, err = s.sealShellOutput(ctx, stdout, "stdout", !process.BackgroundProcessesRunning)
	if err != nil {
		persistenceErrors = append(persistenceErrors, err)
	}
	result.Stderr, result.StderrBytes, result.StderrTruncated, result.StderrArtifact, result.StderrRetainedPath, err = s.sealShellOutput(ctx, stderr, "stderr", !process.BackgroundProcessesRunning)
	if err != nil {
		persistenceErrors = append(persistenceErrors, err)
	}
	stdout.Close()
	stderr.Close()
	if result.StdoutRetainedPath == "" {
		os.Remove(stdout.Name())
	}
	if result.StderrRetainedPath == "" {
		os.Remove(stderr.Name())
	}
	os.Remove(outputDirectory) // 有保留输出时目录非空，不能移除。
	persistenceErr := errors.Join(persistenceErrors...)
	if persistenceErr != nil {
		result.OutputPersistenceError = persistenceErr.Error()
	}
	detail := skill.ToolExecutionDetail{Phase: "host_process", Action: "run", Outcome: "success", TargetHost: host}
	if runErr != nil {
		detail.Outcome, detail.Reason = "failure", result.Outcome
		if process.Started && (!process.Settled || result.Outcome == "timeout" || result.Outcome == "cancelled") {
			detail.Outcome = "unknown"
		}
	}
	if persistenceErr != nil && detail.Outcome != "unknown" {
		detail.Outcome, detail.Reason = "failure", "output_persistence"
	}
	if process.BackgroundProcessesRunning {
		detail.Outcome, detail.Reason = "unknown", "background_processes"
	}
	status := "ok"
	if detail.Outcome != "success" {
		status = "error"
	}
	content := fmt.Sprintf("Process outcome: %s; exit code: %d.\nstdout:\n%s\nstderr:\n%s", result.Outcome, result.ExitCode, result.Stdout, result.Stderr)
	if result.StdoutTruncated || result.StderrTruncated {
		content += "\nOutput summaries are truncated; the complete streams are retained in the execution result."
	}
	for _, stream := range []struct {
		name string
		ref  map[string]any
		path string
	}{
		{"stdout", result.StdoutArtifact, result.StdoutRetainedPath},
		{"stderr", result.StderrArtifact, result.StderrRetainedPath},
	} {
		if len(stream.ref) > 0 {
			if reference, err := json.Marshal(stream.ref); err == nil {
				content += "\nComplete " + stream.name + " artifact: " + string(reference)
			}
		} else if stream.path != "" {
			if process.BackgroundProcessesRunning {
				content += "\nLive " + stream.name + " file (not final): " + stream.path
			} else {
				content += "\nComplete " + stream.name + " retained locally: " + stream.path
			}
		}
	}
	if result.StdoutRetainedPath != "" || result.StderrRetainedPath != "" {
		content += "\nOutput is retained locally; attachment delivery has not been confirmed."
	}
	response := &skill.Result{Content: content, Data: result, Detail: &detail, Metadata: map[string]string{"status": status}}
	if process.BackgroundProcessesRunning {
		runErr = errors.Join(runErr, fmt.Errorf("root process exited with code %d; background processes remain and output is not final", result.ExitCode))
	}
	if executionErr := errors.Join(runErr, persistenceErr); executionErr != nil {
		return response, &skill.ExecutionError{Detail: detail, Err: executionErr}
	}
	return response, nil
}

func openShellInput(request ShellRequest, directory, outputDirectory string) (*os.File, error) {
	if request.StdinFile != "" {
		path := request.StdinFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(directory, path)
		}
		input, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open stdin file: %w", err)
		}
		info, err := input.Stat()
		if err != nil || !info.Mode().IsRegular() {
			input.Close()
			return nil, fmt.Errorf("stdin_file must be a readable regular file")
		}
		return input, nil
	}
	if request.Stdin == nil {
		return os.Open(os.DevNull)
	}
	input, err := os.OpenFile(filepath.Join(outputDirectory, "stdin.txt"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("create stdin file: %w", err)
	}
	if _, err := io.WriteString(input, *request.Stdin); err != nil {
		input.Close()
		os.Remove(input.Name())
		return nil, fmt.Errorf("write stdin file: %w", err)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		input.Close()
		os.Remove(input.Name())
		return nil, fmt.Errorf("rewind stdin file: %w", err)
	}
	return input, nil
}

func (s *ShellSkill) sealShellOutput(ctx context.Context, file *os.File, stream string, final bool) (string, int64, bool, map[string]any, string, error) {
	retainedPath := file.Name()
	if err := file.Sync(); err != nil {
		return "", 0, false, nil, retainedPath, fmt.Errorf("sync %s output: %w", stream, err)
	}
	info, err := file.Stat()
	if err != nil {
		return "", 0, false, nil, retainedPath, fmt.Errorf("stat %s output: %w", stream, err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", info.Size(), false, nil, retainedPath, fmt.Errorf("rewind %s output: %w", stream, err)
	}
	summary, err := io.ReadAll(io.LimitReader(file, shellSummaryBytes))
	if err != nil {
		return string(summary), info.Size(), info.Size() > int64(len(summary)), nil, retainedPath, fmt.Errorf("read %s summary: %w", stream, err)
	}
	// UTF-8 摘要不在一个多字节字符中间截断，二进制完整流仍原样保留。
	for len(summary) > 0 && !utf8.Valid(summary) {
		if info.Size() <= shellSummaryBytes || len(summary) < shellSummaryBytes-utf8.UTFMax {
			break
		}
		summary = summary[:len(summary)-1]
	}
	truncated := info.Size() > int64(len(summary))
	if !final {
		return string(summary), info.Size(), truncated, nil, retainedPath, nil
	}
	if info.Size() == 0 {
		return string(summary), 0, false, nil, "", nil
	}
	if s.outputStore == nil {
		return string(summary), info.Size(), truncated, nil, retainedPath, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return string(summary), info.Size(), truncated, nil, retainedPath, fmt.Errorf("rewind %s artifact: %w", stream, err)
	}
	ref, err := s.outputStore.SaveShellOutput(ctx, skill.CurrentInvocation(ctx), stream, file)
	if err != nil {
		return string(summary), info.Size(), truncated, nil, retainedPath, fmt.Errorf("persist %s output: %w", stream, err)
	}
	if len(ref) == 0 {
		return string(summary), info.Size(), truncated, nil, retainedPath, fmt.Errorf("persist %s output returned no artifact reference", stream)
	}
	return string(summary), info.Size(), truncated, ref, "", nil
}

func shellEnvironment(overrides map[string]string) []string {
	environment := map[string]string{}
	names := map[string]string{}
	for _, item := range os.Environ() {
		// Windows 的驱动器当前目录键以等号开始。
		start := 0
		if strings.HasPrefix(item, "=") {
			start = 1
		}
		index := strings.IndexByte(item[start:], '=')
		if index < 0 {
			continue
		}
		index += start
		key := item[:index]
		canonical := key
		if runtime.GOOS == "windows" {
			canonical = strings.ToUpper(key)
		}
		names[canonical], environment[canonical] = key, item[index+1:]
	}
	for key, value := range overrides {
		canonical := key
		if runtime.GOOS == "windows" {
			canonical = strings.ToUpper(key)
		}
		names[canonical], environment[canonical] = key, value
	}
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, names[key]+"="+environment[key])
	}
	return result
}

func shellEnvironmentValue(environment []string, key string) string {
	for _, item := range environment {
		name, value, found := strings.Cut(item, "=")
		if found && (name == key || runtime.GOOS == "windows" && strings.EqualFold(name, key)) {
			return value
		}
	}
	return ""
}

func resolveShellExecutable(program, directory string, environment []string) (string, error) {
	if strings.ContainsAny(program, "/\\") || filepath.IsAbs(program) {
		if !filepath.IsAbs(program) {
			program = filepath.Join(directory, program)
		}
		resolved, err := exec.LookPath(program)
		if err != nil {
			return "", fmt.Errorf("program is not available: %w", err)
		}
		return filepath.Abs(resolved)
	}
	for _, path := range filepath.SplitList(shellEnvironmentValue(environment, "PATH")) {
		if !filepath.IsAbs(path) {
			path = filepath.Join(directory, path)
		}
		candidate := filepath.Join(path, program)
		if resolved, err := exec.LookPath(candidate); err == nil {
			return filepath.Abs(resolved)
		}
	}
	return "", fmt.Errorf("program %q was not found on the effective PATH", program)
}
