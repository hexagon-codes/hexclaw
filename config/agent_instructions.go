package config

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

//go:embed default_agents.md
var defaultAgentInstructions string

// AgentInstructionsSnapshot 保存实际采用的规则正文，不能用文件修改时间代替内容身份。
type AgentInstructionsSnapshot struct {
	Content string `json:"content"`
	Digest  string `json:"digest"`
	Source  string `json:"source"`
}

type agentInstructionsContextKey struct{}

func AgentInstructionsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".hexclaw", "AGENTS.md"), nil
}

// InitializeAgentInstructions 仅在文件不存在时创建模板；空文件也属于用户内容。
func InitializeAgentInstructions() error {
	path, err := AgentInstructionsPath()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".agents-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.WriteString(defaultAgentInstructions)
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	// 完整文件落盘后原子创建目标，不替换同时出现的用户文件。
	if err := os.Link(f.Name(), path); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

// ReadAgentInstructions 只读文件，缺失、空内容或读取失败均使用内嵌规则。
func ReadAgentInstructions() AgentInstructionsSnapshot {
	content, source, status := strings.TrimSpace(defaultAgentInstructions), "embedded", "fallback"
	path, err := AgentInstructionsPath()
	if err == nil {
		data, readErr := os.ReadFile(path)
		if readErr == nil && strings.TrimSpace(string(data)) != "" {
			content, source, status = strings.TrimSpace(string(data)), "file", "loaded"
		} else if readErr != nil {
			status = "read_error"
		} else {
			status = "empty"
		}
	}
	sum := sha256.Sum256([]byte(content))
	snapshot := AgentInstructionsSnapshot{Content: content, Digest: "sha256:" + hex.EncodeToString(sum[:]), Source: source}
	slog.Debug("Agent instructions selected", "source", source, "digest", snapshot.Digest, "length", len(content), "status", status)
	return snapshot
}

func WithAgentInstructions(ctx context.Context, snapshot AgentInstructionsSnapshot) context.Context {
	return context.WithValue(ctx, agentInstructionsContextKey{}, snapshot)
}
func AgentInstructionsFromContext(ctx context.Context) (AgentInstructionsSnapshot, bool) {
	if ctx == nil {
		return AgentInstructionsSnapshot{}, false
	}
	snapshot, ok := ctx.Value(agentInstructionsContextKey{}).(AgentInstructionsSnapshot)
	return snapshot, ok
}
func FreezeAgentInstructions(ctx context.Context) (context.Context, AgentInstructionsSnapshot) {
	if snapshot, ok := AgentInstructionsFromContext(ctx); ok {
		return ctx, snapshot
	}
	snapshot := ReadAgentInstructions()
	return WithAgentInstructions(ctx, snapshot), snapshot
}

// ParentExpressionInstructions 仅约束讲解与交付表达，不能改动已冻结事实及结果合同。
func ParentExpressionInstructions(ctx context.Context) string {
	snapshot, ok := AgentInstructionsFromContext(ctx)
	if !ok || snapshot.Content == "" {
		return ""
	}
	return fmt.Sprintf("以下公共规则仅用于家长讲解和交付表达；不得修改题目、已验算答案、识别事实、学科方法与必需结果字段。\n<agent-instructions>\n%s\n</agent-instructions>\n\n", snapshot.Content)
}
