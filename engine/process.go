package engine

import (
	"encoding/json"
	"github.com/hexagon-codes/hexclaw/adapter"
	"strings"
)

// 沙箱报告从内置工具完整输出的固定信封读取，在显示截断之前保留真实执行信息。
func sandboxExecutionForDisplay(name, result string) *adapter.SandboxExecution {
	if name != "code_exec" {
		return nil
	}
	const marker = "\n[hexclaw_sandbox_result]\n"
	index := strings.LastIndex(result, marker)
	if index < 0 {
		return nil
	}
	var report adapter.SandboxExecution
	if json.Unmarshal([]byte(result[index+len(marker):]), &report) != nil || report.RunID == "" || report.Status == "" {
		return nil
	}
	return &report
}
