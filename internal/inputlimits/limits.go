// Package inputlimits 提供用户输入的长度计数，不改变字段内容或历史值。
package inputlimits

import (
	"fmt"
	"unicode/utf8"
)

const (
	DisplayName    = 64
	ChildName      = 40
	Title          = 200
	Description    = 2000
	Keyword        = 512
	TimeExpression = 256
	TechnicalID    = 256
	URLBytes       = 8 << 10
	SecretBytes    = 64 << 10
)

// Text 只校验新增或改变的文字字段，原样保存旧值时不收紧历史契约。
func Text(field, value, previous string, max int) error {
	if value != previous && utf8.RuneCountInString(value) > max {
		return fmt.Errorf("%s must not exceed %d characters", field, max)
	}
	return nil
}

// Bytes 按原始 UTF-8 字节校验地址或凭据，不修剪、截断或回显其内容。
func Bytes(field, value, previous string, max int) error {
	if value != previous && len(value) > max {
		return fmt.Errorf("%s must not exceed %d UTF-8 bytes", field, max)
	}
	return nil
}
