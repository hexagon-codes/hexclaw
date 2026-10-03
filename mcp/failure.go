package mcp

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/hexagon-codes/hexagon"
	"github.com/hexagon-codes/toolkit/util/logger"
)

const (
	mcpFailureRetryBase = 30 * time.Second
	mcpFailureRetryMax  = 5 * time.Minute
	maxMCPFailureText   = 4 << 10
)

// connectFailure 是一个 server 最近一次连接失败的事实快照。
// Manager.mu 保护整个值，避免状态接口看到半套重连信息。
type connectFailure struct {
	lastError   string
	retryable   bool
	retryCount  int
	nextRetryAt time.Time
	fingerprint string
	stage       string
}

type failureLogEvent struct {
	name        string
	stage       string
	errorText   string
	retryable   bool
	retryCount  int
	nextRetryAt time.Time
}

var (
	mcpCredentialPattern    = regexp.MustCompile(`(?i)((?:password|passwd|pass|token|secret|api[_-]?key|authorization|credential)["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|(?:bearer|basic)\s+[^\s,;]+|[^\s,;]+)`)
	mcpURLCredentialPattern = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)([^/@\s]+)@`)
	mcpBearerPattern        = regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)[^\s,;]+`)
)

func (m *Manager) recordConnectFailure(name string, err error) {
	m.recordConnectFailureWithRevision(name, 0, false, err)
}

func (m *Manager) recordConnectFailureForRevision(name string, revision uint64, err error) {
	m.recordConnectFailureWithRevision(name, revision, true, err)
}

func (m *Manager) recordConnectFailureWithRevision(name string, revision uint64, guarded bool, err error) {
	if name == "" || err == nil {
		return
	}

	safeText := safeMCPErrorText(err)
	stage := mcpFailureStage(err)
	retryable := isRetryableMCPFailure(err)
	fingerprint := stage + "|" + safeText
	now := time.Now()
	var event *failureLogEvent

	m.mu.Lock()
	if guarded && m.revisions[name] != revision {
		m.mu.Unlock()
		return
	}
	if m.failures == nil {
		m.failures = make(map[string]connectFailure)
	}
	previous, existed := m.failures[name]
	retryCount := 1
	if existed && previous.retryable == retryable && previous.fingerprint == fingerprint {
		retryCount = previous.retryCount + 1
	}
	nextRetryAt := time.Time{}
	if retryable {
		nextRetryAt = now.Add(mcpRetryDelay(retryCount))
	}
	m.failures[name] = connectFailure{
		lastError:   safeText,
		retryable:   retryable,
		retryCount:  retryCount,
		nextRetryAt: nextRetryAt,
		fingerprint: fingerprint,
		stage:       stage,
	}
	// 同一根因只在首次/根因变化时记 error，避免固定周期刷屏；计数仍持续更新到状态接口。
	if !existed || previous.retryable != retryable || previous.fingerprint != fingerprint {
		event = &failureLogEvent{
			name:        name,
			stage:       stage,
			errorText:   safeText,
			retryable:   retryable,
			retryCount:  retryCount,
			nextRetryAt: nextRetryAt,
		}
	}
	m.mu.Unlock()

	if event == nil {
		return
	}
	args := []any{
		"server", event.name,
		"stage", event.stage,
		"error", event.errorText,
		"retryable", event.retryable,
		"retry_count", event.retryCount,
	}
	if event.nextRetryAt.IsZero() {
		args = append(args, "retry_state", "blocked")
	} else {
		args = append(args, "next_retry_at", event.nextRetryAt.UTC().Format(time.RFC3339Nano))
	}
	logger.Error("MCP server connection failed", args...)
}

func (m *Manager) clearConnectFailure(name string) {
	if name == "" {
		return
	}
	m.mu.Lock()
	delete(m.failures, name)
	m.mu.Unlock()
}

func mcpRetryDelay(retryCount int) time.Duration {
	if retryCount <= 1 {
		return mcpFailureRetryBase
	}
	delay := mcpFailureRetryBase
	for i := 1; i < retryCount; i++ {
		if delay >= mcpFailureRetryMax/2 {
			return mcpFailureRetryMax
		}
		delay *= 2
	}
	if delay > mcpFailureRetryMax {
		return mcpFailureRetryMax
	}
	return delay
}

func mcpRetryDue(failure connectFailure, now time.Time) bool {
	if !failure.retryable {
		return false
	}
	// 保留显式 tryReconnect 的即时恢复能力；后台 ticker 仍会在首个 30s 窗口执行。
	if failure.retryCount <= 1 {
		return true
	}
	return failure.nextRetryAt.IsZero() || !now.Before(failure.nextRetryAt)
}

func mcpFailureStage(err error) string {
	var stdioErr *hexagon.MCPStdioConnectError
	if errors.As(err, &stdioErr) && stdioErr != nil && stdioErr.Stage != "" {
		return stdioErr.Stage
	}
	var protocolErr *hexagon.MCPProtocolError
	if errors.As(err, &protocolErr) && protocolErr != nil && protocolErr.Stage != "" {
		return protocolErr.Stage
	}
	return "connect"
}

func safeMCPErrorText(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	s = mcpURLCredentialPattern.ReplaceAllString(s, `$1[REDACTED]@`)
	s = mcpCredentialPattern.ReplaceAllString(s, `$1[REDACTED]`)
	s = mcpBearerPattern.ReplaceAllString(s, `$1$2[REDACTED]`)
	if len(s) > maxMCPFailureText {
		s = strings.ToValidUTF8(s[:maxMCPFailureText], "") + "…"
	}
	if s == "" {
		return "MCP connection failed"
	}
	return s
}

func isRetryableMCPFailure(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	// 这些错误由当前配置/安装环境直接决定，继续拉起只会制造进程和日志风暴。
	for _, marker := range []string{
		"stdio 传输需要指定 command",
		"sse 传输需要指定 endpoint",
		"streamable http 传输需要指定 endpoint",
		"不支持的传输方式",
		"executable file not found",
		"no such file or directory",
		"permission denied",
		"invalid command",
		"invalid endpoint",
	} {
		if strings.Contains(s, marker) {
			return false
		}
	}
	return true
}

func failureStatusFields(st *ServerStatus, failure connectFailure) {
	if st == nil || failure.lastError == "" {
		return
	}
	st.LastError = failure.lastError
	retryable := failure.retryable
	st.Retryable = &retryable
	st.RetryCount = failure.retryCount
	if failure.retryable {
		st.RetryState = "retrying"
		if !failure.nextRetryAt.IsZero() {
			st.NextRetryAt = failure.nextRetryAt.UTC().Format(time.RFC3339Nano)
		}
	} else {
		st.RetryState = "blocked"
	}
}
