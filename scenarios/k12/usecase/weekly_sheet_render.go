package usecase

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/mathtext"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

const weeklyMistakeSheetRenderContract = "weekly-mistake-sheet-v2"

// 固定版式只进入新错题卷的私有渲染投影，规范题面和历史PDF不改写。
const weeklyMistakeSheetStyle = "```{=typst}\n" + `
#let hc-weekly-question(n, body) = {
  let answer = block(breakable: false, stack(dir: ttb, spacing: 0pt, ..range(4).map(_ => block(width: 100%, height: 7mm, stroke: (bottom: 0.35pt + rgb("#dddddd"))))))
  let content = grid(columns: (8mm, 1fr), column-gutter: 0pt, align: (left, top), text(font: hc-heading-font, weight: "bold")[#n.], [#body #v(3mm) #answer])
  layout(size => {
    let height = measure(content, width: size.width).height
    if height > 246mm {
      block(breakable: true, above: 0mm, below: 2mm, inset: (top: 5mm))[
        #block(breakable: true, sticky: true)[
          #set par(hanging-indent: 8mm)
          #box(width: 8mm)[#text(font: hc-heading-font, weight: "bold")[#n.]]#body
          #v(3mm)
        ]
        #pad(left: 8mm, answer)
      ]
    } else {
      block(breakable: false, height: calc.max(height, 42mm) + 5mm, above: 0mm, below: 2mm, inset: (top: 5mm), content)
    }
  })
}
` + "\n```\n\n"

var weeklySheetFraction = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)/([0-9]+(?:\.[0-9]+)?)`)

// 纯数字算式先通过已有语法，再核对可读投影的表达式身份；隐式乘法不猜分母范围。
func weeklySheetQuestionMarkdown(question string) string {
	readable, _ := mathtext.ProjectReadable(question)
	facts := k12.ProblemAssetFacts{Subject: "数学", Stem: readable}
	// 日期或版本号也可能符合数字语法，缺少数学问法时仍保留原文字。
	mathIntent := strings.ContainsAny(readable, "×÷=") || strings.ContainsAny(question, "$\\")
	if _, ok := k12.ProblemAssetExpressionTerms(facts); ok && mathIntent {
		latex := weeklySheetFraction.ReplaceAllString(readable, `\frac{$1}{$2}`)
		latex = strings.NewReplacer("×", `\times `, "*", `\times `, "÷", `\div `, "−", "-").Replace(latex)
		projected, _ := mathtext.ProjectReadable("$" + latex + "$")
		if k12.EquivalentProblemAssetExpression(facts, k12.ProblemAssetFacts{Subject: "数学", Stem: projected}) {
			return "$" + latex + "$\n\n"
		}
	}
	// 混合题干交给既有Markdown reader形成文字和Math节点，不能整题退化为Typst字符串。
	// 代码、链接及既有数学定界符沿共享投影保留；题号和答题横线由外层布局提供。
	return mathtext.FormatPDFMarkdown(question) + "\n\n"
}

func weeklyMistakeSheetRenderMarkdown(canonical string) string {
	const answerMarker = "\n\n（　　）\n\n"
	first := "**1.** "
	start := strings.Index(canonical, first)
	if start < 0 {
		return weeklyMistakeSheetStyle + canonical
	}
	var b strings.Builder
	b.WriteString(weeklyMistakeSheetStyle)
	b.WriteString(canonical[:start])
	remaining := canonical[start+len(first):]
	for number := 1; ; number++ {
		next := answerMarker + fmt.Sprintf("**%d.** ", number+1)
		end := strings.Index(remaining, next)
		question := remaining
		if end >= 0 {
			question = remaining[:end]
		} else {
			question = strings.TrimSuffix(question, answerMarker)
		}
		fmt.Fprintf(&b, "```{=typst}\n#hc-weekly-question(%d)[\n```\n\n", number)
		b.WriteString(weeklySheetQuestionMarkdown(question))
		b.WriteString("```{=typst}\n]\n```\n\n")
		if end < 0 {
			break
		}
		remaining = remaining[end+len(next):]
	}
	return b.String()
}
