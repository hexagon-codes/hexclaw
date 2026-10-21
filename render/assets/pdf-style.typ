// PDF共享排版：宋体正文与黑体层级保持小学学习材料的阅读顺序，来源与冻结版本不由样式改写。
#let hc-ink = rgb("#23313f")
#let hc-muted = rgb("#65727e")
#let hc-line = rgb("#cfd7de")
#let hc-blue = rgb("#2f6484")
#let hc-logo-blue = rgb("#75b4e9")
#let hc-red = rgb("#a13d38")
#let hc-header-footer = __HC_HEADER_FOOTER__
#let hc-body-font = ("Songti SC", "Noto Serif CJK SC", "Noto Serif CJK SC Regular")
#let hc-heading-font = ("PingFang SC", "Noto Sans CJK SC")
#set text(font: hc-body-font, size: 12pt, fill: hc-ink)
#set par(justify: false, leading: 1.02em, spacing: 0.95em)
#show "🔧": box(image(__HC_TOOL_ICON__, width: 0.9em))
// 列表项使用独立留白，避免紧凑列表的项间距小于正文换行。
#set list(tight: false, indent: 4mm, body-indent: 2mm, spacing: 0.85em)
#set enum(tight: false, indent: 4mm, body-indent: 2mm, spacing: 0.85em)
#show math.equation.where(block: false): it => box(inset: (top: 0.08em, bottom: 0.08em), it)
#show math.equation.where(block: true): set text(size: 12pt)
#show math.equation.where(block: true): set align(left)
#show math.equation.where(block: true): set block(above: 0.65em, below: 0.65em)
#show heading: set text(font: hc-heading-font)
#show heading.where(level: 1): it => block(above: 0pt, below: 5mm, sticky: true)[
  #text(font: hc-heading-font, size: 13pt, weight: 650, fill: hc-blue, it.body)
]
#show heading.where(level: 2): it => block(above: 6mm, below: 3.5mm, sticky: true)[
  #text(font: hc-heading-font, size: 13pt, weight: 650, fill: hc-blue, it.body)
]
#show heading.where(level: 3): it => block(above: 6mm, below: 3.5mm, sticky: true)[
  #text(font: hc-heading-font, size: 13pt, weight: 600, fill: hc-blue, it.body)
]
#show heading.where(level: 4): it => block(above: 2mm, below: 1.5mm, sticky: true)[
  #text(font: hc-heading-font, size: 13pt, weight: 600, fill: hc-blue, it.body)
]
// 标准PDF将独立题号与下一正文段合并，题号保留标题样式，正文自然换行。
#let hc-numbered-line(level, number, body) = block(
  above: if level == 1 { 0pt } else if level >= 4 { 2mm } else { 6mm },
  below: if level == 1 { 5mm } else if level >= 4 { 1.5mm } else { 3.5mm },
)[
  #text(font: hc-heading-font, size: 13pt, weight: if level <= 2 { 650 } else { 600 }, fill: hc-blue, number)#h(2mm)#body
]
#set table(stroke: 0.4pt + hc-line, inset: (x: 2.5mm, y: 2mm), align: left)
#show table.cell.where(y: 0): set text(font: hc-heading-font, weight: 600)
#let hc-header() = stack(dir: ttb, spacing: 2.5mm,
  grid(columns: (7mm, 1fr), column-gutter: 3mm, align: (left, horizon),
    image(__HC_LOGO__, width: 7mm),
    text(font: hc-heading-font, size: 10pt, weight: 600, fill: hc-muted)[HexClaw 河蟹 AI]),
  line(length: 100%, stroke: 0.3pt + hc-line))
#let hc-footer() = context {
  let current = counter(page).get().first()
  let total = counter(page).final().first()
  set text(font: ("PingFang SC", "Noto Sans CJK SC"), size: 8.5pt, weight: 400, fill: hc-muted)
  line(length: 100%, stroke: 0.4pt + hc-line)
  v(2mm)
  grid(columns: (1fr, auto), column-gutter: 3mm, align: (left, bottom),
    grid(columns: (1fr,), row-gutter: 1.5mm,
      [#text(size: 9pt, weight: 600, fill: hc-logo-blue)[本地云端均可部署 · 数据自主掌控 · 开源免费]],
      [#link("https://hexclaw.net/")[官网：hexclaw.net]#h(4mm)#link("https://github.com/hexagon-codes")[GitHub：github.com/hexagon-codes]],
      [微博：\@HexClaw#h(4mm)#link("https://x.com/hexclawapp")[X：\@hexclawapp]#h(4mm)#link("mailto:ai@hexclaw.net")[邮箱：ai\@hexclaw.net]]),
    [#current / #total])
}
#set page(paper: "a4", margin: (x: 20mm, top: 24mm, bottom: 22mm), header: if hc-header-footer { hc-header() } else { none }, header-ascent: 3.5mm, footer: if hc-header-footer { hc-footer() } else { none }, footer-descent: 4mm)
// 正文图片完整等比适配；高度上限预留标题与段距，不裁边、不拉伸、不改变原图坐标。
#let hc-image-max-height = __HC_IMAGE_MAX_HEIGHT__mm
#show image: it => layout(size => {
  let natural = measure(it, width: size.width)
  let factor = calc.min(1, size.width / natural.width, hc-image-max-height / natural.height)
  if factor >= 1 { it } else {
    image(it.source, format: it.format, width: natural.width * factor,
      height: natural.height * factor, fit: "contain", alt: it.alt,
      scaling: it.scaling, icc: it.icc)
  }
})
#let hc-meta(body) = block(below: 4mm)[#text(font: hc-heading-font, size: 9pt, fill: hc-muted, body)]
#let hc-item(body) = layout(size => {
  // 订正与解答每题固定按批改状态、原始作答、正确答案三项显示，缺值保留空项。
  // 家长讲题的题号与必要步骤同一行；各题沿同一标题与列表规则。
  // 长答案按正文自然跨页；题号与题干由首行投影保持同行，不把整题强推至下一页。
  block(breakable: true, above: 0mm, below: 0mm, inset: (top: 3mm, bottom: 3mm), body)
})
#let hc-compare(student, answer) = block(above: 0mm, below: 0mm, inset: (top: 3mm, bottom: 3mm))[
  #grid(columns: (1fr, 1fr), column-gutter: 8mm,
    [#text(font: hc-heading-font, size: 9.5pt, fill: hc-muted)[原始作答]#v(4mm)#student],
    [#text(font: hc-heading-font, size: 9.5pt, fill: hc-muted)[正确答案]#v(4mm)#answer])
]

// 同步巩固专用练习册投影；正文、品牌页眉页脚和其他PDF版式保持原规则。
#let hc-workbook-lines(count, gap: 8.5mm) = block(width: 100%, above: 1.5mm, below: 0mm)[
  #grid(columns: (1fr,), rows: (gap,) * count, align: bottom,
    ..range(count).map(_ => line(length: 100%, stroke: 0.35pt + hc-line.darken(18%))))
]
#let hc-workbook-header(title) = {
  block(above: 0pt, below: 5mm, breakable: false)[
    #text(font: hc-heading-font, size: 13pt, weight: 650, fill: hc-blue, title)#h(1fr)#box[姓名：#box(width: 30mm, height: 4mm, stroke: (bottom: 0.4pt + hc-muted))]#h(7mm)#box[日期：#box(width: 35mm, height: 4mm, stroke: (bottom: 0.4pt + hc-muted))]
  ]
  v(1mm)
}
#let hc-workbook-question(number, title, body, lines: 0) = layout(size => {
  let content = grid(columns: (7mm, 1fr), column-gutter: 1.5mm, align: left,
    text(font: hc-heading-font, weight: 600)[#number.],
    [#title#v(2.5mm)#body#if lines > 0 {
      if body != [] { v(if lines == 2 { 1mm } else { 2mm }) }
      [答：]
      hc-workbook-lines(lines)
    }])
  // 普通题整体换页；超一页的长题允许续排，保留完整题面。
  block(breakable: measure(content, width: size.width).height > 240mm,
    above: 4.5mm, below: 3mm, content)
})
#let hc-workbook-blank() = [（#h(10mm)）]
#let hc-workbook-fill(cells) = {
  grid(columns: (1fr, 1fr), row-gutter: 4mm, column-gutter: 12mm, ..cells)
  v(3mm)
}
#let hc-workbook-matching(left, right) = grid(
  columns: (22mm, 3mm, 70mm, 3mm, 22mm), rows: (15mm,) * left.len(),
  align: center + horizon,
  ..range(left.len()).map(index => (
    left.at(index), circle(radius: 0.55mm, fill: hc-muted, stroke: none), [],
    circle(radius: 0.55mm, fill: hc-muted, stroke: none), right.at(index),
  )).flatten())
#let hc-workbook-matching-group(label, left, right) = block(above: 2mm, below: 2.5mm)[
  #text(weight: 600, label)
  #v(2.5mm)
  #hc-workbook-matching(left, right)
]
#let hc-workbook-calculations(cells) = grid(columns: (1fr, 1fr), column-gutter: 8mm, ..cells)
