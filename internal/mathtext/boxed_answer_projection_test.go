package mathtext

import "testing"

func TestBoxedAnswerProjectionPreservesActualRoadWorkReply(t *testing.T) {
	const source = `甲单独修完需要：
\[
1\div\frac1{10}=\boxed{10\text{天}}
\]

**检验：**甲做3天完成 \(\frac3{10}\)，乙做10.5天完成 \(\frac7{10}\)，合起来恰好完成整条公路，符合题意。`
	const want = "甲单独修完需要：\n1÷(1/10)=10天\n\n**检验：**甲做3天完成 3/10，乙做10.5天完成 7/10，合起来恰好完成整条公路，符合题意。"
	got, changed := ProjectReadable(source)
	if !changed || got != want {
		t.Fatalf("boxed answer or surrounding mathematical meaning changed:\n got %q\nwant %q", got, want)
	}
	if again, changed := ProjectReadable(got); changed || again != got {
		t.Fatalf("answer projection is not idempotent: %q", again)
	}
}

func TestBoxedAnswerProjectionRetainsNestedMathAndProtectedText(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"nested fraction", `\(\boxed{\frac{3}{4}}\)`, "3/4"},
		{"escaped mathematical tokens", `\\[\\boxed{10\\text{天}}\\]`, "10天"},
		{"ordinary word", "boxed{10天}", "boxed{10天}"},
		{"longer command", `\boxedExample{10天}`, `\boxedExample{10天}`},
		{"inline code", "`\\boxed{10\\text{天}}`", "`\\boxed{10\\text{天}}`"},
		{"fenced code", "```tex\n\\boxed{10\\text{天}}\n```", "```tex\n\\boxed{10\\text{天}}\n```"},
		{"URL", `https://example.test/\boxed{10}`, `https://example.test/\boxed{10}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := ProjectReadable(tc.source); got != tc.want {
				t.Fatalf("projection=%q want=%q", got, tc.want)
			}
		})
	}
}
