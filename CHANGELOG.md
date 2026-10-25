# Changelog

本文件记录 hexclaw 的用户可见变更，遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## v0.5.0（2026-10-25）

### Fixed
- 家长指南从已验证解答恢复完整明确答案，避免漏掉多空答案、单位或否定条件；保留原解法、学生作答及批改事实，不额外调用模型。
- K12专用讲法及作品点评遵从Skill停用/卸载设置，不以同名内嵌内容回退；重新启用/安装生效，旧冻结任务保持原结果。

## 2026-10-24 源码变更

### Fixed
- 会话摘要压缩保留原消息和附件，恢复原回复按会话/请求索引读取；历史分支按实际摘要覆盖关系恢复上下文。
- 家庭学习档案 v9 保留经核验同题来源组、冻结来源与原图，支持原事务恢复、跨智能体迁移及回退，失效关系不因恢复复活。
- 开发测试部署可按既有政策仅保留当前服务与最新状态快照；后置备份失败保留恢复材料并明确返回未完成。
- 同轮 PDF 与媒体附件由原回复批次统一投递和恢复，保留完整附件、发送前主动停止检查及未知回执不重发。
- 可信冻结附件读取实际字节并校验摘要；没有持久 PDF 投递意图的渠道保留原内联回复。
- 启动后直接卸载技能不再残留可读取的旧正文；MCP 在途启用遵从后续停用或卸载，停用资源保留在已添加列表中。
- 显式 Hub 来源可替换临时出厂目录，同源缓存和原离线回退保持可用。
- 文字解题复用题库答案前检查本次课程范围，保留当前未映射知识点，不改写已发布答案。
- 排队音频的新调用遵从当前启用设置；取消后的晚到结果保留实际尝试记录和耗时。

## 2026-10-23 源码变更

### Fixed
- 连接发送测试在没有接收目标时返回未通过；至少存在一个目标且所有目标均取得平台接收或送达回执后才判为成功，保留冻结回执重放与未知结果不重发。

## 2026-10-22 源码变更

### Changed
- 钉钉回复采用分隔线与工具、技能、模型三行尾注；隐藏未使用栏目，按实际回执合并工具状态，尾注不计入PDF正文阈值。
- 连接账号元信息展示可归属的累计实际模型tokens；日志通过`observe.log_retention_days`配置滚动保留天数，默认15天，修改后重启生效。
- 首轮普通问答按需选择知识检索；明确匹配的手动问答记忆优先保留完整正文，不降低整体相关性阈值。

### Fixed
- 标准PDF在现有首选字体缺失时使用明确的Windows中文字体回退，修复中文页脚缺字；保留原字号、颜色、页边距、例外设置及旧冻结文件。

## 2026-10-21 源码变更

本节记录 `main` 上的源码变更，不指定新发布版本。已发布的 `v0.5.0` 标签保持原内容；实际安装包、云端制品和远端 CI 状态应按各自构建记录核对。

### Changed
- 依赖更新为 ai-core **v0.2.12**、Hexagon **v0.5.15**、toolkit **v0.3.5**；默认 Hub 标签更新为 **v0.0.10**。已有 `skills.hub.branch` 配置继续覆盖默认值，已安装技能仍按现有安装和版本化种子规则处理。
- 会话快速技能、自动检索与回答缓存使用当前有效会话和 Agent 设置；本轮原始输入、图片及工具调用组在压缩中保持明确身份。
- 请求级产品事实与运行快照保留实际模型尝试、工具结果及名称来源；工具的应用审批、上游 HTTP 失败和结果未知分别记录。
- K12 来源核验保留印刷题干、运算符、数字边界及版本差异；学生笔迹不清时，独立参考答案与学生判分、学习证据分开。
- 已批准的教学图、诊断式练习和学习证据进入既有 K12 链路；冻结图示支持同源 PDF 与备份恢复，周练和档案更新使用对应冻结版本及命令回执。
- 完善现有 PDF 题号、分页和讲法布局；旧冻结文件和已发送附件保留原字节与身份。

### Fixed
- 修复带历史的短追问误走无历史技能快路径、回答缓存跨有效上下文命中、空摘要或归档失败删消息、人物不同属性互相取代等问题。
- 修复 MCP 图文内容丢失、旧工具媒体累积及压缩淘汰当前原图；模型故障后的同次续接保留已完成工具、已知用量和流式文本，精确重复前缀只输出一次。
- 加强数值单位、按序答案、配对和乘除关系的校验，保留题干来源与实际程序执行的证据边界。

## [0.5.0] - 2026-10-19

### Changed
- 库版本、CLI 默认版本及 OpenAPI 文档版本统一为 `0.5.0`；构建时的版本、提交与日期注入保持。Desktop 固定来源同步为后端 `refs/tags/v0.5.0`，发布顺序为后端 Tag 在先、Desktop 安装包在后。
- 同步当前 K12 单元资料、同版 PDF、教材来源、数学脚本与题库复用、处理过程及模型配置的说明，保留既有来源、归属、冻结结果及未知调用合同。
- 共享执行摘要和 PDF 预算归入 `messagecontent`，保留既有 adapter 公开 API、输出及预算。

### Fixed
- 统一钉钉 Markdown 强调、链接边界与摘要节选投影，保留规范正文、其他渠道及旧冻结消息。
- 来源修正复用当前逐题回执生成必需批改图，合法跳过项保留原题号；终稿验证不重新求解组外题。
- 修正历史迁移、模型能力与恢复测试夹具，保留当前完整迁移、来源验证及失败/未知分类；文档和项目记忆明确遵守现有 CI 合同。

### Documentation
- 更新中英文 README、公共 API、贡献、安全支持版本及云端运维说明；教程截图与旧版本记录保留其实际适用版本。
- 本记录对应当前源码变更，不代表已经创建 Tag、发布二进制/镜像或更新现有部署；CI、平台矩阵、部署分支与分发渠道保持。

## [0.5.0-beta.1] - 2026-10-04

### Fixed
- 升级 toolkit 至 **v0.3.5**，修复 Linux ARM64 沙箱链接计数字段类型不兼容导致的发布编译失败；保留原硬链接校验和五个平台发布目标。

## [0.5.0-beta] - 2026-10-03
> 家长辅导场景包正式落地，打通作业图片识题、批改、错题、复习和定时投递闭环；同时加固本地/云端模型路由、知识召回、IM 送达与沙箱执行。

### Added
- **场景包扩展框架**：新增 `scenario` 六缝注册表（记录集 / 约束 / 视图槽 / Agent mode / 按钮 / eval suite），平台层不再硬编码具体业务包。
- **通用记录本**：新增 `records.agent_records`，以 Agent 为隔离键，支持 schema 校验、去重键、到期复习队列、状态机和乐观锁。
- **K12 家长辅导场景包**：内置 `/api/k12/*`、`k12_grade`/`k12_review`、错题本、积累本、年级约束、备课卡、默认 cron 投递和 K12 专项评测 workflow；支持图片识题内联回显、学科/题型标注、空白题求解、错题纠正与变式复习。
- **专用推理模型与嵌入模型闭环**：解题/批改可独立选择 reasoning model；知识库可发现已安装的 Ollama 嵌入模型，并提供状态检查与自动安装闭环。
- **出站上下文**：新增 purpose × data-class 出网策略，按 chat、vision、embedding、rerank 等用途隔离本地/云端数据边界。

### Changed
- 可选 Ollama Compose 镜像更新至 **0.35.1**，保留独立模型数据卷和原连接配置；默认部署仍不安装 Ollama 或自动下载模型。
- 根模块依赖基线更新为 hexagon **v0.5.14**、ai-core **v0.2.11**、toolkit **v0.3.4**、`golang.org/x/net` **v0.55.0** 与 Go **1.25.13**；固定使用已发布版本，全仓编译型回归不依赖本地 workspace。保留 hexagon v0.5.13 的 `testing/record` 可取消 Token 计数修复；HexClaw 当前运行链不直接导入该测试工具包。Go 补丁版本与 `x/net` 升级修复此前安全扫描确认的可达漏洞。
- **执行原语收敛**：`code_exec` 成为推荐执行入口，支持 snippet/file/module/project 与 artifact metadata；`code`/`shell` 保留兼容但标记弃用，顶层 `runtime/` 包删除，沙箱能力收敛到 toolkit + `skill/sandbox`。
- **技能种子版本化**：首启内嵌技能支持版本感知升级，默认市场目录对齐 `hexagon-codes/hexclaw-hub` `v0.0.6`。
- **本地/云端路由韧性**：本地默认模型启动预热并对齐 `num_ctx`；推理、视觉、embedding 和 rerank 按用途选路，provider failover 时重建符合目标 locality 的请求。
- **工作流与平台生命周期**：条件节点、路由规则与配置更新改为原子持久化；MCP 客户端与 IM adapter 收敛启停、重连和投递语义。
- 收敛自动 CI/CD：功能分支编译预检维持现有自动部署，main / PR 由一个主 CI 执行 Linux 普通全量测试，以及必要的跨平台 sandbox / CodeExec；现有主 CI 手动入口执行 Linux 全量 race 并保留 Windows / macOS 平台检查。K12 确定性测试包含在全量中，自动 CI 不使用真实模型密钥，真实模型门仅在手动 K12 专项配置密钥时执行。K12 / Sandbox 专项与公网爬虫保持手动，上游 toolkit 自身测试不在普通提交重复执行；变更遵循[CI/CD 固定基线](CONTRIBUTING.md#cicd-固定基线)。
- 主 CI 仅对指定纯说明文档改动跳过构建与自动部署，不广义忽略可能参与 `go:embed` 的 Markdown；render 按自身路径规则独立运行。移除覆盖率上传、Windows 非阻塞重复测试及 render 每周上游版本查询；Deploy / render 独立成功不能代替主 CI。
- CI 与 K12 使用 `GOWORK=off`、`GOFLAGS=-mod=readonly`；Linux main push / PR 执行 `go test -count=1 -timeout 30m ./...`，主 CI 手动入口执行 `go test -race -count=1 -timeout 30m ./...`，两种全量入口的单包测试超时均为 30 分钟、job 总预算均为 50 分钟。
- 普通业务测试及迁移包内仅检查最新 schema 约束的用例复用进程内真实迁移的空库模板，各用例保持独立数据库、原连接语义和全部断言，全量 race 通过主 CI 手动入口执行；历史 schema 升级、全链重跑、重开及生产迁移流程不变，减少重复迁移导致的 CI 超时。
- Windows Go 构建缓存组仅两项真实初始化 / 预算用例按平台能力跳过；规则与清理断言仍跨平台执行，跳过不计为 Windows Go 运行已验证。
- 更新中英文安装、开发与云端部署文档，区分功能分支编译、main / PR 全量验证、按提交自动部署和独立 Tag Release；历史审计结果保留原证据范围。

### Fixed
- 修复 K12 通用指导产物校验误拒现有中文依据说明、导致批改任务停在投影阶段的问题；历史英文产物仍兼容。
- 会话标题长度按去除首尾空白后的实际内容检查，保留已有标题兼容行为。
- 修复钉钉模板切换覆盖既有消息投影、Windows Go 构建缓存误拒普通文件，以及 Linux 沙箱 CI 安装的 bubblewrap 与 toolkit 所需参数不匹配的问题。
- 修复 Linux `code_exec` 将不存在的默认凭据路径当作启动依赖的问题；现存凭据和调用方显式拒绝路径仍保留原保护。
- Linux 全量沙箱测试对齐 toolkit 已验证的 Ubuntu 22.04 Runner；既有探测执行真实 bubblewrap 空命令并保留错误输出，避免工具已安装但 namespace 不可用时仍显示探测成功。
- 修复 Windows Go 安装目录 junction 的物理路径解析及缓存文件刷盘句柄，移除 Linux Go helper 对自身可写工作区的重复只读挂载和运行阶段被父规则覆盖的子挂载；保留工具链身份和只读访问边界。Windows 真实执行集成仍按 toolkit 当前能力明确门控，不把跳过计为 Go 执行通过。
- 修复微信公众号被动回复与上下文取消的竞争，防止及时生成的文本回复偶发变为空响应；保留无回复处理和真实超时后的客服消息投递。
- 修复工作流初始响应快照与后台模型路由写入之间的竞态，响应保持独立快照，不改变执行和终态查询。
- 修复作品归档对历史 OCR 摘要格式的误拒、迁移后点评仍引用原 OCR 任务，以及历史作品点评调用与当前生成记录的关联；保留正文摘要核验、公开作品版本和已发送调用的回滚保护。
- 对齐既有测试夹具与当前鉴权、画像来源版本、模型能力、备份协议及平台关闭契约；旧照片恢复夹具补齐原图和运行检查点，V80 历史升级夹具固定到 V79 前置 schema，保留资产集合、归属、篡改拒绝与升级后数据保留断言。
- 对齐作品反馈违规样本和自动图片批改回归：允许的家长参考稿不再被旧样本误判；不确定题保留无判分结果，终态复用完整逐题讲法与回执，不要求额外页摘要模型调用。
- WebSocket 测试连接通过应用层 ping/pong 确认服务端已完成登记，消除握手返回后立即检查连接身份的偶发失败。
- 修复 release 构建不可复现的依赖锁定问题：`skill/builtin/code_exec.go` 依赖的 sandbox 限额字段已由 `toolkit v0.2.6` 提供，不再需要本地 workspace 才能编译。
- 将 runner 完整性故意失败探针改为 `HEXCLAW_RUNNER_PROBE=1` 手工门控，默认 `go test ./...` 不再被取证用例打红。
- **视觉多轮请求**：按路由策略限制历史图片预算，遇到上游图片数超限时保留当轮图并丢弃最旧图重试，避免多轮作业批改因图片累积失败或超时。
- **钉钉图片与终态反馈**：`picture` 消息经 `downloadCode` 进入多模态管道，增加格式/大小/时限护栏；成功、错误和 handler 超时都会撤回思考占位并使用独立预算送达终态消息。
- **知识与记忆召回**：短输入/场景会话增加注入门控，无证据召回不再伪造命中，记忆和知识上下文不会越过云端出站边界。
- **适配器与任务韧性**：加固有界发送队列、webhook 体限额、停止上下文和多平台连接生命周期；数学纯文本降级新增 Unicode 归一化。
- **定时任务编译**：使用文本推理模型而不是视觉模型，并加固持续任务的幂等创建、状态恢复与历史记录。

### Security
- 场景包挂载路由纳入统一鉴权前缀；执行、视觉、embedding 和 rerank 统一遵循出站用途/数据分类策略，避免本地记忆、知识命中或图片在 provider 回退时误出网。

## [0.4.9] - 2026-07-04
> `code_exec` 强沙箱与文件授权、无人值守派发治理、官方 IM SDK 与知识/记忆边界加固。

### Added
- **沙箱运行工作区**：`code_exec` 新增 run workspace、FileAccessBroker 和项目/文件授权边界。
- **无人值守治理**：新增 autonomy profile、preflight、summary、decision audit、task grant 端点与显式派发矩阵。
- **知识库与模型能力 API**：开放记忆配置/状态与模型能力查询，知识库支持视觉文档内容提取。

### Changed
- 功能优先配置成为默认，cron/webhook/spawn/heartbeat/workflow 按派发来源和工具类别裁决无人值守执行。
- 飞书、钉钉、LINE 适配器对齐官方 SDK；路由规则、多模态回复、流式思考时长和 cron Starlark 修复语义收敛。
- 升级至 ai-core **v0.2.1**，并同步当时的 hexagon/toolkit 依赖栈。

### Fixed
- 收紧记忆召回与 RAG 辅助调用的数量/超时边界，修复 cron、MCP 与钉钉集成中的生命周期问题。
- 稳定 Linux/macOS/Windows `code_exec` 沙箱 CI，发版模式可使用离线 Go module cache。

## [0.4.8] - 2026-06-28
> 多 Agent 编排 + 记忆生命周期 + RAG 增强 + 持续型任务；依赖升级 ai-core v0.1.11 / hexagon v0.5.7 / toolkit v0.2.3。

### Added
- **多 Agent 编排**：新增子代理注册、监督、合成、写隔离、反挂起与 goal/solve 验收闭环。
- **记忆生命周期**：新增梦境整理、反思、PII 处理、画像、原子写、迁移和结构化召回。
- **RAG 增强**：新增多模态、忠实度评估、精排、快照和嵌入装饰器；内置知识检索、记忆管理与会话检索技能。
- **持续型任务**：打通 continuous cron、webhook/知识库链路与目标触发，工作流支持并行恢复。

### Changed
- 持久化会话内容块、工具调用与技能推理；IM 支持会话模型切换、工具摘要与代理拨号。
- 升级至 ai-core **v0.1.11**、hexagon **v0.5.7**、toolkit **v0.2.3**。

### Fixed
- 统一中英文人设拆分与本地化语义；字符串截断改为 rune 边界，避免多字节字符被切裂。

## [0.4.7] - 2026-06-26
> 技能/MCP 市场离线优先 + 应用自省自愈 skill（app_query/app_heal）+ 文档上传抽取预览 + 会话附件持久化；一组 CJK 截断 / 上游脱敏 / API 契约修复；框架依赖升级 hexagon v0.5.5 / ai-core v0.1.8。

### Added
- **技能/MCP 市场离线优先**：出厂嵌入快照 + 磁盘缓存 + 带 TTL/退避的后台异步刷新；`EnsureCatalog` 保证目录非阻塞可用，网络与缓存均不可用时回落嵌入种子。`McpHub` 收敛为 `Hub` 门面，目录抓取/离线/缓存统一委托 `Hub`。新增 `scripts/sync-hub-embed.sh`，发版时从锚定分支同步嵌入快照。
- **应用自省与自愈 skill**：`app_query`（P0 只读自省——连接/MCP/cron/webhook/agent/config/logs 脱敏查询，凭据红线打码 + `<app-data>` 围栏）与 `app_heal`（P1 自愈——cron retry/resume/pause、workflow_run，含限频与审计）；`react` 能力上下文注入系统卡片（版本/计数），交互式会话仍可走确认门，无人值守自动化按功能优先执行。
- **文档上传/抽取/预览**：`POST /documents/extract`（PDF/DOC/PPTX → 纯文本，供对话上下文注入；PDF 走 poppler、.doc 走 textutil、.pptx 走 hexagon PPTXLoader）、`POST/GET /documents/preview/{token}`（原文件内存环形缓存预览）；知识库放开 .pdf/.doc/.pptx 格式。
- **会话附件持久化**：图片 base64 单列存储（migration v7 `messages.attachments`，8MB 上限）规避 64KB metadata 截断导致重载丢图；`documents` 元数据落库（`json.RawMessage`）；`message-id` 优先用 `request_id` 作存储主键，修复删除返回 404。

### Changed
- 升级框架依赖：hexagon v0.5.3 → **v0.5.5**、ai-core v0.1.7 → **v0.1.8**（API 兼容，本仓无源码适配改动）。
- 对齐版本常量：`hexclaw.Version` 0.4.4 → **0.4.7**（历次发版漏更，本次补齐）。

### Fixed
- **CJK 截断乱码（AP-049）**：`skill_judge`/`eval`/`llmrouter.capabilities`/`risk_reviewer`/`react` 的工具与描述截断统一改 rune-safe `stringx.SubString`，杜绝字节边界切断 CJK 产出非法 UTF-8 序列。
- **上游错误脱敏 + 数字 error.code**：`upstreamerr` 兼容 JSON number 形式的 `error.code`（如 Nvidia 返回数字 `400`）；WS 流式分片错误统一过 `PublicMessage` 脱敏，避免上游原始 JSON 漏进聊天气泡。
- **API 契约**：绑定到禁用 provider 的 agent 在注册校验期即拒绝（不再延后到运行时）；email 能力仅 `["receive"]`（send 未接入 instances.Manager）；Webhook 在 jobID 绑定时 `prompt` 可选、空 jobID 时仍必填。
- **记忆闸门对称（bug#3-3）**：`memory=off` 同时阻断 `memProvider.Inject`，杜绝 standing/fact 注入泄漏。

## [0.4.6] - 2026-06-23
> 连接中心 MCP 收敛（env keystone + 密钥箱静态加密）+ max-turns 优雅降级（对齐 hexagon StopReason）+ 一组用户反馈修复；框架依赖与技能市场版本升级。

### Added
- **MCP env keystone + 密钥箱（连接中心）**：新增 `config/mcp_secret`——MCP Server 的 `env` 凭据（DB 密码、API Key 等）经主密钥（`~/.hexclaw/master.key`，load-or-create）静态加密落盘（`enc:v1:`），启动时 `LoadBox` + `DecryptMCPEnv` 解密后注入 `mcpMgr` 连接；加载失败降级明文并仅告警，绝不记录密钥或明文凭据。MCP Server 连接透传 `Env` 字段。API 响应一律脱敏。
- **MCP 参数校验 + AP-031/032/034 修复**：`api/handler_misc`/`handler_extended` 补 env 读写脱敏与参数校验，配套 env 持久化 / 校验 / mysql MCP e2e 审计回归测试。

### Changed
- 升级框架依赖：hexagon v0.5.2 → **v0.5.3**（`Result.StopReason` 提升为一等字段、移除 `ErrMaxTurns`/`KindMaxTurns`，达到轮次上限不再是错误，对齐 `stop_reason` 语义）。
- **技能市场默认分支 v0.0.4 → v0.0.5**：`HubConfig` / `SkillsHubConfig` 默认 `Branch` 同步 hexclaw-hub v0.0.5 发布。
- **agent 工具轮次上限默认 5 → 25**：budget 模式仍以 `hardMaxTurns=50` 兜底；上下文压缩在本地模型场景跳过，避免无谓调用。
- **功能优先默认值**：本地/云端模型在 `tools.enabled=auto` 下都默认开启工具；代码执行、Shell、MCP/Skill 管理、RAG Pipeline、Runtime Sandbox、Plugin Extension、真实 Agent Dispatcher 等产品级能力默认可用，显式配置可回退。

### Fixed
- **max-turns 优雅降级**：达到轮次上限按 `result.StopReason==max_turns` 优雅返回部分结果（非错误），尾部追加「继续」提示，不再向用户抛错。
- **文件记忆挂载闸门（bug#3a）**：只要文件记忆系统创建成功即挂到引擎，不再用「启动时记忆是否为空」当闸门——否则首次启动新增的记忆要等重启才注入，问答答不上。
- **用户反馈修复**：记忆注入闸门、挂载 skill 人设透传、人设 prompt、mounted persona skill 一组回归取证；`api/handler_webhook` 契约对齐。
- **桌面通知来源标识**：`desktop.Notification` 新增 `Source`（`cron`/`im`），供前端映射图标与深链；`Notify` 保持旧行为（`NotifySource` 空来源）。

## [0.4.5] - 2026-06-23
> 框架依赖升级 + 桌面定位下的网络/扫描放开 + 出站 UA 统一 + 一组 R4 契约修复与审计回归测试。

### Added
- **数据连接器（§15.1）**：新增 `connector/` 包——token（PAT / Integration Token）只读接入 GitHub / Notion，token 经 `secret.Box` 静态加密落盘（`~/.hexclaw/connectors.json`，`enc:v1:`），API 响应一律脱敏。端点 `GET/POST/DELETE /api/v1/connectors`、`POST /api/v1/connectors/test`、`GET /api/v1/connectors/{id}/resources`（真实拉取仓库 / 页面）。
- **默认助理人设（SOUL）端点**：注册 `GET/PUT /api/v1/assistant/soul`（读写 `~/.hexclaw/SOUL.md`，空=恢复内置默认；引擎每轮读取，保存即时生效）。
- **定时任务显式投递目标**：`AddCronJobRequest.Deliver` 透传到 `cron.AddJobRequest`，前端「从连接库下拉选投递目标」即走此字段（§5 一处存处处引）。
- **AI Skill 生成端点**：`handleGenerateSkill`（对话式生成 Skill）+ 安装路径，配套生成/安装测试。
- **`httpua` 包**：统一出站 HTTP 默认 User-Agent（image 下载 / render 抓取 / cron starlark http）。裸 `Go-http-client` UA 易被站点反爬返回 HTML 拦截页，致下游 json/图片解码 `invalid character '<'`；`httpua.Set` 在调用方未显式设 UA 时注入真实浏览器 UA（显式 UA 优先）。

### Changed
- **工作流图执行器兼容前端字段**：`workflow_runtime.go` parse() 同时接受节点 `data`/`config`、边 `source/target`/`from/to`，使桌面端线性工作流保存的形状能被图执行器正确读取并链接。
- 升级框架依赖：toolkit v0.2.0 → **v0.2.1**（`net/httpx.RawClient` 遵循 `HTTP(S)_PROXY`/`NO_PROXY`）、ai-core v0.1.6 → **v0.1.7**（`llmcall` 退避委托 `toolkit/util/retry`）、hexagon v0.5.1 → **v0.5.2**（circuit/retry/sse/ssrf 复用 toolkit；`CircuitBreaker` 状态回调改异步——本仓不直接依赖该回调同步性）。
- **adapter.ReplyChunk 补小写 json tag（R4-1）**：SSE 聊天 wire JSON 由 PascalCase（`{"Content":…}`）改为 `content`/`reasoning`/… 小写，修复桌面端读取聊天正文恒空。
- **adapter/dingtalk 重连退避复用 `toolkit/util/retry.ExponentialBackoff`（A-1）**，退避序列保真。
- **api/team 枚举校验（C10）**：`visibility`/`role` 非法值返 400，空值取默认（private/member）。

### Security
- **网络放开（桌面定位）**：移除 cron starlark / skill browser / api 各处应用层 SSRF 私网阻断及相关回归测试（`engine_starlark.go` 瘦身、删 4 个 SSRF 测试）。hexclaw 作为**本地单用户桌面后端**运行，出站受用户机网络策略约束，不再在应用层强制私网名单。⚠️ 若改为多租户/公网部署，需自行恢复 SSRF 防护。
- **`security/desktop_mode` 部署模式开关**：`--desktop` 启动时置位，桌面（单用户自有机）放行内容注入扫描 / 危险模式扫描（`ScanUserPrompt`/`ScanAssembled`/`SkillScanner.Scan`）——单用户无第三方提示注入面，误杀代价 > 收益；**服务端（多租户）默认仍开启**全部扫描。

## [0.4.4] - 2026-06-21
> 接续 v0.4.3（已 tag 于 aa34a45）之后的地基功能 + 安全加固发布。本次新增静态凭据加密、
> 注入扫描、统一权限闸 GA、Skill 工具盘、library 记忆薄版，并修复一组无人值守安全缺口。

### Changed
- 升级框架依赖到 hexagon v0.5.1 / ai-core v0.1.6 / toolkit v0.2.0（go.mod 去除 toolchain 行，Go 1.25.5）。上游均为带回归测试的缺陷修复：ai-core `streamx` 超时改无损语义（不再丢在途元素）、`llm/failover` 错误分类收窄；hexagon `runtime/runner` 工具去重收敛为仅续跑（修复 provider 复用 tool-call ID 时漏配对结果）+ MaxTurns 返回部分结果。toolkit v0.2.0 `crypto/sign` `APISigner` wire 格式为 BREAKING，但本仓仅用 `HMACSHA256` 原语、未引用 `APISigner`，不受影响。
- 媒体/genstore/ssrf/cache/trace/events 迁移到 ai-core/toolkit/hexagon；gateway HMAC 改用 toolkit/crypto/sign。
- LLM failover 逻辑下沉到 ai-core/llm，删除本地等价实现，消费点改用 `llm.*`。
- Skill 沙箱包从顶层 `sandbox/` 迁移到 `skill/sandbox/`。

### Fixed
- matrix 适配器 Stop 幂等（消除二次调用 close(closed channel) panic）。
- knowledge 时间衰减：零值 CreatedAt 不再被衰减清零（修复无时间戳 chunk 永不召回）。
- cron：多副本 job 双跑防护（DB 原子领取 + fencing），fail-open 保纯内存行为。
- 安全加固：SSRF 仅放行 loopback（封禁元数据与内网地址）；文件操作 symlink 越界防护；WhatsApp webhook 验签 + 微信/企微常量时间比较；shell 后续已调整为功能优先执行模型。
- **安全（BUG-F1）**：`manage_skill`（安装/卸载市场技能 = 能力注入）补入默认权限策略 `DefaultBaselinePolicy`，归为 require_approval。此前它漏出策略 → 默认 allow，可被 webhook/spawn 无人值守派发免审批自动调用（交互态也无确认）。回归测试 `engine/bug_f1_manage_skill_gate_test.go` 枚举全部能力变更类工具（新增同类工具忘记加规则会自动 FAIL）。
- **安全（BUG-F4）**：cron starlark `http_get/http_post` 的 SSRF 拦截补齐 RFC6598 CGNAT `100.64.0.0/10` 及 `192.0.0.0/24`、`198.18.0.0/15`——此前依赖的 toolkit stdlib 谓词漏判这些保留段，与 SECURITY 文档「封锁 RFC 6598」的承诺不符。回归测试 `cron/bug_f4_ssrf_reserved_test.go`、`cron/ssrf_guard_edge_test.go`（含 IPv4-mapped IPv6 / ULA / 整段 CGNAT）。
- **无人值守权限口径变更**：早期 BUG-F5 曾将无人值守代码执行/能力变更类工具硬拒；当前功能优先策略已改为 cron/webhook/spawn/heartbeat/workflow 自动放行 `require_approval` 工具，显式 `PermissionPolicy` deny 仍是运营硬限制。回归测试 `engine/bug_f5_unattended_reviewer_override_test.go` 覆盖 {工具 × 来源 × 顾问判定} 授权矩阵。
- **测试（BUG-F6）**：修复 feishu `TestStart_LogMessageWellFormed` 在 `-race` 下偶发（3/10）的数据竞争——`captureDefaultLog` 改用 mutex 包裹的 `syncBuffer` 作日志 sink，消除后台 Start 协程写入与测试读取的竞争（仅测试侧，生产代码不变）。

## [基线]
- 与 ai-core v0.1.6 / hexagon v0.5.1 / toolkit v0.2.0 对齐。
