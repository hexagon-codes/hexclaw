package mathtext

import "testing"

func TestPlainMarkdownTextPreservesReadableMeaning(t *testing.T) {
	for _, tc := range []struct {
		name, markdown, want string
	}{
		{"solve heading", "### 第一步\n\n**先找单位一：** 再列式。", "第一步 先找单位一： 再列式。"},
		{"literal code", "`a*b _x_ ** # []` 与\n\n```python\nprint(2 ** 3)\n```", "a*b _x_ ** # [] 与 print(2 ** 3)"},
		{"links and table", "[教材第36页](https://example.invalid/book?page=36)\n\n| 项目 | 值 |\n| --- | --- |\n| 原长 | **40米** |", "教材第36页 项目 值 原长 40米"},
		{"escapes and entities", `\*字面\* &amp; \&copy; &copy;`, "*字面* & &copy; ©"},
		{"code entities", "`&amp; \\*`", `&amp; \*`},
		{"code math literals", "`\\frac{1}{2} \\[x\\]`", `\frac{1}{2} \[x\]`},
		{"blocks and image label", "> 引用\n\n1. 第一个\n2. 第二个\n\n![图示](https://example.invalid/image.png)<!--hidden-->", "引用 第一个 第二个 图示"},
		{"mixed text", "### سوئال\n\n**单位一** بىلەن 40 مېتىر", "سوئال 单位一 بىلەن 40 مېتىر"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PlainMarkdownText(tc.markdown); got != tc.want {
				t.Fatalf("readable projection=%q, want %q", got, tc.want)
			}
			if got := PlainMarkdownText(EscapeMarkdownInline(tc.want)); got != tc.want {
				t.Fatalf("inline projection changed literal characters: %q, want %q", got, tc.want)
			}
		})
	}
}
