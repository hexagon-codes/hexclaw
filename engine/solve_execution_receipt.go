package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"sync"

	"github.com/hexagon-codes/hexagon/observe/trace"
	"github.com/hexagon-codes/hexclaw/skill"
)

// CodeExecutionReceipt 只由实际工具结果生成，随子任务结果持久化；不从模型正文恢复。
type CodeExecutionReceipt struct {
	InputDigest     string `json:"input_digest"`
	RunID           string `json:"run_id"`
	Status          string `json:"status"`
	ExitCode        int    `json:"exit_code"`
	Timeout         bool   `json:"timeout"`
	RuntimeMissing  bool   `json:"runtime_missing"`
	Error           string `json:"error,omitempty"`
	StdoutBytes     int64  `json:"stdout_bytes"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
	Truncated       bool   `json:"truncated"`
	Stdout          string `json:"stdout"`
}

type codeExecutionReceiptKey struct{}

type codeExecutionReceiptSink struct {
	mu          sync.Mutex
	inputDigest string
	receipt     *CodeExecutionReceipt
}

func executionInputDigest(task string) string {
	digest := sha256.Sum256([]byte(task))
	return hex.EncodeToString(digest[:])
}

// captureCodeExecutionReceipt 在展示截断与后置钩子之前读取工具的结构化报告。
func captureCodeExecutionReceipt(ctx context.Context, toolName string, result *skill.Result) {
	sink, _ := ctx.Value(codeExecutionReceiptKey{}).(*codeExecutionReceiptSink)
	if sink == nil || toolName != codeExecToolName {
		return
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	// 后一次执行失败时不能借用前一次回执。
	sink.receipt = nil
	if result == nil || result.Data == nil {
		return
	}
	raw, err := json.Marshal(result.Data)
	if err != nil {
		return
	}
	var receipt CodeExecutionReceipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.RunID == "" {
		return
	}
	receipt.InputDigest = sink.inputDigest
	// code_exec 的 Content 以原始 stdout 开头；长度只取结构化报告，不解析正文中的报告片段。
	receipt.Stdout = ""
	if receipt.StdoutBytes > 0 && receipt.StdoutBytes <= int64(len(result.Content)) {
		receipt.Stdout = result.Content[:receipt.StdoutBytes]
	}
	sink.receipt = &receipt
	trace.L(ctx).Info("solve execution receipt captured", "input_digest", receipt.InputDigest,
		"sandbox_run_id", receipt.RunID, "status", receipt.Status, "exit_code", receipt.ExitCode,
		"timeout", receipt.Timeout, "truncated", receipt.Truncated,
		"stdout_bytes", receipt.StdoutBytes, "stdout_digest", executionInputDigest(receipt.Stdout))
}

var executionComputedMarker = regexp.MustCompile(`(?im)^COMPUTED:\s*([^\r\n]+)\s*$`)

func (r *CodeExecutionReceipt) computed(task string) (string, bool) {
	if r == nil || r.InputDigest != executionInputDigest(task) || r.RunID == "" ||
		r.Status != "success" || r.ExitCode != 0 || r.Timeout || r.RuntimeMissing || r.Error != "" ||
		r.Truncated || r.StdoutTruncated || r.StderrTruncated ||
		r.StdoutBytes <= 0 || int64(len(r.Stdout)) != r.StdoutBytes {
		return "", false
	}
	computed := strings.TrimSpace(r.Stdout)
	if matches := executionComputedMarker.FindAllStringSubmatch(r.Stdout, -1); len(matches) == 1 {
		computed = strings.TrimSpace(matches[0][1])
	} else if len(matches) > 1 || strings.ContainsAny(computed, "\r\n") {
		return "", false
	}
	if values, ok := numberSet(computed); ok {
		for _, value := range values {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return "", false
			}
		}
		return computed, true
	}
	if _, ok := parseAnswerQuantity(computed); ok {
		return computed, true
	}
	return "", false
}
