package mathtext

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// NormalizeDingTalkMarkdown 只为新钉钉投影修正可靠强调的标点边界，不改规范原文。
// 代码、链接目标、转义和数学表达式保持原样；未配对标记不推断补全。
func NormalizeDingTalkMarkdown(s string) string {
	if !strings.Contains(s, "**") {
		return s
	}
	protected := make([]bool, len(s))
	protect := func(start, end int) {
		for i := start; i < end; i++ {
			protected[i] = true
		}
	}
	for _, span := range findProtectedSpans(s) {
		protect(span.start, span.end)
	}
	for i := 0; i < len(s); i++ {
		if protected[i] || markdownEscapedAt(s, i) {
			continue
		}
		// 引用式链接的目标与可选标题同样不参与强调投影。
		if s[i] == '[' && isFenceLineStart(s, i) {
			if end := strings.Index(s[i:], "]:"); end >= 0 && !strings.Contains(s[i:i+end], "\n") {
				start := i + end + 2
				end := len(s)
				if newline := strings.IndexByte(s[start:], '\n'); newline >= 0 {
					end = start + newline
				}
				protect(start, end)
			}
		}
		open, close := "", ""
		switch {
		case strings.HasPrefix(s[i:], "\\("):
			open, close = "\\(", "\\)"
		case strings.HasPrefix(s[i:], "\\["):
			open, close = "\\[", "\\]"
		case strings.HasPrefix(s[i:], "$$"):
			open, close = "$$", "$$"
		case s[i] == '$':
			open, close = "$", "$"
		}
		if open != "" {
			for j := i + len(open); j < len(s); j++ {
				if !protected[j] && !markdownEscapedAt(s, j) && strings.HasPrefix(s[j:], close) {
					protect(i, j+len(close))
					i = j + len(close) - 1
					break
				}
			}
		}
	}
	type delimiter struct {
		start, end int
		left       bool
	}
	var stack []delimiter
	inserts := make(map[int]string)
	deletes := make(map[int]int)
	for i := 0; i < len(s); {
		if protected[i] || s[i] != '*' || markdownEscapedAt(s, i) {
			i++
			continue
		}
		end := i + 1
		for end < len(s) && s[end] == '*' {
			end++
		}
		if end-i != 2 && end-i != 3 {
			i = end
			continue
		}
		before, after := markdownRuneBefore(s, i), markdownRuneAfter(s, end)
		// 无空白的数字幂表达式不能被一对远处的星号改成强调。
		if unicode.IsDigit(before) && unicode.IsDigit(after) {
			i = end
			continue
		}
		beforeWhite, afterWhite := before == 0 || unicode.IsSpace(before), after == 0 || unicode.IsSpace(after)
		beforePunct, afterPunct := markdownPunctuation(before), markdownPunctuation(after)
		left := !afterWhite && (!afterPunct || beforeWhite || beforePunct)
		right := !beforeWhite && (!beforePunct || afterWhite || afterPunct)
		canOpen := left || (!afterWhite && afterPunct && unicode.Is(unicode.Han, before))
		canClose := right || (!beforeWhite && beforePunct && (unicode.IsLetter(after) || unicode.IsDigit(after)))
		if canClose && len(stack) > 0 && stack[len(stack)-1].end-stack[len(stack)-1].start == end-i {
			opener := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			inner := s[opener.end:i]
			if strings.TrimSpace(inner) != "" && !strings.Contains(inner, "\n\n") {
				if !opener.left {
					inserts[opener.start] = " "
				}
				last, size := utf8.DecodeLastRuneInString(inner)
				if last == '：' || last == ':' {
					deletes[i-size] = size
					inserts[end] = string(last)
				} else if !right {
					inserts[end] = " "
				}
			}
		} else if canOpen {
			stack = append(stack, delimiter{start: i, end: end, left: left})
		}
		i = end
	}
	if len(inserts) == 0 {
		return s
	}
	var out strings.Builder
	for i := 0; i <= len(s); {
		out.WriteString(inserts[i])
		if i == len(s) {
			break
		}
		if skip := deletes[i]; skip > 0 {
			i += skip
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

func markdownEscapedAt(s string, index int) bool {
	slashes := 0
	for i := index - 1; i >= 0 && s[i] == '\\'; i-- {
		slashes++
	}
	return slashes%2 != 0
}

func markdownRuneBefore(s string, index int) rune {
	if index == 0 {
		return 0
	}
	r, _ := utf8.DecodeLastRuneInString(s[:index])
	return r
}

func markdownRuneAfter(s string, index int) rune {
	if index == len(s) {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(s[index:])
	return r
}

func markdownPunctuation(r rune) bool {
	return unicode.IsPunct(r) || unicode.IsSymbol(r)
}
