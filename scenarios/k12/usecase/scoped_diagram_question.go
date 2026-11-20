package usecase

import (
	"regexp"
	"strings"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

var diagramFigureReference = regexp.MustCompile(`(?:按如图|按照图|按图)([①-⑳0-9]+)所示`)
var diagramNamedDimension = regexp.MustCompile(`^[rRdD]\s*=\s*[0-9]+(?:\.[0-9]+)?\s*(?:mm|cm|dm|km|m|毫米|厘米|分米|千米|米)$`)

// 复核可能在当前子问前重述公共图上的尺寸。只消去同一范围内完整匹配的尺寸声明。
func scopedDiagramQuestionText(q RecognizedQuestion, value string) string {
	if len(q.SourceNumberPath) < 2 || q.ImageSourceContext == nil || len(q.ImageSourceContext.Materials) == 0 {
		return value
	}
	before, rest, ok := strings.Cut(value, "将")
	if !ok || !diagramFigureReference.MatchString(rest) {
		return value
	}
	clauses := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune("，,；;。\n", r) })
	}
	dimensions := map[string]string{}
	for _, material := range q.ImageSourceContext.Materials {
		for _, part := range clauses(k12.RecognitionSourcePlainTextV1(material.Text)) {
			if part = strings.TrimSpace(part); diagramNamedDimension.MatchString(part) {
				name, _, _ := strings.Cut(part, "=")
				name = strings.TrimSpace(name)
				observed := k12.RecognitionSourceComparisonV1(part)
				if prior, exists := dimensions[name]; exists && prior != observed {
					// 同一符号可能属于不同图形，不能按数值碰巧命中来删除局部条件。
					return value
				}
				dimensions[name] = observed
			}
		}
	}
	if len(dimensions) == 0 {
		return value
	}
	// 编号必须与本题的冻结末级标签一致；图号始终留在问题中。
	label := q.SourceNumberPath[len(q.SourceNumberPath)-1]
	if label != "" && label == q.DisplayLabel && strings.HasPrefix(label, "（") && strings.HasSuffix(label, "）") {
		before = strings.TrimSuffix(strings.TrimSpace(before), label)
	}
	var retained []string
	for _, part := range clauses(before) {
		part = strings.TrimSpace(part)
		name, _, _ := strings.Cut(part, "=")
		if !diagramNamedDimension.MatchString(part) || dimensions[strings.TrimSpace(name)] != k12.RecognitionSourceComparisonV1(part) {
			retained = append(retained, part)
		}
	}
	return strings.Join(retained, "。") + "将" + diagramFigureReference.ReplaceAllString(rest, "按图${1}所示")
}
