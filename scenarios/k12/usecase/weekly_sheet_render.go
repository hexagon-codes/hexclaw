package usecase

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hexagon-codes/hexclaw/internal/mathtext"
	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

const weeklyMistakeSheetRenderContract = "weekly-mistake-sheet-v1"

// 固定版式只进入新错题卷的私有渲染投影，规范题面和历史PDF不改写。
const weeklyMistakeSheetStyle = "```{=typst}\n" + `
#set page(paper: "a4", margin: 20mm, numbering: "第 1/1 页", number-align: center)
#set text(size: 12pt)
#set par(leading: 0.45em, spacing: 0.25em)
#show heading.where(level: 1): set text(size: 18pt)
#let hc-weekly-question(n, body) = {
  let answer = block(breakable: false, stack(dir: ttb, spacing: 0pt, ..range(4).map(_ => block(width: 100%, height: 7mm, stroke: (bottom: 0.35pt + rgb("#dddddd"))))))
  let content = grid(columns: (8mm, 1fr), column-gutter: 0pt, align: (left, top), text(weight: "bold")[#n.], [#body #v(3mm) #answer])
  layout(size => {
    let height = measure(content, width: size.width).height
    if height > 257mm {
      block(breakable: true, above: 5mm, below: 2mm)[
        #block(breakable: true, sticky: true)[
          #set par(hanging-indent: 8mm)
          #box(width: 8mm)[#text(weight: "bold")[#n.]]#body
          #v(3mm)
        ]
        #pad(left: 8mm, answer)
      ]
    } else {
      block(breakable: false, height: calc.max(height, 42mm), above: 5mm, below: 2mm, content)
    }
  })
}
` + "\n```\n\n"

var weeklySheetFraction = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)/([0-9]+(?:\.[0-9]+)?)`)

// 纯数字算式先通过已有语法，再核对可读投影的表达式身份；隐式乘法不猜分母范围。
func weeklySheetQuestionMarkdown(question string) string {
	readable, _ := mathtext.ProjectReadable(question)
	facts := k12.ProblemAssetFacts{Subject: "数学", Stem: readable}
	if _, ok := k12.ProblemAssetExpressionTerms(facts); ok {
		latex := weeklySheetFraction.ReplaceAllString(readable, `\frac{$1}{$2}`)
		latex = strings.NewReplacer("×", `\times `, "*", `\times `, "÷", `\div `, "−", "-").Replace(latex)
		projected, _ := mathtext.ProjectReadable("$" + latex + "$")
		if k12.EquivalentProblemAssetExpression(facts, k12.ProblemAssetFacts{Subject: "数学", Stem: projected}) {
			return "$" + latex + "$\n\n"
		}
	}
	trimmed := strings.TrimSpace(question)
	if !strings.ContainsAny(trimmed, "\r\n") && ((strings.HasPrefix(trimmed, "$") && strings.HasSuffix(trimmed, "$") && strings.Count(trimmed, "$") == 2) ||
		(strings.HasPrefix(trimmed, "$$") && strings.HasSuffix(trimmed, "$$") && strings.Count(trimmed, "$") == 4) ||
		(strings.HasPrefix(trimmed, `\(`) && strings.HasSuffix(trimmed, `\)`) && strings.Count(trimmed, `\(`) == 1 && strings.Count(trimmed, `\)`) == 1) ||
		(strings.HasPrefix(trimmed, `\[`) && strings.HasSuffix(trimmed, `\]`) && strings.Count(trimmed, `\[`) == 1 && strings.Count(trimmed, `\]`) == 1)) {
		return question + "\n\n"
	}
	// 动态题干仅作为文字参数，不拼成可执行Typst代码。
	var literal strings.Builder
	literal.WriteString("```{=typst}\n#text(\"")
	for _, r := range question {
		switch r {
		case '\\', '"':
			literal.WriteByte('\\')
			literal.WriteRune(r)
		default:
			if r < 0x20 {
				fmt.Fprintf(&literal, `\u{%x}`, r)
			} else {
				literal.WriteRune(r)
			}
		}
	}
	literal.WriteString("\")\n```\n\n")
	return literal.String()
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
