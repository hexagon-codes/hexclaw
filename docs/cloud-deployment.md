# 云端部署与日常运维

适用于本仓库包含云端连接支持的源码构建。当前镜像为 Linux amd64，包含 Pandoc、Typst、中文和数学字体、Python 及 SymPy。服务存储使用 SQLite；同一数据目录只运行一个实例。单机日常运行推荐 Docker Compose；已有 Kubernetes 集群使用相同镜像和独立 PVC。

## Docker Compose

日常服务器拉取已发布镜像，沿用现有项目名、数据卷和完整 Compose 文件集合。在部署目录的私有 `.env` 中设置 `HEXCLAW_IMAGE=ghcr.io/hexagon-codes/hexclaw@sha256:<实际摘要>`，摘要从对应构建产物取得；不要原样使用占位符。

```bash
docker compose pull hexclaw
docker compose up -d --no-build hexclaw
docker compose ps
```

发布镜像使用 `ghcr.io/hexagon-codes/hexclaw:<版本>` 和 `sha-<完整提交 SHA>` 标签；`latest` 只指向正式稳定版，日常部署仍固定 digest。上述命令不表示镜像已发布或自动部署已启用。

本地源码开发可执行 `docker compose build hexclaw`，默认镜像名为 `hexclaw:dev`；随后执行 `docker compose up -d --no-build hexclaw`。源码构建与日常服务器拉取使用同一 Compose 文件，后者不得省略 `--no-build`。

`HOME=/data`，命名卷覆盖整个 `/data`。首次启动创建 `/data/.hexclaw/hexclaw.yaml` 和随机 `server.api_token`；正常重启和重建保留令牌、配置及数据库身份。缺少模型时服务仍可启动，连接 Desktop 后在当前远端的「模型服务」配置模型。连接成功不表示模型或钉钉已经配置完成。

在管理员自己的终端读取访问令牌并粘贴到 Desktop 的远端连接弹窗；不要保存到截图、工单或日志：

```bash
docker compose exec -T hexclaw python3 -c 'import yaml; print(yaml.safe_load(open("/data/.hexclaw/hexclaw.yaml"))["server"]["api_token"])'
```

远端 URL 为 `http://服务器地址:16060`，也可配置已有 HTTPS 反向代理的路径前缀，例如 `https://example.com/hexclaw`。代理需要保留 Authorization、转发 WebSocket Upgrade 并关闭 SSE 缓冲；移除对外前缀后转发至 HexClaw。`/health` 和 `/version` 是公开就绪信息，业务请求使用 `Authorization: Bearer <token>`。Desktop 原生层负责附加令牌，不放到资产 URL 查询参数。

模型、渠道、孩子、任务和产物属于选中的服务器；连接或切换不会自动上传本机配置、数据或规则。通过该服务器的设置配置模型与钉钉实例，再创建或配置 TutorAgent。迁移同一钉钉实例时先停止旧实例消费，避免两台服务同时争用消息；不要把两套独立任务库同时绑定到同一实际渠道进行验收。

### 可选 HTTPS 路径前缀

[`deploy/docker-compose.https.yml`](../deploy/docker-compose.https.yml) 提供独立 Nginx gateway，默认对外端口 16061，不占用已有 443。准备包含 `letsencrypt/` 证书目录和 `nginx.conf` 的私有目录，在部署 `.env` 设置 `HEXCLAW_TLS_DIR`；将 [`deploy/nginx.conf.example`](../deploy/nginx.conf.example) 中 `HEXCLAW_CERT_NAME` 替换为实际证书名。示例提供 `/hexclaw/` 前缀、WebSocket Upgrade 和不缓冲的 SSE，上传大小仍由应用自身决定。

gateway 使用 Docker DNS 动态解析 `hexclaw`，后端容器重建后继续解析新地址，不依赖人工重启代理。示例的 upstream `resolve` 需要 Nginx 1.27.3 以上；override 使用满足该条件的版本。参见 [Nginx upstream resolve](https://nginx.org/en/docs/http/ngx_http_upstream_module.html#resolve)。

```bash
docker compose -f docker-compose.yml -f deploy/docker-compose.https.yml up -d
```

域名可使用已有可信证书；裸公网 IP 可按 [Let’s Encrypt 官方说明](https://letsencrypt.org/2026/03/11/shorter-certs-certbot) 使用 Certbot 5.4 以上的 `--ip-address` 与 `--preferred-profile shortlived` 获取约 6 天证书。`standalone` 验证需要公网 80 端口可达且空闲；已有 Web 服务则使用 webroot。配置每日至少两次 `certbot renew`，成功后执行 `docker compose ... exec -T gateway nginx -s reload`；同时保存证书续期目录，不能只复制一次证书后不续期。验证正常证书链，不以跳过校验代替 HTTPS 验收。

使用多个 override 时，在部署 `.env` 的 `COMPOSE_FILE` 保存完整文件列表（Linux 用冒号分隔），后续更新／停止／备份沿用同一项目与配置。证书私钥单独保存在 `HEXCLAW_TLS_DIR/letsencrypt`，不提交仓库；需要恢复 HTTPS 时同时恢复该目录或重新签发。

### 可选首次配置种子

已有初始化配置可以挂到 `/run/hexclaw-init/hexclaw.yaml:ro`，其中应设置 `server.host: 0.0.0.0`、`server.port: 16060`、非空 `server.api_token` 和容器内存储路径。使用单独 Compose override：

```yaml
services:
  hexclaw:
    volumes:
      - ./initial-config.yaml:/run/hexclaw-init/hexclaw.yaml:ro
```

种子仅在目标文件不存在时复制。运行配置必须位于可写卷，不把只读 YAML 挂到运行文件上。完成初始化后可以移除种子挂载。不要通过日常 `DEEPSEEK_API_KEY` 等环境变量注入 Provider；这些变量会在启动时补回配置，导致已删除的模型重新出现。配置保存后使用原生层或业务 API 的同一持久化合同。

### 更新和停止

更新前记录当前 digest，取得一致备份并核对新版本迁移兼容性；将 `.env` 的 `HEXCLAW_IMAGE` 改为本次实际 digest 后执行：

```bash
docker compose pull hexclaw
docker compose up -d --no-build hexclaw
docker compose logs --tail 100 hexclaw
# 停止服务，保留数据卷
docker compose stop hexclaw
```

拉取失败不停止原服务；新版本已修改数据后，不根据健康检查失败直接切回旧镜像或覆盖旧备份。无迁移或明确向后兼容时可保留当前数据恢复原 digest；不兼容迁移须保留当前数据及调用回执后按该版本恢复方案处理。自动部署、定时异机备份仍须完成对应配置和验收，不把手动命令当作已启用自动化。

Compose 留出 60 秒停止窗口，覆盖服务当前 30 秒收尾预算。已接纳任务按原回执恢复，结果未知的调用不自动重发。不要使用 `down -v` 删除交付数据，也不要同时运行两个共享该卷的容器。

## 按提交自动部署

源码已提供 `.github/workflows/deploy.yml` 和 `scripts/ops/deploy.py`。当前尚未在交付服务器启用，脚本的完整 Docker 故障恢复验证仍待完成。开启前先完成隔离恢复并确定部署分支；不要将源码存在或语法检查通过等同于运维验收。

部署目录放置私有 `deployment-target.json`，按该服务器的实际项目填写。例如：

```json
{
  "repository": "https://github.com/hexagon-codes/hexclaw.git",
  "ref": "refs/heads/<已确认的部署分支>",
  "project_name": "<当前Compose项目名>",
  "compose_files": ["docker-compose.yml", "docker-compose.https.yml"],
  "env_files": [".env"],
  "backup_directory": "backups",
  "minimum_free_bytes": 0
}
```

`compose_files` 必须使用该项目现有文件，不照抄示例中的 HTTPS 文件名；也可省略该字段并沿用 `.env` 的完整 `COMPOSE_FILE`。`minimum_free_bytes` 为运维确定的预留空间，`0` 不额外设置空间门槛，实际备份写入失败仍停止更新。服务器需有 Python 3、Git、Docker Compose 和镜像拉取权限；私有仓库的 Git／GHCR 凭据由服务器自己的登录配置提供。

GitHub 配置项：

| 位置 | 配置 |
| --- | --- |
| Repository variables | `HEXCLAW_DEPLOY_BRANCH`：上述同一分支；`HEXCLAW_DEPLOY_ENABLED=true`：完成恢复验收后启用 |
| Environment `hexclaw-cloud` variables | `HEXCLAW_DEPLOY_PROJECT`：服务器部署目录绝对路径 |
| Environment `hexclaw-cloud` secrets | `HEXCLAW_DEPLOY_HOST`、`HEXCLAW_DEPLOY_SSH_KEY`、`HEXCLAW_DEPLOY_KNOWN_HOSTS`：沿用服务器已有 SSH 身份及主机信任信息 |

CI 对指定分支的 push 完成且成功后，构建对应的完整提交 SHA，推送 `sha-<commit>` 镜像；部署任务传递构建返回的 digest。构建可取消过期任务，进入服务器变更的部署不被下一次提交自动取消。服务端与备份共用 `.hexclaw-maintenance.lock`，取得锁后及备份完成后再次向仓库核对绑定分支的当前提交，过期任务只记录 `superseded`。

手工发布同样调用该脚本，使用已经核实的提交和 digest，不另起绕过锁的自动更新命令：

```bash
python3 scripts/ops/deploy.py \
  --project-dir <当前部署目录> --target-config <当前部署目录>/deployment-target.json \
  --image ghcr.io/hexagon-codes/hexclaw@sha256:<镜像摘要> \
  --commit <完整提交SHA> --run-id <发布记录编号>
```

脚本先拉取镜像并核对 OCI revision，然后检查原项目、原卷及配置；停止旧进程、完整备份、只重建 HexClaw，再启动唯一的新进程。就绪检查包含健康端点、受保护配置／知识库读取、原 backend ID 和配置摘要，记录前后 schema 版本。该检查不发送模型或 IM 请求，不能替代实际批改、图片附件和语义召回验收。

成功后 `.hexclaw-image.env` 固定当前 digest，`.hexclaw-deploy/current.json` 和独立历史回执记录结果。此后手工 Compose 操作也须在原参数之后追加 `--env-file .hexclaw-image.env`，并保留原 `.env`，避免退回其中的旧镜像。部署回执、展开后的 Compose 配置和备份属于私有运维资料，不提交 Git。

新程序尚未启动时的失败使用冻结的原 Compose 配置和原镜像恢复原卷；一旦请求启动，新迁移和业务副作用便可能已经发生，失败标记 `recovery_required`，保留现场及备份。管理员按前后迁移记录判定无迁移、向后兼容或不兼容，再选择原镜像保留现数据、前向修复或隔离恢复；不自动覆盖数据库。主机断电或 SIGKILL 后同样先核对持久回执，不盲重发部署或业务请求。

## Kubernetes

清单位于 [`docker/kubernetes.yaml`](../docker/kubernetes.yaml)。先把同一已构建镜像推送到集群可拉取的仓库并将清单的 `image` 改成其不可变标签或摘要；本地 K3s 也可用 `k3s ctr images import` 导入镜像。清单使用单副本、`Recreate`、60 秒终止窗口及整个 HOME 的 PVC。需要提供支持 SQLite 文件锁和同步写入语义的 StorageClass；不使用多个 Pod 共享的网络文件系统。资源 requests 是调度起点，不是模型推理或真实作业的容量承诺。

```bash
kubectl create namespace hexclaw
# 可选种子；文件包含 server.api_token，不从命令行 literal 传真实凭据
kubectl -n hexclaw create secret generic hexclaw-init --from-file=hexclaw.yaml=initial-config.yaml
kubectl -n hexclaw apply -f docker/kubernetes.yaml
kubectl -n hexclaw rollout status deployment/hexclaw --timeout=180s
kubectl -n hexclaw port-forward service/hexclaw 16060:16060
```

没有种子时省略 Secret 命令；入口脚本创建空模型配置与持久令牌。配置保存只写 PVC 中的 YAML，不修改 Secret，不把 ConfigMap/Secret `subPath` 当作运行文件。`/health` 探针只说明进程就绪；业务验收仍需要受保护请求、模型结果、上传及实际产物。

更新通过 `kubectl set image deployment/hexclaw hexclaw=<新镜像>`。`Recreate` 用于版本更新先停后启；维护或替换 Pod 时先 scale 到 0 并确认旧 Pod 完全退出，再 scale 到 1。不要强制删除未退出的 Pod后在同一 PVC 启动第二个进程。[Kubernetes Deployment 更新语义](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#recreate-deployment)。

## Ollama 的地址归属

默认 Compose 不包含 Ollama，也不自动下载模型。云端可直接使用已配置的 Embedding API 和服务器持久化索引；未配置有效 Embedding 时，关键词检索与向量检索的可用状态分开判断。2 GB 日常服务器不安装 Ollama。

「默认地址」指当前 HexClaw 后端所在运行环境的 `127.0.0.1:11434`。Docker 容器或 Pod 的回环不是宿主机，也不是 Desktop 所在电脑。使用宿主机或另一容器的 Ollama 时在模型服务设置「自定义地址」，填写该容器可访问的完整 HTTP/HTTPS 地址。目标可以包含路径前缀，管理、下载及已关联 Provider 的推理一起保存。目标变更不改变已发送的模型调用；下载断线只查询原操作，服务重启后无法确认的下载不会自动重下。

仓库提供可选 [`deploy/docker-compose.ollama.yml`](../deploy/docker-compose.ollama.yml)，与根 Compose 合并启动后，Ollama 模型保存在独立卷，HexClaw 自定义目标填写 `http://ollama:11434`。它不暴露宿主端口；管理、拉取模型与推理均经当前 HexClaw 后端访问。

```bash
docker compose -f docker-compose.yml -f deploy/docker-compose.ollama.yml up -d
```

按服务器实际内存选择模型；小模型的文本连通验证不能当作图片批改能力。模型文件不在 HexClaw 的 HOME 卷内，备份／迁移时按需要另行保留 `ollama-data`；不要把 Desktop 的模型目录误当成服务器模型。

Desktop 只对其管理的本机服务提供恢复操作；远端服务生命周期由 Compose、Kubernetes 或服务器管理员管理。

## 完整备份与恢复

仓库提供 [`scripts/ops/backup.py`](../scripts/ops/backup.py)，需要部署主机的 Python 3、Docker Compose；异机传输另需 SSH，接收主机需 `sha256sum`。脚本已落源码，尚未在交付服务器启用定时任务或完成恢复验收。

```bash
python3 scripts/ops/backup.py backup \
  --project-dir /opt/hexclaw --output /opt/hexclaw/backups \
  --remote-host <已配置的异机SSH别名> --remote-dir <异机备份目录>
```

沿用部署目录 `.env` 的完整 `COMPOSE_FILE` 和项目身份；需要覆盖时使用重复的 `--compose-file`、`--env-file` 及 `--project-name`。不要只传根 Compose 而丢失 HTTPS override。HOME 之外的其他业务文件用重复的 `--include` 指定；当前 gateway 的证书／配置 bind mount 会按实际容器自动归档。

脚本在同一项目的 `.hexclaw-maintenance.lock` 内停止原容器，完整归档 `/data` 与部署文件，然后启动原容器；不会用变动后的 `.env` 重建服务。停止期间即使归档失败也尝试恢复原容器，恢复失败单独报错。恢复服务之后才计算摘要并传输异机副本，上传失败保留本地副本和失败状态，不自动删除旧备份。

每次生成私有 `.tar` 归档及同名 JSON 状态，分别记录 `local_complete`、`service_restored`、`remote_complete`。缺少异机参数时只完成本地归档，不能当作容灾备份；调度器须核对预期状态，而非只看到文件存在。正常终止会尝试恢复服务；强制杀进程或主机断电后需核对原容器和状态文件。

先在恢复主机取得状态 JSON 中的 SHA256，并准备归档记录的同一镜像，再恢复到全新隔离卷：

```bash
python3 scripts/ops/backup.py restore \
  --archive <本次归档.tar> --sha256 <状态文件中的摘要> \
  --volume hexclaw-restore-<本次唯一标识>
```

恢复先核对整个归档和内部文件摘要，拒绝覆盖已有卷；失败保留隔离卷供排查，不删除交付数据。脚本不启动恢复副本，不接入 IM 或自动执行任务。部署文件留在归档的 `deployment.tar.gz`、`compose.json` 与 `deployment-paths.json` 中供管理员核对；不能直接将恢复副本作为第二个业务消费者启动。完成隔离文件／数据库核对及恢复点后副作用对账后，再按所需迁移兼容方案恢复业务。

定时周期、异机目的地和保留策略确定后再启用调度；当前源码不自动创建 cron／systemd 任务，也不删除备份。

备份须包含记忆目录的 `.event-receipts/` 和数据库中的 Outbox／题目资产回执，不能只复制可见记忆正文。备份需要数据库及 WAL、原图、批注、导出文件、YAML、`master.key`、AGENTS、记忆、技能、工作区和任务回执保持同一时点。最简单的单机方法是停止该实例后归档整个 HOME 卷，再启动：

```bash
docker compose stop hexclaw
docker compose run --rm -T --no-deps --entrypoint tar hexclaw -C /data -czf - . </dev/null > hexclaw-home-backup.tar.gz
docker compose up -d hexclaw
chmod 600 hexclaw-home-backup.tar.gz
```

备份只停止写入该 HOME 的 HexClaw 实例；辅助容器不分配终端且关闭标准输入，避免在 SSH 脚本中消费后续命令。自动化脚本应在退出处理里恢复原服务，并单独检查归档与恢复结果，不能仅凭脚本退出码判断全部步骤已执行。

归档包含真实凭据与家庭数据，保存在管理员自己的备份位置。恢复到新建的隔离卷，不能覆盖仍在运行的原交付卷。恢复文件检查可使用不启动 HexClaw 的容器完成；如果启动恢复副本，先在该副本配置中停用所有 IM 实例、定时任务及投递 worker，避免与原实例消费或发送同一消息。凭据与 `master.key` 必须配套保留，不能只恢复数据库后重新生成密钥。Kubernetes 使用相同的停机与完整 PVC 快照原则。

## 文件分类与公共规则

服务的 `.hexclaw/hexclaw.yaml` 为运行配置，`master.key` 为静态加密密钥，`AGENTS.md` 为当前服务公共工作规则。首次缺失时初始化默认 AGENTS，已有文件不覆盖；空文件或读取异常使用内嵌默认，不中断业务。每个新任务读取并冻结本次正文和摘要，已持久化 K12 阶段恢复原规则。SOUL、MEMORY 与 Agent 路由配置不迁入 AGENTS。固定 OCR、判分和结构化结果合同不由表达规则覆盖。

Desktop 本机的 `.hexclaw/auth.json` 仅由原生层管理本机和远端连接令牌；不是上传到服务器的部署文件。本机和云端各自维护公共规则，切换不复制。
