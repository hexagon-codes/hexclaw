# 作业报告渲染合同 v2

本合同是宿主报告投影到模板的展示协议，不代替领域报告或学生评价合同。模板只读取确定的内容，字段空值不触发补写。

## 宿主调用

在同一个渲染目录准备 `template.typ`、`components.typ`、`report.json`、`formulas.typ` 及已解析的本地图片。模板入口读取 JSON 和公式字典后调用 `render-report`。

`formulas.typ` 导出 `formulas` 字典，key 为宿主生成的稳定公式 ID，value 为既有 Pandoc 一次批量转换得到的 Typst content。JSON 的 `math_ref` 只能引用该字典，不允许携带待执行的 Typst 源码。公式缺失必须报错，不能静默删除。

宿主负责根据固定 BundleRef 解析全部资源，核对报告来源/版本/状态，构造实际字体清单，编译与检查真实 PDF，并冻结和投递。模板不安装资源、不读取网络、不触发模型或消息。

## 输入结构

```text
schema_version: homework-report-render-v2
document: title, agent_name, grade_term, subject, date, source_kind, example_label?
branding: logo_path?, name, tagline, website_url, website_label,
          wechat_id, github_url, github_label
source_images: [{path, alt}]
source_text: Block[]
questions: [{id, number, title, status: {label, tone}, answer: Inline[],
             calculations: Inline[][], calculation_columns?: 1|2,
             original_answer: [{label?, content: Inline[]}]}]
deep: {method?: {name, application}, steps: [{number, title, blocks: Block[]}]}
```

`questions[].number` 是最终题号展示标签，逐字保留宿主提供的 source label（例如 `（1）`、`3.（2）`）；模板不额外加括号、不从数组下标猜编号。空字符串不画序号。`status.label` 是宿主传入的真实展示状态；`tone` 为 `amber/green/red/neutral`。未填写不等于错误，已解答不等于学生答对。

`Inline` 只有两种：`{kind:"text",text:"…"}` 或 `{kind:"math",math_ref:"…"}`。文本均按文字处理，不解析其内的 Markdown、Typst 或 HTML。

`Block` 支持：

| kind | 字段 | 用途 |
|---|---|---|
| paragraph | content: Inline[] | 自然分页的说明 |
| supplement | content: Inline[] | 家长补充等次要说明，9.5 pt 灰色正文 |
| method_note | name, application | 本组实际采用的方法及用途，9.5 pt 灰色注释 |
| equation | content: Inline[]；size_pt? | 完整的关键算式，默认 13 pt |
| diagram | path, alt；caption?: Inline[] | 已验证的本地教学 SVG/图片 |
| note | content: Inline[]；label? | 易错点等简短提醒 |
| table | headers: Inline[][]；rows: Inline[][][]；widths?: number[] | 多种答案与适用前提，表头可重复 |
| columns | columns: Block[][]；widths?: number[] | 图文并列；任一列实测高度超整页正文时转顺序纵排 |

表格每行列数必须等于表头列数，widths 长度必须等于列数。显式 widths 优先；两列表格默认 31/69，其他列数默认均分。并列块同样核对 widths 和 columns。两栏算式仅在宿主明确 `calculation_columns=2` 且每个算式的实际排版宽度能放入一栏时启用；任一算式放不下即转单栏，不缩小字号，也不以字符数猜测宽度。

图文并列按当前可用宽度、17 pt 栏间距及各列权重实际测量。任一列高度超过 A4 正文的 252 mm（297−22−23 mm）时，将各列按原顺序转成可分页正文；不缩字、不裁切、不按字符数猜测。短图文保留并列，页内剩余高度不足时沿正常分页移页。

各讲解组的方法分别放在对应 step 的 `method_note` 块，不汇总成与该组无关的全局方法。单一 `deep.method` 保持兼容；宿主不同时重复传入同一条全局和分组方法注释。

## 来源与兼容

- `document.source_kind` 是 `grading/text_solve`，由宿主真实来源确定，不由教学模型选择。图片保持定版整图及批改标签；纯文字在标题后用普通正文保留完整原题，采用解题状态与生成日期。
- 未提供作答时由宿主明确投影“未提供”，不能变成未作答、判错或空字段。纯求解不生成批改事实。
- 内容包1.1.3使用profile API 2与v2输入；宿主同时支持v1/v2，已接纳任务仍读自己的冻结资源。v0.0.11中原1.0.0包、1.1.0快照及旧PDF不重写。

## 版面与资产

- A4，正文边距左右 18 mm、上 22 mm、下 23 mm。标题 22 pt 与辅助信息 9.5 pt 同基线，放不下则辅助信息另起一行，不缩字。
- 快速层：直接答案 14 pt；必要算式 11.5 pt；原始作答 9 pt 灰字置后。三项标签常驻，不以空值隐藏。
- 深度层独立 `equation` 使用真正的块级数学公式并左对齐，完整分子分母高度参与段落布局，不以行内公式的基线高度代替。
- 非空题号与标题之间保留 6 pt 间隔，仍在同一行自然续排；空题号不占间距。
- 完整作业图按正文全宽等比显示；超过整页可用高度时等比收小，不能裁切。方向校正由宿主依据同源方向凭据处理。
- 短题实测能放入完整页正文时，题头、答案、必要算式和原始作答整体移页；超整页的长题允许分页，题号/标题贴住后续内容。不固定题数、内容或页数。深度层自然续排，不强制另起一页。
- 纯数学片段的原始作答中，换行只作为分号展示；含叙述文字时保留换行。该显示变换不产生空位身份、位置证据或新的学生作答。
- 图片 `path` 由宿主解析到本次编译工作目录内的文件；`alt` 来自已知图意。模板不接受远程 URL 作为下载指令。
- `profile.json` 声明首选字体与回退候选；宿主解析实际字体后使用 Typst `--input font-sans=... --input font-serif=... --input font-math=...` 传入名称并记录清单。未传值时仅使用样板本机字体，不把不存在的候选静默当作已安装。本包不含 macOS/Windows 字体文件，不宣称跨平台像素一致。
- `branding` 由产品事实配置提供，模板不从题目推断品牌、官网、公众号或智能体名称。

## 内容包身份

`assets/pdf-chrome.typ` 是 HexClaw `render/assets/pdf-chrome.typ` 共享页眉页脚的版本化分发副本。更新资源包时同步该组件，并把实际字节纳入清单；恢复旧报告只读取原包冻结的组件，不读取宿主最新版。该组件只定义页眉页脚，不设置题目、章节、表格或正文样式。

`bundle.json` 保存按 UTF-8 路径字节序排序的文件清单。清单仅含包内实际资源，不含自身；每项为 `path/size/sha256/role`。语义摘要的计算对象为根对象 `{bundle_contract_version,skill_id,version,files}` 的 UTF-8 JSON：所有对象 key 按字典序、无额外空白、保留非 ASCII 字符。宿主外层索引存这个摘要，避免包内自引用；压缩包摘要另记为 transport digest。

本目录仅提供内容包。共享安装、索引、注册、热加载和离线种子需要宿主按已批准生命周期接入；存在目录或可编译示例不等于安装完成。

## 示例边界

`examples/` 全部为 AI 生成的去身份排版数据，不是真实学生作业，不作为 OCR 或批改质量证据。示例公式字典仅用于本地模板检查，生产公式必须使用宿主实际转换结果。
