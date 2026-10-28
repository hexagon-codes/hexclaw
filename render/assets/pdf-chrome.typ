// 品牌页眉页脚只消费产品资料、真实助手名称与字体，不改变场景正文。
#let hc-chrome-ink = rgb("#1D2D40")
#let hc-chrome-muted = rgb("#596A7C")
#let hc-chrome-rule = rgb("#CCD9E4")

#let hc-brand-header(brand, agent-name, font, logo: none) = context {
  set text(font: font, size: 9pt, weight: 400, fill: hc-chrome-muted)
  let agent = text(agent-name)
  grid(columns: if logo == none { (1fr, auto) } else { (auto, 1fr, auto) },
    column-gutter: 6pt, align: horizon,
    ..if logo == none { () } else { (image(logo, width: 5mm),) },
    text(font: font, size: 9pt, weight: "semibold", fill: hc-chrome-ink, brand.name),
    box(width: calc.min(measure(agent).width, 65mm), align(right, agent)))
  v(4pt)
  line(length: 100%, stroke: 0.5pt + hc-chrome-rule)
}

#let hc-brand-footer(brand, font) = context {
  set text(font: font, size: 8.5pt, weight: 400, fill: hc-chrome-muted)
  line(length: 100%, stroke: 0.4pt + hc-chrome-rule)
  v(5pt)
  set par(leading: 0.4em, spacing: 0pt)
  grid(columns: (1fr, auto), column-gutter: 12pt, row-gutter: 5pt,
    [#brand.name · #brand.tagline], [],
    [官网：#link(brand.website_url, brand.website_label) · 公众号 ID：#brand.wechat_id · GitHub：#link(brand.github_url, brand.github_label)],
    counter(page).display("1 / 1", both: true))
}
