// PDF共享排版保持各业务的内容结构，统一文字层级与品牌，来源和冻结版本不由样式改写。
#let hc-ink = rgb("#1D2D40")
#let hc-muted = rgb("#596A7C")
#let hc-line = rgb("#CCD9E4")
#let hc-blue = rgb("#2D6088")
#let hc-logo-blue = rgb("#75b4e9")
#let hc-red = rgb("#a13d38")
#let hc-header-footer = __HC_HEADER_FOOTER__
#let hc-metadata = __HC_METADATA__
#let hc-agent-name = hc-metadata.at("agent_display_name", default: "")
#let hc-branding = __HC_BRANDING__
__HC_PDF_CHROME__
#let hc-document-title = hc-metadata.at("title", default: "")
#let hc-document-subtitle = hc-metadata.at("subtitle", default: "")
// 保留首选字体；缺失时明确回退到Windows中文字体，避免系统回退产生缺字。
#let hc-body-font = ("Songti SC", "Noto Serif CJK SC", "Noto Serif CJK SC Regular", "SimSun")
#let hc-heading-font = ("PingFang SC", "Noto Sans CJK SC", "Microsoft YaHei", "SimHei", "SimSun")
#set text(font: hc-body-font, size: 11pt, fill: hc-ink)
#set par(justify: false, leading: 0.8em, spacing: 0.9em)
#show "🔧": box(image(__HC_TOOL_ICON__, width: 0.9em))
// 列表项使用独立留白，避免紧凑列表的项间距小于正文换行。
#set list(tight: false, indent: 4mm, body-indent: 2mm, spacing: 0.85em)
#set enum(tight: false, indent: 4mm, body-indent: 2mm, spacing: 0.85em)
// 行内公式按实际字形边界占位，分数进入页首时完整保留分子与分母。
#show math.equation.where(block: false): it => box(inset: (top: 2pt, bottom: 2pt), text(top-edge: "bounds", bottom-edge: "bounds", math.display(it)))
#show math.equation.where(block: true): set text(size: 13pt)
#show math.equation.where(block: true): set align(left)
#show math.equation.where(block: true): set block(above: 6pt, below: 7pt)
#show heading: set text(font: hc-heading-font)
#let hc-title-used = state("hexclaw-pdf-document-title-used", false)
#let hc-heading-text(body) = {
  if type(body) == str { body }
  else if type(body) == content {
    let fields = body.fields()
    if "text" in fields { fields.text }
    else if "children" in fields { fields.children.map(hc-heading-text).join("") }
    else if "body" in fields { hc-heading-text(fields.body) }
    else { "" }
  } else { "" }
}
#let hc-document-heading(title, subtitle) = layout(size => {
  let title-content = text(font: hc-heading-font, size: 22pt, weight: 650, fill: hc-ink, title)
  let details = text(font: hc-heading-font, size: 9.5pt, weight: 400, fill: hc-muted, subtitle)
  if measure(title-content).width + measure(details).width + 12pt <= size.width {
    block(above: 0pt, below: 7pt, sticky: true)[#title-content#h(1fr)#details]
  } else {
    block(above: 0pt, below: 7pt, sticky: true)[#title-content#v(6pt)#details]
  }
})
#let hc-section-heading(body) = block(above: 18pt, below: 13pt, sticky: true)[
  #text(font: hc-heading-font, size: 17pt, weight: 650, fill: hc-ink, body)
]
#show heading.where(level: 1): it => context {
  let matches = hc-document-title == "" or hc-heading-text(it.body) == hc-document-title
  if not hc-title-used.get() and matches {
    hc-title-used.update(true)
    hc-document-heading(it.body, hc-document-subtitle)
  } else { hc-section-heading(it.body) }
}
#show heading.where(level: 2): it => hc-section-heading(it.body)
#show heading.where(level: 3): it => block(above: 14pt, below: 9pt, sticky: true)[
  #text(font: hc-heading-font, size: 12pt, weight: 600, fill: hc-ink, it.body)
]
#show heading.where(level: 4): it => block(above: 10pt, below: 7pt, sticky: true)[
  #text(font: hc-heading-font, size: 11.5pt, weight: 600, fill: hc-ink, it.body)
]
// 标准PDF将独立题号与下一正文段合并，题号保留标题样式，正文自然换行。
#let hc-numbered-line(level, number, body) = block(
  above: if level == 1 { 0pt } else if level >= 4 { 10pt } else { 14pt },
  below: if level >= 4 { 7pt } else { 9pt },
)[
  #text(font: hc-heading-font, size: 12pt, weight: 600, fill: hc-blue, number)#h(6pt)#body
]
#set table(stroke: 0.4pt + hc-line, inset: (x: 9pt, y: 10pt), align: left)
#show table.cell.where(y: 0): set text(font: hc-heading-font, weight: 600)
#let hc-header() = hc-brand-header(hc-branding, hc-agent-name, hc-heading-font, logo: __HC_LOGO__)
#let hc-footer() = hc-brand-footer(hc-branding, hc-heading-font)
#set page(paper: "a4", margin: (left: __HC_MARGIN_LEFT__mm, right: __HC_MARGIN_RIGHT__mm, top: __HC_MARGIN_TOP__mm, bottom: __HC_MARGIN_BOTTOM__mm), header: if hc-header-footer { hc-header() } else { none }, footer: if hc-header-footer { hc-footer() } else { none })
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

// 同步巩固沿用题型与作答留白，文字层级和品牌使用共享排版。
#let hc-workbook-lines(count, gap: 8.5mm) = block(width: 100%, above: 1.5mm, below: 0mm)[
  #grid(columns: (1fr,), rows: (gap,) * count, align: bottom,
    ..range(count).map(_ => line(length: 100%, stroke: 0.35pt + hc-line.darken(18%))))
]
#let hc-workbook-header(title) = {
  hc-document-heading(title, [#box[姓名：#box(width: 30mm, height: 4mm, stroke: (bottom: 0.4pt + hc-muted))]#h(7mm)#box[日期：#box(width: 35mm, height: 4mm, stroke: (bottom: 0.4pt + hc-muted))]])
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
