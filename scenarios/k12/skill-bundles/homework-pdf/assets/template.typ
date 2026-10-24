// 输入与公式由宿主解析后放入同一渲染目录，模板不从网络读取资源。
#import "components.typ": render-report
#import "formulas.typ": formulas
#let data = json("report.json")
#render-report(data, formulas: formulas)
