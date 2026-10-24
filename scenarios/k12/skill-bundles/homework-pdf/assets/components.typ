// 报告组件只处理展示内容，不据答案推导评价或改写学生作答。
#let ink = rgb("#1D2D40")
#let blue = rgb("#2D6088")
#let muted = rgb("#596A7C")
#let rule = rgb("#CCD9E4")
#let pale = rgb("#F3F7FA")
#let sans = sys.inputs.at("font-sans", default: "PingFang SC")
#let serif = sys.inputs.at("font-serif", default: "Songti SC")
#let math-font = sys.inputs.at("font-math", default: "New Computer Modern Math")

#let label(body, size: 10pt, fill: ink) = text(font: sans, weight: "semibold", size: size, fill: fill, body)

#let inlines(items, formulas) = {
  for item in items {
    if item.kind == "text" { text(item.text) }
    else if item.kind == "math" {
      assert(item.math_ref in formulas, message: "Missing formula reference: " + item.math_ref)
      let formula = formulas.at(item.math_ref)
      // 公式内容由宿主的 Pandoc 转换生成；所有分数保持完整的展示尺寸。
      if formula.func() == math.equation {
        box(inset: (top: 2pt, bottom: 2pt), math.equation(block: false, math.display(formula.body)))
      } else { formula }
    } else { panic("Unsupported inline kind: " + item.kind) }
  }
}

#let report-heading(title, details) = layout(size => {
  let heading = label(title, size: 22pt)
  let metadata = text(font: sans, size: 9.5pt, fill: muted, details)
  if measure(heading).width + measure(metadata).width + 12pt <= size.width {
    block(sticky: true)[#heading#h(1fr)#metadata]
  } else {
    block(sticky: true)[#heading #v(6pt) #metadata]
  }
})

#let badge(status) = {
  let tones = (
    amber: (rgb("#86570A"), rgb("#FFF1D6")),
    green: (rgb("#246343"), rgb("#EAF4ED")),
    red: (rgb("#9B3333"), rgb("#FBEDED")),
    neutral: (muted, rgb("#EFF2F5")),
  )
  let colors = tones.at(status.tone, default: tones.neutral)
  box(fill: colors.at(1), radius: 2pt, inset: (x: 5pt, y: 2pt),
    text(font: sans, size: 9pt, fill: colors.at(0), status.label))
}

#let section(body) = block(above: 18pt, below: 13pt, sticky: true)[#label(body, size: 17pt)]

// 整图先按正文全宽展示；超过完整页的可用高度才等比收小。
#let full-image(path, alt, max-height: 225mm) = layout(size => {
  let candidate = image(path, width: 100%, fit: "contain", alt: alt)
  let measured = measure(candidate, width: size.width)
  if measured.height > max-height {
    block(width: 100%, align(center, image(path, height: max-height, fit: "contain", alt: alt)))
  } else { candidate }
})

#let quick-question(q, formulas, first: false, source-kind: "grading") = block(
  above: if first { 0pt } else { 10pt }, below: 4pt, breakable: true, width: 100%,
)[
  #block(sticky: true)[
    #grid(columns: (1fr, auto), align: horizon, column-gutter: 8pt,
      [#if q.number != "" { label(q.number, size: 12pt, fill: blue); h(6pt) }#label(q.title, size: 11.5pt)],
      [#text(font: sans, size: 8.5pt, fill: muted, if source-kind == "text_solve" { "解题状态：" } else { "批改状态：" })#badge(q.status)],
    )
    #v(5pt)
  ]
  #block(width: 100%, breakable: true, fill: pale, radius: 2pt, inset: (x: 10pt, y: 7pt))[
    #grid(columns: (46pt, 1fr), column-gutter: 5pt, align: horizon,
      text(font: sans, size: 8.5pt, fill: muted)[答案解析],
      text(font: sans, size: 14pt, weight: "semibold", inlines(q.answer, formulas)),
    )
    #if q.calculations.len() > 0 {
      v(8pt)
      let cells = q.calculations.map(calculation => text(size: 11.5pt, inlines(calculation, formulas)))
      layout(size => {
        let column-width = (size.width - 12pt) / 2
        let fits = cells.all(cell => measure(cell).width <= column-width)
        if q.at("calculation_columns", default: 1) == 2 and fits {
          grid(columns: (1fr, 1fr), column-gutter: 12pt, row-gutter: 5pt, ..cells)
        } else {
          // 公式按实际排版宽度转单栏，保留字号和完整数学内容。
          for cell in cells { block(above: 0pt, below: 4pt, cell) }
        }
      })
    }
  ]
  #v(5pt)
  #set par(leading: 0.5em, spacing: 0pt)
  #text(size: 9pt, fill: muted)[
    #text(font: sans)[原始作答：]
    #for (index, part) in q.original_answer.enumerate() {
      if index > 0 { [；] }
      if part.at("label", default: "") != "" { [#part.label：] }
      inlines(part.content, formulas)
    }
  ]
]

#let block-content(node, formulas) = {
  if node.kind == "paragraph" {
    block(above: 0pt, below: 6pt, inlines(node.content, formulas))
  } else if node.kind == "supplement" {
    block(above: 0pt, below: 6pt)[
      #text(size: 9.5pt, fill: muted, inlines(node.content, formulas))
    ]
  } else if node.kind == "method_note" {
    block(above: 0pt, below: 6pt)[
      #text(font: sans, size: 9.5pt, fill: muted)[教学方法：#node.name——#node.application]
    ]
  } else if node.kind == "equation" {
    block(above: 6pt, below: 7pt, breakable: false)[
      #set text(size: node.at("size_pt", default: 13) * 1pt)
      #set align(left)
      #for item in node.content {
        if item.kind == "math" {
          assert(item.math_ref in formulas, message: "Missing formula reference: " + item.math_ref)
          let formula = formulas.at(item.math_ref)
          if formula.func() == math.equation {
            // 独立关键式使用真正的数学块，让分子分母高度参与上下段落排版。
            math.equation(block: true, math.display(formula.body))
          } else { formula }
        } else if item.kind == "text" { text(item.text) }
        else { panic("Unsupported inline kind: " + item.kind) }
      }
    ]
  } else if node.kind == "diagram" {
    block(above: 4pt, below: 6pt, breakable: false)[
      #full-image(node.path, node.alt, max-height: 50mm)
      #if node.at("caption", default: ()).len() > 0 {
        v(4pt)
        align(center, text(font: sans, size: 9.5pt, inlines(node.caption, formulas)))
      }
    ]
  } else if node.kind == "note" {
    block(width: 100%, breakable: true, fill: rgb("#FFF6E2"), inset: (x: 10pt, y: 7pt),
      stroke: (left: 1.5pt + rgb("#C68B27")), above: 7pt, below: 5pt)[
      #set text(size: 10pt)
      #if node.at("label", default: "") != "" { label(node.label, size: 10pt, fill: rgb("#86570A")); [ ] }
      #inlines(node.content, formulas)
    ]
  } else if node.kind == "table" {
    let count = node.headers.len()
    assert(count > 0, message: "Table requires headers")
    let widths = node.at("widths", default: if count == 2 { (31, 69) } else { range(count).map(_ => 1) })
    assert(widths.len() == count, message: "Table widths differ from header")
    let head = node.headers.map(cell => label(inlines(cell, formulas), size: 10pt))
    let cells = node.rows.map(row => {
      assert(row.len() == count, message: "Table row width differs from header")
      row.map(cell => inlines(cell, formulas))
    }).flatten()
    block(above: 8pt, below: 8pt)[
      #table(columns: widths.map(value => value * 1fr), align: left,
        inset: (x: 9pt, y: 10pt), stroke: 0.4pt + rule,
        fill: (x, y) => if y == 0 { pale } else { none },
        table.header(..head), ..cells)
    ]
  } else if node.kind == "columns" {
    let widths = node.at("widths", default: node.columns.map(_ => 1))
    assert(widths.len() == node.columns.len(), message: "Column widths differ from content")
    layout(size => {
      let contents = node.columns.map(column => { for child in column { block-content(child, formulas) } })
      let width-sum = widths.fold(0, (total, value) => total + value)
      let available-width = size.width - 17pt * (contents.len() - 1)
      let body-height = 297mm - 22mm - 23mm
      let fits = contents.enumerate().all(pair => {
        let column-width = available-width * widths.at(pair.at(0)) / width-sum
        measure(pair.at(1), width: column-width).height <= body-height
      })
      if fits {
        grid(columns: widths.map(value => value * 1fr), column-gutter: 17pt, align: horizon, ..contents)
      } else {
        // 过高的图文列改为顺序正文，各段沿原有分页规则继续排版。
        for content in contents { block(width: 100%, breakable: true, content) }
      }
    })
  } else { panic("Unsupported block kind: " + node.kind) }
}

#let blocks(nodes, formulas) = { for node in nodes { block-content(node, formulas) } }

#let deep-step(step, formulas) = [
  #block(above: 18pt, below: 9pt, sticky: true)[
    #grid(columns: (17pt, 1fr), column-gutter: 7pt, align: horizon,
      circle(radius: 8pt, fill: blue, stroke: none)[#text(font: sans, fill: white, size: 9pt, weight: "semibold")[#step.number]],
      label(step.title, size: 12pt),
    )
  ]
  #pad(left: 24pt, blocks(step.blocks, formulas))
]

#let render-report(data, formulas: (:)) = {
  assert(data.schema_version == "homework-report-render-v2", message: "Unsupported report schema")
  let doc = data.document
  let brand = data.branding
  set document(title: doc.title, author: "HexClaw", date: none)
  set text(font: serif, size: 11pt, lang: "zh", fill: ink)
  set par(justify: false, leading: 0.8em, spacing: 0.9em)
  set page(paper: "a4", margin: (left: 18mm, right: 18mm, top: 22mm, bottom: 23mm),
    header: [
      #set text(font: sans, size: 9pt, fill: muted)
      #grid(columns: if brand.at("logo_path", default: "") == "" { (1fr, auto) } else { (auto, 1fr, auto) },
        align: horizon, column-gutter: 6pt,
        ..if brand.at("logo_path", default: "") == "" { () } else { (image(brand.logo_path, width: 5mm),) },
        label(brand.name, size: 9pt), [#doc.agent_name])
      #v(4pt)
      #line(length: 100%, stroke: 0.5pt + rule)
    ],
    footer: [
      #set text(font: sans, size: 8.5pt, fill: muted)
      #line(length: 100%, stroke: 0.4pt + rule)
      #v(5pt)
      #set par(leading: 0.4em, spacing: 0pt)
      #grid(columns: (1fr, auto), column-gutter: 12pt, row-gutter: 5pt,
        [#brand.name · #brand.tagline], [],
        [官网：#link(brand.website_url, brand.website_label) · 公众号 ID：#brand.wechat_id · GitHub：#link(brand.github_url, brand.github_label)],
        context counter(page).display("1 / 1", both: true))
    ])
  show math.equation: set text(font: (math-font, serif))
  show math.equation.where(block: true): set block(above: 4pt, below: 4pt)
  let source-kind = doc.source_kind
  let date-label = if source-kind == "text_solve" { "生成日期：" } else { "批改日期：" }
  let metadata = (doc.grade_term, doc.subject, if doc.date == "" { "" } else { date-label + doc.date }).filter(value => value != "").join(" · ")
  let heading-content = [
    #report-heading(doc.title, metadata)
    #if doc.at("example_label", default: "") != "" {
      v(5pt)
      text(font: sans, size: 9pt, fill: muted, doc.example_label)
    }
    #v(7pt)
  ]
  if data.source_images.len() > 0 {
    let first = data.source_images.first()
    // 首图与标题合为一个可放入整页的块，避免高图把首页标题独自留下。
    layout(size => {
      let available = 252mm - measure(heading-content, width: size.width).height
      block(breakable: false)[
        #heading-content
        #full-image(first.path, first.alt, max-height: calc.min(225mm, available))
      ]
    })
    for source in data.source_images.slice(1) { full-image(source.path, source.alt) }
  } else {
    heading-content
    // 纯文字原题沿现有正文与公式块排版，完整保留后再进入答案区。
    for node in data.source_text { block-content(node, formulas) }
  }
  section("一、快速解答")
  for (index, q) in data.questions.enumerate() { quick-question(q, formulas, first: index == 0, source-kind: source-kind) }
  if data.deep.steps.len() > 0 {
    // 两层自然续排；不把少量内容强推到新页或强制固定页数。
    section("二、深度讲解")
    let method = data.deep.at("method", default: none)
    if method != none and method.name != "" {
      text(font: sans, size: 9.5pt, fill: muted)[教学方法：#method.name——#method.application]
    }
    for step in data.deep.steps { deep-step(step, formulas) }
  }
}
