package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	StderrBytes     int64  `json:"stderr_bytes,omitempty"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
	Truncated       bool   `json:"truncated"`
	Stdout          string `json:"stdout"`
}

// ErrVerificationExecutionFailed 表示同输入的程序执行已明确失败。
var ErrVerificationExecutionFailed = errors.New("verification execution failed")

// KnownExecutionFailure 只认同一次输入的明确执行失败；缺失或未知回执不据此重试。
// 没有执行失败回执的概念题仍可返回 unverifiable，超时沿原未知结果边界处理。
func (r *CodeExecutionReceipt) KnownExecutionFailure(inputDigest string) error {
	if r == nil || inputDigest == "" || r.InputDigest != inputDigest || r.RunID == "" ||
		r.Status != "failed" || r.Timeout {
		return nil
	}
	if r.Error != "" {
		return fmt.Errorf("%w: %s", ErrVerificationExecutionFailed, r.Error)
	}
	return ErrVerificationExecutionFailed
}

type codeExecutionReceiptKey struct{}

type solverExecutionProofKey struct{}

// 求解器证明只投影已选中解法的实际执行，不代替独立数学审计。
type solverExecutionProof struct {
	InputDigest string `json:"input_digest"`
	RunID       string `json:"run_id"`
	Computed    string `json:"computed"`
}

func withSolverExecutionProof(ctx context.Context, solution solverSolution) context.Context {
	var proof *solverExecutionProof
	if computed, valid := solution.executionReceipt.computed(solution.executionTask); valid &&
		answersEqual(computed, solution.answer) {
		proof = &solverExecutionProof{
			InputDigest: solution.executionReceipt.InputDigest,
			RunID:       solution.executionReceipt.RunID,
			Computed:    computed,
		}
	}
	return context.WithValue(ctx, solverExecutionProofKey{}, proof)
}

type codeExecutionReceiptSink struct {
	mu          sync.Mutex
	inputDigest string
	receipt     *CodeExecutionReceipt
	receipts    []CodeExecutionReceipt
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
	sink.receipts = append(sink.receipts, receipt)
	trace.L(ctx).Info("solve execution receipt captured", "input_digest", receipt.InputDigest,
		"sandbox_run_id", receipt.RunID, "status", receipt.Status, "exit_code", receipt.ExitCode,
		"timeout", receipt.Timeout, "truncated", receipt.Truncated,
		"stdout_bytes", receipt.StdoutBytes, "stdout_digest", executionInputDigest(receipt.Stdout))
}

var executionRunReference = regexp.MustCompile(`(?im)^EXECUTION_RUN_ID:[ \t]*([^\s]+)[ \t]*$`)

// selectForResult 将实际执行与本次最终数学结论关联，不用文件制作的最后一次命令覆盖证明。
// 多份证据无法唯一关联时保持无证明；原始回执仍全部保存供恢复核对。
func (s *codeExecutionReceiptSink) selectForResult(spec SubAgentSpec, output string) *CodeExecutionReceipt {
	if len(s.receipts) == 0 {
		return nil
	}
	if refs := executionRunReference.FindAllStringSubmatch(output, -1); len(refs) != 0 {
		if len(refs) != 1 {
			return nil
		}
		for i := range s.receipts {
			if s.receipts[i].RunID == refs[0][1] {
				copy := s.receipts[i]
				return &copy
			}
		}
		return nil
	}
	expected := extractFinalAnswer(output)
	if spec.Agent == verifierAgentName {
		_, expected = parseVerdict(output)
	}
	var selected *CodeExecutionReceipt
	for i := range s.receipts {
		receipt := &s.receipts[i]
		computed, ok := receipt.computed(spec.Task)
		if !ok || !answersEqual(computed, expected) {
			continue
		}
		if selected != nil && selected.RunID != receipt.RunID {
			return nil
		}
		copy := *receipt
		selected = &copy
	}
	if selected != nil {
		return selected
	}
	// 单调用保留原有失败及弱证据合同；多调用失败不冒充已通过的数学证明。
	if len(s.receipts) == 1 {
		copy := s.receipts[0]
		return &copy
	}
	return nil
}

var executionComputedMarker = regexp.MustCompile(`(?im)^COMPUTED:[ \t]*([^\r\n]+)[ \t]*\r?$`)
var executionComputedLabel = regexp.MustCompile(`(?im)^COMPUTED:`)

// completeFor 共用同输入执行成功且输出完整的判断，不解释 stdout 的数学含义。
func (r *CodeExecutionReceipt) completeFor(task string) bool {
	if r == nil || r.InputDigest != executionInputDigest(task) || r.RunID == "" ||
		r.Status != "success" || r.ExitCode != 0 || r.Timeout || r.RuntimeMissing || r.Error != "" ||
		r.Truncated || r.StdoutTruncated || r.StderrTruncated ||
		r.StdoutBytes <= 0 || int64(len(r.Stdout)) != r.StdoutBytes {
		return false
	}
	return true
}

// computedOutput 只确认本次执行完整、成功并读取唯一结果行，不把数量解析能力当成执行状态。
func (r *CodeExecutionReceipt) computedOutput(task string) (string, bool) {
	if !r.completeFor(task) {
		return "", false
	}
	computed := strings.TrimSpace(r.Stdout)
	labels := executionComputedLabel.FindAllStringIndex(r.Stdout, -1)
	if matches := executionComputedMarker.FindAllStringSubmatch(r.Stdout, -1); len(labels) == 1 && len(matches) == 1 {
		computed = strings.TrimSpace(matches[0][1])
	} else if len(labels) != 0 || strings.ContainsAny(computed, "\r\n") {
		return "", false
	}
	if computed == "" {
		return "", false
	}
	return computed, true
}

// computed 保留数值证明边界：成功 stdout 不自动成为整份答案的数学证明。
func (r *CodeExecutionReceipt) computed(task string) (string, bool) {
	computed, complete := r.computedOutput(task)
	if !complete || !completeVerificationAnswer(computed) {
		return "", false
	}
	return computed, true
}
