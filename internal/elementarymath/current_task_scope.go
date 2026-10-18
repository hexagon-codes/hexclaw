package elementarymath

import (
	"encoding/json"
	"strings"
)

const mathSubproblemPrefix = "k12-math-subproblem-v1\nShared context:\n"
const mathSubproblemCurrent = "\nCurrent original:\n"
const mathSubproblemCanonical = "\nCurrent canonical:\n"
const mathSubproblemTarget = "\nAnswer target:\n"
const mathSubproblemInstruction = "Solve and independently verify only the current original task. Its canonical text is another representation of the same current task. Shared context supplies facts and diagrams, not additional questions to answer. System ordering is not printed question numbering. Do not substitute any other question or blank from the shared context. Preserve the quantities and units requested by the current task."

// BuildMathSubproblem 把数学子问的实际题面与公共材料分开，不以系统序号推定印刷问项。
// 调用方提供同代次来源，现有请求摘要同时覆盖全部原始字节和当前目标。
func BuildMathSubproblem(shared, current, canonical string) (string, bool) {
	if strings.TrimSpace(shared) == "" || strings.TrimSpace(current) == "" {
		return "", false
	}
	sharedJSON, _ := json.Marshal(shared)
	currentJSON, _ := json.Marshal(current)
	canonicalJSON, _ := json.Marshal(canonical)
	return mathSubproblemPrefix + string(sharedJSON) + mathSubproblemCurrent + string(currentJSON) +
		mathSubproblemCanonical + string(canonicalJSON) + mathSubproblemTarget + mathSubproblemInstruction, true
}

// ParseMathSubproblem 只读取完整版本合同，原始换行和公式保留在各自 JSON 字符串内。
func ParseMathSubproblem(problem string) (shared, current, canonical string, ok bool) {
	if !strings.HasPrefix(problem, mathSubproblemPrefix) {
		return "", "", "", false
	}
	sharedJSON, rest, found := strings.Cut(strings.TrimPrefix(problem, mathSubproblemPrefix), mathSubproblemCurrent)
	if !found || json.Unmarshal([]byte(sharedJSON), &shared) != nil {
		return "", "", "", false
	}
	currentJSON, rest, found := strings.Cut(rest, mathSubproblemCanonical)
	if !found || json.Unmarshal([]byte(currentJSON), &current) != nil {
		return "", "", "", false
	}
	canonicalJSON, target, found := strings.Cut(rest, mathSubproblemTarget)
	if !found || target != mathSubproblemInstruction || json.Unmarshal([]byte(canonicalJSON), &canonical) != nil {
		return "", "", "", false
	}
	_, valid := BuildMathSubproblem(shared, current, canonical)
	return shared, current, canonical, valid
}
