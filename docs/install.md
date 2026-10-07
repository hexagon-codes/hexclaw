# HexClaw 安装与部署指南

**[English](install.en.md) | 中文**

## 目录

- [系统要求](#系统要求)
- [安装方式](#安装方式)
- [配置](#配置)
- [部署方式](#部署方式)
- [平台接入](#平台接入)
- [运维](#运维)
- [故障排查](#故障排查)

---

## 系统要求

| 项目 | 最低要求 | 推荐 |
|------|---------|------|
| 操作系统 | Linux / macOS / Windows | Linux (Ubuntu 22.04+) |
| Go（仅 Go 安装或源码构建） | >= 1.25.13 | 与所选版本 `go.mod` 兼容的工具链 |
| 内存 | 按任务、文件大小与模型部署方式规划 | 为文档渲染、图片任务及本地模型分别预留资源 |
| 磁盘 | 程序及完整用户数据目录可写 | 为原始文档、产物、索引和备份预留空间 |
| 网络 | 所选在线 Provider 与渠道可达；本地模型按自身部署要求配置 | 低延迟连接 |

HexClaw 核心为单二进制（SQLite 使用纯 Go）；文档导出、中文/数学渲染及题目计算还需要 Pandoc、Typst、字体和 Python/SymPy，云端镜像已包含。

---

## 安装方式

本页介绍独立 HexClaw 服务的安装与部署。桌面应用的一键安装见 [HexClaw Desktop 安装说明](https://github.com/hexagon-codes/hexclaw-desktop#安装)。

`@latest` 和 Releases 的 `latest/download` 选择稳定版。安装预发布版时，使用 [Releases](https://github.com/hexagon-codes/hexclaw/releases) 中已发布的完整 Tag，并选择该版本实际提供的资产。

### 方式一：go install（推荐开发者使用）

```bash
go install github.com/hexagon-codes/hexclaw/cmd/hexclaw@latest
```

需要指定版本时，将 `@latest` 替换为该版本的完整 Tag。确保 `GOBIN`（默认 `$(go env GOPATH)/bin`）位于 `PATH`。

验证安装：

```bash
hexclaw version
# HexClaw <当前构建版本>
```

### 方式二：从源码编译

```bash
git clone https://github.com/hexagon-codes/hexclaw.git
cd hexclaw
go build -o hexclaw ./cmd/hexclaw/

# 可选：带版本信息编译（推荐 release/自测构建都注入 git describe）
VERSION=$(git describe --tags --always --dirty)
go build -ldflags "-X main.version=${VERSION} -X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o hexclaw ./cmd/hexclaw/

# 移动到 PATH
sudo mv hexclaw /usr/local/bin/
```

### 方式三：预编译二进制

从 [GitHub Releases](https://github.com/hexagon-codes/hexclaw/releases) 选择版本，再下载对应系统和 CPU 架构的压缩包。例如 Linux amd64 使用 `hexclaw-linux-amd64.tar.gz`，Apple Silicon 使用 `hexclaw-darwin-arm64.tar.gz`。

解压后，将 `hexclaw`（Windows 为 `hexclaw.exe`）放入 `PATH` 中的目录，再运行 `hexclaw version` 核对版本。资产列表与文件名以所选 Release 为准。

### 方式四：Docker

单机日常部署推荐 Docker Compose。在部署目录私有 `.env` 设置 `HEXCLAW_IMAGE=ghcr.io/hexagon-codes/hexclaw@sha256:<实际摘要>`，摘要须来自已可拉取的构建产物：

```bash
docker compose pull hexclaw
docker compose up -d --no-build hexclaw
```

本地源码开发先执行 `docker compose build hexclaw`，默认镜像为 `hexclaw:dev`。发布镜像使用版本号及完整提交 SHA 标签；预发布版不更新 `latest`，该标签仅用于正式稳定版。更新保留现有项目、数据卷及完整 Compose override 文件集合。

源码镜像当前面向 Linux amd64，持久化完整可写 HOME。默认 Compose **不安装 Ollama，也不下载模型**；在 Desktop 为当前远端配置模型和 Embedding API，知识数据与索引保存在服务器。未配置有效 Embedding 时，关键词检索与向量可用性分别判断。初始化令牌、Kubernetes、备份恢复、自动部署及当前验收边界见[云端部署指南](cloud-deployment.md)。

---

## 配置

### 初始化配置

```bash
hexclaw init
# 生成: ~/.hexclaw/hexclaw.yaml
```

默认配置目录为 `~/.hexclaw/`，包含以下文件：

```
~/.hexclaw/
├── AGENTS.md       # 公共工作规则，缺失时初始化，已有内容不覆盖
├── auth.json       # Desktop 原生层管理的本机/远端连接凭据
├── hexclaw.yaml     # 主配置文件
├── data.db          # SQLite 数据库（自动创建）
├── master.key       # 静态凭据加密主密钥（自动创建，权限 0600）
├── logs/            # 本地日志 / 运行诊断（按部署方式可选）
├── workspace/       # code_exec / 文件工具的默认工作目录
├── memory/          # 文件记忆目录
│   ├── MEMORY.md    # 长期记忆
│   └── YYYY-MM-DD.md  # 每日日记（以当天日期命名）
└── skills/          # 技能安装目录（含 .drafts 草稿目录）
```

### 技能市场（hexclaw-hub）

桌面端「技能市场」从在线目录拉取 `index.json` 与 `skills/*.md`。默认仓库为 **`hexagon-codes/hexclaw-hub`** 的 **`v0.0.7`** 标签。自建镜像时可覆盖：

```yaml
skills:
  enabled: true
  hub:
    repo_url: https://github.com/hexagon-codes/hexclaw-hub
    branch: v0.0.7
```

安装或卸载 Markdown 技能后，引擎会**自动同步**运行时技能注册表，一般**无需重启** sidecar。
通过 API 在线安装时，`source` 可写为 `clawhub://skill-name`；`GET /api/v1/clawhub/search` 支持 `q` 和 `category` 参数。

### 环境变量

独立服务首次配置可通过环境变量提供所选 Provider 的凭据。通过 Desktop 管理模型时，日常配置保存到当前服务的 YAML；不要在云端服务的启动环境中持续注入 Provider Key，否则删除或更改配置后可能在重启时重新补入。初始化与持久化约定见[云端部署指南](cloud-deployment.md)。

```bash
# 云端 Provider 的 API Key 示例，按所选 Provider 设置
export DEEPSEEK_API_KEY="sk-xxx"
export OPENAI_API_KEY="sk-xxx"
export ANTHROPIC_API_KEY="sk-xxx"

# 平台 Token（按需设置）
export TELEGRAM_BOT_TOKEN="xxx"
export DISCORD_BOT_TOKEN="xxx"
export SLACK_BOT_TOKEN="xoxb-xxx"
export SLACK_SIGNING_SECRET="xxx"
export FEISHU_APP_ID="cli_xxx"
export FEISHU_APP_SECRET="xxx"
```

配置文件中使用 `${VAR_NAME}` 引用环境变量：

```yaml
llm:
  providers:
    deepseek:
      api_key: ${DEEPSEEK_API_KEY}
```

### 配置优先级

配置按以下顺序加载与补齐：

1. 使用默认配置作为基线。
2. 加载 `hexclaw.yaml`，其中 `${VAR_NAME}` 按当前进程环境展开，未设置的变量为空值。
3. 从已支持的 LLM Key 环境变量添加缺省 Provider，或补齐已有 Provider 的空 Key；不覆盖配置中已加载的非空 Key。
4. 应用受支持的命令行参数，例如 `--feishu-app-id`，覆盖对应字段。

### 最小配置

服务本身可以在没有可用模型 Provider 时启动；聊天、识题、批改、RAG 增强等功能需要对应用途可用的本地或云端 Provider。未配置可用 Provider 时会返回配置错误。本地 Ollama 不一定需要 API Key。以下为云端 Provider 示例：

```bash
export DEEPSEEK_API_KEY="sk-xxx"
hexclaw serve
```

默认配置启用认证、输入检测和沙箱等能力，RBAC 按需配置；具体值见[默认配置](../config/defaults.go)。存储使用 SQLite 自动创建。

### 配置示例与选项

最小 YAML 示例见 [README.md](../README.md#配置)。完整选项见[配置结构](../config/config.go)，生成配置时使用的默认模板见[配置加载器](../config/loader.go)。

---

## 部署方式

### 1. 直接运行（开发/测试）

```bash
hexclaw serve
hexclaw serve --config /path/to/hexclaw.yaml
# Desktop 由原生层携带持久令牌启动；独立服务使用 hexclaw serve。
```

### 2. systemd 服务（直接管理二进制）

以下为 Linux systemd 示例。先将选定版本的可执行文件安装到 `/usr/local/bin/hexclaw`，并运行 `/usr/local/bin/hexclaw version` 核对版本；Go 安装方式的文件来自 `GOBIN`，源码或 Release 方式使用对应构建或解压产物。

服务使用 `/opt/hexclaw` 作为 HOME，配置与数据位于 `/opt/hexclaw/.hexclaw/`，临时文件位于 `/opt/hexclaw/tmp/`。`WorkingDirectory` 不会改变 HOME；两个目录都必须由服务用户拥有。

创建服务文件 `/etc/systemd/system/hexclaw.service`：

```ini
[Unit]
Description=HexClaw AI Agent
After=network.target

[Service]
Type=simple
User=hexclaw
Group=hexclaw
WorkingDirectory=/opt/hexclaw
ExecStart=/usr/local/bin/hexclaw serve --config /opt/hexclaw/.hexclaw/hexclaw.yaml
Restart=always
RestartSec=5

# 配置、数据与临时文件均使用现有可写目录
Environment=HOME=/opt/hexclaw
Environment=TMPDIR=/opt/hexclaw/tmp

# 安全加固
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/opt/hexclaw

# 资源限制
LimitNOFILE=65536
MemoryMax=1G

[Install]
WantedBy=multi-user.target
```

首次初始化并启动服务：

```bash
# 创建用户
sudo useradd -r -U -d /opt/hexclaw -s /bin/false hexclaw
sudo mkdir -p /opt/hexclaw/tmp
sudo chown hexclaw:hexclaw /opt/hexclaw /opt/hexclaw/tmp

# 在服务自身 HOME 中生成配置，不复制个人配置
sudo -u hexclaw env HOME=/opt/hexclaw /usr/local/bin/hexclaw init

# 按“配置”章节设置模型；保留服务用户的配置文件所有权
sudoedit /opt/hexclaw/.hexclaw/hexclaw.yaml

# 启动
sudo systemctl daemon-reload
sudo systemctl enable hexclaw
sudo systemctl start hexclaw

# 查看状态
sudo systemctl status hexclaw
sudo journalctl -u hexclaw -f
```

上述用户创建与 `init` 是首次安装步骤，已有服务直接编辑原配置，不重复初始化。Provider 凭据通过该服务的 YAML 或启动后的 Desktop 模型设置持久化；使用模板中的环境变量引用时，先替换为该服务的实际持久配置，日常 unit 不持续注入 Provider Key。

现有目录限制保持不变；`TMPDIR` 让附件暂存、文档提取和脚本校验在可写范围内创建临时文件。`MemoryMax=1G` 是示例预算，按任务与文件大小规划资源。需要从其他机器连接时，按实际监听地址与反向代理方式设置 `server.host`；缺省回环地址只供本机访问。systemd 的目录语义见[官方执行环境说明](https://raw.githubusercontent.com/systemd/systemd/main/man/systemd.exec.xml)。

### 3. Docker Compose 与 Kubernetes

使用维护中的 [Compose 文件](../docker-compose.yml)或 [Kubernetes 单实例清单](../docker/kubernetes.yaml)，按[云端部署指南](cloud-deployment.md)操作。两者均持久化整个可写 HOME `/data`，仅首次初始化凭据。不要把只读配置挂到运行 YAML，或在日常启动环境变量中重复注入 Provider Key，否则远端配置不能正常保存或删除。Kubernetes 使用单副本和 Recreate；初始化种子、Ollama 地址、停机、更新与完整备份均见指南。

---

## 平台接入

### Web UI

默认启用。启动后访问 `http://127.0.0.1:16060`。

```yaml
platforms:
  web:
    enabled: true
```

### 飞书 Bot

1. 在[飞书开放平台](https://open.feishu.cn/)创建应用，获取 App ID 和 App Secret
2. 配置事件订阅 URL: `http://YOUR_HOST:6061/feishu/webhook`（或使用命令行参数）
3. 启用配置：

```yaml
platforms:
  feishu:
    enabled: true
    app_id: ${FEISHU_APP_ID}
    app_secret: ${FEISHU_APP_SECRET}
    verification_token: ${FEISHU_VERIFICATION_TOKEN}
```

或通过命令行参数：

```bash
hexclaw serve --feishu-app-id cli_xxx --feishu-app-secret xxx
```

### Telegram Bot

1. 通过 [@BotFather](https://t.me/BotFather) 创建 Bot，获取 Token
2. 启用配置：

```yaml
platforms:
  telegram:
    enabled: true
    token: ${TELEGRAM_BOT_TOKEN}
```

Telegram 使用长轮询模式，无需公网 IP。

### Discord Bot

1. 在 [Discord Developer Portal](https://discord.com/developers/applications) 创建应用
2. 获取 Bot Token，启用 Message Content Intent
3. 启用配置：

```yaml
platforms:
  discord:
    enabled: true
    token: ${DISCORD_BOT_TOKEN}
```

### Slack Bot

1. 在 [Slack API](https://api.slack.com/apps) 创建应用
2. 配置 Events Request URL: `http://YOUR_HOST:6063/slack/events`
3. 订阅 `message.im` 事件
4. 启用配置：

```yaml
platforms:
  slack:
    enabled: true
    token: ${SLACK_BOT_TOKEN}
    signing_secret: ${SLACK_SIGNING_SECRET}
```

### 钉钉 Bot

1. 在[钉钉开放平台](https://open-dev.dingtalk.com/)创建企业内部应用并配置机器人。
2. 准备应用的 `app_key`、`app_secret` 和机器人 `robot_code`。
3. 启用以下配置并启动服务。适配器通过官方 Stream SDK 接收消息，无需公网回调地址：

```yaml
platforms:
  dingtalk:
    enabled: true
    app_key: ${DINGTALK_APP_KEY}
    app_secret: ${DINGTALK_APP_SECRET}
    robot_code: ${DINGTALK_ROBOT_CODE}
```

若使用 HTTP 兼容接入，回调地址由实际服务地址与 `/api/v1/platforms/hooks/dingtalk/{name}` 组成；`{name}` 使用已配置平台实例的实际名称。实例名称可从平台实例列表查询；外部回调使用部署中可访问的服务地址。

### 企业微信

1. 在[企业微信管理后台](https://work.weixin.qq.com/)创建自建应用
2. 配置接收消息 URL: `http://YOUR_HOST:6064/wecom/callback`
3. 启用配置：

```yaml
platforms:
  wecom:
    enabled: true
    corp_id: ${WECOM_CORP_ID}
    agent_id: ${WECOM_AGENT_ID}
    secret: ${WECOM_SECRET}
    token: ${WECOM_TOKEN}
    aes_key: ${WECOM_AES_KEY}
```

### 微信公众号

1. 在[微信公众平台](https://mp.weixin.qq.com/)配置服务器
2. 服务器 URL: `http://YOUR_HOST:6065/wechat/callback`
3. 启用配置：

```yaml
platforms:
  wechat:
    enabled: true
    app_id: ${WECHAT_APP_ID}
    app_secret: ${WECHAT_APP_SECRET}
    token: ${WECHAT_TOKEN}
    aes_key: ${WECHAT_AES_KEY}
```

---

## 运维

### 健康检查

```bash
curl http://127.0.0.1:16060/health
# {"status":"healthy"}
```

### 启动后联调检查

下面的业务接口读写均需要当前服务的 Bearer 令牌。`hexclaw init` 会生成 `server.api_token`，直接启动独立服务也会补齐缺少的令牌；已有值保持不变。先设置示例使用的环境变量，Desktop 本机令牌的区别见[连接与身份](api.md#连接与身份)。

```bash
export HEXCLAW_API_TOKEN="配置文件中的 server.api_token"
```

按需选择检查项，并替换模型凭据、技能名和记录 ID。模型连通性检查会请求对应 Provider；技能安装与状态更新会修改当前服务配置。

```bash
# 1. 真实测试 LLM 配置，不写入磁盘
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" -X POST http://127.0.0.1:16060/api/v1/config/llm/test \
  -H "Content-Type: application/json" \
  -d '{
    "provider": {
      "type": "openai",
      "base_url": "https://api.openai.com/v1",
      "api_key": "sk-xxx",
      "model": "gpt-4o-mini"
    }
  }'

# 1b. 本地 Ollama 连通性测试可不填 api_key
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" -X POST http://127.0.0.1:16060/api/v1/config/llm/test \
  -H "Content-Type: application/json" \
  -d '{
    "provider": {
      "type": "ollama",
      "base_url": "http://127.0.0.1:11434/v1",
      "api_key": "",
      "model": "llama3.1"
    }
  }'

# 2. 搜索并在线安装 ClawHub 技能
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" "http://127.0.0.1:16060/api/v1/clawhub/search?q=calendar&category=automation"
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" -X POST http://127.0.0.1:16060/api/v1/skills/install \
  -H "Content-Type: application/json" \
  -d '{"source":"clawhub://<SKILL_NAME>"}'

# 3. 检查 Skills 运行态字段
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" http://127.0.0.1:16060/api/v1/skills
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" -X PUT http://127.0.0.1:16060/api/v1/skills/example/status \
  -H "Content-Type: application/json" \
  -d '{"enabled":true}'

# 4. 查看 Cron 历史结果字段
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" "http://127.0.0.1:16060/api/v1/cron/jobs/实际任务ID/history"

# 5. 验证知识库结构化搜索
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" -X POST http://127.0.0.1:16060/api/v1/knowledge/search \
  -H "Content-Type: application/json" \
  -d '{"query":"RAG","limit":5}'

# 6. 检查平台实例与 IM 通道测试接口
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" http://127.0.0.1:16060/api/v1/platforms/instances
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" -X POST http://127.0.0.1:16060/api/v1/im/channels/telegram/test \
  -H "Content-Type: application/json" \
  -d '{"token":"123:abc"}'

# 7. 检查 autonomy 权限治理和媒体 Provider 状态
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" http://127.0.0.1:16060/api/v1/autonomy/summary
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" http://127.0.0.1:16060/api/v1/images/status
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" http://127.0.0.1:16060/api/v1/videos/status

# 8. 检查内置 K12 场景包挂载
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" http://127.0.0.1:16060/api/k12/view-descriptor
```

重点返回语义：

- `/api/v1/config/llm/test` 返回 `ok/message/provider/model/latency_ms`；当 `provider.type=ollama` 时允许空 `api_key` 做本地连通性测试
- `/api/v1/skills` 和 `/api/v1/skills/{name}/status` 返回 `enabled/effective_enabled/requires_restart/message`
- `/api/v1/skills/install` 支持 `clawhub://skill-name` 或本地相对路径；在线安装成功时返回 `requires_restart=false/runtime_registered=true`
- `/api/v1/cron/jobs/{id}/history` 的历史项包含 `result`，便于直接展示执行输出摘要
- `/api/v1/knowledge/search` 返回结构化 chunk 结果（`result` 和 `results` 字段均为 `[]SearchHit` 数组），不再只是拼接后的上下文字符串
- `/api/v1/knowledge/documents` 返回 `status/error_message/updated_at/source_type`
- `/api/v1/knowledge/documents/{id}` 返回单个文档的完整内容
- 图片/视频生成优先返回 `file_path`，通过 `/api/v1/files/generated/{path}` 访问产物
- `/api/k12/*` 是场景包挂载路由；读写均需要 Bearer Token，包括 loopback 请求
- `/api/v1/logs` 支持 `domain` 过滤，便于按功能域诊断问题

### 日志

HexClaw 使用标准 `log` 输出到 stderr，可通过系统日志工具管理：

```bash
# systemd
journalctl -u hexclaw -f

# Docker
docker logs -f hexclaw

# 重定向到文件
hexclaw serve 2>&1 | tee /var/log/hexclaw.log
```

实时日志也可通过 API 获取：

```bash
# 查询日志（支持 level/source/domain/keyword 过滤）
curl -H "Authorization: Bearer TOKEN" \
  "http://127.0.0.1:16060/api/v1/logs?domain=knowledge&level=error&limit=50"

# WebSocket 实时流
wscat -H "Authorization: Bearer TOKEN" \
  -c "ws://127.0.0.1:16060/api/v1/logs/stream"
```

### 数据备份

Compose 使用[一致备份与隔离恢复流程](cloud-deployment.md#完整备份与恢复)：停止写入，归档完整 HOME 和部署配置，恢复原服务后再异机复制。同机归档不等于异机备份；恢复先进入新卷并检查实际内容，再明确切换。

直接运行二进制时，先停止服务及同目录的其他写入者，再归档完整 `~/.hexclaw/`、外部对象／配置路径和必要渲染资源；SQLite 主库及 WAL 必须来自同一次停写状态。解包到新目录核对，不直接覆盖运行实例，也不单独复制正在写入的 `data.db`。

### 安全审计

定期运行安全审计检查配置安全性：

```bash
hexclaw security audit
```

审计检查项：
- 配置文件权限
- API Key 是否泄露
- 网络暴露风险
- 安全选项是否启用
- 工具权限检查
- 成本预算检查
- 沙箱配置检查

### 升级

以下 Go 命令升级到最新稳定版；使用预发布版时，将 `@latest` 替换为 [Releases](https://github.com/hexagon-codes/hexclaw/releases) 中实际存在的 Tag。二进制安装从所选 Release 下载与系统、架构对应的资产，核对 checksum 后替换原程序并重启服务。

```bash
# go install 方式
go install github.com/hexagon-codes/hexclaw/cmd/hexclaw@latest

# Docker
docker compose pull hexclaw
docker compose up -d --no-build hexclaw
```

Docker 命令适用于已选择发布镜像的部署；默认源码 Compose 使用 `hexclaw:dev`，更新源码后先执行 `docker compose build hexclaw`。完整镜像版本与 Compose override 约定见[云端部署指南](cloud-deployment.md)。

更新前完成一致备份；涉及数据库迁移时按[部署恢复约定](cloud-deployment.md#按提交自动部署)处理，不能仅替换旧镜像而假设数据格式可回退。

---

## 故障排查

### 启动失败

**"没有可用的 LLM Provider"**

服务可以启动，但聊天、K12 识题/批改、备课热身题、语音/媒体等依赖 Provider 的功能需要至少一个可用 LLM 或本地兼容 Provider：

```bash
export DEEPSEEK_API_KEY="sk-xxx"
```

本地 Ollama 的连通性测试允许空 `api_key`；实际聊天前仍需在配置里启用可用的本地或云端 Provider。

**"初始化存储失败"**

检查数据目录权限：

```bash
ls -la ~/.hexclaw/
# 确保当前用户有读写权限
chmod 700 ~/.hexclaw/
```

**端口被占用**

修改监听端口：

```yaml
server:
  port: 7070  # 改为其他端口
```

### 连接问题

**LLM API 超时**

检查网络连接，或配置代理：

```bash
export HTTPS_PROXY="http://proxy:8080"
hexclaw serve
```

对于国内用户，可配置 API 中转：

```yaml
llm:
  providers:
    openai:
      api_key: ${OPENAI_API_KEY}
      base_url: https://your-proxy.com/v1  # API 中转地址
      model: gpt-4o
```

**平台 Webhook 收不到消息**

1. 确认服务器公网可达
2. 检查防火墙是否放行对应端口
3. 使用 ngrok 或 frp 进行内网穿透（开发阶段）：

```bash
ngrok http 16060
# 使用 ngrok 提供的 HTTPS URL 配置 Webhook
```

### 性能问题

**响应慢**

- 启用语义缓存减少重复请求：
  ```yaml
  llm:
    cache:
      enabled: true
      ttl: 24h
  ```
- 使用低成本模型处理简单任务
- 检查知识库大小，适当调整 `chunk_size` 和 `top_k`

**内存占用高**

- 减少上下文保留消息数：
  ```yaml
  compaction:
    enabled: true
    max_messages: 30
    keep_recent: 5
  ```
- 减小语义缓存条目数：
  ```yaml
  llm:
    cache:
      max_entries: 1000
  ```
