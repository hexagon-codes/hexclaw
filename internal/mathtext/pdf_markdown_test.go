package mathtext

import "testing"

func TestPDFNumericFractionsPreserveMeaningAndProtectedContent(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"textbook", "剩余3/4，20÷2/3＝30。", `剩余$\frac{3}{4}$，20÷$\frac{2}{3}$＝30。`},
		{"bare numeric TeX in a teaching paragraph", `把\frac14x理解为x的四分之一，代回24−\frac14×24=18。`, `把$\frac{1}{4}$x理解为x的四分之一，代回24−$\frac{1}{4}$×24=18。`},
		{"braced numeric TeX", `比较\frac{12}{25}和\tfrac1{2}。`, `比较$\frac{12}{25}$和$\tfrac{1}{2}$。`},
		{"bare arguments must be unambiguous", `\frac123，\frac14.5，\frac{ 12 }{ 25 }。`, `\frac123，\frac14.5，$\frac{12}{25}$。`},
		{"non-ASCII file paths", `C:\题目\frac14  /tmp/题目\frac14.png  ./课程/题目\frac14`, `C:\题目\frac14  /tmp/题目\frac14.png  ./课程/题目\frac14`},
		{"protected and incomplete TeX", "\x60\\frac14\x60 https://example.test/\\frac14 " + `C:\docs\frac14 \\frac14 \fracture \frac1，\frac{x}{4}，$\frac14$。`, "\x60\\frac14\x60 https://example.test/\\frac14 " + `C:\docs\frac14 \\frac14 \fracture \frac1，\frac{x}{4}，$\frac14$。`},
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
