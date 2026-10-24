package k12

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hexagon-codes/hexclaw/internal/mathtext"
)

// CompactQuestionSourceFragments 合并同一题已经确认的来源片段。
// 只比较前序来源中的完整句或行，保留首次出现的原始Markdown及同一片段内部的重复。
// 调用者负责同题来源归属；不能把不同错题记录当作这些片段传入。
func CompactQuestionSourceFragments(fragments ...string) string {
	seen := make(map[string]struct{})
	kept := make([]string, 0, len(fragments))
	for _, fragment := range fragments {
		if strings.TrimSpace(fragment) == "" {
			continue
		}
		pieces := splitQuestionSourceFragment(fragment)
		var content strings.Builder
		var newKeys []string
		removed := false
		for _, piece := range pieces {
			key := questionSourceFragmentKey(piece.text)
			if key != "" {
				key = piece.scope + "\x00" + key
				if _, exists := seen[key]; exists {
					removed = true
					continue
				}
				newKeys = append(newKeys, key)
			}
			content.WriteString(piece.text)
		}
		// 本片段处理完再记录，避免把英语跟读等原材料内部的有意重复删除。
		for _, key := range newKeys {
			seen[key] = struct{}{}
		}
		value := fragment
		if removed {
			value = strings.Trim(content.String(), "\r\n")
		}
		if strings.TrimSpace(value) != "" {
			kept = append(kept, value)
		}
	}
	return strings.Join(kept, "\n\n")
}

type questionSourcePiece struct {
	text  string
	scope string
}

var questionSourceLabel = regexp.MustCompile(`^[ \t]*([（(][ \t]*[0-9０-９]+[ \t]*[）)]|[0-9０-９]+[、.)．]|第[0-9０-９一二三四五六七八九十百]+题)`)

// 数学和代码定界符内不拆句，避免比较不完整公式或改变引用代码的含义。
func splitQuestionSourceFragment(source string) []questionSourcePiece {
	var pieces []questionSourcePiece
	start, offset := 0, 0
	scope, mathDelimiter := "", ""
	codeRun := 0
	lineStart := true
	for offset < len(source) {
		if lineStart && mathDelimiter == "" && codeRun == 0 {
			if location := questionSourceLabel.FindStringSubmatchIndex(source[offset:]); location != nil {
				label := source[offset+location[2] : offset+location[3]]
				after := offset + location[1]
				next, _ := utf8.DecodeRuneInString(source[after:])
				// 1.25是数量，不是题号1；印刷题号本身始终参与比较。
				if !strings.HasSuffix(label, ".") && !strings.HasSuffix(label, "．") || !unicode.IsDigit(next) {
					scope = questionSourceFragmentKey(label)
				}
			}
			lineStart = false
		}
		if source[offset] == '`' {
			end := offset
			for end < len(source) && source[end] == '`' {
				end++
			}
			if codeRun == 0 {
				codeRun = end - offset
			} else if codeRun == end-offset {
				codeRun = 0
			}
			offset = end
			continue
		}
		if codeRun == 0 {
			if mathDelimiter != "" && strings.HasPrefix(source[offset:], mathDelimiter) {
				offset += len(mathDelimiter)
				mathDelimiter = ""
				continue
			}
			if mathDelimiter == "" {
				switch {
				case strings.HasPrefix(source[offset:], `\(`):
					mathDelimiter, offset = `\)`, offset+2
					continue
				case strings.HasPrefix(source[offset:], `\[`):
					mathDelimiter, offset = `\]`, offset+2
					continue
				case strings.HasPrefix(source[offset:], "$$"):
					mathDelimiter, offset = "$$", offset+2
					continue
				case source[offset] == '$' && (offset == 0 || source[offset-1] != '\\'):
					mathDelimiter, offset = "$", offset+1
					continue
				}
			}
		}
		character, size := utf8.DecodeRuneInString(source[offset:])
		offset += size
		if mathDelimiter == "" && codeRun == 0 && (character == '\n' || strings.ContainsRune("。！？?!", character)) {
			pieces = append(pieces, questionSourcePiece{text: source[start:offset], scope: scope})
			start = offset
		}
		if character == '\n' {
			lineStart = true
		}
	}
	if start < len(source) {
		pieces = append(pieces, questionSourcePiece{text: source[start:], scope: scope})
	}
	return pieces
}

// 比较副本只规范已支持的数学和全角字符，数字、英文单词及单位保留词边界和大小写。
func questionSourceFragmentKey(source string) string {
	readable, _ := mathtext.ProjectReadable(source)
	normalized := strings.Map(func(r rune) rune {
		if r >= '！' && r <= '～' {
			return r - 0xFEE0
		}
		switch r {
		case '。':
			return '.'
		case '，':
			return ','
		case '；':
			return ';'
		case '：', '∶':
			return ':'
		case '　':
			return ' '
		}
		return r
	}, readable)
	runes := []rune(normalized)
	var tokens []string
	for i := 0; i < len(runes); {
		if unicode.IsSpace(runes[i]) {
			i++
			continue
		}
		start := i
		i++
		if runes[start] >= '0' && runes[start] <= '9' {
			for i < len(runes) && ((runes[i] >= '0' && runes[i] <= '9') || runes[i] == '.' && i+1 < len(runes) && runes[i+1] >= '0' && runes[i+1] <= '9') {
				i++
			}
		} else if questionSourceLatinLetter(runes[start]) {
			for i < len(runes) && questionSourceLatinLetter(runes[i]) {
				i++
			}
		}
		tokens = append(tokens, string(runes[start:i]))
	}
	// 缺失的句末句号只属于转写排版差异；问号、感叹号和数字内部的小数点仍保留。
	if len(tokens) > 0 && tokens[len(tokens)-1] == "." {
		tokens = tokens[:len(tokens)-1]
	}
	return strings.Join(tokens, "\x1f")
}

func questionSourceLatinLetter(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}
