package mathtext

import (
	"regexp"
	"strings"
)

var pdfDateLabel = regexp.MustCompile(`(?i)(日期|生日|截至|截止|date)[：: \t]*$`)

// FormatPDFMarkdown 仅在PDF副本中把可靠的裸数字分数排成上下分数。
// 代码、链接、URL、日期和已有数学定界符保持，原始规范Markdown不修改。
func FormatPDFMarkdown(source string) string {
	var out strings.Builder
	last := 0
	for _, span := range findProtectedSpans(source) {
		out.WriteString(formatPDFFractionSegment(source[last:span.start]))
		out.WriteString(source[span.start:span.end])
		last = span.end
	}
	out.WriteString(formatPDFFractionSegment(source[last:]))
	return out.String()
}

func formatPDFFractionSegment(source string) string {
	var out strings.Builder
	for i := 0; i < len(source); {
		end := 0
		if source[i] == '$' && !isEscapedAt(source, i) {
			if strings.HasPrefix(source[i:], "$$") {
				if close := strings.Index(source[i+2:], "$$"); close >= 0 {
					end = i + 2 + close + 2
				}
			} else if close := findInlineDollarClose(source, i+1); close > i+1 {
				end = close + 1
			}
		} else if strings.HasPrefix(source[i:], `\(`) || strings.HasPrefix(source[i:], `\[`) {
			close := `\)`
			if source[i+1] == '[' {
				close = `\]`
			}
			if offset := strings.Index(source[i+2:], close); offset >= 0 {
				end = i + 2 + offset + 2
			}
		}
		if end > i {
			out.WriteString(source[i:end])
			i = end
			continue
		}
		if source[i] >= '0' && source[i] <= '9' && (i == 0 || !pdfFractionBoundary(source[i-1])) && !pdfDateLabel.MatchString(source[:i]) {
			numeratorEnd := i
			for numeratorEnd < len(source) && source[numeratorEnd] >= '0' && source[numeratorEnd] <= '9' {
				numeratorEnd++
			}
			if numeratorEnd < len(source) && source[numeratorEnd] == '/' {
				end = numeratorEnd + 1
				for end < len(source) && source[end] >= '0' && source[end] <= '9' {
					end++
				}
				if end > numeratorEnd+1 && (end == len(source) || !pdfFractionBoundary(source[end])) {
					out.WriteString(`$\frac{`)
					out.WriteString(source[i:numeratorEnd])
					out.WriteString(`}{`)
					out.WriteString(source[numeratorEnd+1 : end])
					out.WriteString(`}$`)
					i = end
					continue
				}
			}
		}
		out.WriteByte(source[i])
		i++
	}
	return out.String()
}

func pdfFractionBoundary(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		strings.ContainsRune(`/\._$,#:`, rune(value))
}
