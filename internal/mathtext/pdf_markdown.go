package mathtext

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	pdfDateLabel         = regexp.MustCompile(`(?i)(日期|生日|截至|截止|date)[：: \t]*$`)
	pdfPageNumberPrefix  = regexp.MustCompile(`第[ \t]*$`)
	pdfPageNumberSuffix  = regexp.MustCompile(`^[ \t]*页`)
	pdfHeadingDatePrefix = regexp.MustCompile(`(?m)^#{1,6}[ \t]+[^\r\n]+·[ \t]*$`)
	pdfHeadingDateSuffix = regexp.MustCompile(`^[ \t]*(?:·[ \t]*答案卷)?[ \t]*(?:\r?\n|$)`)
	pdfQuotedPathPrefix  = regexp.MustCompile(`["'“‘「『](?:[A-Za-z]:[\\/]|\.{0,2}/|\\\\)[^"'“”‘’「」『』\r\n]*$`)
	pdfRelativePath      = regexp.MustCompile(`^(?:[\p{L}_][\p{L}\p{N}_.-]*/)+[\p{L}\p{N}_.-]*[\p{L}_][\p{L}\p{N}_.-]*$`)
)

// FormatPDFMarkdown 仅在 PDF 副本中把可靠的数字分数排成上下分数。
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
		if source[i] == '\\' && !isEscapedAt(source, i) {
			if formula, next, ok := pdfNumericTeXFraction(source, i); ok {
				out.WriteString(formula)
				i = next
				continue
			}
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
				if end > numeratorEnd+1 && (end == len(source) || !pdfFractionBoundary(source[end])) &&
					!pdfLiteralNumberPair(source, i, numeratorEnd, end) {
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

// 只接纳两个完整数字参数，与已有文本分数使用相同的数学节点。例如 \frac14x 中的 x
// 仍是后继变量。路径、代码和原本已定界的公式保留它们自己的语义。
func pdfNumericTeXFraction(s string, at int) (string, int, bool) {
	if at > 0 && (isASCIILetter(s[at-1]) || strings.ContainsRune(`/\.:_`, rune(s[at-1]))) {
		return "", 0, false
	}
	for _, cmd := range [...]string{`\frac`, `\dfrac`, `\tfrac`} {
		if !strings.HasPrefix(s[at:], cmd) {
			continue
		}
		const separators = " \t\r\n，。；：！？（(【[「“\"'"
		start := max(0, strings.LastIndexAny(s[:at], separators))
		word := strings.TrimLeft(s[start:at], separators)
		if strings.Contains(word, `:\`) || strings.Contains(word, ":/") || strings.HasPrefix(word, "/") ||
			strings.HasPrefix(word, "./") || strings.HasPrefix(word, "../") || strings.HasPrefix(word, `\\`) ||
			pdfQuotedPathPrefix.MatchString(s[:at]) || pdfRelativePath.MatchString(word) {
			break
		}
		n, next, ok := parseBraceArg(s, at+len(cmd), true)
		n = strings.TrimSpace(n)
		if !ok || n == "" || strings.Trim(n, "0123456789") != "" {
			break
		}
		d, end, ok := parseBraceArg(s, next, true)
		d = strings.TrimSpace(d)
		if !ok || d == "" || strings.Trim(d, "0123456789") != "" {
			break
		}
		ambiguousDigits := end < len(s) && s[end] >= '0' && s[end] <= '9' && s[end-1] != '}'
		fileOrDecimalSuffix := end+1 < len(s) && s[end] == '.' && (isASCIILetter(s[end+1]) || s[end+1] >= '0' && s[end+1] <= '9')
		if ambiguousDigits || fileOrDecimalSuffix {
			break
		}
		return "$" + cmd + "{" + n + "}{" + d + "}$", end, true
	}
	return "", 0, false
}

func pdfLiteralNumberPair(source string, start, slash, end int) bool {
	if pdfPageNumberPrefix.MatchString(source[:start]) && pdfPageNumberSuffix.MatchString(source[end:]) {
		return true
	}
	// 卷名末尾的月/日和可选答案卷标签属于元信息，正文中的数字分数仍按数学排版。
	if !pdfHeadingDatePrefix.MatchString(source[:start]) || !pdfHeadingDateSuffix.MatchString(source[end:]) {
		return false
	}
	month, _ := strconv.Atoi(source[start:slash])
	day, _ := strconv.Atoi(source[slash+1 : end])
	return month >= 1 && month <= 12 && day >= 1 && day <= 31
}

func pdfFractionBoundary(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		strings.ContainsRune(`/\._$,#:`, rune(value))
}
