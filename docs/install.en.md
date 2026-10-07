# HexClaw Installation & Deployment Guide

**English | [中文](install.md)**

## Table of Contents

- [System Requirements](#system-requirements)
- [Installation Methods](#installation-methods)
- [Configuration](#configuration)
- [Deployment Methods](#deployment-methods)
- [Platform Integration](#platform-integration)
- [Operations](#operations)
- [Troubleshooting](#troubleshooting)

---

## System Requirements

| Item | Minimum | Recommended |
|------|---------|-------------|
| OS | Linux / macOS / Windows | Linux (Ubuntu 22.04+) |
| Go (Go installation or source builds only) | >= 1.25.13 | A toolchain compatible with the selected version's `go.mod` |
| Memory | Size for the tasks, file sizes, and model deployment | Reserve resources separately for rendering, image tasks, and local models |
| Disk | Writable program and complete user-data directories | Allow space for source documents, artifacts, indexes, and backups |
| Network | Reachable online Providers and channels in use; local models follow their deployment requirements | Low-latency connection |

The core is a single binary with pure Go SQLite. Document exports, Chinese/math rendering and numeric execution also need Pandoc, Typst, fonts and Python/SymPy; the cloud image includes them.

---

## Installation Methods

This page covers installation and deployment of the standalone HexClaw service. For the desktop app's one-line installer, see [HexClaw Desktop installation](https://github.com/hexagon-codes/hexclaw-desktop#安装).

`@latest` and Releases' `latest/download` select stable releases. For a prerelease, use its complete published Tag from [Releases](https://github.com/hexagon-codes/hexclaw/releases) and the assets actually available for that version.

### Method 1: go install (recommended for developers)

```bash
go install github.com/hexagon-codes/hexclaw/cmd/hexclaw@latest
```

For a specific version, replace `@latest` with its complete Tag. Ensure `GOBIN`, or `$(go env GOPATH)/bin` by default, is on your `PATH`.

Verify installation:

```bash
hexclaw version
# HexClaw <current build version>
```

### Method 2: Build from source

```bash
git clone https://github.com/hexagon-codes/hexclaw.git
cd hexclaw
go build -o hexclaw ./cmd/hexclaw/

# Optional: build with version info (recommended for release/self-test builds)
VERSION=$(git describe --tags --always --dirty)
go build -ldflags "-X main.version=${VERSION} -X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o hexclaw ./cmd/hexclaw/

# Move to PATH
sudo mv hexclaw /usr/local/bin/
```

### Method 3: Pre-built binary

Choose a version from [GitHub Releases](https://github.com/hexagon-codes/hexclaw/releases), then download the archive for your operating system and CPU architecture. For example, Linux amd64 uses `hexclaw-linux-amd64.tar.gz`, while Apple Silicon uses `hexclaw-darwin-arm64.tar.gz`.

Extract `hexclaw` (`hexclaw.exe` on Windows), place it in a directory on your `PATH`, and run `hexclaw version`. Use the asset list and filenames from the selected release.

### Method 4: Docker

Use Docker Compose for a single server. In the private deployment `.env`, set `HEXCLAW_IMAGE=ghcr.io/hexagon-codes/hexclaw@sha256:<actual-digest>` using a digest from an available build:

```bash
docker compose pull hexclaw
docker compose up -d --no-build hexclaw
```

For local source development, run `docker compose build hexclaw` first; the default image is `hexclaw:dev`. Published images use version and full commit SHA tags. Prereleases do not update `latest`, which is reserved for stable releases. Keep the existing project, data volume and complete Compose override set when updating.

The source image targets Linux amd64 and persists the complete writable HOME. The default Compose setup **does not install Ollama or download models**. Configure model and Embedding APIs for the selected remote backend in Desktop. Knowledge data and indexes belong to that server; keyword retrieval and vector availability are separate when no effective Embedding configuration exists. See the [cloud deployment guide](cloud-deployment.md) for initialization, Kubernetes, backup, automation and current verification limits.

---

## Configuration

### Initialize Configuration

```bash
hexclaw init
# Generates: ~/.hexclaw/hexclaw.yaml
```

The default config directory is `~/.hexclaw/`, containing:

```
~/.hexclaw/
├── AGENTS.md       # Shared working rules, initialized only when absent
├── auth.json       # Desktop-owned local/remote connection credentials
├── hexclaw.yaml     # Main config file
├── data.db          # SQLite database (auto-created)
├── master.key       # At-rest credential encryption master key (auto-created, 0600)
├── logs/            # Local logs / runtime diagnostics (deployment-dependent)
├── workspace/       # Default workspace for code_exec and file tools
├── memory/          # File memory directory
│   ├── MEMORY.md    # Long-term memory
│   └── YYYY-MM-DD.md  # Daily journal (named by date)
└── skills/          # Skill installation directory (includes .drafts)
```

### Skill marketplace (hexclaw-hub)

The desktop **Skill Marketplace** fetches `index.json` and `skills/*.md` from a GitHub repo. The default is **`hexagon-codes/hexclaw-hub`** on tag **`v0.0.7`**. Override for a mirror:

```yaml
skills:
  enabled: true
  hub:
    repo_url: https://github.com/hexagon-codes/hexclaw-hub
    branch: v0.0.7
```

After installing or uninstalling a Markdown skill, the engine **syncs** the runtime skill registry; you usually **do not need** to restart the sidecar.
For online installs through the API, `source` may be `clawhub://skill-name`; `GET /api/v1/clawhub/search` supports `q` and `category`.

### Environment Variables

Environment variables can provide credentials for the selected Provider during initial standalone setup. When managing models through Desktop, daily configuration is saved in the active service's YAML. Persistently injecting Provider keys into a cloud service's startup environment can restore them after deletion or changes on restart. See the [cloud deployment guide](cloud-deployment.md) for initialization and persistence rules.

```bash
# API key examples for your selected cloud Providers
export DEEPSEEK_API_KEY="sk-xxx"
export OPENAI_API_KEY="sk-xxx"
export ANTHROPIC_API_KEY="sk-xxx"

# Platform tokens (as needed)
export TELEGRAM_BOT_TOKEN="xxx"
export DISCORD_BOT_TOKEN="xxx"
export SLACK_BOT_TOKEN="xoxb-xxx"
export SLACK_SIGNING_SECRET="xxx"
export FEISHU_APP_ID="cli_xxx"
export FEISHU_APP_SECRET="xxx"
```

Reference environment variables in the config file using `${VAR_NAME}`:

```yaml
llm:
  providers:
    deepseek:
      api_key: ${DEEPSEEK_API_KEY}
```

### Configuration Priority

Configuration is loaded and completed in this order:

1. Start with the default configuration.
2. Load `hexclaw.yaml`, expanding `${VAR_NAME}` from the current process environment; unset variables become empty values.
3. Add missing Providers from supported LLM key environment variables, or fill empty keys in existing Providers. Non-empty keys already loaded from configuration are preserved.
4. Apply supported command-line arguments, such as `--feishu-app-id`, to their corresponding fields.

### Minimal Configuration

The service can start without an available model Provider. Chat, homework recognition/grading, RAG-augmented answers, and other model-dependent tasks require an available local or cloud Provider for their purpose. Missing Providers produce configuration errors; local Ollama does not necessarily need an API key. This example configures a cloud Provider:

```bash
export DEEPSEEK_API_KEY="sk-xxx"
hexclaw serve
```

The default configuration enables authentication, input checks, and sandbox capabilities; RBAC is configured as needed. See the [defaults](../config/defaults.go) for exact values. SQLite storage is created automatically.

### Configuration Example and Options

See [README.en.md](../README.en.md#configuration) for a minimal YAML example, [configuration types](../config/config.go) for all options, and the [configuration loader](../config/loader.go) for the generated default template.

---

## Deployment Methods

### 1. Direct Run (development/testing)

```bash
hexclaw serve
hexclaw serve --config /path/to/hexclaw.yaml
# Desktop is launched by the native host with its persistent token.
# Use hexclaw serve for a standalone service.
```

### 2. systemd Service (direct binary management)

This is a Linux systemd example. Install the selected executable at `/usr/local/bin/hexclaw` and confirm its version with `/usr/local/bin/hexclaw version`. Go installations place it in `GOBIN`; source builds and Release archives provide their corresponding executable.

The service uses `/opt/hexclaw` as HOME, stores configuration and data in `/opt/hexclaw/.hexclaw/`, and creates temporary files in `/opt/hexclaw/tmp/`. `WorkingDirectory` does not change HOME. The service user must own both directories.

Create service file `/etc/systemd/system/hexclaw.service`:

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

# Configuration, data, and temporary files use the writable directory
Environment=HOME=/opt/hexclaw
Environment=TMPDIR=/opt/hexclaw/tmp

# Security hardening
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/opt/hexclaw

# Resource limits
LimitNOFILE=65536
MemoryMax=1G

[Install]
WantedBy=multi-user.target
```

Initialize and start the service for the first time:

```bash
# Create user
sudo useradd -r -U -d /opt/hexclaw -s /bin/false hexclaw
sudo mkdir -p /opt/hexclaw/tmp
sudo chown hexclaw:hexclaw /opt/hexclaw /opt/hexclaw/tmp

# Generate the configuration in the service HOME, without copying personal settings
sudo -u hexclaw env HOME=/opt/hexclaw /usr/local/bin/hexclaw init

# Follow the Configuration section; preserve the service user's file ownership
sudoedit /opt/hexclaw/.hexclaw/hexclaw.yaml

# Start
sudo systemctl daemon-reload
sudo systemctl enable hexclaw
sudo systemctl start hexclaw

# Check status
sudo systemctl status hexclaw
sudo journalctl -u hexclaw -f
```

User creation and `init` are first-install steps. For an existing service, edit its configuration rather than initializing it again. Persist Provider credentials through this service's YAML or Desktop model settings after startup. Replace template environment-variable references with the service's persistent values; do not keep injecting Provider keys in the daily unit.

The existing directory restrictions remain. `TMPDIR` lets attachment staging, document extraction, and script validation create temporary files inside the writable area. `MemoryMax=1G` is an example budget; size resources for the tasks and files. For access from another machine, configure `server.host` for the actual listener/proxy arrangement; the default loopback address is local-only. See the [official systemd execution environment documentation](https://raw.githubusercontent.com/systemd/systemd/main/man/systemd.exec.xml) for directory semantics.

### 3. Docker Compose and Kubernetes

Use the maintained [Compose file](../docker-compose.yml) or [single-instance Kubernetes manifest](../docker/kubernetes.yaml), following the [cloud deployment guide](cloud-deployment.md). Both keep `/data` as a writable persistent HOME and initialize credentials only once. Do not mount a read-only configuration over the runtime YAML, or inject daily Provider keys through environment variables; either would break remote configuration persistence. Kubernetes uses one replica and Recreate. The full guide covers first-time seeds, Ollama networking, shutdown, updates and complete backups.

---

## Platform Integration

### Web UI

Enabled by default. Access at `http://127.0.0.1:16060` after startup.

```yaml
platforms:
  web:
    enabled: true
```

### Feishu Bot

1. Create an app on [Feishu Open Platform](https://open.feishu.cn/), get App ID and App Secret
2. Configure event subscription URL: `http://YOUR_HOST:6061/feishu/webhook` (or use CLI args)
3. Enable in config:

```yaml
platforms:
  feishu:
    enabled: true
    app_id: ${FEISHU_APP_ID}
    app_secret: ${FEISHU_APP_SECRET}
    verification_token: ${FEISHU_VERIFICATION_TOKEN}
```

Or via CLI:

```bash
hexclaw serve --feishu-app-id cli_xxx --feishu-app-secret xxx
```

### Telegram Bot

1. Create a bot via [@BotFather](https://t.me/BotFather), get the token
2. Enable in config:

```yaml
platforms:
  telegram:
    enabled: true
    token: ${TELEGRAM_BOT_TOKEN}
```

Telegram uses long polling — no public IP required.

### Discord Bot

1. Create an application on [Discord Developer Portal](https://discord.com/developers/applications)
2. Get Bot Token, enable Message Content Intent
3. Enable in config:

```yaml
platforms:
  discord:
    enabled: true
    token: ${DISCORD_BOT_TOKEN}
```

### Slack Bot

1. Create an app on [Slack API](https://api.slack.com/apps)
2. Configure Events Request URL: `http://YOUR_HOST:6063/slack/events`
3. Subscribe to `message.im` event
4. Enable in config:

```yaml
platforms:
  slack:
    enabled: true
    token: ${SLACK_BOT_TOKEN}
    signing_secret: ${SLACK_SIGNING_SECRET}
```

### DingTalk Bot

1. Create an internal app on [DingTalk Open Platform](https://open-dev.dingtalk.com/) and configure its robot.
2. Obtain the app's `app_key`, `app_secret`, and robot `robot_code`.
3. Enable the configuration below and start the service. The adapter receives messages through the official Stream SDK without a public callback address:

```yaml
platforms:
  dingtalk:
    enabled: true
    app_key: ${DINGTALK_APP_KEY}
    app_secret: ${DINGTALK_APP_SECRET}
    robot_code: ${DINGTALK_ROBOT_CODE}
```

For HTTP compatibility, combine the actual service address with `/api/v1/platforms/hooks/dingtalk/{name}`. Use the configured platform instance's actual name, available from the platform instance list. External callbacks require an address reachable in your deployment.

### WeCom (Enterprise WeChat)

1. Create an internal app in [WeCom Admin Console](https://work.weixin.qq.com/)
2. Configure message receive URL: `http://YOUR_HOST:6064/wecom/callback`
3. Enable in config:

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

### WeChat Official Account

1. Configure server on [WeChat Official Platform](https://mp.weixin.qq.com/)
2. Server URL: `http://YOUR_HOST:6065/wechat/callback`
3. Enable in config:

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

## Operations

### Health Check

```bash
curl http://127.0.0.1:16060/health
# {"status":"healthy"}
```

### Post-Startup Integration Checks

All business reads and writes below require the current service's Bearer token. `hexclaw init` generates `server.api_token`; starting the standalone service directly also fills a missing token while preserving an existing value. Set the variable used by these examples first; see [connection and identity](api.en.md#connection-and-identity) for Desktop's local token distinction.

```bash
export HEXCLAW_API_TOKEN="the server.api_token from your configuration file"
```

Choose the checks you need and replace model credentials, skill names, and record IDs. Model connectivity checks call the selected Provider; installing a skill or changing its state modifies the current service configuration.

```bash
# 1. Test a real LLM config without persisting it
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

# 1b. Local Ollama connectivity test can leave api_key empty
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

# 2. Search and install a ClawHub skill online
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" "http://127.0.0.1:16060/api/v1/clawhub/search?q=calendar&category=automation"
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" -X POST http://127.0.0.1:16060/api/v1/skills/install \
  -H "Content-Type: application/json" \
  -d '{"source":"clawhub://<SKILL_NAME>"}'

# 3. Check runtime skill status fields
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" http://127.0.0.1:16060/api/v1/skills
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" -X PUT http://127.0.0.1:16060/api/v1/skills/example/status \
  -H "Content-Type: application/json" \
  -d '{"enabled":true}'

# 4. Check Cron history result fields
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" "http://127.0.0.1:16060/api/v1/cron/jobs/REPLACE_WITH_JOB_ID/history"

# 5. Verify structured knowledge search
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" -X POST http://127.0.0.1:16060/api/v1/knowledge/search \
  -H "Content-Type: application/json" \
  -d '{"query":"RAG","limit":5}'

# 6. Check platform instances and IM channel test APIs
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" http://127.0.0.1:16060/api/v1/platforms/instances
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" -X POST http://127.0.0.1:16060/api/v1/im/channels/telegram/test \
  -H "Content-Type: application/json" \
  -d '{"token":"123:abc"}'

# 7. Check autonomy governance and media provider status
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" http://127.0.0.1:16060/api/v1/autonomy/summary
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" http://127.0.0.1:16060/api/v1/images/status
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" http://127.0.0.1:16060/api/v1/videos/status

# 8. Check the built-in K12 scenario-pack mount
curl -H "Authorization: Bearer ${HEXCLAW_API_TOKEN}" http://127.0.0.1:16060/api/k12/view-descriptor
```

Key response semantics:

- `/api/v1/config/llm/test` returns `ok/message/provider/model/latency_ms`; when `provider.type=ollama`, an empty `api_key` is allowed for local connectivity checks
- `/api/v1/skills` and `/api/v1/skills/{name}/status` return `enabled/effective_enabled/requires_restart/message`
- `/api/v1/skills/install` accepts `clawhub://skill-name` or a local relative path; successful online installs return `requires_restart=false/runtime_registered=true`
- `/api/v1/cron/jobs/{id}/history` includes `result` in each history item so execution output summaries can be shown directly
- `/api/v1/knowledge/search` returns structured chunk results (both `result` and `results` are now `[]SearchHit` arrays) instead of one concatenated context string
- `/api/v1/knowledge/documents` returns `status/error_message/updated_at/source_type`
- `/api/v1/knowledge/documents/{id}` returns a single document with full content
- Image/video generation prefers returning `file_path`; resolve artifacts through `/api/v1/files/generated/{path}`
- `/api/k12/*` is a scenario-pack mount; reads and writes require a Bearer token, including loopback requests
- `/api/v1/logs` supports `domain` filtering for functional diagnostics

### Logs

HexClaw uses standard `log` output to stderr, manageable via system log tools:

```bash
# systemd
journalctl -u hexclaw -f

# Docker
docker logs -f hexclaw

# Redirect to file
hexclaw serve 2>&1 | tee /var/log/hexclaw.log
```

Real-time logs also available via API:

```bash
# Query logs (supports level/source/domain/keyword filtering)
curl -H "Authorization: Bearer TOKEN" \
  "http://127.0.0.1:16060/api/v1/logs?domain=knowledge&level=error&limit=50"

# WebSocket real-time stream
wscat -H "Authorization: Bearer TOKEN" \
  -c "ws://127.0.0.1:16060/api/v1/logs/stream"
```

### Data Backup

For Compose, use the [consistent backup and isolated restore procedure](cloud-deployment.md#完整备份与恢复). Stop writes, archive the full HOME and deployment configuration, restart the original service, then transfer the completed archive off-host. A local archive is not an off-host backup. Restore to a new volume and check its contents before switching the active deployment.

For a standalone binary, first stop the service and all other writers of the same data directory. Archive the complete `~/.hexclaw/`, external object/configuration paths and required rendering assets; SQLite main and WAL files must come from the same stopped state. Extract to a new directory for verification instead of overwriting the running instance. Do not copy only a live `data.db`.

### Security Audit

Run security audit periodically to check configuration security:

```bash
hexclaw security audit
```

Audit checks:
- Config file permissions
- API key exposure
- Network exposure risks
- Security options status
- Tool permissions
- Cost budget settings
- Sandbox configuration

### Upgrade

The Go command below upgrades to the latest stable release. For a prerelease, replace `@latest` with a Tag that actually exists in [Releases](https://github.com/hexagon-codes/hexclaw/releases). For binary installations, download the asset for your system and architecture from the selected Release, verify its checksum, then replace the executable and restart the service.

```bash
# go install method
go install github.com/hexagon-codes/hexclaw/cmd/hexclaw@latest

# Docker
docker compose pull hexclaw
docker compose up -d --no-build hexclaw
```

The Docker commands apply to deployments using a published image. The default source Compose setup uses `hexclaw:dev`; after updating source, run `docker compose build hexclaw` first. See the [cloud deployment guide](cloud-deployment.md) for image versions and complete Compose override rules.

Create a consistent backup before updating. For database migrations, follow the [deployment recovery rules](cloud-deployment.md#按提交自动部署); replacing the image alone does not establish data compatibility.

---

## Troubleshooting

### Startup Failures

**"No LLM provider available"**

The service can start, but chat, K12 recognition/grading, provider-generated tutoring-tip explanations, voice/media, and other provider-dependent features require at least one usable local or cloud provider:

```bash
export DEEPSEEK_API_KEY="sk-xxx"
```

Local Ollama connectivity checks allow an empty `api_key`; actual chat still requires a configured local or cloud provider.

**"Failed to initialize storage"**

Check data directory permissions:

```bash
ls -la ~/.hexclaw/
# Ensure current user has read/write permissions
chmod 700 ~/.hexclaw/
```

**Port already in use**

Change the listen port:

```yaml
server:
  port: 7070  # Change to another port
```

### Connection Issues

**LLM API timeout**

Check network connectivity, or configure a proxy:

```bash
export HTTPS_PROXY="http://proxy:8080"
hexclaw serve
```

For users with restricted access to certain APIs, configure an API relay:

```yaml
llm:
  providers:
    openai:
      api_key: ${OPENAI_API_KEY}
      base_url: https://your-proxy.com/v1  # API relay URL
      model: gpt-4o
```

**Platform webhook not receiving messages**

1. Confirm the server is publicly accessible
2. Check firewall rules for the relevant port
3. Use ngrok or frp for intranet tunneling (development stage):

```bash
ngrok http 16060
# Use the HTTPS URL provided by ngrok to configure the webhook
```

### Performance Issues

**Slow responses**

- Enable semantic cache to reduce duplicate requests:
  ```yaml
  llm:
    cache:
      enabled: true
      ttl: 24h
  ```
- Use lower-cost models for simple tasks
- Check knowledge base size, adjust `chunk_size` and `top_k` as needed

**High memory usage**

- Reduce the number of messages retained in context:
  ```yaml
  compaction:
    enabled: true
    max_messages: 30
    keep_recent: 5
  ```
- Reduce semantic cache entry count:
  ```yaml
  llm:
    cache:
      max_entries: 1000
  ```
