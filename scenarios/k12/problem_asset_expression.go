package k12

import (
	"strings"
	"unicode"
)

// ProblemAssetExpressionTerms 仅为可完整解析的纯数字四则题提供候选词。
// 不求值、不合并数字、不展开括号；无法确认的表达式继续正常求解。
func ProblemAssetExpressionTerms(facts ProblemAssetFacts) ([]string, bool) {
	if normalizeAssetText(facts.Subject) != "数学" {
		return nil, false
	}
	_, terms, ok := problemAssetExpression(facts.Stem)
	return terms, ok
}

// EquivalentProblemAssetExpression 核对表达式结构及其余所有事实，候选分数不参与判定。
func EquivalentProblemAssetExpression(a, b ProblemAssetFacts) bool {
	if normalizeAssetText(a.Subject) != "数学" || normalizeAssetText(b.Subject) != "数学" {
		return false
	}
	left, _, leftOK := problemAssetExpression(a.Stem)
	right, _, rightOK := problemAssetExpression(b.Stem)
	if !leftOK || !rightOK || left != right {
		return false
	}
	a.Stem, b.Stem = left, right
	aID, aErr := a.ExactIdentity("expression-comparison")
	bID, bErr := b.ExactIdentity("expression-comparison")
	return aErr == nil && bErr == nil && aID.FactsDigest == bID.FactsDigest
}

func problemAssetExpression(input string) (string, []string, bool) {
	text := normalizeAssetText(input)
	// 候选通道只处理短算式；长题仍沿原求解，不增加常见任务的检索负担。
	if len(text) == 0 || len(text) > 256 {
		return "", nil, false
	}
	for _, pair := range [][2]string{{`\(`, `\)`}, {`\[`, `\]`}, {"$$", "$$"}, {"$", "$"}} {
		if strings.HasPrefix(text, pair[0]) && strings.HasSuffix(text, pair[1]) && len(text) > len(pair[0])+len(pair[1]) {
			text = strings.TrimSpace(text[len(pair[0]) : len(text)-len(pair[1])])
			break
		}
	}
	text = strings.NewReplacer(`\times`, "*", `\div`, "/", "×", "*", "÷", "/", "−", "-").Replace(text)
	var tokens, numbers []string
	runes := []rune(text)
	for i := 0; i < len(runes); {
		r := runes[i]
		if unicode.IsSpace(r) {
			i++
			continue
		}
		if r >= '0' && r <= '9' {
			start := i
			for i < len(runes) && runes[i] >= '0' && runes[i] <= '9' {
				i++
			}
			if i < len(runes) && runes[i] == '.' {
				i++
				decimal := i
				for i < len(runes) && runes[i] >= '0' && runes[i] <= '9' {
					i++
				}
				if i == decimal {
					return "", nil, false
				}
			}
			token := string(runes[start:i])
			tokens, numbers = append(tokens, token), append(numbers, token)
			continue
		}
		if !strings.ContainsRune("+-*/()=?", r) {
			return "", nil, false
		}
		tokens = append(tokens, string(r))
		i++
	}
	if len(numbers) < 2 {
		return "", nil, false
	}
	end := len(tokens)
	if end > 0 && tokens[end-1] == "?" {
		end--
		if end == 0 || tokens[end-1] != "=" {
			return "", nil, false
		}
	}
	if end > 0 && tokens[end-1] == "=" {
		end--
	}
	// 只校验语法，保留运算顺序、括号和结尾问法，不能把等值计算视作同题。
	position := 0
	var expression, term, factor func() bool
	factor = func() bool {
		if position >= end {
			return false
		}
		if tokens[position] == "+" || tokens[position] == "-" {
			position++
			return factor()
		}
		if tokens[position] == "(" {
			position++
			if !expression() || position >= end || tokens[position] != ")" {
				return false
			}
			position++
			return true
		}
		if tokens[position][0] < '0' || tokens[position][0] > '9' {
			return false
		}
		position++
		return true
	}
	term = func() bool {
		if !factor() {
			return false
		}
		for position < end && (tokens[position] == "*" || tokens[position] == "/") {
			position++
			if !factor() {
				return false
			}
		}
		return true
	}
	expression = func() bool {
		if !term() {
			return false
		}
		for position < end && (tokens[position] == "+" || tokens[position] == "-") {
			position++
			if !term() {
				return false
			}
		}
		return true
	}
	if !expression() || position != end {
		return "", nil, false
	}
	return strings.Join(tokens, " "), numbers, true
}
