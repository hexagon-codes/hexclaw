# K12 档案、教材与投递 API

家长指南的`answer`在已验证解答有明确答案段时保留该段的完整所求量、单位和否定条件；讲法模型的缺项候选由用例从可信原文恢复，不为此重调模型。无明确答案段的历史解答保留原锚定兼容，原作答及批改事实不因答案投影改变。

新专用教学和作品点评遵从Skill当前启用设置；明确停用/卸载不回退同名内嵌方法，应用必要的解题/事实/输出合同仍保留。旧已冻结结果继续按原身份读取，不因当前目录或设置变化重新生成。

照片错题的 `question` 表示原题。已知版本的求解范围包装和完整内部限定尾注仅用于模型请求，列表与复习队列返回恢复后的原题；未知格式保持原文。此读取投影不修改历史记录、ID、版本、状态或冻结产物，新错题写入同一原题表示。

本参考覆盖当前源码（包含工作区变更）注册的 51 个方法/路径，适用于 K12 场景已启用的 HexClaw 服务。已发布版本以相应 Tag 的文档与安装包为准；当前主线接口不保证已进入旧版安装包。

- [场景 API 与共同规则](../../API.md) · [English](records.en.md) · [完整 OpenAPI](../../../../api/openapi.yaml)
- [档案原子更新](../../API.md#profile-bundle)与[教材/进度十条共同契约](../../API.md#textbook-and-curriculum-progress)由场景 API 维护；本页列出逐接口字段、响应与副作用。

所有路径使用当前服务 `Authorization: Bearer <token>`。本机 Sidecar 使用服务端组成的 owner；云端 owner 来自已认证上下文。`agent` 是辅导实例名/孩子范围，不是身份凭据；教材、档案事务与 Cron 注册另外核对 owner→agent 归属。不要通过请求体、查询或自定义 header 指定 owner。只有当前实例已组成的能力可用；未开启 K12 时路径不存在，缺失的投递/Cron适配器返回501，图片/出题协调器缺失可返回503。

除备份恢复上限128MiB外，JSON命令上限1MiB，须为恰好一个JSON值；表内标为严格解码的接口拒绝未知字段。场景错误体是 `{"error":"..."}`，没有统一 `code/message` 字段。表内错误是该 handler 的实际状态映射，Bearer middleware 可额外返回401。400通常表示输入问题，404为不存在/归属不可见，409为版本/状态/幂等或未知模型调用需要核查，502为实际下游执行失败；不能把技术错误转成“无法识别”。Unix时间字段以秒计。

发送成果应先读取服务端批次/回执：`pending → sending → delivered`；`failed`与`partial_failed`可按服务端能力重试，`outcome_unknown`须先query，不能盲目重复付费调用或投递。GET回执只读；POST/query会核查并持久化Provider结果。200仅表示本次命令响应成功，只有所有必要子回执为delivered才表示整批送达。

## 接口索引

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `GET` | [`/api/k12/view-descriptor`](#viewdescriptor) | 读取辅导视图描述符 |
| `POST` | [`/api/k12/grade`](#grade) | 单题补批 |
| `POST` | [`/api/k12/record-mistake`](#recordmistake) | 手动记录已知错题 |
| `POST` | [`/api/k12/solve`](#solve) | 单题求解 |
| `GET` | [`/api/k12/text-solves/{solve_run_id}`](#gettextsolve) | 读取文字求解账本与最终结算 |
| `POST` | [`/api/k12/text-solves/{solve_run_id}/resume-publication`](#resumetextsolvepublication) | 恢复原文字结算及题库发布 |
| `GET` | [`/api/k12/print-replies/{message_id}`](#print-replies) | 读取原回合 PDF 投递事实 |
| `POST` | [`/api/k12/print-replies/{message_id}/resume`](#print-replies) | 恢复原回合 PDF 批次 |
| `GET` | [`/api/k12/review-queue`](#reviewqueue) | 读取到期复习队列 |
| `POST` | [`/api/k12/review-source-groups`](#recordreviewsourcegroup) | 记录经核验的同题来源关联 |
| `GET` | [`/api/k12/insight-report`](#insightreport) | 读取一份学情快照 |
| `POST` | [`/api/k12/mark-mastered`](#markmastered) | 记录家长确认并安排复验 |
| `POST` | [`/api/k12/tutoring-tips`](#tutoringtips) | 生成已完成作业的家长辅导要点 |
| `POST` | [`/api/k12/tutoring-tips/send`](#sendtutoringtips) | 发送冻结辅导成果到已绑定私聊 |
| `GET` | [`/api/k12/delivery-receipts/{id}`](#getdeliveryreceipt) | 读取投递回执 |
| `POST` | [`/api/k12/delivery-receipts/{id}/retry`](#retrydeliveryreceipt) | 重试投递回执 |
| `POST` | [`/api/k12/delivery-receipts/{id}/query`](#querydeliveryreceipt) | 核查投递回执 |
| `GET` | [`/api/k12/delivery-batches/{id}`](#getdeliverybatch) | 读取投递批次 |
| `POST` | [`/api/k12/delivery-batches/{id}/retry`](#retrydeliverybatch) | 重试投递批次 |
| `POST` | [`/api/k12/delivery-batches/{id}/query`](#querydeliverybatch) | 核查投递批次 |
| `POST` | [`/api/k12/grounding`](#addgrounding) | 写入教材上下文 |
| `POST` | [`/api/k12/accumulation`](#addaccumulation) | 收藏一条积累内容 |
| `GET` | [`/api/k12/accumulation`](#listaccumulation) | 读取积累本 |
| `GET` | [`/api/k12/accumulation/{id}`](#getaccumulation) | 读取一条积累 |
| `POST` | [`/api/k12/accumulation/{id}/send`](#sendaccumulation) | 发送服务端积累原文 |
| `POST` | [`/api/k12/accumulation/{id}/dictation-to-basket`](#accumdictationtobasket) | 接受积累默写出题任务 |
| `DELETE` | [`/api/k12/accumulation/{id}`](#deleteaccumulation) | 删除当前积累记录 |
| `GET` | [`/api/k12/backup`](#backup) | 读取完整家庭学习档案备份 |
| `POST` | [`/api/k12/restore`](#restore) | 合并恢复同一辅导实例档案 |
| `POST` | [`/api/k12/restore-as`](#restoreas) | 经监护人确认迁移到另一辅导实例 |
| `POST` | [`/api/k12/restore-as/{migration_id}/rollback`](#rollbackrestoreas) | 回退一次跨实例迁移 |
| `GET` | [`/api/k12/export`](#export) | 导出当前学期完整学习档案 |
| `GET` | [`/api/k12/mistake-sheet`](#mistakesheet) | 生成当前到期错题卷正文 |
| `GET` | [`/api/k12/profile`](#getprofile) | 读取孩子档案及版本 |
| `PUT` | [`/api/k12/profile`](#updateprofile) | 已停用的档案写入入口 |
| `GET` | [`/api/k12/textbook-binding-options`](#listtextbookbindingoptions) | 读取真实教材候选 |
| `GET` | [`/api/k12/curriculum-catalog`](#getcurriculumcatalog) | 读取数学真实教材目录 |
| `GET` | [`/api/k12/curriculum-progress`](#getcurriculumprogress) | 读取课程进度或只读建议 |
| `PUT` | [`/api/k12/profile-bundle`](#updateprofilebundle) | 原子更新完整档案、进度和周练设置 |
| `POST` | [`/api/k12/cold-start`](#coldstart) | 预览或确认冷启动档案 |
| `POST` | [`/api/k12/tutor-turn`](#tutorturn) | 生成家长讲题指令 |
| `GET` | [`/api/k12/cron/mistake-sheet`](#cronmistakesheet) | 周五错题卷正文 |
| `POST` | [`/api/k12/cron/fill-basket`](#cronfillbasket) | 兼容旧脚本的无操作入口 |
| `GET` | [`/api/k12/cron/daily-reminder`](#crondailyreminder) | 每日复习提醒正文 |
| `GET` | [`/api/k12/cron/return-reminder`](#cronreturnreminder) | 回传提醒正文 |
| `GET` | [`/api/k12/cron/monthly-report`](#cronmonthlyreport) | 月度学情报告正文 |
| `GET` | [`/api/k12/cron/semester-check`](#cronsemestercheck) | 学期确认提醒正文 |
| `GET` | [`/api/k12/cron/year-archive`](#cronyeararchive) | 学年归档建议正文 |
| `POST` | [`/api/k12/cron/provision`](#cronprovision) | 覆盖注册四个默认自动化任务 |
| `POST` | [`/api/k12/cron/reconcile-defaults`](#cronreconciledefaults) | 只补缺少的默认任务 |
| `POST` | [`/api/k12/bind-im`](#bindim) | 绑定辅导实例到一对一私聊 |

<a id="viewdescriptor"></a>
## GET /api/k12/view-descriptor

读取辅导视图描述符。

只读。slot 省略或空值使用 tutor；返回已注册视图的标签、徽章、输入提示、面板和 schema_version。

依赖：K12 module and ViewExtensionRegistry.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `slot` | query | 否 | string  |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `actions` | array<string> / null | 是 |  |
| `composer_chips` | array<string> / null | 是 |  |
| `composer_placeholder` | string | 是 |  |
| `header_tabs` | array<string> / null | 是 |  |
| `i18n_keys` | array<string> / null | 是 |  |
| `message_badges` | array<string> / null | 是 |  |
| `record_collections` | array<string> / null | 是 |  |
| `schema_version` | integer | 是 |  |
| `side_panels` | array<string> / null | 是 |  |


| 状态 | 错误含义 |
| --- | --- |
| `404` | Unknown view slot. |

[实现参考](../../apihttp/handler.go)


<a id="grade"></a>
## POST /api/k12/grade

单题补批。

保留的单题校准接口，并非图片任务入口。agent/problem 必填；空 student_answer 转为只解题，solve_only=true，不作对错判定、不入错题本。有作答时真实批改可写错题和复习状态；record_created 与 record_id 表达实际入库，不能凭 verdict 推测。超纲与词表未映射分别用 out_of_scope 与 curriculum_unmapped 表达。

当前调用者的可信owner由服务端认证装配确定。真实辅导档案继续核对owner与辅导实例归属；普通助手和默认助手可直接解题或批改，不要求创建学生档案，不写入学生错题、复习或学情记录。直接HTTP请求不必伪造工具回合的request/session/message；提供来源会话时仍核对实际访问权。

依赖：K12 record store, Solver/Grader and optional curriculum/insight adapters.

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 minLength=1 |
| `grade` | `` / `一年级上` / `一年级下` / `二年级上` / `二年级下` / `三年级上` / `三年级下` / `四年级上` / `四年级下` / `五年级上` / `五年级下` / `六年级上` / `六年级下` / `初一上` / `初一下` / `初二上` / `初二下` / `初三上` / `初三下` | 否 | When omitted or empty, use the saved profile term if available; otherwise no term constraint is injected. |
| `knowledge_points` | array<string> / null | 否 | 相关知识点名称。 |
| `problem` | string | 是 | 题目正文。 minLength=1 |
| `source_session` | string | 否 | 可选来源会话关联。 |
| `student_answer` | string | 否 | 孩子的作答；不是标准答案。 |
| `subject` | `` / `数学` / `语文` / `英语` / `物理` / `化学` | 否 | Empty preserves the default route; these direct-calibration endpoints use Chinese subject names. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `assessment_status` | `correct` / `correct_with_process_issue` / `wrong` / `unanswered` / `answer_unclear` / `blank_solved` / `out_of_scope` / `untrusted` | 否 |  |
| `badge` | string | 是 | 验算徽章机器值，如verified-strong。 |
| `curriculum_unmapped` | array<string> / null | 否 | 不在课标映射中的知识点；与超纲不同。 |
| `error_cause` | string | 否 |  |
| `evidence_type` | `numeric_exec` / `symbolic_exec` / `heterogeneous_model` / `heuristic` / `verbatim` / `none` | 是 |  |
| `final_answer_correct` | boolean / null | 否 |  |
| `out_of_scope` | boolean | 是 | 是否触发年级边界投影。 |
| `out_of_scope_kp` | string | 否 | 触发超纲的知识点，可省略。 |
| `record_created` | boolean | 是 | 本次是否实际创建错题记录。 |
| `record_id` | string | 否 | 服务端记录ID。 |
| `teaching_report` | object | 否 | 来自实际冻结来源的不可变结构化报告；旧记录或不适用来源可省略。 |
| `report_markdown` | string | 否 | 与PDF使用同一报告对象的快速解答和深度讲解。 |
| `solution` | string | 是 | 完整解法正文。 |
| `solve_only` | boolean | 是 | true表示只解题，不能显示批改对错。 |
| `verdict` | `agree` / `disagree` / `unverifiable` / `out_of_scope` / `verbatim` | 是 |  |
| `wrong_step` | string | 否 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid input or term/subject. |
| `404` | Scoped record not found. |
| `409` | Model invocation needs reconciliation; do not blindly resend. |
| `500` | Unclassified execution/storage error. |
| `502` | Solver, grader or verification failed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/handler.go)


<a id="recordmistake"></a>
## POST /api/k12/record-mistake

手动记录已知错题。

轻量写入已知错题，不运行 solve+verify 判定链。error_cause 空时可执行一次错因归纳，归纳失败仍可入库；重复题由记录去重决定 record_created=false。student_answer 是孩子作答，不是标准答案。

依赖：K12 records; optional cause summarizer.

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 minLength=1 |
| `error_cause` | string | 否 |  |
| `grade` | `` / `一年级上` / `一年级下` / `二年级上` / `二年级下` / `三年级上` / `三年级下` / `四年级上` / `四年级下` / `五年级上` / `五年级下` / `六年级上` / `六年级下` / `初一上` / `初一下` / `初二上` / `初二下` / `初三上` / `初三下` | 否 | When omitted or empty, use the saved profile term if available; otherwise no term constraint is injected. |
| `knowledge_points` | array<string> / null | 否 | 相关知识点名称。 |
| `problem` | string | 是 | 题目正文。 minLength=1 |
| `source_session` | string | 否 | 可选来源会话关联。 |
| `student_answer` | string | 否 | 孩子的作答；不是标准答案。 |
| `subject` | `` / `数学` / `语文` / `英语` / `物理` / `化学` | 否 | Empty preserves the default route; these direct-calibration endpoints use Chinese subject names. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `error_cause` | string | 否 |  |
| `record_created` | boolean | 是 | 本次是否实际创建错题记录。 |
| `record_id` | string | 否 | 服务端记录ID。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid input or term/subject. |
| `404` | Scoped record not found. |
| `409` | Model invocation needs reconciliation; do not blindly resend. |
| `500` | Unclassified execution/storage error. |
| `502` | Solver, grader or verification failed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/handler.go)


<a id="solve"></a>
## POST /api/k12/solve

单题求解。

agent/problem 必填；只给解法、验算证据和超纲投影，不批改、不写错题。与 grade 一样支持空或数学/语文/英语/物理/化学学科；省略年级优先采用持久档案。

启用文字执行账本时，同一 owner/agent/solve_run_id 冻结完整输入、来源和路由，相同输入重放原结果；同键异题、修订或来源冲突返回409。建议调用方在发送前冻结稳定solve_run_id；省略则创建新run，不能以重复无键请求代替恢复。未提供来源时保持空，非空消息须实际属于给定会话；会话访问沿现有认证装配核查。模型路由不可用不影响本地确定性计算或已验证题库采用，实际需要Provider的新执行才失败。

普通助手、默认助手与辅导助手复用同一文字结算合同，不补造学生或来源消息。已结算结果按原输入、回执和结果摘要读取，后续题库资产更新或归档不追溯改写该结算。首次采用新资产仍核对当前资格。教学补充失败保留已验证完整答案和可靠步骤，失败或未知回执继续保留，读取历史不重发模型调用。

依赖：K12 Solver and optional curriculum adapter.

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 minLength=1 |
| `grade` | `` / `一年级上` / `一年级下` / `二年级上` / `二年级下` / `三年级上` / `三年级下` / `四年级上` / `四年级下` / `五年级上` / `五年级下` / `六年级上` / `六年级下` / `初一上` / `初一下` / `初二上` / `初二下` / `初三上` / `初三下` | 否 | When omitted or empty, use the saved profile term if available; otherwise no term constraint is injected. |
| `knowledge_points` | array<string> / null | 否 | 相关知识点名称。 |
| `problem` | string | 是 | 题目正文。 minLength=1 |
| `solve_run_id` | string | 否 | 调用方稳定run标识，限定在可信owner和agent内；省略时生成新值。 |
| `source_session` | string | 否 | 实际来源会话；不提供时保持空。 |
| `source_message_id` | string | 否 | 给定来源会话内实际存在的user消息；不能伪造。 |
| `input_revision` | integer | 否 | 默认为1，必须为正；同run不能改变修订。 |
| `subject` | `` / `数学` / `语文` / `英语` / `物理` / `化学` | 否 | Empty preserves the default route; these direct-calibration endpoints use Chinese subject names. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `badge` | string | 是 | 验算徽章机器值，如verified-strong。 |
| `answer_source` | object | 否 | 实际答案来源：asset、deterministic或model，携采用或执行窄引用；不等于学生批改结论。 |
| `finalization` | object | 否 | 不可变文字结算，含source_kind=text_solve、solve_run_id、finalization_id、input_revision、input_digest、facts_digest、result_json/result_digest、invocation_id及可选publication。 |
| `curriculum_unmapped` | array<string> / null | 否 | 不在课标映射中的知识点；与超纲不同。 |
| `evidence_type` | `numeric_exec` / `symbolic_exec` / `heterogeneous_model` / `heuristic` / `verbatim` / `none` | 是 |  |
| `out_of_scope` | boolean | 是 | 是否触发年级边界投影。 |
| `out_of_scope_kp` | string | 否 | 触发超纲的知识点，可省略。 |
| `teaching_report` | object | 否 | 来自实际冻结来源的不可变结构化报告；旧记录或不适用来源可省略。 |
| `report_markdown` | string | 否 | 与PDF使用同一报告对象的快速解答和深度讲解。 |
| `solution` | string | 是 | 完整解法正文。 |
| `verdict` | `agree` / `disagree` / `unverifiable` / `out_of_scope` / `verbatim` | 是 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid input or term/subject. |
| `404` | Scoped record not found. |
| `409` | Model invocation needs reconciliation; do not blindly resend. |
| `500` | Unclassified execution/storage error. |
| `502` | Solver, grader or verification failed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/handler.go)


<a id="gettextsolve"></a>
## GET /api/k12/text-solves/{solve_run_id}

读取可信owner/agent内的文字求解原账本。无副作用，不重发模型，不把unknown状态变成成功。未最终结算但run已存在时仍可返回200及原invocations；实际Provider步骤与本地动作分开，账本行数不是逐HTTP请求或计费次数。

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `solve_run_id` | path | 是 | 非空稳定run标识。 |
| `agent` | query | 是 | 实际辅导实例，必须处于认证归属范围。 |

成功：`200` `application/json`。

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `run` | object | 是 | 冻结的身份、来源、输入修订、完整事实/摘要、request_json及route_snapshot。 |
| `invocations` | array<object> / null | 是 | 实际执行步骤及原状态、结果摘要/JSON；execution_kind区分provider与local_deterministic，sent/outcome_unknown不得盲重发。 |
| `finalized` | boolean | 是 | 是否存在已提交最终结算。 |
| `publication_eligible` | boolean | 是 | 是否存在原结算发布意图；不替代发布时资格复核或表示已发布。 |
| `finalization` | object | 否 | 已完成时返回原不可变结算；读取历史不表示采用来源当前仍有效。 |
| `adoption_currently_valid` | boolean | 否 | 仅采用过题库答案时返回；核对原采用回执及资产当前有效性。 |

| 状态 | 错误含义 |
| --- | --- |
| `400` | agent或solve_run_id为空。 |
| `404` | run不存在，或辅导实例归属不可见。 |
| `409` | 身份、版本或结果冲突。 |
| `503` | 文字执行账本未装配。 |
| `500` | 未分类存储/读取错误。 |

[实现参考](../../apihttp/text_solve_handler.go)

<a id="resumetextsolvepublication"></a>
## POST /api/k12/text-solves/{solve_run_id}/resume-publication

不需要JSON正文；path/query与上一个读取接口相同。只消费原最终结算及原题库发布合同，不调用Solver或Provider。若求解执行已成功落账而最终事务尚未提交，仅从完整、摘要一致的真实成功结果恢复本地结算；缺结果返回404，存在sent/outcome_unknown则409，不重解。原发布资格继续要求实际agree+numeric_exec及现有范围/来源复核，不创建GradingJob或学生评价。归档资产不因恢复而自动复活。

成功：`200` `application/json`。没有原发布资格时published=false、asset=null，不代表求解失败。

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `published` | boolean | 是 | 本次是否返回原发布资产。 |
| `asset` | object / null | 是 | 原题库版本及修订；没有可发布意图时为null。 |
| `model_invocations_added` | integer | 是 | 恒为0；恢复不调用模型。 |

状态同上述读取接口；409包括结果未知和已映射的身份/采用/发布冲突，未分类的发布证据或存储错误按实际handler返回500。技术错误不改成新的未验证答案。

[实现参考](../../apihttp/text_solve_handler.go)


<a id="reviewqueue"></a>
## GET /api/k12/review-queue

读取到期复习队列。

只读，跨错题与纠错型积累；review_kind 为 verify（验算变式）或 verbatim（原词重现）。家长确认和抽查状态不等于已掌握。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `items` | array<`mistakeDTO`> | 是 | 服务端列表投影。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required. |
| `500` | Queue or review-state read failed. |

[实现参考](../../apihttp/handler.go)


<a id="recordreviewsourcegroup"></a>
## POST /api/k12/review-source-groups

保存已经核验的同题来源，供错题卷和周练只读归并。严格JSON，必需`agent`、成员中的`canonical_record_id`、至少两个不同记录的完整来源元组及`evidence: {kind: "reviewed_sources", note: "..."}`。服务端从已发布冻结输入取得题面、图摘要和题号，不接收替代答案或客户端自编题面。

首次201，重放200；无效请求400，不存在或跨实例记录404，来源变化或有效组重叠409，存储失败500。响应`{group, created, replayed}`，保存首次审核正文、说明及创建时间。原错题、学习状态和旧PDF不改，来源失效则不再用整组归并。无模型调用或消息投递。

完整字段与语义见[来源关联合同](review-source-groups.md)，实现见[handler](../../apihttp/review_source_group_handler.go)。

<a id="insightreport"></a>
## GET /api/k12/insight-report

读取一份学情快照。

只读，所有数字、薄弱点、筛选范围及正文来自同一 as_of/source_digest/source_record_ids 快照。review_completion_rate=-1 表示分母为零；不能把未知比例当 0。message_content/render_manifest 是同一正文的显示投影。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `as_of` | integer | 是 | 冻结来源快照时间，Unix秒。 |
| `consecutive_fail_kps` | array<string> / null | 是 |  |
| `grade_term` | string | 是 | 孩子档案的年级学期。 |
| `learner` | string | 是 |  |
| `month_new_mistakes` | integer | 是 |  |
| `practice_pending` | integer | 是 |  |
| `review_completion_rate` | number | 是 | -1 means the review-week denominator is zero; otherwise the server-derived proportion. Do not recompute using another snapshot. |
| `review_week_end` | integer | 是 |  |
| `review_week_start` | integer | 是 |  |
| `source_digest` | string | 是 | 同一来源快照摘要。 |
| `source_record_ids` | array<string> / null | 是 |  |
| `suggestion` | string | 是 |  |
| `trend` | `TrendCounts` | 是 |  |
| `unscoped_source_count` | integer | 是 |  |
| `weak_top3` | array<`WeakPoint`> / null | 是 |  |
| `week_pending` | integer | 是 |  |
| `message_content` | `MessageContent` / null | 否 |  |
| `render_manifest` | `RenderManifest` / null | 否 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required. |
| `500` | Snapshot read/render projection failed. |

[实现参考](../../apihttp/handler.go)


<a id="markmastered"></a>
## POST /api/k12/mark-mastered

记录家长确认并安排复验。

名称是历史路径；当前行为保留错题状态、写 parent_confirmed_at 并安排复验，不直接把孩子判为已掌握。version 是当前记录版本，冲突返回409。成功体仅为 {"ok":true}。

依赖：K12 module, record store and the dependencies described for this operation.

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 minLength=1 |
| `record_id` | string | 是 | 服务端记录ID。 minLength=1 |
| `version` | integer | 是 | Expected current record version, compared by the storage CAS. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `ok` | `True` | 是 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid JSON or input. |
| `404` | Scoped resource not found. |
| `409` | Version, command or state conflict. |
| `500` | Storage or other unclassified execution error. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/handler.go)


<a id="tutoringtips"></a>
## POST /api/k12/tutoring-tips

生成已完成作业的家长辅导要点。

只接受 agent 与公开 dispatch_id；从归属内已确认作业事实、持久年级和教材生成，不接受内部 GradingJob ID 或客户端正文。

依赖：K12 ImageTask facade and owner-scoped tutoring/grounding dependencies.

JSON 正文（严格解码）：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 minLength=1 |
| `dispatch_id` | string | 是 | minLength=1 |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `knowledge_points` | array<string> | 是 | 相关知识点名称。 |
| `sections` | array<[`TutoringTipsSection`](#tutoringtipssection)> | 是 | 固定三个小节。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent/dispatch_id required; unknown JSON fields rejected. |
| `404` | Owned dispatch/profile/catalog not found. |
| `409` | Dispatch not in a tips-capable state. |
| `500` | Unclassified execution failure. |
| `502` | Grounding/model dependency failure. |
| `503` | ImageTask facade not composed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/handler.go)


<a id="sendtutoringtips"></a>
## POST /api/k12/tutoring-tips/send

发送冻结辅导成果到已绑定私聊。

发送服务端 final_artifact_id，digest 可选但提供时须匹配；不接受任意正文。投递到当前全部有效物理私聊目标的冻结快照，去重 (platform, instance_id, chat_id)。HTTP200只返回批次事实，须继续查回执直到 delivered；未知结果先 query。

依赖：K12 durable delivery adapter and at least one active direct binding.

JSON 正文（严格解码）：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 minLength=1 |
| `final_artifact_id` | string | 是 | minLength=1 |
| `final_artifact_digest` | string | 否 |  |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `agent_name` | string | 是 | 响应资源所属辅导实例。 |
| `batch_id` | string | 是 | 持久投递批次ID。 |
| `content_digest` | string | 是 | 冻结投递正文摘要。 |
| `created_at` | integer | 是 | 创建时间，Unix秒。 |
| `dedupe_key` | string | 是 | 服务端持久去重身份。 |
| `object_id` | string | 是 |  |
| `object_kind` | string | 是 |  |
| `receipts` | array<`DeliveryReceipt`> / null | 是 | 按批次顺序返回全部目标/内容回执。 |
| `status` | `DeliveryBatchStatus` | 是 | 当前服务端持久状态；按所属对象枚举解释。 |
| `updated_at` | integer | 是 | 最后更新时间，Unix秒。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid/unknown JSON or final artifact mismatch. |
| `404` | Owned artifact not found. |
| `409` | No active direct bindings, invalid delivery state or unsupported reconciliation. 未分类执行/存储错误当前也映射为409。 |
| `501` | Delivery not composed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/delivery_receipt_handler.go)


<a id="getdeliveryreceipt"></a>
## GET /api/k12/delivery-receipts/{id}

读取投递回执。

只读取已持久化投递状态；sending/external_message_id 不等于 delivered。

依赖：K12 durable delivery adapter; query additionally needs provider reconciliation support.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string Server-issued resource identity. |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `agent_name` | string | 是 | 响应资源所属辅导实例。 |
| `attempt` | integer | 是 | 服务端记录的尝试次数。 |
| `batch_id` | string | 否 | 持久投递批次ID。 |
| `batch_ordinal` | integer | 否 |  |
| `binding_id` | string | 是 |  |
| `created_at` | integer | 是 | 创建时间，Unix秒。 |
| `dedupe_key` | string | 是 | 服务端持久去重身份。 |
| `delivery_id` | string | 是 | 持久投递回执ID。 |
| `external_message_id` | string | 否 |  |
| `last_error` | string | 否 | 最近错误说明，可省略。 |
| `object_id` | string | 是 |  |
| `object_kind` | string | 是 |  |
| `part_digest` | string | 是 |  |
| `part_kind` | `PartKind` | 是 |  |
| `part_mime` | string | 否 |  |
| `part_ordinal` | integer | 是 |  |
| `payload_digest` | string | 是 |  |
| `payload_json` | string | 是 |  |
| `render_manifest_json` | string | 是 |  |
| `status` | `DeliveryReceiptStatus` | 是 | 当前服务端持久状态；按所属对象枚举解释。 |
| `target` | `DeliveryTarget` | 是 |  |
| `updated_at` | integer | 是 | 最后更新时间，Unix秒。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required or malformed JSON. |
| `404` | Owned delivery resource not found. |
| `409` | Illegal delivery state or query not available. 未分类执行/存储错误当前也映射为409。 |
| `501` | Delivery adapter not composed. |

[实现参考](../../apihttp/delivery_receipt_handler.go)


<a id="retrydeliveryreceipt"></a>
## POST /api/k12/delivery-receipts/{id}/retry

重试投递回执。

只由服务端按允许状态重试失败的回执；不会盲目重发 outcome_unknown。批次只重试合法的失败子回执。

依赖：K12 durable delivery adapter; query additionally needs provider reconciliation support.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string Server-issued resource identity. |

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 minLength=1 |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `agent_name` | string | 是 | 响应资源所属辅导实例。 |
| `attempt` | integer | 是 | 服务端记录的尝试次数。 |
| `batch_id` | string | 否 | 持久投递批次ID。 |
| `batch_ordinal` | integer | 否 |  |
| `binding_id` | string | 是 |  |
| `created_at` | integer | 是 | 创建时间，Unix秒。 |
| `dedupe_key` | string | 是 | 服务端持久去重身份。 |
| `delivery_id` | string | 是 | 持久投递回执ID。 |
| `external_message_id` | string | 否 |  |
| `last_error` | string | 否 | 最近错误说明，可省略。 |
| `object_id` | string | 是 |  |
| `object_kind` | string | 是 |  |
| `part_digest` | string | 是 |  |
| `part_kind` | `PartKind` | 是 |  |
| `part_mime` | string | 否 |  |
| `part_ordinal` | integer | 是 |  |
| `payload_digest` | string | 是 |  |
| `payload_json` | string | 是 |  |
| `render_manifest_json` | string | 是 |  |
| `status` | `DeliveryReceiptStatus` | 是 | 当前服务端持久状态；按所属对象枚举解释。 |
| `target` | `DeliveryTarget` | 是 |  |
| `updated_at` | integer | 是 | 最后更新时间，Unix秒。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required or malformed JSON. |
| `404` | Owned delivery resource not found. |
| `409` | Illegal delivery state or query not available. 未分类执行/存储错误当前也映射为409。 |
| `501` | Delivery adapter not composed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/delivery_receipt_handler.go)


<a id="querydeliveryreceipt"></a>
## POST /api/k12/delivery-receipts/{id}/query

核查投递回执。

查询 Provider 对未知或仍发送中的消息结果并持久化状态；不是重新发送。Provider 不支持查询时返回409。

依赖：K12 durable delivery adapter; query additionally needs provider reconciliation support.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string Server-issued resource identity. |

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 minLength=1 |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `agent_name` | string | 是 | 响应资源所属辅导实例。 |
| `attempt` | integer | 是 | 服务端记录的尝试次数。 |
| `batch_id` | string | 否 | 持久投递批次ID。 |
| `batch_ordinal` | integer | 否 |  |
| `binding_id` | string | 是 |  |
| `created_at` | integer | 是 | 创建时间，Unix秒。 |
| `dedupe_key` | string | 是 | 服务端持久去重身份。 |
| `delivery_id` | string | 是 | 持久投递回执ID。 |
| `external_message_id` | string | 否 |  |
| `last_error` | string | 否 | 最近错误说明，可省略。 |
| `object_id` | string | 是 |  |
| `object_kind` | string | 是 |  |
| `part_digest` | string | 是 |  |
| `part_kind` | `PartKind` | 是 |  |
| `part_mime` | string | 否 |  |
| `part_ordinal` | integer | 是 |  |
| `payload_digest` | string | 是 |  |
| `payload_json` | string | 是 |  |
| `render_manifest_json` | string | 是 |  |
| `status` | `DeliveryReceiptStatus` | 是 | 当前服务端持久状态；按所属对象枚举解释。 |
| `target` | `DeliveryTarget` | 是 |  |
| `updated_at` | integer | 是 | 最后更新时间，Unix秒。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required or malformed JSON. |
| `404` | Owned delivery resource not found. |
| `409` | Illegal delivery state or query not available. 未分类执行/存储错误当前也映射为409。 |
| `501` | Delivery adapter not composed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/delivery_receipt_handler.go)


<a id="getdeliverybatch"></a>
## GET /api/k12/delivery-batches/{id}

读取投递批次。

只读取已持久化投递状态；sending/external_message_id 不等于 delivered。

依赖：K12 durable delivery adapter; query additionally needs provider reconciliation support.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string Server-issued resource identity. |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `agent_name` | string | 是 | 响应资源所属辅导实例。 |
| `batch_id` | string | 是 | 持久投递批次ID。 |
| `content_digest` | string | 是 | 冻结投递正文摘要。 |
| `created_at` | integer | 是 | 创建时间，Unix秒。 |
| `dedupe_key` | string | 是 | 服务端持久去重身份。 |
| `object_id` | string | 是 |  |
| `object_kind` | string | 是 |  |
| `receipts` | array<`DeliveryReceipt`> / null | 是 | 按批次顺序返回全部目标/内容回执。 |
| `status` | `DeliveryBatchStatus` | 是 | 当前服务端持久状态；按所属对象枚举解释。 |
| `updated_at` | integer | 是 | 最后更新时间，Unix秒。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required or malformed JSON. |
| `404` | Owned delivery resource not found. |
| `409` | Illegal delivery state or query not available. 未分类执行/存储错误当前也映射为409。 |
| `501` | Delivery adapter not composed. |

[实现参考](../../apihttp/delivery_receipt_handler.go)


<a id="retrydeliverybatch"></a>
## POST /api/k12/delivery-batches/{id}/retry

重试投递批次。

只由服务端按允许状态重试失败的回执；不会盲目重发 outcome_unknown。批次只重试合法的失败子回执。

依赖：K12 durable delivery adapter; query additionally needs provider reconciliation support.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string Server-issued resource identity. |

JSON 正文（严格解码）：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 minLength=1 |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `agent_name` | string | 是 | 响应资源所属辅导实例。 |
| `batch_id` | string | 是 | 持久投递批次ID。 |
| `content_digest` | string | 是 | 冻结投递正文摘要。 |
| `created_at` | integer | 是 | 创建时间，Unix秒。 |
| `dedupe_key` | string | 是 | 服务端持久去重身份。 |
| `object_id` | string | 是 |  |
| `object_kind` | string | 是 |  |
| `receipts` | array<`DeliveryReceipt`> / null | 是 | 按批次顺序返回全部目标/内容回执。 |
| `status` | `DeliveryBatchStatus` | 是 | 当前服务端持久状态；按所属对象枚举解释。 |
| `updated_at` | integer | 是 | 最后更新时间，Unix秒。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required or malformed JSON. |
| `404` | Owned delivery resource not found. |
| `409` | Illegal delivery state or query not available. 未分类执行/存储错误当前也映射为409。 |
| `501` | Delivery adapter not composed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/delivery_receipt_handler.go)


<a id="querydeliverybatch"></a>
## POST /api/k12/delivery-batches/{id}/query

核查投递批次。

查询 Provider 对未知或仍发送中的消息结果并持久化状态；不是重新发送。Provider 不支持查询时返回409。

依赖：K12 durable delivery adapter; query additionally needs provider reconciliation support.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string Server-issued resource identity. |

JSON 正文（严格解码）：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 minLength=1 |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `agent_name` | string | 是 | 响应资源所属辅导实例。 |
| `batch_id` | string | 是 | 持久投递批次ID。 |
| `content_digest` | string | 是 | 冻结投递正文摘要。 |
| `created_at` | integer | 是 | 创建时间，Unix秒。 |
| `dedupe_key` | string | 是 | 服务端持久去重身份。 |
| `object_id` | string | 是 |  |
| `object_kind` | string | 是 |  |
| `receipts` | array<`DeliveryReceipt`> / null | 是 | 按批次顺序返回全部目标/内容回执。 |
| `status` | `DeliveryBatchStatus` | 是 | 当前服务端持久状态；按所属对象枚举解释。 |
| `updated_at` | integer | 是 | 最后更新时间，Unix秒。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required or malformed JSON. |
| `404` | Owned delivery resource not found. |
| `409` | Illegal delivery state or query not available. 未分类执行/存储错误当前也映射为409。 |
| `501` | Delivery adapter not composed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/delivery_receipt_handler.go)


<a id="addgrounding"></a>
## POST /api/k12/grounding

写入教材上下文。

agent/title/content 必填；subject 可空（兼容不分科语义）或六学科英文枚举。非空学科要求分科写入适配器；不静默降级到无学科。标题采用当前 Title 输入限制。

依赖：K12 GroundingWriter; SubjectGroundingWriter when subject is nonempty.

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 minLength=1 |
| `subject` | `` / `math` / `chinese` / `english` / `science` / `information_technology` / `art` | 否 | default= |
| `title` | string | 是 | minLength=1, maxLength=200 |
| `content` | string | 是 | minLength=1 |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `ok` | `True` | 是 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid JSON or input. |
| `404` | Scoped resource not found. |
| `409` | Version, command or state conflict. |
| `500` | Storage or other unclassified execution error. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/handler.go)


<a id="addaccumulation"></a>
## POST /api/k12/accumulation

收藏一条积累内容。

正文仅 content；学科、类型和来源由当前元数据派生器决定，客户端不能指定。同 agent/key/content 重放返回同记录、created=false；相同 key 不同内容409。模型元数据派生失败502，不以规则猜测兜底。

依赖：K12 record store and AccumulationMetadataDeriver.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |
| `Idempotency-Key` | header | 是 | string Reuse the same key only for the same immutable command. |

JSON 正文（严格解码）：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `content` | string | 是 | minLength=1 |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `record_id` | string | 是 | 服务端记录ID。 |
| `created` | boolean | 是 | 是否实际新建；重放或去重可能false。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid input or term/subject. |
| `404` | Scoped record not found. |
| `409` | Model invocation needs reconciliation; do not blindly resend. |
| `500` | Unclassified execution/storage error. |
| `502` | Solver, grader or verification failed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/handler.go)


<a id="listaccumulation"></a>
## GET /api/k12/accumulation

读取积累本。

只读；created_at 为 Unix 秒；source 可省略；dictation_generation 是持久出题进度投影。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |
| `subject` | query | 否 | string Optional subject filter, commonly 语文 or 英语. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `items` | array<`accumDTO`> | 是 | 服务端列表投影。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required. |
| `500` | Accumulation list failed. |

[实现参考](../../apihttp/handler.go)


<a id="getaccumulation"></a>
## GET /api/k12/accumulation/{id}

读取一条积累。



依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string Server-issued resource identity. |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `content` | string | 是 |  |
| `created_at` | integer | 是 | 创建时间，Unix秒。 |
| `dictation_generation` | `accumulationDictationGenerationDTO` / null | 否 | 持续查询同一积累条目的出题进度。 |
| `entry_type` | string | 是 |  |
| `record_id` | string | 是 | 服务端记录ID。 |
| `source` | string | 否 |  |
| `subject` | string | 是 |  |
| `version` | integer | 是 | 记录/备份的版本，按所在响应含义解释。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid JSON or input. |
| `404` | Scoped resource not found. |
| `409` | Version, command or state conflict. |
| `500` | Storage or other unclassified execution error. |

[实现参考](../../apihttp/handler.go)


<a id="sendaccumulation"></a>
## POST /api/k12/accumulation/{id}/send

发送服务端积累原文。

从归属记录读正文，拒绝未知字段和客户端替代正文；发送到完整私聊绑定快照。使用已冻结成果及去重键，按批次/回执查询送达状态。

依赖：K12 durable delivery and active direct bindings.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string Server-issued resource identity. |

JSON 正文（严格解码）：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 minLength=1 |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `agent_name` | string | 是 | 响应资源所属辅导实例。 |
| `batch_id` | string | 是 | 持久投递批次ID。 |
| `content_digest` | string | 是 | 冻结投递正文摘要。 |
| `created_at` | integer | 是 | 创建时间，Unix秒。 |
| `dedupe_key` | string | 是 | 服务端持久去重身份。 |
| `object_id` | string | 是 |  |
| `object_kind` | string | 是 |  |
| `receipts` | array<`DeliveryReceipt`> / null | 是 | 按批次顺序返回全部目标/内容回执。 |
| `status` | `DeliveryBatchStatus` | 是 | 当前服务端持久状态；按所属对象枚举解释。 |
| `updated_at` | integer | 是 | 最后更新时间，Unix秒。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required or invalid/unknown JSON. |
| `404` | Owned accumulation not found. |
| `409` | No active bindings or invalid delivery state. 未分类执行/存储错误当前也映射为409。 |
| `501` | Delivery unavailable. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/handler.go)


<a id="accumdictationtobasket"></a>
## POST /api/k12/accumulation/{id}/dictation-to-basket

接受积累默写出题任务。

默认 full_dictation=false 为补空；true 是显式整首默写选择。任务键由服务端固定为 dictation:{id}。202只表示持久任务已接纳；用积累详情 dictation_generation 跟踪到 committed 后才有 practice_item_id，期间 queued/generating/validating 不等于已入集。

依赖：K12 records and PracticeGeneration coordinator.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string Server-issued resource identity. |

JSON 正文（严格解码）：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 minLength=1 |
| `source_session` | string | 否 | 可选来源会话关联。 |
| `full_dictation` | boolean | 否 | default=False |

成功：`202` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `dictation_generation` | `accumulationDictationGenerationDTO` | 是 | 持续查询同一积累条目的出题进度。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid request or unsupported accumulation. |
| `404` | Owned accumulation not found. |
| `409` | Generation/state conflict or invocation requires reconciliation. |
| `500` | Unclassified generation/storage error. |
| `502` | Generation dependency failed. |
| `503` | Practice-generation coordinator unavailable. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/handler.go)


<a id="deleteaccumulation"></a>
## DELETE /api/k12/accumulation/{id}

删除当前积累记录。

持久墓碑删除，要求正整数 If-Match 与 Idempotency-Key；同一命令可重放，版本不符/键内容冲突409。不得用新键重复一个结果未知的删除意图。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `id` | path | 是 | string Server-issued resource identity. |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |
| `If-Match` | header | 是 | string Current positive row version; bare, quoted and weak ETag forms are accepted. |
| `Idempotency-Key` | header | 是 | string Reuse the same key only for the same immutable command. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `accumulation_id` | string | 是 |  |
| `deleted` | boolean | 是 |  |
| `version` | integer | 是 | 记录/备份的版本，按所在响应含义解释。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid JSON or input. |
| `404` | Scoped resource not found. |
| `409` | Version, command or state conflict. |
| `500` | Storage or other unclassified execution error. |

[实现参考](../../apihttp/handler.go)


<a id="backup"></a>
## GET /api/k12/backup

作品点评新调用在发送前冻结完整请求和实际方法内容；恢复及安全重试使用原快照，成功结果只读，未知调用不重发。`current_creative_works[].generations[].request_snapshot_json`为可选内部归档字符串，包含该请求及方法正文/摘要；客户端须原样保留，不从当前Skill目录补写或解析重排字符串。旧归档没有该字段时保持缺失。公开作品generation DTO不返回原始提示词，`feedback_skill`来源戳也不能替代完整快照。

v8 追加 `teaching_diagrams`，携带图示归属、资产身份、SHA-256 与精确 SVG 字节；摘要校验覆盖该字段，恢复与记录写入使用同一事务，RestoreAs 重写目标资产引用。旧版未携带图示时保留原 checksum 合同。

v9 追加 `review_sources`：保存同题来源关联、原已提交来源快照与所需页元数据；相应原图字节仍通过 `assets` 的单一清单保存。恢复与原记录共用事务，不重建模型调用或投递任务。来源读取继续核对当前题面、页摘要与作答身份，任何已有批改账本均优先于归档来源；失效关系不会因再次导出或恢复而重新生效。跨 Tutor 恢复重写资产归属及关联ID，回退恢复原关联集合。旧版归档继续按原版本校验和读取。

`snapshots[].source` 与 `pages[]` 保留冻结投影的 PascalCase 字段名，客户端须原样传输，不做大小写转换。`active` 只是导出时的来源状态，不是置信度，也不能绕过恢复后的当前身份复核；跨 Tutor 迁移前已不匹配原题的关系继续失效。

读取完整家庭学习档案备份。

JSON 响应不是附件下载；将完整响应保存为 .hexbak。当前 version=9，保留 archive_id/checksum/记录/图片/确认OCR/题目作答/source-action/当前作品档案原值。当前备份不含完整模型配置与全部长期记忆。任一引用资产缺失或损坏会使整份备份失败，不能丢字段后自行改 checksum。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `agent_name` | string | 是 | 响应资源所属辅导实例。 |
| `archive_id` | string | 否 | 不可变归档身份。 |
| `assets` | array<`HexbakAsset`> / null | 否 |  |
| `checksum` | string | 是 | 版本化完整归档checksum；原样保留。 |
| `creative_work_ocr` | array<`CreativeWorkOCRArchiveEvidence`> / null | 否 |  |
| `current_creative_works` | array<`CreativeWorkArchiveV7`> / null | 否 |  |
| `exported_at` | integer | 是 | 备份时间，Unix秒。 |
| `problem_attempts` | array<`ProblemAttemptSnapshot`> / null | 否 |  |
| `problem_source` | `ProblemSourceArchiveV6` / null | 否 |  |
| `profile` | `ChildProfile` / null | 否 |  |
| `records` | array<`AgentRecord` / null> / null | 是 |  |
| `version` | integer | 是 | Current backup version is 7; restoration supports versioned checksum contracts through 7. Preserve this complete envelope unchanged. |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required. |
| `500` | Backup, referenced asset bytes or checksum packing failed. |

[实现参考](../../apihttp/handler.go)


<a id="restore"></a>
## POST /api/k12/restore

合并恢复同一辅导实例档案。

body 为完整备份；上限128MiB。先校验该版本checksum与资产，导入记录ID冲突采用归档，归档中未出现的当前记录保留，孩子档案精确恢复。snapshot 是恢复前 best-effort 快照，可能 null；不能保证一定可回退。较新归档版本409，checksum错误400。

依赖：K12 module, record store and the dependencies described for this operation.

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent_name` | string | 是 | 响应资源所属辅导实例。 |
| `archive_id` | string | 否 | 不可变归档身份。 |
| `assets` | array<`HexbakAsset`> / null | 否 |  |
| `checksum` | string | 是 | 版本化完整归档checksum；原样保留。 |
| `creative_work_ocr` | array<`CreativeWorkOCRArchiveEvidence`> / null | 否 |  |
| `current_creative_works` | array<`CreativeWorkArchiveV7`> / null | 否 |  |
| `exported_at` | integer | 是 | 备份时间，Unix秒。 |
| `problem_attempts` | array<`ProblemAttemptSnapshot`> / null | 否 |  |
| `problem_source` | `ProblemSourceArchiveV6` / null | 否 |  |
| `profile` | `ChildProfile` / null | 否 |  |
| `records` | array<`AgentRecord` / null> / null | 是 |  |
| `version` | integer | 是 | Current backup version is 7; restoration supports versioned checksum contracts through 7. Preserve this complete envelope unchanged. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `restored` | integer | 是 | 此次恢复的记录数。 |
| `snapshot` | `Hexbak` / null | 是 | 恢复前快照；普通restore可为null。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid JSON, checksum, asset manifest or archive fields. |
| `409` | Backup version newer than supported, data/invocation state conflict. |
| `413` | Body exceeds 128 MiB. |
| `500` | Restore/storage failure. |

[实现参考](../../apihttp/handler.go)


<a id="restoreas"></a>
## POST /api/k12/restore-as

经监护人确认迁移到另一辅导实例。

archive 完整保存源身份与checksum；source_agent 必须等于原归属，target_agent 必须不同且为重建目标；guardian_confirmed=true；idempotency_key 经trim后须非空，且最多200个UTF-8字节。仅接受v2及以上兼容档案，当前上限v9。原档案、目标恢复前快照、owner改写journal与领域写入同一SQLite事务，结果带 migration_id。重复同键返回持久收据，修改内容须新键。

依赖：K12 module, record store and the dependencies described for this operation.

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `archive` | `Hexbak` | 是 | 须为完整非null归档；null返回400。 |
| `guardian_confirmed` | `True` | 是 |  |
| `idempotency_key` | string | 是 | trim后非空；最多200个UTF-8字节。 |
| `source_agent` | string | 是 |  |
| `target_agent` | string | 是 |  |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `idempotent` | boolean | 是 |  |
| `journal_entries` | integer | 是 |  |
| `migrated_checksum` | string | 否 |  |
| `migration_id` | string | 是 |  |
| `original_archive_digest` | string | 否 |  |
| `original_archive_preserved` | boolean | 是 |  |
| `restored` | integer | 是 | 此次恢复的记录数。 |
| `snapshot` | `Hexbak` / null | 否 | 恢复前快照；普通restore可为null。 |
| `snapshot_digest` | string | 否 |  |
| `source_agent` | string | 否 |  |
| `status` | string | 是 | 当前服务端持久状态；按所属对象枚举解释。 |
| `target_agent` | string | 是 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid archive, scope, guardian confirmation or command fields. |
| `404` | Required scoped object not found. |
| `409` | Version/state/idempotency conflict. |
| `413` | Body exceeds 128 MiB. |
| `500` | Atomic migration/storage failed. |

[实现参考](../../apihttp/handler.go)


<a id="rollbackrestoreas"></a>
## POST /api/k12/restore-as/{migration_id}/rollback

回退一次跨实例迁移。

正文 target_agent 与 guardian_confirmed=true；migration_id 由路径决定，即使正文带同名字段也由路径覆盖。请求上限1MiB。精确回退不可变恢复前快照并追加journal；重复调用返回同一 rolled_back 收据。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `migration_id` | path | 是 | string Server-issued resource identity. |

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `guardian_confirmed` | `True` | 是 |  |
| `migration_id` | string | 否 | Compatibility body field, ignored because the path migration_id always replaces it. |
| `target_agent` | string | 是 |  |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `idempotent` | boolean | 是 |  |
| `journal_entries` | integer | 是 |  |
| `migrated_checksum` | string | 否 |  |
| `migration_id` | string | 是 |  |
| `original_archive_digest` | string | 否 |  |
| `original_archive_preserved` | boolean | 是 |  |
| `restored` | integer | 是 | 此次恢复的记录数。 |
| `snapshot` | `Hexbak` / null | 否 | 恢复前快照；普通restore可为null。 |
| `snapshot_digest` | string | 否 |  |
| `source_agent` | string | 否 |  |
| `status` | string | 是 | 当前服务端持久状态；按所属对象枚举解释。 |
| `target_agent` | string | 是 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid/absent guardian confirmation or target. |
| `404` | Migration or target not found. |
| `409` | Migration/state conflict. |
| `413` | Body exceeds 1 MiB. |
| `500` | Rollback/storage failed. |

[实现参考](../../apihttp/handler.go)


<a id="export"></a>
## GET /api/k12/export

导出当前学期完整学习档案。

md/省略返回 Markdown JSON；无Renderer也返回同一JSON。PDF/DOCX渲染成功返回对应二进制附件及 X-HexClaw-Artifact-ID/Source-Digest/Object-Counts。渲染失败仍200，但返回同一canonical快照的 Markdown JSON 与 render_error；根据 Content-Type 和实际内容识别格式，不能把200直接保存为PDF。五类对象来自同一scope/as_of/source_digest。

依赖：K12 records; optional PDF/DOCX Renderer.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |
| `format` | query | 否 | `md` / `pdf` / `docx`  |

成功：`200` `application/json` or PDF/DOCX binary attachment.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `format` | `markdown` | 是 |  |
| `content` | string | 是 |  |
| `schema_version` | `v1` | 是 |  |
| `scope` | `LearningArchiveScope` | 是 | 该成果固定的Tutor与学期。 |
| `as_of` | integer | 是 | 冻结来源快照时间，Unix秒。 |
| `source_digest` | string | 是 | 同一来源快照摘要。 |
| `object_counts` | `LearningArchiveObjectCounts` | 是 | 同一成果的五类对象计数。 |
| `artifact_id` | string | 是 | 冻结导出成果身份。 |
| `render_error` | string | 否 | PDF/DOCX失败后的JSON降级原因。 |
| `attachments` | array<`LearningArchiveAttachment`> | 否 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required. |
| `500` | Canonical archive construction failed. |

[实现参考](../../apihttp/handler.go)


<a id="mistakesheet"></a>
## GET /api/k12/mistake-sheet

生成当前到期错题卷正文。

只读返回 {format:"markdown",content}，不是PDF/Word附件。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `format` | `markdown` | 是 |  |
| `content` | string | 是 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required. |
| `500` | Sheet generation failed. |

[实现参考](../../apihttp/handler.go)


<a id="getprofile"></a>
## GET /api/k12/profile

读取孩子档案及版本。

当前响应精确为 child_name/grade_term/textbook_edition/revision；textbook_edition 为数学教材兼容字段，不包含完整六学科配置。完整更新走profile-bundle。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `child_name` | string | 是 | 孩子称呼。 |
| `grade_term` | string | 是 | 孩子档案的年级学期。 |
| `revision` | integer | 是 | 当前CAS生命周期版本。 |
| `textbook_edition` | string | 是 | 教材版本；档案响应中为数学兼容字段。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required. |
| `404` | Profile read failed/not found. |

[实现参考](../../apihttp/handler.go)


<a id="updateprofile"></a>
## PUT /api/k12/profile

已停用的档案写入入口。

路由仍注册但始终405，正文不读取，没有成功更新契约。使用 PUT /api/k12/profile-bundle。

依赖：K12 module, record store and the dependencies described for this operation.

已停用：任何调用都返回405、Allow: GET。

| 状态 | 错误含义 |
| --- | --- |
| `405` | Always returns {"error":"profile updates require /api/k12/profile-bundle"}, with Allow: GET. |

[实现参考](../../apihttp/handler.go)


<a id="listtextbookbindingoptions"></a>
## GET /api/k12/textbook-binding-options

读取真实教材候选。

mode=create 在可信owner范围只读数学候选，不创建Agent或绑定；普通模式需agent/subject并校验归属，读取时可协调已有候选与绑定状态。catalog 为真实manifest目录或null；索引失败、可重试性按各字段表达。共同规则链接权威教材章节。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `mode` | query | 否 | `create` create selects the owner-scoped pre-creation preview; omitted uses the existing Tutor scope. |
| `agent` | query | 否 | string Required when mode is omitted; unnecessary for mode=create. |
| `subject` | query | 否 | `math` / `chinese` / `english` / `science` / `information_technology` / `art` Required in existing-scope mode. mode=create fixes the subject to math. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `items` | array<`TextbookBindingOption`> | 是 | 服务端列表投影。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid owner/agent/subject. |
| `404` | Agent scope unavailable. |
| `500` | Records/principal/candidate read failed. |

[实现参考](../../apihttp/weekly_practice_handler.go)


<a id="getcurriculumcatalog"></a>
## GET /api/k12/curriculum-catalog

读取数学真实教材目录。

优先当前活跃教材绑定目录；未绑定时需 textbook_edition/volume 与有效档案，调用目录适配器。目录不可用502；缺失404。不会从文件名造出年级或课时。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |
| `subject` | query | 是 | `math`  |
| `textbook_edition` | query | 否 | string Required only for the fallback catalog source when no active manifest binding exists. |
| `volume` | query | 否 | string Required only for fallback catalog lookup. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 |
| `grade_term` | string | 否 | 孩子档案的年级学期。 |
| `page_max` | integer | 是 |  |
| `page_min` | integer | 是 |  |
| `subject` | string | 是 |  |
| `textbook_binding_id` | string | 是 |  |
| `textbook_edition` | string | 是 | 教材版本；档案响应中为数学兼容字段。 |
| `textbook_manifest_id` | string | 否 |  |
| `textbook_version` | string | 是 |  |
| `title` | string | 是 |  |
| `units` | array<`CurriculumCatalogUnit`> / null | 是 |  |
| `volume` | string | 是 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid scope/subject or missing fallback fields. |
| `404` | Owned profile/catalog not found. |
| `500` | Unclassified scope/store failure. |
| `502` | Authoritative catalog unavailable. |

[实现参考](../../apihttp/weekly_practice_handler.go)


<a id="getcurriculumprogress"></a>
## GET /api/k12/curriculum-progress

读取课程进度或只读建议。

stored模式返回 progress:null或对象，外层revision在null时仍表示生命周期版本。estimate只读，不写入或绑定；带manifest/lesson/page任一参数时构建显式建议，页码须整数。无匹配教材可为null；不是实际已学证据。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `mode` | query | 否 | `estimate` Omitted reads the stored state; estimate previews without adopting it. |
| `agent` | query | 否 | string Required for stored-state mode; optional for pre-creation estimates. |
| `subject` | query | 否 | `math` Stored reads require math; estimate accepts omitted or math. |
| `grade_term` | query | 否 | string Estimate input; saved profile fills empty values when agent is present. |
| `textbook_edition` | query | 否 | string Estimate input; saved profile fills empty values when agent is present. |
| `textbook_manifest_id` | query | 否 | string  |
| `unit_id` | query | 否 | string  |
| `lesson_id` | query | 否 | string  |
| `page_from` | query | 否 | integer  |
| `page_to` | query | 否 | integer  |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `progress` | `CurriculumProgress` / null | 是 |  |
| `revision` | integer | 是 | 当前CAS生命周期版本。 minimum=0 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid subject, term, page integers or selected catalog. |
| `404` | Agent/profile scope not found. |
| `500` | Unclassified principal/state failure. |
| `502` | Catalog dependency unavailable. |

[实现参考](../../apihttp/weekly_practice_handler.go)


<a id="updateprofilebundle"></a>
## PUT /api/k12/profile-bundle

原子更新完整档案、进度和周练设置。

严格解码。完整六学科教材、孩子称呼、空或小学12档grade_term、有效时区与1..5分钟必须满足；始终发送刚读到的三个expected_*_revision。可选agent_config为完整配置，provider/model须同时为空或同时非空。curriculum_progress省略/null/对象具有不同语义。事务失败不留部分绑定或档案；同键同命令replayed=true。十条教材规则及完整curl示例以权威API章节为准。

依赖：K12 module, record store and the dependencies described for this operation.

JSON 正文（严格解码）：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 |
| `agent_config` | object / null | 否 |  |
| `curriculum_progress` | `ProgressSelection` / null | 否 | Omitted: automatic resolution preserving parent-confirmed progress. Explicit null: clear this time. Object: explicit shared selection. These states are not interchangeable. |
| `expected_profile_revision` | integer | 否 | minimum=0 |
| `expected_progress_revision` | integer | 否 | minimum=0 |
| `expected_settings_revision` | integer | 否 | minimum=0 |
| `idempotency_key` | string | 是 | minLength=1 |
| `profile` | object | 是 |  |
| `profile.child_name` | string | 是 | 孩子称呼。 minLength=1 |
| `profile.grade_term` | `` / `一年级上` / `一年级下` / `二年级上` / `二年级下` / `三年级上` / `三年级下` / `四年级上` / `四年级下` / `五年级上` / `五年级下` / `六年级上` / `六年级下` | 否 | Empty is accepted; nonempty terms are restricted to primary school. |
| `profile.subject_textbooks` | `SubjectTextbooksInput` | 是 |  |
| `weekly_practice_settings` | object | 是 |  |
| `weekly_practice_settings.arithmetic_minutes` | integer | 是 | minimum=1, maximum=5 |
| `weekly_practice_settings.arithmetic_warmup_enabled` | boolean | 否 |  |
| `weekly_practice_settings.textbook_consolidation_enabled` | boolean | 否 |  |
| `weekly_practice_settings.textbook_consolidation_tier` | `less` / `standard` / `more` | 否 | default=standard |
| `weekly_practice_settings.timezone` | string | 是 | Valid IANA timezone, for example Asia/Shanghai. minLength=1 |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `agent_config` | `ProfileBundleAgentConfig` / null | 否 |  |
| `curriculum_progress` | `CurriculumProgress` / null | 是 |  |
| `profile` | `ProfileBundleProfile` | 是 |  |
| `replayed` | boolean | 是 | 是否命中同一持久命令。 |
| `weekly_practice_settings` | `WeeklyPracticeSettings` | 是 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Invalid/unknown JSON, incomplete profile/settings or selected progress. |
| `404` | Owned Agent/profile/catalog not found. |
| `409` | Revision or idempotency conflict. |
| `500` | Transaction/publication failure. |
| `502` | Curriculum source unavailable. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/weekly_practice_handler.go)


<a id="coldstart"></a>
## POST /api/k12/cold-start

预览或确认冷启动档案。

confirm缺省false仅推断不落库；显式true才采用建议且不覆盖已有有效档案。fallback_grade可空或小学12档；推断到初中视为无可用推断，显式初中fallback返回400。教材未提供保持空，不默认任何版本。

依赖：K12 optional curriculum constraints; profile writer required only with confirm=true.

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 |
| `child_name` | string | 否 | 孩子称呼。 |
| `confirm` | boolean | 否 | default=False |
| `fallback_grade` | `` / `一年级上` / `一年级下` / `二年级上` / `二年级下` / `三年级上` / `三年级下` / `四年级上` / `四年级下` / `五年级上` / `五年级下` / `六年级上` / `六年级下` | 否 |  |
| `knowledge_points` | array<string> / null | 否 | 相关知识点名称。 |
| `textbook_edition` | string | 否 | 教材版本；档案响应中为数学兼容字段。 |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `child_name` | string | 是 | 孩子称呼。 |
| `created` | boolean | 是 | 是否实际新建；重放或去重可能false。 |
| `grade_term` | string | 是 | 孩子档案的年级学期。 |
| `inferred` | boolean | 是 |  |
| `textbook_edition` | string | 是 | 教材版本；档案响应中为数学兼容字段。 |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required or invalid explicit fallback. |
| `500` | Profile persistence/execution failed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/handler.go)


<a id="tutorturn"></a>
## POST /api/k12/tutor-turn

生成家长讲题指令。

建议提供agent以选择孩子范围；当前handler允许省略，此时不会补入该孩子档案年级或推进其错题状态。problem可空，此时仅讲题指令/情绪回应。当前stage固定3，情绪提示补充安抚建议但不阻断解题。有题且Solver可用时自动求解并验算，取得solution才展示badge，也可将同题new错题推进explained；省略grade由持久档案补齐。prior_stage是历史兼容输入，当前不强制分步确认。

依赖：K12 tutor/model adapters.

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 否 | 需要读取档案年级或推进同题错题状态时应提供真实辅导实例名；当前handler允许省略。 |
| `grade` | string | 否 |  |
| `parent_message` | string | 否 |  |
| `prior_stage` | integer | 否 | Historical compatibility input; not a required user confirmation step. |
| `problem` | string | 否 | 题目正文。 |
| `student_answer` | string | 否 | 孩子的作答；不是标准答案。 |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `badge` | string | 否 | 验算徽章机器值，如verified-strong。 |
| `comfort` | boolean | 是 |  |
| `emotion_cue` | string | 否 |  |
| `escalated` | boolean | 是 |  |
| `prompt_hint` | string | 是 |  |
| `solution` | string | 否 | 完整解法正文。 |
| `stage` | integer | 是 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Malformed JSON. |
| `500` | Tutoring or model execution failed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/handler.go)


<a id="cronmistakesheet"></a>
## GET /api/k12/cron/mistake-sheet

周五错题卷正文。

无到期错题返回空正文；仅生成投递内容，不直接发IM。 所有成功均为 text/plain UTF-8 的200，空串表示跳过；由外层Cron/Deliverer负责实际投递。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` UTF-8 `text/plain`.

| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required. |
| `500` | Content generation/read failed. |

[实现参考](../../apihttp/handler.go)


<a id="cronfillbasket"></a>
## POST /api/k12/cron/fill-basket

兼容旧脚本的无操作入口。

保留兼容入口，实际永远 added=0/skipped=0，不自动加入练习集。当前加入练习集由家长逐题显式触发。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `added` | `0` | 是 |  |
| `skipped` | `0` | 是 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required. |

[实现参考](../../apihttp/handler.go)


<a id="crondailyreminder"></a>
## GET /api/k12/cron/daily-reminder

每日复习提醒正文。

无待复习返回空正文；此接口仍可用，但不是当前四个默认任务之一。 所有成功均为 text/plain UTF-8 的200，空串表示跳过；由外层Cron/Deliverer负责实际投递。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` UTF-8 `text/plain`.

| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required. |
| `500` | Content generation/read failed. |

[实现参考](../../apihttp/handler.go)


<a id="cronreturnreminder"></a>
## GET /api/k12/cron/return-reminder

回传提醒正文。

查昨日固化且未回传的卷，每卷最多一次；读取可记录提醒事实，不能当纯无副作用轮询。无待提醒返回空正文。 所有成功均为 text/plain UTF-8 的200，空串表示跳过；由外层Cron/Deliverer负责实际投递。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` UTF-8 `text/plain`.

| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required. |
| `500` | Content generation/read failed. |

[实现参考](../../apihttp/handler.go)


<a id="cronmonthlyreport"></a>
## GET /api/k12/cron/monthly-report

月度学情报告正文。

无记录返回空正文；接口保留但未列入当前四个默认任务。 所有成功均为 text/plain UTF-8 的200，空串表示跳过；由外层Cron/Deliverer负责实际投递。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` UTF-8 `text/plain`.

| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required. |
| `500` | Content generation/read failed. |

[实现参考](../../apihttp/handler.go)


<a id="cronsemestercheck"></a>
## GET /api/k12/cron/semester-check

学期确认提醒正文。

未建档、无年级或到末档返回空正文。只给确认提醒，不自动升学期。 所有成功均为 text/plain UTF-8 的200，空串表示跳过；由外层Cron/Deliverer负责实际投递。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` UTF-8 `text/plain`.

| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required. |
| `500` | Content generation/read failed. |

[实现参考](../../apihttp/handler.go)


<a id="cronyeararchive"></a>
## GET /api/k12/cron/year-archive

学年归档建议正文。

无记录返回空正文；只建议、不执行归档。接口保留但非当前默认任务。 所有成功均为 text/plain UTF-8 的200，空串表示跳过；由外层Cron/Deliverer负责实际投递。

依赖：K12 module, record store and the dependencies described for this operation.

| 参数 | 位置 | 必填 | 类型 / 约束 |
| --- | --- | --- | --- |
| `agent` | query | 是 | string Tutor Agent name; selects the learner scope, not the authenticated owner. |

成功：`200` UTF-8 `text/plain`.

| 状态 | 错误含义 |
| --- | --- |
| `400` | agent required. |
| `500` | Content generation/read failed. |

[实现参考](../../apihttp/handler.go)


<a id="cronprovision"></a>
## POST /api/k12/cron/provision

覆盖注册四个默认自动化任务。

显式切换入口，会覆盖默认任务并回收该Agent稳定来源键的历史超集；不会按展示名删除自建任务。默认四项：周五19:00错题卷、每天20:00回传提醒、3/1及9/1 09:00学期确认。持久任务与active map按同一事务提交；200仅注册成功，不是内容生成或投递成功。

依赖：K12 CronRegistrar and authorized Agent owner scope.

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 |
| `base_url` | string | 否 | Used only when the composed Runtime.BaseURL is empty; required in that case. |
| `chat_id` | string | 否 |  |
| `deliver` | array<string> / null | 否 |  |
| `platform` | string | 否 |  |
| `user_id` | string | 否 | Optional compatibility claim: if nonempty, it must equal the service-owned principal; it cannot assign quota ownership. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `provisioned` | array<`provisionedJob`> | 是 |  |
| `reclaimed` | array<`ReclaimedCronJob`> | 是 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent/base_url required or claimed user_id differs from owner. |
| `404` | Agent scope not found. |
| `500` | Atomic provision/reclaim or registrar result failed. |
| `501` | Cron registrar not composed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/handler.go)


<a id="cronreconciledefaults"></a>
## POST /api/k12/cron/reconcile-defaults

只补缺少的默认任务。

档案生命周期修复入口，仅按精确source key补缺项，保留已暂停、改时区、改目标或改脚本任务；符合精确规范的旧无来源键任务只读视为存在。部分失败可保留之前成功创建的任务，重试只补剩余项；created省略表示false。

依赖：K12 CronRegistrar and authorized Agent owner scope.

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 |
| `base_url` | string | 否 | Used only when the composed Runtime.BaseURL is empty; required in that case. |
| `chat_id` | string | 否 |  |
| `deliver` | array<string> / null | 否 |  |
| `platform` | string | 否 |  |
| `user_id` | string | 否 | Optional compatibility claim: if nonempty, it must equal the service-owned principal; it cannot assign quota ownership. |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `provisioned` | array<`provisionedJob`> | 是 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | agent/base_url required or claimed user_id differs from owner. |
| `404` | Agent scope not found. |
| `500` | A missing default could not be created; previous successful creations remain. |
| `501` | Cron registrar not composed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/handler.go)


<a id="bindim"></a>
## POST /api/k12/bind-im

绑定辅导实例到一对一私聊。

agent/platform/chat_id必填，instance_id可空。conversation_type省略/空/1/direct表示私聊；群或其他值400。绑定改变随后该私聊入站路由；一个物理私聊不能同时接收两个孩子的助手，冲突409。成功不发送测试消息，不证明之后作业任务或回传已经完成。

依赖：K12 IMBinder and configured platform instance.

JSON 正文：

| 字段 | 类型 | 必填 | 约束 |
| --- | --- | --- | --- |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 |
| `chat_id` | string | 是 |  |
| `conversation_type` | `` / `1` / `direct` | 否 | default=direct |
| `instance_id` | string | 否 |  |
| `platform` | string | 是 |  |

成功：`200` `application/json`.

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `bound` | `True` | 是 |  |
| `agent` | string | 是 | 辅导实例名；不改变可信owner。 |
| `platform` | string | 是 |  |
| `chat_id` | string | 是 |  |


| 状态 | 错误含义 |
| --- | --- |
| `400` | Missing fields or non-direct conversation. |
| `409` | Binding belongs to a different Tutor. |
| `500` | Binding failed. |
| `501` | IM binder not composed. |
| `413` | 请求正文超过该接口上限。 |

[实现参考](../../apihttp/handler.go)

## 常用嵌套字段

完整版本化备份结构由OpenAPI定义；备份须保存完整响应，不能用字段摘录重建。以下展开普通调用所需的嵌套字段。

### TutoringTipsSection

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `title` | string | 是 | 小节标题。 |
| `content` | string | 是 | 小节Markdown正文。 |
| `source_label` | string | 是 | 服务端提供的来源标签。 |

成功的tutoring-tips响应包含同一份已确认作业事实生成的三个小节。

### accumulationDictationGenerationDTO

| 字段 | 类型 | 必含 | 含义 |
| --- | --- | --- | --- |
| `generation_id` | string | 是 | 持久出题任务身份。 |
| `status` | string | 是 | queued / generating / validating / committed / failed / re_add。 |
| `practice_item_id` | string | 否 | 只有成功入集后才出现。 |
| `failure_reason` | string | 否 | 非空时返回失败说明。 |
| `attempt` | integer | 否 | 尝试次数；为0时省略。 |
| `updated_at` | integer | 否 | Unix秒；为0时省略。 |

### 投递状态值

- `DeliveryReceiptStatus`：pending / sending / delivered / failed / outcome_unknown。
- `DeliveryBatchStatus`：pending / sending / delivered / failed / partial_failed / outcome_unknown。

### SubjectTextbooksInput

| 字段 | 类型 | 必填 / 必含 | 含义 |
| --- | --- | --- | --- |
| `math` | string | 是 | minLength=1 |
| `chinese` | string | 是 | minLength=1 |
| `english` | string | 是 | minLength=1 |
| `science` | string | 是 | minLength=1 |
| `information_technology` | string | 是 | minLength=1 |
| `art` | string | 是 | minLength=1 |

### AgentConfigInput

| 字段 | 类型 | 必填 / 必含 | 含义 |
| --- | --- | --- | --- |
| `display_name` | string | 是 | trim后须非空白。 |
| `description` | string | 是 | trim后须非空白；agent_config完整配置的一部分。 |
| `model` | string | 否 | provider and model must either both be empty or both be nonempty. |
| `provider` | string | 否 | provider and model must either both be empty or both be nonempty. |
| `skills` | array<string> / null | 否 | The service trims/deduplicates entries and adds the required K12 skills; omission does not remove mandatory skills. |
| `system_prompt` | string | 是 | trim后须非空白。 |

### ProgressSelection

| 字段 | 类型 | 必填 / 必含 | 含义 |
| --- | --- | --- | --- |
| `subject` | `math` | 是 |  |
| `textbook_manifest_id` | string | 是 | minLength=1 |
| `volume` | string | 否 |  |
| `unit_id` | string | 否 |  |
| `lesson_id` | string | 否 |  |
| `page_from` | integer / null | 否 |  |
| `page_to` | integer / null | 否 |  |
| `evidence_source` | `parent_confirmed` / `ai_estimated` | 是 |  |

### DeliveryTarget

| 字段 | 类型 | 必填 / 必含 | 含义 |
| --- | --- | --- | --- |
| `chat_id` | string | 是 |  |
| `instance_id` | string | 否 |  |
| `label` | string | 否 |  |
| `platform` | string | 是 |  |

### DeliveryReceipt

| 字段 | 类型 | 必填 / 必含 | 含义 |
| --- | --- | --- | --- |
| `agent_name` | string | 是 | 响应资源所属辅导实例。 |
| `attempt` | integer | 是 | 服务端记录的尝试次数。 |
| `batch_id` | string | 否 | 持久投递批次ID。 |
| `batch_ordinal` | integer | 否 |  |
| `binding_id` | string | 是 |  |
| `created_at` | integer | 是 | 创建时间，Unix秒。 |
| `dedupe_key` | string | 是 | 服务端持久去重身份。 |
| `delivery_id` | string | 是 | 持久投递回执ID。 |
| `external_message_id` | string | 否 |  |
| `last_error` | string | 否 | 最近错误说明，可省略。 |
| `object_id` | string | 是 |  |
| `object_kind` | string | 是 |  |
| `part_digest` | string | 是 |  |
| `part_kind` | `PartKind` | 是 |  |
| `part_mime` | string | 否 |  |
| `part_ordinal` | integer | 是 |  |
| `payload_digest` | string | 是 |  |
| `payload_json` | string | 是 |  |
| `render_manifest_json` | string | 是 |  |
| `status` | `DeliveryReceiptStatus` | 是 | 当前服务端持久状态；按所属对象枚举解释。 |
| `target` | `DeliveryTarget` | 是 |  |
| `updated_at` | integer | 是 | 最后更新时间，Unix秒。 |

### CatalogUnit

| 字段 | 类型 | 必填 / 必含 | 含义 |
| --- | --- | --- | --- |
| `lessons` | array<`CurriculumCatalogLesson`> / null | 是 |  |
| `page_from` | integer | 是 |  |
| `page_to` | integer | 是 |  |
| `title` | string | 是 |  |
| `unit_id` | string | 是 |  |

### CatalogLesson

| 字段 | 类型 | 必填 / 必含 | 含义 |
| --- | --- | --- | --- |
| `lesson_id` | string | 是 |  |
| `page_from` | integer | 是 |  |
| `page_to` | integer | 是 |  |
| `title` | string | 是 |  |

### ManifestCatalog

| 字段 | 类型 | 必填 / 必含 | 含义 |
| --- | --- | --- | --- |
| `grade_term` | string | 否 | 孩子档案的年级学期。 |
| `page_max` | integer | 是 |  |
| `page_min` | integer | 是 |  |
| `page_refs` | array<`textbookCatalogPageRef`> / null | 是 |  |
| `subject` | string | 是 |  |
| `textbook_edition` | string | 是 | 教材版本；档案响应中为数学兼容字段。 |
| `textbook_version` | string | 是 |  |
| `title` | string | 是 |  |
| `units` | array<`textbookCatalogUnit`> / null | 是 |  |
| `volume` | string | 是 |  |

### CatalogPageReference

| 字段 | 类型 | 必填 / 必含 | 含义 |
| --- | --- | --- | --- |
| `logical_page` | integer | 是 |  |
| `pdf_page` | integer | 是 |  |
| `segment_refs` | array<string> / null | 是 |  |

### EstimateBasis

| 字段 | 类型 | 必填 / 必含 | 含义 |
| --- | --- | --- | --- |
| `as_of_date` | string | 是 |  |
| `evidence_receipt_hash` | string | 否 |  |
| `reference_term_end` | string | 是 |  |
| `reference_term_start` | string | 是 |  |
| `weight_method` | string | 是 |  |

### ArchiveCounts

| 字段 | 类型 | 必填 / 必含 | 含义 |
| --- | --- | --- | --- |
| `accumulation` | integer | 是 |  |
| `creative_works` | integer | 是 |  |
| `mistakes` | integer | 是 |  |
| `practice_sets` | integer | 是 |  |
| `weekly_review` | integer | 是 |  |

### ArchiveAttachment

| 字段 | 类型 | 必填 / 必含 | 含义 |
| --- | --- | --- | --- |
| `byte_size` | integer | 是 |  |
| `data_base64` | string | 是 |  |
| `media_type` | string | 是 |  |
| `relative_path` | string | 是 |  |
| `sha256` | string | 是 |  |

<a id="print-replies"></a>
## 原回合 PDF 回复与恢复

普通会话的冻结 PDF 由原助手消息返回；钉钉的 `PrintArtifact` 经工具元数据 `print_artifacts` 进入同一原助手事务及 Outbox，再沿原 K12 DeliveryBatch 投递。它不是 `MediaOperation`，也不是另一次“发送到手机”。下列 API 只消费已经持久提交的原回合来源；`message_id` 是原 **assistant_message_id**，不是 ArtifactID 或平台入站消息ID。

| 方法 / 路径 | 输入 | 响应与副作用 |
|---|---|---|
| `GET /api/k12/print-replies/{message_id}` | 必需 query `agent` | `{source,batch?,state}`。只读；source 固定原 owner/session/request、Agent、物理实例/Chat及原PDF引用；存在batch时读取其实际状态，不触发发送 |
| `POST /api/k12/print-replies/{message_id}/resume` | 必需 query `agent`；无请求体字段 | `{batch,model_invocations_added:0}`；恢复原冻结批次，不再解题/渲染、不重发成功part。accepted/unknown沿原回执查询处理，HTTP200不等于delivered |

两接口沿现有认证与Agent归属。缺agent/message_id返回400，来源不存在或不属于当前scope返回404；持久来源读取失败返回500。GET的进一步领域/存储错误沿原K12错误映射；POST准备/发送/查询失败返回502 `{batch,error,model_invocations_added:0}`，保留实际批次状态。失败或结果未知不能通过新消息ID伪装为恢复。

`source.artifacts[]` 固定 `kind=print_artifact`、`agent_name/artifact_id/source_digest/byte_digest/byte_size/name/url`。URL是认证读取地址；恢复以真实持久身份和字节摘要为准，不按名称或当前模板重生成。首次批次准备前核原助手摘要；已存在批次恢复使用原冻结发送计划。平台接纳、最终送达、手机观察是不同事实。

同轮同时生成 PDF 与图片、视频或语音时，由原 PDF 回复批次统一冻结正文及全部附件；媒体操作、资产、执行回执和原助手关联继续保存，不另建媒体发送批次。恢复按原 owner 校验媒体资产与字节摘要，PDF 不转换为 MediaOperation。每次实际发送前仍检查媒体的主动停止意图；明确重发的原操作按既有重发语义处理。尚未发送的计划停止后，GET 的 `state` 可为 `cancelled`；已发送或结果未知的回执保持原事实，停止不表示撤回或已送达。主动停止与请求断线、服务退出分别处理，后两者不会被当成业务取消而结束 Outbox。

非钉钉原会话没有本 PDF Outbox 时，沿原内联回复返回，不静默接管。可信附件从 Source 读取并核验冻结字节，稳定下载 URL 不会被当作外部下载来源；各渠道原有附件发送能力保持，不据此宣称新增渠道 PDF 支持。

## 可复制示例

配置当前服务的HEXCLAW_URL与HEXCLAW_TOKEN，Agent及资源ID替换为同一服务真实读到的值。响应示例只展示结构，不冻结模型文字。

```bash
export HEXCLAW_URL=http://127.0.0.1:16060
export HEXCLAW_TOKEN="your-service-token"
curl -fsS "$HEXCLAW_URL/api/k12/solve" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" -H "Content-Type: application/json" \
  -d '{"agent":"TutorAgent","subject":"数学","problem":"2 + 3 = ?"}'
```

```json
{
  "solution": "2 + 3 = 5",
  "verdict": "agree",
  "evidence_type": "numeric_exec",
  "badge": "verified-strong",
  "out_of_scope": false,
  "curriculum_unmapped": []
}
```

```bash
curl -fsS "$HEXCLAW_URL/api/k12/accumulation?agent=TutorAgent" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" -H "Content-Type: application/json" \
  -H "Idempotency-Key: collect-poem-1" \
  -d '{"content":"床前明月光，疑是地上霜。"}'
```

```json
{"record_id":"accum-example","created":true}
```

```bash
curl -fsS "$HEXCLAW_URL/api/k12/backup?agent=TutorAgent" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" -o learner.hexbak
curl -fsS "$HEXCLAW_URL/api/k12/restore" \
  -H "Authorization: Bearer $HEXCLAW_TOKEN" -H "Content-Type: application/json" \
  --data-binary @learner.hexbak
```

恢复会修改记录，仅在确有恢复意图时执行，并保存非null的恢复前snapshot。档案原子更新的完整示例见上方权威profile-bundle章节。
