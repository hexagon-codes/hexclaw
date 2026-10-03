# 贡献指南 — hexclaw

hexclaw 是 Hexagon 生态的 **L3 应用**（IM 适配器 / 网关 / Skill / 任务 / 知识库 / API / 桌面端）。

## 分层与复用
- 可依赖 toolkit / ai-core / hexagon；**通用能力一律复用下层，禁止重造**（HTTP/重试/缓存/ID/哈希/HMAC/SSE 等）。
- 应用领域特有（K12 引擎、cron 业务、平台适配、渠道路由）留 hexclaw；通用能力发现后应下沉。

## 本地开发
```bash
GOWORK=off go test ./... -run '^$'
go build ./... && go vet ./...
go test -race ./...
golangci-lint run
```
跨仓联调用根目录 go.work（use 四仓）。

### CI/CD 门禁说明

本次版本为 `v0.5.0-beta`。分支验证、云端部署和 Tag 发布是三个不同入口：

| 工作流 | 触发条件 | 检查或产物 |
| --- | --- | --- |
| CI：功能分支 | push 到 `feat/**`，纯说明文档改动除外 | `go test -run '^$' ./...`，只编译应用和测试包，不执行用例 |
| CI：主分支 / PR | push 到 main，或 PR 的目标为 main，纯说明文档改动除外 | Linux 全量 `go test -race -count=1 -timeout 20m ./...`（包含 K12 确定性测试）；Windows 全仓构建；必要的跨平台 sandbox / CodeExec；配置真实模型密钥时执行 K12 真实模型门 |
| K12 Eval Gate | 手动触发 | 定向重跑 K12 harness；真实模型评测仅在配置相应密钥时执行 |
| render | main push 或 PR 匹配渲染路径 | Linux / macOS / Windows 构建及 render、API Render 测试；不再每周定时执行 |
| Sandbox CodeExec | 手动触发 | 三平台 toolkit sandbox 与 HexClaw code_exec 专项验证；公网爬虫仅在手动输入 `run_live_network=true` 时运行（该输入默认 true） |
| Deploy | 已启用部署，且指定分支的 push CI 成功 | 构建已验证提交的镜像，以 digest 更新云端并核对部署结果 |
| Release | `v*` Tag push，或手动指定已有 Tag | 五组平台二进制、checksum、GitHub Release 和 Linux amd64 镜像 |

功能分支的编译成功可以触发现有自动部署，但不代表 main / PR 全量测试通过。Deploy 或 render 独立成功不能代替主 CI。Release 只依赖自身构建，不等待主 CI 或专项工作流；发布产物与云端部署结果应分别核对。

CI 的 push / PR 仅修改 `README*.md`、`CHANGELOG.md`、`CONTRIBUTING.md`、`SECURITY*.md`、`docs/**/*.md` 或 `LICENSE` 时跳过，不触发该提交的 CI 构建及后续自动部署。这是明确的纯说明文档范围，不使用 `**/*.md` 广义忽略：其他目录中的 Markdown 可能参与 `go:embed`，影响运行产物。

CI 与 K12 固定使用 `GOWORK=off`、`GOFLAGS=-mod=readonly`，验证已发布依赖而不改写 `go.mod` / `go.sum`。Linux 当前全量命令设置单包测试超时 20 分钟，job 总预算为 40 分钟。

普通 main / PR 提交以一个主 CI 执行全量测试和 race，以及必要的跨平台 sandbox / CodeExec；K12 确定性测试已包含在全量中，不再重复运行独立 subset。配置 `HEXCLAW_LLM_EVAL_KEY` 时，主 CI 执行 K12 真实模型门；未配置时不计为真实模型已验证。K12 / Sandbox 专项改为手动，上游 toolkit 自身测试不在普通提交重复执行；render 仍按自身路径规则独立运行。已移除覆盖率文件上传和 Windows 非阻塞的核心重复测试，覆盖率仍可按需用 `make test-cover` 在本地生成。

主 CI 与手动 Sandbox CodeExec 的 Linux 环境复用固定 toolkit 版本提供的 bubblewrap 安装脚本，安装并检查运行所需参数，避免发行版旧包与当前后端不兼容。

普通业务测试通过 `internal/testutil/sqlitefixture` 复用进程内一次真实全量迁移的空库模板；每个用例仍使用独立数据库，原 Init、断言、清理和 race 保留。文件库与内存库保留原连接语义，后端身份独立；迁移专项、旧 schema 和重开测试仍按原流程执行。这减少重复迁移耗时，不修改生产迁移或减少测试。

Windows 的平台 sandbox 硬门禁继续执行。Go 构建缓存组仅两项真实初始化 / 预算用例沿用平台能力跳过，缓存策略与清理断言仍跨平台执行。当前 toolkit 不支持 Go helper 所需的只读工具链路径映射，其他真实 code_exec 运行集成仍沿用既有能力门控，Linux / macOS 的真实执行保留。Windows 跳过项不计为 Go 执行功能已验证，不能通过取消只读路径或改用宿主执行冒充支持。

三项大型真实 PDF 回归仅在显式设置 `HEXCLAW_REAL_PDF_FIXTURE` 时使用外部冻结样本，不从其他仓库自动读取。未设置或文件不存在时明确跳过，不能计为真实 PDF 边界已验证；文件存在时仍检查固定大小、SHA 和全部功能断言，其他读取失败或内容不符继续失败。默认构建、CI 与应用运行不依赖该外部素材。

- GitHub Actions 的 Linux 硬门禁等价于 `GOWORK=off GOFLAGS=-mod=readonly go test -race -count=1 -timeout 20m ./...`。
- 发布/CI 兼容性必须用 `GOWORK=off` 复验，避免本地 `go.work` 把未发布的 `toolkit` / `ai-core` / `hexagon` API 变化遮住。
- 故意失败的 runner 完整性探针不得进入默认 `go test ./...` 路径；这类测试必须默认 `t.Skip`，或只在显式环境变量/手工 workflow 下启用。

主 CI 没有手动触发入口；K12 Eval Gate 与 Sandbox CodeExec 提供手动入口，公网爬虫不进入普通自动 CI。手动 Sandbox 的 `run_live_network` 输入默认 true，可设为 false 不运行公网爬虫。render 的 main push 路径包含 `.github/workflows/render.yml` 自身，PR 路径不包含该文件；仅推送 CI / render 工作流文件到功能分支，不能验证完整平台矩阵或 render 三平台测试。工作流配置和绿色编译结果不等于功能缺陷已修复。

云端配置、部署结果及恢复边界见[云端部署与日常运维](docs/cloud-deployment.md)。`v0.5.0-beta` 属于预发布；版本记录不表示已创建 Tag 或完成 Release。

## 提交规范
- Conventional Commits；注释中文、只写功能描述，禁暴露内部开发文档/客户名/金额。
- 涉及 DB/缓存/幂等/配额/状态流转的改动，按 bug 修复闭环走 RED→GREEN + 真实环境 E2E。

## PR Checklist
- [ ] build+vet+test -race 全绿、golangci-lint 0 issue
- [ ] 复用下层而非重造；新发现的通用能力评估下沉
- [ ] CHANGELOG.md 记录用户可见变更
