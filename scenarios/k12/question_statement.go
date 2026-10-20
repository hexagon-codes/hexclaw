package k12

import (
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/elementarymath"
)

// MistakeQuestionStatement 恢复照片错题的原题，不把求解范围协议当作教材内容。
// 未识别的格式保持原文，历史模型请求和持久记录不由此投影改写。
func MistakeQuestionStatement(fields MistakeFields) string {
	problem := fields.Question
	if fields.EntrySource != MistakeEntryPhoto {
		return problem
	}
	shared, current, _, ok := elementarymath.ParsePartCountSlotProblemSources(problem)
	if !ok {
		shared, current, _, ok = elementarymath.ParseFractionSlotProblemSources(problem)
	}
	if !ok {
		shared, current, _, ok = elementarymath.ParseMathSubproblem(problem)
	}
	if ok {
		if strings.TrimSpace(shared) == strings.TrimSpace(current) {
			return current
		}
		return current + "\n\n" + shared
	}
	const prefix = "\n\nFor this request, solve or assess only subproblem "
	const suffix = ". Use the shared material as context; do not answer the other subproblems."
	if index := strings.LastIndex(problem, prefix); index > 0 && strings.HasSuffix(problem, suffix) {
		subproblem := strings.TrimSuffix(problem[index+len(prefix):], suffix)
		if strings.TrimSpace(subproblem) != "" && !strings.ContainsAny(subproblem, "\r\n") {
			return problem[:index]
		}
	}
	return problem
}
