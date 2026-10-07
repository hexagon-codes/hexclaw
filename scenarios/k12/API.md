# K12 公共 API

[English](API.en.md) · [核心 API](../../docs/api.md) · [返回 README](../../README.md)

场景端点使用 `/api/k12/*`；Agent 创建仍使用平台 `POST /api/v1/agents`。业务请求携带当前服务的 Bearer 令牌，身份来自鉴权上下文；`agent` 使用实例 `name`，作用域与记录归属的具体校验按各操作说明，不能把该字段当成身份令牌。

本文描述当前源码契约，已发布版本使用对应版本的文档。普通 JSON 错误为 `{"error":"..."}` 和 HTTP 状态码；导出、资产和结果接口还可能返回二进制，不能把所有响应当成 JSON。本文示例中的 ID、版本号和业务内容需替换为实际响应值。

## 完整接口参考

| 模块 | 内容 |
| --- | --- |
| [图片任务、创作与材料](docs/api/tasks.md) | 图片 facade、结果与恢复、逐题来源与反馈、作品和资产、材料准备 |
| [练习、周练与打印](docs/api/practice.md) | 错题练习生成、候选选择、练习集、作答与批改、周练、原生打印回执 |
| [档案、教材与学习记录](docs/api/records.md) | 展示描述、单题处理、档案与课程进度、积累与学情、导出备份、辅导与 IM 自动化 |

三份模块参考覆盖当前 K12 显式挂载路由；[OpenAPI](../../api/openapi.yaml)提供对应请求和响应结构。下方说明兼容范围及档案、教材进度和图片终态的共享规则，原生或外部客户端按对应操作的字段、版本与副作用说明调用。

- [档案整体更新](#profile-bundle)
- [教材与课程进度](#textbook-and-curriculum-progress)
- [图片任务终态](#image-task-completion)
- [接口范围与兼容](#1-端点清单当前契约摘录)

---

<a id="1-端点清单当前契约摘录"></a>
## 接口范围与兼容

完整字段、响应、错误与示例按上方三份模块参考维护。`POST /api/v1/agents` 属于平台接口，K12 创建的 metadata 与课程进度选择也使用下方共享规则。

- 图片入口为公开 `image-tasks` facade；内部识题、GradingJob 与 OCR 对象不是客户端任务接口。
- `POST /grade`、`POST /solve` 和显式学习记录操作仍按[档案与学习记录](docs/api/records.md)中的合同保留，不把单题校准等操作误标为已删除。
- `PUT /profile` 保留已注册的兼容拒绝入口，返回 `405` 与 `Allow: GET`；更新使用 `PUT /profile-bundle`。
- 备份、导出、学习积累和辅导请求使用当前模块字段，不从旧示意字段推断调用方式。备份版本、JSON 降级和二进制产物在模块中明确区分。
- 已绑定 IM 的结果交付与平台通用 Cron 接收配置分别说明；目标通道的连通性检查不代表任务已完成或结果已送达。

<a id="image-task-completion"></a>
## 图片任务终态

`POST /api/k12/image-tasks/{id}/retry` 的可选固定 `intent: "known_local_technical"` 仅恢复原 job 当前代次有确定本地技术失败、无未知调用且无终稿的 `failed_terminal/assessing` 任务；账户由服务认证派生，父子窗口双版本 CAS 提交后才启动。阶段计数须已达到正常上限 3，完整冻结模型、输入、历史及当前计数保持，新调用进入当前计数加一的代次，例如 3→4、4→5；不传 intent 时普通重试与 max3 保持。重复版本返回 `409`，未知结果只查询。精确资格、拒绝条件与请求字段见[图片任务重试契约](docs/api/tasks.md#op-post-api-k12-image-tasks-id-retry)。

- Desktop 和已绑定钉钉使用同一领域任务、判定、持久结果与自动推进语义。渠道差异只用于消息传输和展示，不产生不同批改标准或确认流程。当前 K12 专门渠道投影声明为钉钉，不能把其他通用适配器视为同等 K12 验收。
- 默认链路为发图 → 自动识别/判定 → 实际结果。作业批改以带批注的原图为主要交付；可识别部分照常完成。
- 经自动识别仍无法可靠辨认的原图内容在结果中标注“无法识别”，作为任务终态的一部分。该部分不猜题、不编答案、不判错，不写入掌握或错题结论；不强制确认、纠正识别、补拍或跳过。补拍是后续自愿输入。
- Provider 超时、请求结果未知、协议错误和应用故障属于技术失败，不能伪装成“无法识别”；结果未知时不盲重发。失败查看 `failure_kind`、`retryable` 和恢复状态。
- 客户端轮询同一 `dispatch_id`，读取该任务的 `target_projection`、进度及 `/result`。派发状态 `routed` 表示已分流，不代表最终批注图或其他产物已经交付；处理中间态不能代替最终结果。
- 手工录入作品的 `creative_entry` 与显式 `creative.action=commit` 保留其创建/版本提交语义，不作为自动作业批改的默认前置步骤。

<a id="profile-bundle"></a>
## 档案整体更新

`PUT /api/k12/profile-bundle` 将孩子档案、课程进度、周练设置及可选 Agent 配置作为一个事务命令提交，不是字段 PATCH。纯数据更新本身不需要模型。

### 请求与版本

更新前分别读取 `GET /api/k12/profile?agent=...`、`GET /api/k12/curriculum-progress?agent=...&subject=math`、`GET /api/k12/weekly-practice/settings?agent=...` 的 `revision`，放入三个 `expected_*_revision`。未持久化状态可返回 `0`；不要猜测已有数据的版本。

三个版本字段未传时会解码为 `0`，不表示跳过版本校验；正确调用应发送刚读取的实际值。档案的 `grade_term` 可为空，非空时使用当前小学学期枚举，不借用单题解题接口的其他学段枚举。

| 字段 | 契约 |
| --- | --- |
| `agent`、`idempotency_key` | 当前归属的实例 name、该事务命令的非空幂等键 |
| `expected_profile_revision` | 刚读取的档案 revision |
| `expected_progress_revision` | 刚读取的数学进度生命周期 revision，进度为 null 时也读取外层 revision |
| `expected_settings_revision` | 刚读取的周练设置 revision |
| `profile` | 非空 `child_name`、完整 `subject_textbooks`，以及空值或有效小学学期的 `grade_term`；按本次完整档案值提交 |
| `subject_textbooks` | 严格六键 `math/chinese/english/science/information_technology/art`，每项为非空教材版本字符串 |
| `weekly_practice_settings` | 有效 IANA `timezone`、`arithmetic_minutes: 1..5`；两个 enabled 布尔及 `textbook_consolidation_tier: less/standard/more`，tier 省略默认 standard |
| `curriculum_progress` | 省略=自动处理；null=本次清除；非空对象遵守[教材与课程进度](#textbook-and-curriculum-progress) |
| `agent_config` | 可选；提供时必须完整非空 display_name/description/system_prompt，provider/model 同时空或同时非空，skills 由服务补齐必要场景技能 |

请求采用严格字段解码，未知字段会被拒绝。下面示例显式清除进度；三个零版本仅为示意，调用时替换为读取值：

```json
{
  "agent": "mingming",
  "idempotency_key": "profile-update-1",
  "expected_profile_revision": 0,
  "expected_progress_revision": 0,
  "expected_settings_revision": 0,
  "profile": {
    "child_name": "明明",
    "grade_term": "五年级下",
    "subject_textbooks": {
      "math": "人教版",
      "chinese": "统编版",
      "english": "外研版",
      "science": "教科版",
      "information_technology": "浙教版",
      "art": "人美版"
    }
  },
  "curriculum_progress": null,
  "weekly_practice_settings": {
    "timezone": "Asia/Shanghai",
    "textbook_consolidation_enabled": false,
    "textbook_consolidation_tier": "standard",
    "arithmetic_warmup_enabled": false,
    "arithmetic_minutes": 2
  }
}
```

将请求保存为本地 `profile-bundle.json` 后，使用当前服务地址和令牌：

```bash
curl --fail-with-body "$HEXCLAW_API_BASE/api/k12/profile-bundle" \
  -X PUT \
  -H "Authorization: Bearer $HEXCLAW_API_TOKEN" \
  -H "Content-Type: application/json" \
  --data-binary @profile-bundle.json
```

### 响应与失败

成功为 `200`，响应为 `{"profile":{...},"curriculum_progress":null或对象,"weekly_practice_settings":{...},"replayed":false}`，提交可选 Agent 配置时另含 `agent_config`。重放命令通过 `replayed` 表达；修改命令内容时使用新键。profile 的 `textbook_edition` 是数学教材兼容投影。

| HTTP | 原因 |
| --- | --- |
| `400` | 非法 JSON、未知字段、缺少完整六学科档案、无效年级/周练设置或不匹配的显式教材进度 |
| `401` | 当前服务令牌无效 |
| `404` | 引用的领域记录不存在，或不可在当前范围读取 |
| `409` | revision 已变更，或同幂等键的命令内容冲突；重读状态并重新形成命令 |
| `502` | 需要的教材目录不可用 |
| `500` | 未分类的存储或应用错误 |

错误体为 `{"error":"..."}`。校验或事务失败不留下部分档案、进度、绑定或设置。

<a id="textbook-and-curriculum-progress"></a>
## 教材与课程进度

本节为创建、编辑和预览课程进度的共同契约。

1. K12 创建使用 `POST /api/v1/agents`。省略 `curriculum_progress` 时可采用唯一匹配的真实教材目录建议；显式 `null` 保持未设进度，无可用目录仍正常创建。
2. Agent、非空进度和绑定在同一 SQLite 事务中提交，失败不留下部分建档。平台响应沿用 `{"message":"Agent 已注册","name":"..."}`。
3. 创建与 `PUT /api/k12/profile-bundle` 共用非空选择：`subject: math`、当前归属的 `textbook_manifest_id`，可选 `lesson_id/page_from/page_to`。`evidence_source` 区分 `parent_confirmed` 和 `ai_estimated`。
4. 人工进度须提供目录内的 `volume/unit_id`。AI 输入允许 unit_id 为空，后端重算并保留显式课时、页码。显式非空 AI 选择无法匹配当前档案的真实教材目录时，创建与编辑均返回 `400`，不保存任何变更。
5. 同 scope 人工进度不被推算覆盖。PUT 省略表示自动处理，显式 null 表示本次清除；旧空进度生命周期不关闭后续建议。
6. `GET /api/k12/curriculum-progress?mode=estimate` 只读预览，返回 `{"progress":null或对象,"revision":...}`。创建前无需 agent；参数为 `grade_term/textbook_edition` 和可选 `textbook_manifest_id/lesson_id/page_from/page_to`。已有实例可带 agent，由持久档案补齐缺省年级与教材。
7. `GET /api/k12/textbook-binding-options?mode=create` 在创建前只读当前可信 owner 的数学教材候选，无需 agent、不创建绑定，返回 `{"items":[...]}`。未指定创建模式时保留已有实例的候选查询与归属校验。
8. 年级来自持久封面证据，不从文件名猜测；多个匹配目录不任取首项。confirmed_at 保持 Unix 数字，AI 建议为 `0`；estimate_basis 保存推算日期、参考窗口、权重方法和可选引用回执哈希。
9. 推算参考窗口为上学期 9 月 1 日至次年 1 月 31 日、下学期 3 月 1 日至 6 月 30 日；按服务端上海日期选择最近对应周期，结合工作日比例、目录课时数与缺省页跨度估计单元。这是建议参考，不是真实学校校历或已学、掌握证据。
10. 只读预览不写入；仅新业务请求通过 CAS 采用建议。已冻结成果不会因新建议重新生成，无教材或进度不阻断真实题目处理。

实现参考：[场景路由](apihttp/handler.go)、[档案与周练处理器](apihttp/weekly_practice_handler.go)、[只读建议](apihttp/curriculum_progress_handler.go)。客户端以本节契约为入口，无需通过源码推断调用方式。
