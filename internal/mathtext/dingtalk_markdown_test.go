package mathtext

import "testing"

func TestDingTalkEmphasisNormalizeReliableBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"observed reason one", "**原因：**后文已经写了使用过程，这里合成一句就能交代清楚，把更多注意力留给爸爸和“我”。", "**原因**：后文已经写了使用过程，这里合成一句就能交代清楚，把更多注意力留给爸爸和“我”。"},
		{"observed reason two", "**原因：**用“原来”和“也在帮我”突出“我”对爸爸的新理解，让这句话不只是在说明功能。", "**原因**：用“原来”和“也在帮我”突出“我”对爸爸的新理解，让这句话不只是在说明功能。"},
		{"nested strong emphasis", "***原因：***后文继续。", "***原因***：后文继续。"},
		{"closing Chinese quote", "最后让她说：**“为什么等式两边要同时除以0.75？”**检查她是否理解。", "最后让她说：**“为什么等式两边要同时除以0.75？”** 检查她是否理解。"},
		{"already separated quote", "**“先验算。”** 再说理由。", "**“先验算。”** 再说理由。"},
		{"preserve label spacing", "**原因：** 后文继续。", "**原因**： 后文继续。"},
		{"two independent labels", "**原句：**保留原句。\n\n**建议：**修改建议。", "**原句**：保留原句。\n\n**建议**：修改建议。"},
		{"link visible label", "[**原因：**说明](https://example.invalid/path)", "[**原因**：说明](https://example.invalid/path)"},
		{"linked emphasis next to Chinese", "**[资料](https://example.invalid/a)**继续阅读。", "**[资料](https://example.invalid/a)** 继续阅读。"},
		{"math remains independent", `**公式：** $\frac{1}{2}$`, `**公式**： $\frac{1}{2}$`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeDingTalkMarkdown(tc.source)
			if got != tc.want {
				t.Fatalf("emphasis projection changed content or boundary:\n got %q\nwant %q", got, tc.want)
			}
			if twice := NormalizeDingTalkMarkdown(got); twice != got {
				t.Fatalf("normalization is not idempotent: %q", twice)
			}
		})
	}
}

func TestDingTalkEmphasisPreservesProtectedAndAmbiguousContent(t *testing.T) {
	for _, source := range []string{
		"```markdown\n**原因：**后文\n```",
		"~~~text\n**原因：**后文\n~~~",
		"代码 `**原因：**后文` 保持。",
		`\*\*原因：\*\*后文`,
		"https://example.invalid/**原因：**后文",
		"[说明](https://example.invalid/**原因：**后文)",
		"![说明](https://example.invalid/**原因：**后文.png)",
		`$x=**原因：**y$`,
		`\(**原因：**x\)`,
		"$$\n**原因：**x\n$$",
		`\[**原因：**x\]`,
		"未配对 **原因：后文",
		"普通算式 2**3 + 4**5。",
		"2**3",
		"**结论**成立。",
		"**原因**：已规范。",
	} {
		t.Run(source, func(t *testing.T) {
			if got := NormalizeDingTalkMarkdown(source); got != source {
				t.Fatalf("protected or unpaired content changed:\n got %q\nwant %q", got, source)
			}
		})
	}
}
