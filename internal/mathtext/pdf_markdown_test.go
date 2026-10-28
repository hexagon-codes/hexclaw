package mathtext

import "testing"

func TestPDFNumericFractionsPreserveMeaningAndProtectedContent(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"textbook", "剩余3/4，20÷2/3＝30。", `剩余$\frac{3}{4}$，20÷$\frac{2}{3}$＝30。`},
		{"code", "`1/2`\n\n```python\nx = 3/4\n```", "`1/2`\n\n```python\nx = 3/4\n```"},
		{"url", "https://example.test/3/4 [链接](https://example.test/1/2)", "https://example.test/3/4 [链接](https://example.test/1/2)"},
		{"dates and paths", `2026/10/20 v1/2 /tmp/3/4 1.5/2`, `2026/10/20 v1/2 /tmp/3/4 1.5/2`},
		{"explicit short dates", "日期：10/20；date: 12/31", "日期：10/20；date: 12/31"},
		{"question paper date", "# 数学专项 · 10/29\n\n占总数的1/3。", "# 数学专项 · 10/29\n\n占总数的" + `$\frac{1}{3}$。`},
		{"answer paper date", "# 数学专项 · 10/29 · 答案卷\n\n占总数的1/3。", "# 数学专项 · 10/29 · 答案卷\n\n占总数的" + `$\frac{1}{3}$。`},
		{"paper page counts", "第 1/1 页 · P-2644-01\n第2/12页；份数1/3。", "第 1/1 页 · P-2644-01\n第2/12页；份数" + `$\frac{1}{3}$。`},
		{"ordinary heading fraction", "# 分数1/3\n\n# 数学计算 · 13/4", "# 分数" + `$\frac{1}{3}$` + "\n\n# 数学计算 · " + `$\frac{13}{4}$`},
		{"existing math", `$1/2$ $$3/4$$ \(5/6\) \[7/8\]`, `$1/2$ $$3/4$$ \(5/6\) \[7/8\]`},
		{"unicode", "قالدۇق 2/3، 份数1/3。", `قالدۇق $\frac{2}{3}$، 份数$\frac{1}{3}$。`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatPDFMarkdown(tc.input)
			if got != tc.want {
				t.Fatalf("PDF fraction projection: got %q, want %q", got, tc.want)
			}
			if FormatPDFMarkdown(got) != got {
				t.Fatal("PDF fraction projection is not idempotent")
			}
		})
	}
}
