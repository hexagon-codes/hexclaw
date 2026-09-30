package k12

import (
	"encoding/json"
	"errors"
	"strings"
)

// CompleteRecognitionLayoutBatchJSON 只补主批 items 最后一个对象遗漏的单个右括号。
// 返回值仅供解析；调用者必须继续核对冻结合同，不得覆盖原始物理回执或摘要。
func CompleteRecognitionLayoutBatchJSON(raw string) (string, bool) {
	var envelope map[string]json.RawMessage
	err := json.Unmarshal([]byte(raw), &envelope)
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		return raw, false
	}
	index := int(syntax.Offset) - 1
	if index < 0 || index >= len(raw) || raw[index] != ']' || strings.TrimSpace(raw[index+1:]) != "}" {
		return raw, false
	}
	// 忽略字符串及转义里的括号；错误位置只能留下顶层对象、items 数组和末项对象。
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
	if inString || string(stack) != "{[{" {
		return raw, false
	}
	candidate := raw[:index] + "}" + raw[index:]
	if json.Unmarshal([]byte(candidate), &envelope) != nil || len(envelope) != 1 {
		return raw, false
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(envelope["items"], &items) != nil || len(items) == 0 || items[len(items)-1] == nil {
		return raw, false
	}
	return candidate, true
}
