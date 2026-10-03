package k12

import (
	"encoding/json"
	"errors"
	"strings"
)

// CompleteRecognitionLayoutBatchJSON 只补 items 末项遗漏的右括号或完整末项后的封套闭合。
// 返回值仅供解析；调用者必须继续核对冻结合同，不得覆盖原始物理回执或摘要。
func CompleteRecognitionLayoutBatchJSON(raw string) (string, bool) {
	var envelope map[string]json.RawMessage
	err := json.Unmarshal([]byte(raw), &envelope)
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		return raw, false
	}
	index := int(syntax.Offset) - 1
	completion, expectedStack := "}", "{[{"
	if syntax.Offset == int64(len(raw)) && syntax.Error() == "unexpected end of JSON input" {
		// EOF 只能缺 items 数组和顶层对象的闭合，不补末项内容。
		index = len(raw)
		completion, expectedStack = "]}", "{["
	} else if index < 0 || index >= len(raw) || raw[index] != ']' || strings.TrimSpace(raw[index+1:]) != "}" {
		return raw, false
	}
	// 忽略字符串及转义里的括号，严格核对缺损位置留下的封套或末项栈。
	var stack []byte
	inString := false
	for i := 0; i < index; i++ {
		c := raw[i]
		if inString {
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, c)
		case '}', ']':
			if len(stack) == 0 || (c == '}' && stack[len(stack)-1] != '{') || (c == ']' && stack[len(stack)-1] != '[') {
				return raw, false
			}
			stack = stack[:len(stack)-1]
		}
	}
	if inString || string(stack) != expectedStack {
		return raw, false
	}
	candidate := raw[:index] + completion + raw[index:]
	if json.Unmarshal([]byte(candidate), &envelope) != nil || len(envelope) != 1 {
		return raw, false
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(envelope["items"], &items) != nil || len(items) == 0 || items[len(items)-1] == nil {
		return raw, false
	}
	return candidate, true
}
