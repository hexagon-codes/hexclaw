package mathtext

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ExcerptMarkdownParagraph 取首个非标题段落，在预算内保留已配对的行内格式。
// 省略号和闭合标记计入预算；代码与链接整体保留或省略，不猜测修补源文。
func ExcerptMarkdownParagraph(source string, limit int) string {
	source = strings.ReplaceAll(source, "\r\n", "\n")
	protected := findProtectedSpans(source)
	var paragraph []string
	offset, spanIndex := 0, 0
	for _, line := range strings.SplitAfter(source, "\n") {
		for spanIndex < len(protected) && protected[spanIndex].end <= offset {
			spanIndex++
		}
		inside := spanIndex < len(protected) && protected[spanIndex].start <= offset && offset < protected[spanIndex].end
		trimmed := strings.TrimSpace(line)
		marks := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
		heading := marks > 0 && marks <= 6 && (marks == len(trimmed) || trimmed[marks] == ' ' || trimmed[marks] == '\t')
		offset += len(line)
		if !inside && (trimmed == "" || heading) {
			if len(paragraph) > 0 {
				break
			}
			continue
		}
		paragraph = append(paragraph, strings.TrimSuffix(line, "\n"))
	}
	text := strings.TrimSpace(strings.Join(paragraph, "\n"))
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	atomic := markdownExcerptAtomicSpans(text)
	paired := markdownExcerptPairs(text, atomic)
	cut := len(string([]rune(text)[:limit-1]))
	for {
		previous := cut
		for _, span := range atomic {
			if span.start < cut && cut < span.end {
				cut = span.start
			}
		}
		for _, pair := range paired {
			if pair.start < cut && cut <= pair.openEnd {
				cut = pair.start
			} else if pair.closeStart < cut && cut < pair.end {
				cut = pair.closeStart
			}
		}
		if markdownEscapedAt(text, cut) {
			cut--
		}
		if cut != previous {
			continue
		}
		var closers strings.Builder
		for i := len(paired) - 1; i >= 0; i-- {
			pair := paired[i]
			if pair.openEnd < cut && cut <= pair.closeStart {
				closers.WriteString(text[pair.closeStart:pair.end])
			}
		}
		prefix := strings.TrimRightFunc(text[:cut], unicode.IsSpace)
		if utf8.RuneCountInString(prefix)+1+closers.Len() <= limit {
			return prefix + "…" + closers.String()
		}
		_, size := utf8.DecodeLastRuneInString(text[:cut])
		cut -= size
	}
}

func markdownExcerptAtomicSpans(text string) []byteSpan {
	spans := findProtectedSpans(text)
	for i := 0; i < len(text); i++ {
		if text[i] != '[' || markdownEscapedAt(text, i) {
			continue
		}
		end := markdownExcerptBalancedEnd(text, i, '[', ']')
		if end == 0 || end == len(text) || (text[end] != '(' && text[end] != '[') {
			continue
		}
		close := byte(')')
		if text[end] == '[' {
			close = ']'
		}
		if targetEnd := markdownExcerptBalancedEnd(text, end, text[end], close); targetEnd > 0 {
			start := i
			if i > 0 && text[i-1] == '!' && !markdownEscapedAt(text, i-1) {
				start--
			}
			spans = append(spans, byteSpan{start: start, end: targetEnd})
			i = targetEnd - 1
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	merged := make([]byteSpan, 0, len(spans))
	for _, span := range spans {
		if n := len(merged); n > 0 && span.start < merged[n-1].end {
			if span.end > merged[n-1].end {
				merged[n-1].end = span.end
			}
		} else {
			merged = append(merged, span)
		}
	}
	return merged
}

func markdownExcerptBalancedEnd(text string, start int, open, close byte) int {
	depth, quote := 0, byte(0)
	for i := start; i < len(text); i++ {
		if markdownEscapedAt(text, i) {
			continue
		}
		if quote != 0 {
			if text[i] == quote {
				quote = 0
			}
			continue
		}
		if open == '(' && (text[i] == '"' || text[i] == '\'') && i > start && (text[i-1] == ' ' || text[i-1] == '\t') {
			quote = text[i]
			continue
		}
		if text[i] == open {
			depth++
		} else if text[i] == close {
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return 0
}

type markdownExcerptPair struct {
	start, openEnd, closeStart, end int
}

func markdownExcerptPairs(text string, atomic []byteSpan) []markdownExcerptPair {
	var stack, pairs []markdownExcerptPair
	spanIndex := 0
	for i := 0; i < len(text); {
		for spanIndex < len(atomic) && atomic[spanIndex].end <= i {
			spanIndex++
		}
		if spanIndex < len(atomic) && atomic[spanIndex].start <= i {
			i = atomic[spanIndex].end
			continue
		}
		if !strings.ContainsRune("*_~", rune(text[i])) || markdownEscapedAt(text, i) {
			i++
			continue
		}
		end := i + 1
		for end < len(text) && text[end] == text[i] {
			end++
		}
		if end-i > 3 || (text[i] == '~' && end-i != 2) {
			i = end
			continue
		}
		before, after := markdownRuneBefore(text, i), markdownRuneAfter(text, end)
		beforeSpace, afterSpace := before == 0 || unicode.IsSpace(before), after == 0 || unicode.IsSpace(after)
		beforePunct, afterPunct := markdownPunctuation(before), markdownPunctuation(after)
		left := !afterSpace && (!afterPunct || beforeSpace || beforePunct)
		right := !beforeSpace && (!beforePunct || afterSpace || afterPunct)
		if text[i] == '_' {
			left, right = left && (!right || beforePunct), right && (!left || afterPunct)
		}
		if n := len(stack); right && n > 0 && text[stack[n-1].start:stack[n-1].openEnd] == text[i:end] {
			pair := stack[n-1]
			stack = stack[:n-1]
			pair.closeStart, pair.end = i, end
			pairs = append(pairs, pair)
		} else if left {
			stack = append(stack, markdownExcerptPair{start: i, openEnd: end})
		}
		i = end
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].start < pairs[j].start })
	return pairs
}
