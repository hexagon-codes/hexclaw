package usecase

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestWeeklySheetMixedQuestionPreservesPandocMathNodes(t *testing.T) {
	pandoc, err := exec.LookPath("pandoc")
	if err != nil {
		t.Skip("pandoc is unavailable; real math AST verification was not run")
	}
	question := "Read this condition in English. Code example: `1/2`.\n\n" +
		`已用空间是总容量的\(\frac{2}{5}\)，已用了 $4.8\,\mathrm{TB}$。` + "\n\n" +
		"请根据下面的数量关系列式：\n\\[\nx\\times\\frac{2}{5}=4.8\n\\]\n\n列式：________"
	canonical := "# 本周错题卷\n\n**1.** " + question + "\n\n（　　）\n\n"
	projected := weeklyMistakeSheetRenderMarkdown(canonical)
	// 使用当前渲染器相同的reader；断言数学节点和原文，不断言字号或CSS。
	cmd := exec.Command(pandoc, "--from=markdown-raw_html-raw_tex+pipe_tables+task_lists+tex_math_single_backslash+tex_math_double_backslash", "--to=json")
	cmd.Stdin = strings.NewReader(projected)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("parse real Pandoc AST: %v\n%s", err, raw)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	mathNodes := make(map[string]string)
	var codeNodes []string
	var words []string
	var walk func(any)
	walk = func(value any) {
		switch node := value.(type) {
		case []any:
			for _, child := range node {
				walk(child)
			}
		case map[string]any:
			if node["t"] == "Math" {
				content := node["c"].([]any)
				kind := content[0].(map[string]any)["t"].(string)
				mathNodes[strings.TrimSpace(content[1].(string))] = kind
			}
			if node["t"] == "Str" {
				words = append(words, node["c"].(string))
			}
			if node["t"] == "Code" {
				codeNodes = append(codeNodes, node["c"].([]any)[1].(string))
			}
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(tree)
	for latex, kind := range map[string]string{`\frac{2}{5}`: "InlineMath", `4.8\,\mathrm{TB}`: "InlineMath", `x\times\frac{2}{5}=4.8`: "DisplayMath"} {
		if mathNodes[latex] != kind {
			t.Fatalf("formula lost its mathematical representation: %q got=%q", latex, mathNodes[latex])
		}
	}
	if len(codeNodes) != 1 || codeNodes[0] != "1/2" {
		t.Fatalf("literal code was reinterpreted as mathematics: %v", codeNodes)
	}
	text := strings.Join(words, " ")
	if !strings.Contains(text, "Read this condition in English.") || !strings.Contains(text, "已用空间是总容量的") || !strings.Contains(text, "请根据下面的数量关系列式") {
		t.Fatalf("mixed-language source text was lost: %s", text)
	}
}
