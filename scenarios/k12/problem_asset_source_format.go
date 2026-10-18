package k12

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	problemAssetSourceFraction = regexp.MustCompile(`^\\frac\{([0-9]+)\}\{([0-9]+)\}\\,\\(?:text|mathrm)\{([A-Za-z]+)\}$`)
	problemAssetSourceTemplate = regexp.MustCompile(`^\\square\\times\\square=\\square\\?（\\(?:text|mathrm)\{([A-Za-z]+)\}）$`)
)

// ProblemAssetSourceFormatCandidates 为严格数学来源排版生成有限精确查找副本。
// 支持完整分数单位 span 及中文数量边界的展示空格；纯空模板须有冻结空白作答证据。
// 不改写 v1 身份，不规范化中文或其他事实，不扫描候选资产。
func ProblemAssetSourceFormatCandidates(facts ProblemAssetFacts, allowBlankTemplate bool) []ProblemAssetFacts {
	if normalizeAssetText(facts.Subject) != "数学" {
		return nil
	}
	normal, historical, ok := problemAssetSourceFormatStem(facts.Stem, allowBlankTemplate)
	if !ok {
		compact, supported := problemAssetPlainQuantitySpacingStem(facts.Stem)
		if !supported || compact == normalizeAssetText(facts.Stem) {
			return nil
		}
		candidate := facts
		candidate.Stem = compact
		return []ProblemAssetFacts{candidate}
	}
	first, second := facts, facts
	first.Stem, second.Stem = normal, historical
	return []ProblemAssetFacts{first, second}
}

// EquivalentProblemAssetSourceFormat 比较完整原事实，不比较答案或学生作答。
// 数学排版之外的所有字段仍由原 v1 身份规则比较；策略不接纳未知命令或代数等价。
func EquivalentProblemAssetSourceFormat(left, right ProblemAssetFacts, allowBlankTemplate bool) bool {
	if normalizeAssetText(left.Subject) != "数学" || normalizeAssetText(right.Subject) != "数学" {
		return false
	}
	leftStem, _, leftOK := problemAssetSourceFormatStem(left.Stem, allowBlankTemplate)
	rightStem, _, rightOK := problemAssetSourceFormatStem(right.Stem, allowBlankTemplate)
	if !leftOK || !rightOK {
		leftStem, leftOK = problemAssetPlainQuantitySpacingStem(left.Stem)
		rightStem, rightOK = problemAssetPlainQuantitySpacingStem(right.Stem)
	}
	if !leftOK || !rightOK || leftStem != rightStem {
		return false
	}
	left.Stem, right.Stem = leftStem, rightStem
	leftID, leftErr := left.ExactIdentity("math-source-format-v1")
	rightID, rightErr := right.ExactIdentity("math-source-format-v1")
	return leftErr == nil && rightErr == nil && leftID.FactsDigest == rightID.FactsDigest
}

// problemAssetPlainQuantitySpacingStem 只移除完整数量 token 与相邻汉字间的 ASCII 空格。
// 数字间空格、带分数、数字内部空格及其它排版均不进入此候选通道；原事实保持不变。
func problemAssetPlainQuantitySpacingStem(stem string) (string, bool) {
	runes := []rune(normalizeAssetText(stem))
	starts, ends := make([]bool, len(runes)), make([]bool, len(runes))
	hasHan, hasQuantity := false, false
	isDigit := func(r rune) bool { return r >= '0' && r <= '9' }
	for i := 0; i < len(runes); {
		r := runes[i]
		if isDigit(r) {
			start := i
			for i < len(runes) && isDigit(runes[i]) {
				i++
			}
			// 一个数量是整数、小数或紧连的整数分数，不合并或求值。
			if i < len(runes) && (runes[i] == '.' || runes[i] == '/') {
				i++
				if i == len(runes) || !isDigit(runes[i]) {
					return "", false
				}
				for i < len(runes) && isDigit(runes[i]) {
					i++
				}
			}
			starts[start], ends[i-1], hasQuantity = true, true, true
			continue
		}
		if unicode.Is(unicode.Han, r) {
			hasHan = true
		} else if r != ' ' && !strings.ContainsRune("，。！？；：、（）()[]", r) {
			// 英文、变量、引号、公式标记和换行等保留各自原有语义，不猜测等价。
			return "", false
		}
		i++
	}
	if !hasHan || !hasQuantity {
		return "", false
	}
	var compact strings.Builder
	for i := 0; i < len(runes); {
		if runes[i] != ' ' {
			compact.WriteRune(runes[i])
			i++
			continue
		}
		end := i
		for end < len(runes) && runes[end] == ' ' {
			end++
		}
		if i == 0 || end == len(runes) ||
			!(unicode.Is(unicode.Han, runes[i-1]) && starts[end] || ends[i-1] && unicode.Is(unicode.Han, runes[end])) {
			return "", false
		}
		i = end
	}
	return compact.String(), true
}

// problemAssetSourceFormatStem 只投影一个来源明确的分数单位 span。
// 中文、数量、标点和内部空白逐字保留；仅独立末行的全空布局可退出查找副本。
func problemAssetSourceFormatStem(stem string, allowBlankTemplate bool) (string, string, bool) {
	stem = normalizeAssetText(stem)
	if strings.ContainsAny(stem, "`") {
		return "", "", false
	}
	templateUnit := ""
	if line := strings.LastIndexByte(stem, '\n'); line >= 0 {
		body, end, ok := problemAssetSourceMathSpan(stem[line+1:])
		if ok && end == len(stem)-line-1 {
			if match := problemAssetSourceTemplate.FindStringSubmatch(body); match != nil {
				if !allowBlankTemplate {
					return "", "", false
				}
				templateUnit = match[1]
				stem = strings.TrimRight(stem[:line], "\n")
			}
		}
	}
	var normal, historical strings.Builder
	spans, unit := 0, ""
	for i := 0; i < len(stem); {
		if stem[i] != '$' && stem[i] != '\\' {
			normal.WriteByte(stem[i])
			historical.WriteByte(stem[i])
			i++
			continue
		}
		body, end, ok := problemAssetSourceMathSpan(stem[i:])
		if !ok {
			return "", "", false
		}
		match := problemAssetSourceFraction.FindStringSubmatch(body)
		if match == nil || spans != 0 {
			return "", "", false
		}
		spans++
		unit = match[3]
		math := `\(\frac{` + match[1] + `}{` + match[2] + `}\,\text{` + unit + `}\)`
		normal.WriteString(math)
		historical.WriteString(strings.ReplaceAll(math, `\`, `\\`))
		i += end
	}
	if spans != 1 || (templateUnit != "" && templateUnit != unit) {
		return "", "", false
	}
	return normal.String(), historical.String(), true
}

// problemAssetSourceMathSpan 完整读取限定的定界符与一个历史双转义层。
// 不对整段文本 unescape，不递归解码，也不接纳单双反斜杠混合的历史 span。
func problemAssetSourceMathSpan(value string) (string, int, bool) {
	open, close, doubled := "", "", false
	switch {
	case strings.HasPrefix(value, `\\(`):
		open, close, doubled = `\\(`, `\\)`, true
	case strings.HasPrefix(value, `\(`):
		open, close = `\(`, `\)`
	case strings.HasPrefix(value, "$$"):
		return "", 0, false
	case strings.HasPrefix(value, "$"):
		open, close = "$", "$"
	default:
		return "", 0, false
	}
	index := strings.Index(value[len(open):], close)
	if index < 0 {
		return "", 0, false
	}
	body := value[len(open) : len(open)+index]
	if strings.ContainsAny(body, "\r\n$") {
		return "", 0, false
	}
	if doubled {
		decoded := strings.ReplaceAll(body, `\\`, `\`)
		if strings.ReplaceAll(decoded, `\`, `\\`) != body {
			return "", 0, false
		}
		body = decoded
	}
	return body, len(open) + index + len(close), true
}
