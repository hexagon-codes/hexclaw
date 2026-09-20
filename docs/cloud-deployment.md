# 云端部署与日常运维

适用于本仓库包含云端连接支持的源码构建。当前镜像为 Linux amd64，包含 Pandoc、Typst、中文和数学字体、Python 及 SymPy。服务存储使用 SQLite；同一数据目录只运行一个实例。单机日常运行推荐 Docker Compose；已有 Kubernetes 集群使用相同镜像和独立 PVC。

## Docker Compose

在源码目录执行：

```bash
docker compose build
docker compose up -d
docker compose ps
```

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

```bash
docker compose build
docker compose up -d --force-recreate
docker compose logs --tail 100 hexclaw
# 停止服务，保留数据卷
docker compose stop
```

Compose 留出 60 秒停止窗口，覆盖服务当前 30 秒收尾预算。已接纳任务按原回执恢复，结果未知的调用不自动重发。不要使用 `down -v` 删除交付数据，也不要同时运行两个共享该卷的容器。

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

「默认地址」指当前 HexClaw 后端所在运行环境的 `127.0.0.1:11434`。Docker 容器或 Pod 的回环不是宿主机，也不是 Desktop 所在电脑。使用宿主机或另一容器的 Ollama 时在模型服务设置「自定义地址」，填写该容器可访问的完整 HTTP/HTTPS 地址。目标可以包含路径前缀，管理、下载及已关联 Provider 的推理一起保存。目标变更不改变已发送的模型调用；下载断线只查询原操作，服务重启后无法确认的下载不会自动重下。

仓库提供可选 [`deploy/docker-compose.ollama.yml`](../deploy/docker-compose.ollama.yml)，与根 Compose 合并启动后，Ollama 模型保存在独立卷，HexClaw 自定义目标填写 `http://ollama:11434`。它不暴露宿主端口；管理、拉取模型与推理均经当前 HexClaw 后端访问。

```bash
docker compose -f docker-compose.yml -f deploy/docker-compose.ollama.yml up -d
```

按服务器实际内存选择模型；小模型的文本连通验证不能当作图片批改能力。模型文件不在 HexClaw 的 HOME 卷内，备份／迁移时按需要另行保留 `ollama-data`；不要把 Desktop 的模型目录误当成服务器模型。

Desktop 只对其管理的本机服务提供恢复操作；远端服务生命周期由 Compose、Kubernetes 或服务器管理员管理。

## 完整备份与恢复

备份需要数据库及 WAL、原图、批注、导出文件、YAML、`master.key`、AGENTS、记忆、技能、工作区和任务回执保持同一时点。最简单的单机方法是停止该实例后归档整个 HOME 卷，再启动：

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
