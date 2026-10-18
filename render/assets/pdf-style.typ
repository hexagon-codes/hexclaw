// PDF共享排版：宋体正文与黑体层级保持小学学习材料的阅读顺序，来源与冻结版本不由样式改写。
#let hc-ink = rgb("#23313f")
#let hc-muted = rgb("#65727e")
#let hc-line = rgb("#cfd7de")
#let hc-blue = rgb("#2f6484")
#let hc-red = rgb("#a13d38")
#let hc-body-font = ("Songti SC", "Noto Serif CJK SC", "Noto Serif CJK SC Regular")
#let hc-heading-font = ("PingFang SC", "Noto Sans CJK SC")
#set text(font: hc-body-font, size: 12pt, fill: hc-ink)
#set par(justify: false, leading: 0.80em, spacing: 0.65em)
// 列表项使用独立留白，避免紧凑列表的项间距小于正文换行。
#set list(tight: false, indent: 4mm, body-indent: 2mm, spacing: 0.80em)
#set enum(tight: false, indent: 4mm, body-indent: 2mm, spacing: 0.80em)
#show math.equation: it => text(size: 12pt, math.display(it))
#show math.equation.where(block: true): set align(left)
#show heading: set text(font: hc-heading-font)
#show heading.where(level: 1): it => block(above: 0pt, below: 5mm, sticky: true)[
  #grid(columns: (7mm, 1fr), column-gutter: 3mm, align: (left, horizon),
    image(__HC_LOGO__, width: 7mm),
    text(font: hc-heading-font, size: 18pt, weight: 650, it.body))
]
#show heading.where(level: 2): it => block(above: 0mm, below: 0mm, inset: (top: 2mm, bottom: 4mm), sticky: true)[
  #text(font: hc-heading-font, size: 14pt, weight: 650, fill: hc-blue, it.body)
]
#show heading.where(level: 3): it => block(above: 0mm, below: 0mm, inset: (top: 2mm, bottom: 4mm), sticky: true)[
  #text(font: hc-heading-font, size: 12.5pt, weight: 600, it.body)
]
#show heading.where(level: 4): it => block(above: 2mm, below: 1.5mm, sticky: true)[
  #text(font: hc-heading-font, size: 12pt, weight: 600, it.body)
]
#set table(stroke: 0.4pt + hc-line, inset: (x: 2.5mm, y: 2mm), align: left)
#let hc-footer() = context {
  let current = counter(page).get().first()
  let total = counter(page).final().first()
  set text(font: ("PingFang SC", "Noto Sans CJK SC", "Arial Unicode MS"), size: 8.5pt, fill: hc-muted)
  line(length: 100%, stroke: 0.4pt + hc-line)
  v(2mm)
  grid(columns: (1fr, auto), column-gutter: 3mm, align: (left, bottom),
    if current == total {
      stack(dir: ttb, spacing: 1mm,
        [#text(weight: 600)[HexClaw 河蟹 AI]#h(3mm)本地云端均可部署 · 数据自主掌控 · 开源免费],
        [#link("https://hexclaw.net/")[官网：hexclaw.net]#h(3mm)｜#h(3mm)#link("https://github.com/hexagon-codes")[GitHub：github.com/hexagon-codes]],
        [微博：\@HexClaw#h(3mm)｜#h(3mm)#link("https://x.com/hexclawapp")[X：\@hexclawapp]#h(3mm)｜#h(3mm)#link("mailto:ai@hexclaw.net")[邮箱：ai\@hexclaw.net]])
    } else { [HexClaw 河蟹 AI] },
    [#current / #total])
}
#set page(paper: "a4", margin: 20mm, footer: hc-footer(), footer-descent: 2mm)
#let hc-meta(body) = block(below: 4mm)[#text(font: hc-heading-font, size: 9pt, fill: hc-muted, body)]
#let hc-item(body) = layout(size => {
  let height = measure(body, width: size.width).height
  block(breakable: height > 240mm, above: 0mm, below: 0mm, inset: (top: 3mm, bottom: 3mm), body)
})
#let hc-compare(student, answer) = block(above: 0mm, below: 0mm, inset: (top: 3mm, bottom: 3mm))[
  #grid(columns: (1fr, 1fr), column-gutter: 8mm,
    [#text(font: hc-heading-font, size: 9.5pt, fill: hc-muted)[原始作答]#v(4mm)#student],
    [#text(font: hc-heading-font, size: 9.5pt, fill: hc-muted)[正确答案]#v(4mm)#answer])
]

