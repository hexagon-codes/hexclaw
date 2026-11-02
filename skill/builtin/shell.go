package builtin

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/hexagon-codes/ai-core/llm"
	"github.com/hexagon-codes/hexclaw/skill"
)

// ShellSkill Shell 命令执行 Skill
//
// 结构化调用保留 argv，明确需要解释器语义时兼容原 command 和 /sh 入口。
// 完整输出流写入本次结果文件，摘要大小不限制实际输出的持久化。
type ShellSkill struct {
	timeout         time.Duration
	outputStore     ShellOutputStore
	outputDirectory string
}

// ShellOutputStore 将单个输出流接入既有产物存储及调用归属。
// ctx 保留原调用身份，可能已取消；实现负责使用有界的应用生命周期上下文收口。
type ShellOutputStore interface {
	SaveShellOutput(context.Context, skill.InvocationContext, string, io.Reader) (map[string]any, error)
}

type ShellOption func(*ShellSkill)

func WithShellOutputStore(store ShellOutputStore) ShellOption {
	return func(s *ShellSkill) { s.outputStore = store }
}

// SetOutputStore 在启动装配阶段注入统一产物服务，保持已注册实例的身份。
func (s *ShellSkill) SetOutputStore(store ShellOutputStore) { s.outputStore = store }

// WithShellOutputDirectory 设置私有输出暂存根；未配置时使用系统临时目录。
func WithShellOutputDirectory(directory string) ShellOption {
	return func(s *ShellSkill) { s.outputDirectory = directory }
}

// NewShellSkill 创建 Shell 执行 Skill
func NewShellSkill(options ...ShellOption) *ShellSkill {
	s := &ShellSkill{}
	for _, option := range options {
		option(s)
	}
	return s
}

func (s *ShellSkill) Name() string        { return "shell" }
func (s *ShellSkill) Description() string { return "执行 Shell 命令，返回命令输出" }

// ToolDefinition 返回 Shell 工具的 LLM 定义
func (s *ShellSkill) ToolDefinition() llm.ToolDefinition {
	return llm.NewToolDefinition("shell", "Run an installed program with exact argv, working directory, environment and stdin. Use command only when shell syntax is needed. Full stdout/stderr are retained as execution artifacts; exit zero alone does not prove the requested business result. Use action=discover to locate a program before invoking its documented help or task command.", &llm.Schema{
		Type: "object",
		Properties: map[string]*llm.Schema{
			"action":          {Type: "string", Description: "run (default) or discover"},
			"executable":      {Type: "string", Description: "Installed program name on PATH or explicit executable path"},
			"args":            {Type: "array", Items: &llm.Schema{Type: "string"}, Description: "Exact arguments without shell interpolation"},
			"command":         {Type: "string", Description: "Command interpreted by the platform shell; mutually exclusive with executable"},
			"cwd":             {Type: "string", Description: "Working directory; defaults to the current application directory"},
			"env":             {Type: "object", Description: "Explicit string environment overrides; otherwise inherit the existing runtime environment"},
			"stdin":           {Type: "string", Description: "Exact UTF-8 input followed by EOF"},
			"stdin_file":      {Type: "string", Description: "File supplied as stdin, relative to cwd or absolute; mutually exclusive with stdin"},
			"timeout_seconds": {Type: "number", Description: "Optional positive timeout; otherwise use the task context without the old 30 second limit"},
		},
	})
}

// Match 匹配 Shell 命令
//
// 触发格式: /sh <command> 或 /shell <command>
func (s *ShellSkill) Match(content string) bool {
	lower := strings.ToLower(content)
	return strings.HasPrefix(lower, "/sh ") || strings.HasPrefix(lower, "/shell ")
}

// Execute 执行 Shell 命令
func (s *ShellSkill) Execute(ctx context.Context, args map[string]any) (*skill.Result, error) {
	request, err := parseShellRequest(args)
	if err != nil {
		return nil, err
	}
	if request.Executable == "" && request.Command == "" {
		return &skill.Result{Content: "请提供命令，格式：/sh ls -la"}, nil
	}
	return s.executeRequest(ctx, request)
}

// runShell 保留包内旧调用方式，实际执行与结构化工具共享同一生命周期。
func (s *ShellSkill) runShell(ctx context.Context, command string) (string, error) {
	result, err := s.Execute(ctx, map[string]any{"command": command})
	if result == nil {
		return "", err
	}
	execution, ok := result.Data.(*ShellExecution)
	if !ok {
		return result.Content, err
	}
	return strings.TrimRight(execution.Stdout+"\n"+execution.Stderr, "\n"), err
}

// extractShellCommand 从用户输入中提取命令
func extractShellCommand(input string) string {
	for _, prefix := range []string{"/sh ", "/shell "} {
		if strings.HasPrefix(strings.ToLower(input), prefix) {
			return strings.TrimSpace(input[len(prefix):])
		}
	}
	return strings.TrimSpace(input)
}

// allowedCommands 是旧白名单实现遗留数据，默认功能优先路径不使用。
// 保留它是为了未来显式 strict profile 可复用。
//
// 旧白名单：仅允许真正只读/无副作用的命令
//
// 已移除所有具有写能力或数据窃取风险的命令：
//
//	python/python3/go/node/sed/awk/wget/cp/mv/mkdir/touch/
//	tar/zip/unzip/gzip/gunzip/tee/xargs/yes/curl 等
//
// curl 已移除：即使拦截 -o/-O，仍可通过 -d @file / -F file=@path /
// --data-binary @path 窃取本地文件到外部服务器，风险不可控。
var allowedCommands = map[string]bool{
	// 文件查看（只读）
	"ls": true, "cat": true, "head": true, "tail": true,
	"file": true, "wc": true, "du": true, "df": true,
	// 搜索/过滤（只读）
	"find": true, "grep": true,
	// 排序/去重/文本处理
	"sort": true, "uniq": true, "jq": true, "tree": true,
	// 系统信息（只读）
	"date": true, "uptime": true, "uname": true, "whoami": true,
	"env": true, "ps": true, "top": true, "free": true, "which": true,
	// 文本输出
	"echo": true,
	// 网络诊断（只读）
	"ping": true, "dig": true, "nslookup": true,
	"traceroute": true, "ifconfig": true, "ip": true, "ss": true, "netstat": true,
	// Git（只读子命令，危险子命令另行拦截）
	"git": true,
	// 路径/标识
	"pwd": true,
}

// dangerousSubcommands 即使主命令在白名单，某些子命令仍需拦截
var dangerousSubcommands = map[string][]string{
	"git": {
		"push", "remote add", "remote set-url",
		"reset", "checkout", "clean", "rebase",
		"commit", "merge", "stash", "cherry-pick",
		"tag", "branch -D", "branch -d",
		"clone", "pull", "submodule", "config",
	},
}

// findDangerousFlags 是 find 的 action primaries：可执行任意命令、删改文件，
// 即使 find 在白名单也必须拦截。
var findDangerousFlags = map[string]bool{
	"-exec": true, "-execdir": true, "-ok": true, "-okdir": true,
	"-delete": true, "-fprintf": true, "-fls": true, "-fprint": true, "-fprint0": true,
}

func checkFindFlags(segment string) string {
	for _, tok := range strings.Fields(segment) {
		if findDangerousFlags[tok] {
			return fmt.Sprintf("find %s 被禁止（可执行命令或删改文件）", tok)
		}
	}
	return ""
}

// checkAllowed 保留为配置扩展点。默认功能优先：只拒绝空命令，不对白名单、
// 重定向、脚本语言、包管理、git 写操作等做硬拦截。
func checkAllowed(command string) string {
	if strings.TrimSpace(command) == "" {
		return "命令为空"
	}
	return ""
}

// checkDangerousPatterns 拦截无法通过白名单防御的危险模式
func checkDangerousPatterns(cmd string) string {
	// 反引号命令替换
	if strings.Contains(cmd, "`") {
		return "禁止使用反引号命令替换"
	}
	// $() 命令替换
	if strings.Contains(cmd, "$(") {
		return "禁止使用 $() 命令替换"
	}
	// eval
	if strings.HasPrefix(strings.TrimSpace(cmd), "eval ") || strings.Contains(cmd, "; eval ") {
		return "禁止使用 eval"
	}
	// 输出重定向（> 和 >>）
	if strings.Contains(cmd, ">") {
		return "禁止使用输出重定向 > / >>"
	}
	// 输入重定向 here-doc
	if strings.Contains(cmd, "<<") {
		return "禁止使用 here-doc 重定向 <<"
	}
	// 输入重定向 < (单独的 <，不含 << 和 <( 已在上面拦截)
	if strings.Contains(cmd, " < ") || strings.HasPrefix(cmd, "< ") {
		return "禁止使用输入重定向 <"
	}
	// 进程替换
	if strings.Contains(cmd, "<(") || strings.Contains(cmd, ">(") {
		return "禁止使用进程替换 <() / >()"
	}
	// && 和 || 条件链（由 splitCommandSegments 统一处理，但在此提前拦截）
	if strings.Contains(cmd, "&&") {
		return "禁止使用 && 命令链"
	}
	if strings.Contains(cmd, "||") {
		return "禁止使用 || 命令链"
	}
	return ""
}

// splitCommandSegments 按管道、分号、&& 和 || 拆分命令
func splitCommandSegments(cmd string) []string {
	// 先将 && 和 || 替换为分号，再统一按 | 和 ; 拆分
	cmd = strings.ReplaceAll(cmd, "&&", ";")
	cmd = strings.ReplaceAll(cmd, "||", ";")
	var segments []string
	for _, part := range strings.Split(cmd, "|") {
		for _, sub := range strings.Split(part, ";") {
			sub = strings.TrimSpace(sub)
			if sub != "" {
				segments = append(segments, sub)
			}
		}
	}
	return segments
}

// extractBaseCommand 从命令段中提取基础命令名
//
// 跳过环境变量赋值（VAR=val cmd）和路径前缀（/usr/bin/cmd）
func extractBaseCommand(segment string) string {
	fields := strings.Fields(segment)
	for _, f := range fields {
		// 跳过环境变量赋值
		if strings.Contains(f, "=") && !strings.HasPrefix(f, "-") {
			continue
		}
		// 取 basename（处理 /usr/bin/ls 这种情况）
		base := f
		if idx := strings.LastIndex(f, "/"); idx >= 0 {
			base = f[idx+1:]
		}
		return strings.ToLower(base)
	}
	return ""
}
