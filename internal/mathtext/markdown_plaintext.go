package mathtext

import (
	"bufio"
	"bytes"
	"html"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// PlainMarkdownText 从语法树提取可读文本，代码中的字面符号不作为排版标记删除。
// 标题、列表和表格沿块边界分隔；链接保留标签，原始 Markdown 不改写。
func PlainMarkdownText(markdown string) string {
	readable, _ := ProjectReadable(markdown)
	source := []byte(NormalizeDingTalkMarkdown(readable))
	parser := goldmark.New(goldmark.WithExtensions(extension.GFM))
	document := parser.Parser().Parse(text.NewReader(source))
	var out strings.Builder
	var writeNode func(ast.Node, bool)
	writeText := func(value []byte, literal bool) {
		if literal {
			out.Write(value)
			return
		}
		var encoded bytes.Buffer
		writer := bufio.NewWriter(&encoded)
		gmhtml.DefaultWriter.Write(writer, value)
		_ = writer.Flush()
		out.WriteString(html.UnescapeString(encoded.String()))
	}
	writeNode = func(node ast.Node, literal bool) {
		switch value := node.(type) {
		case *ast.Text:
			writeText(value.Value(source), literal || value.IsRaw())
			if value.SoftLineBreak() || value.HardLineBreak() {
				out.WriteByte(' ')
			}
			return
		case *ast.String:
			writeText(value.Value, literal || value.IsRaw() || value.IsCode())
			return
		case *ast.CodeBlock, *ast.FencedCodeBlock:
			out.Write(node.Lines().Value(source))
			out.WriteByte(' ')
			return
		case *ast.AutoLink:
			writeText(value.Label(source), true)
			return
		case *ast.HTMLBlock, *ast.RawHTML:
			return
		case *extast.TaskCheckBox:
			if value.IsChecked {
				out.WriteString("[x] ")
			} else {
				out.WriteString("[ ] ")
			}
			return
		case *ast.CodeSpan:
			literal = true
		}
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			writeNode(child, literal)
		}
		if node.Type() == ast.TypeBlock {
			out.WriteByte(' ')
		}
	}
	writeNode(document, false)
	return strings.Join(strings.Fields(out.String()), " ")
}

// EscapeMarkdownInline 将纯文本作为行内内容嵌入 Markdown，避免摘要被再次解释为语法。
func EscapeMarkdownInline(value string) string {
	var out strings.Builder
	for _, character := range value {
		// 方括号与反斜杠用实体保留，避免后续公式投影误认成 TeX 分隔符。
		switch character {
		case '\\', '[', ']', '<', '>', '&':
			out.WriteString(map[rune]string{'\\': "&#92;", '[': "&#91;", ']': "&#93;", '<': "&lt;", '>': "&gt;", '&': "&amp;"}[character])
			continue
		}
		if strings.ContainsRune("`*_{}#!|~", character) {
			out.WriteByte('\\')
		}
		out.WriteRune(character)
	}
	return out.String()
}
