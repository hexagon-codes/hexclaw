#!/usr/bin/env python3
"""按固定镜像摘要更新单个 Compose 服务，保留部署回执和发布前完整备份。"""

import argparse
import copy
import datetime
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import shutil
import signal
import socket
import sys
import tempfile
import time
import uuid
import urllib.parse

from backup import OperationError, backup, complete_service_command, compose_args, deployment_paths, docker_json, maintenance_lock, run, sha256, write_json


def atomic_bytes(path, content):
    fd, temporary = tempfile.mkstemp(prefix=".deploy-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def current_target(configuration):
    # 分支由服务器环境绑定，不能由一个迟到的 Actions 任务自行指定历史 tag。
    ref = configuration["ref"]
    if not ref.startswith("refs/heads/"):
        raise OperationError("The deployment target must be a branch reference")
    output = run(["git", "ls-remote", "--exit-code", configuration["repository"], ref]).decode().splitlines()
    matches = [line.split()[0] for line in output if len(line.split()) == 2 and line.split()[1] == ref]
    if len(matches) != 1:
        raise OperationError("The deployment target cannot be resolved")
    return matches[0]


def configuration_digest(configuration, compose):
    content = json.dumps({"target": configuration, "compose": compose}, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(content.encode()).hexdigest()


def deployment_tools_digest():
    digest = hashlib.sha256()
    for name in ("deploy.py", "backup.py"):
        digest.update(name.encode())
        digest.update(Path(__file__).with_name(name).read_bytes())
    return digest.hexdigest()


def pending_deployment(state_dir):
    """恢复未决状态不能被后来的部署当作健康基线。"""
    pending = []
    for path in state_dir.glob("*.json"):
        if not re.fullmatch(r"\d{8}T\d{6}Z-[0-9a-f]{8}\.json", path.name):
            continue
        saved = json.loads(path.read_text())
        if saved.get("resolved"):
            continue
        status = saved.get("status")
        unresolved = status in {"stopping", "starting", "recovery_required", "deployed_retention_pending"}
        unresolved = unresolved or (status == "failed_before_start" and not saved.get("original_service_restored"))
        unresolved = unresolved or (status == "preparing" and saved.get("previous_image_id"))
        if unresolved:
            pending.append((path, saved))
    if len(pending) > 1:
        raise OperationError("Multiple deployment receipts require recovery; inspect them before updating")
    return pending[0] if pending else None


class DockerConnection(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect("/var/run/docker.sock")


def ensure_runtime_system_paths(configuration, candidate):
    """保留目标已声明的运行路径策略，未配置的部署继续使用 Compose 默认值。"""
    paths = configuration.get("runtime_system_paths")
    if paths is None:
        return candidate
    if (not isinstance(paths, dict) or set(paths) != {"MaskedPaths", "ReadonlyPaths"}
            or any(not isinstance(values, list) or any(not isinstance(value, str) for value in values)
                   for values in paths.values())):
        raise OperationError("The runtime system path configuration is invalid")
    if all(candidate["HostConfig"].get(key) == value for key, value in paths.items()):
        return candidate
    if candidate["State"]["Running"]:
        raise OperationError("Runtime system paths must be applied before starting the replacement")
    body = copy.deepcopy(candidate["Config"])
    body["Image"] = candidate["Image"]
    body["HostConfig"] = copy.deepcopy(candidate["HostConfig"])
    body["HostConfig"].update(copy.deepcopy(paths))
    body["NetworkingConfig"] = {"EndpointsConfig": {}}
    for network, endpoint in candidate["NetworkSettings"]["Networks"].items():
        selected = {"Aliases": endpoint.get("Aliases") or []}
        if endpoint.get("IPAMConfig"):
            selected["IPAMConfig"] = endpoint["IPAMConfig"]
        body["NetworkingConfig"]["EndpointsConfig"][network] = selected
    name = candidate["Name"].lstrip("/")
    version = run(["docker", "version", "--format", "{{.Server.APIVersion}}" ]).decode().strip()
    run(["docker", "rm", candidate["Id"]])
    creation_error = None
    connection = DockerConnection("localhost", timeout=60)
    try:
        connection.request("POST", "/v" + version + "/containers/create?name=" + urllib.parse.quote(name),
                           json.dumps(body), {"Content-Type": "application/json"})
        response = connection.getresponse()
        response.read()
        if response.status >= 300:
            creation_error = OperationError("Docker create returned status " + str(response.status))
    except Exception as error:
        creation_error = error
    finally:
        connection.close()
    # 创建回执丢失时仅核对原名称，不自动创建第二个容器。
    try:
        current = json.loads(run(["docker", "inspect", name]))[0]
    except OperationError as error:
        raise OperationError("The replacement has no matching container after applying runtime paths") from (creation_error or error)
    if current["Image"] != candidate["Image"] or mounted_data(current) != mounted_data(candidate):
        raise OperationError("Applying runtime paths changed the image or persistent data mount")
    if current["Config"].get("Labels") != candidate["Config"].get("Labels") or current["Config"].get("Env") != candidate["Config"].get("Env"):
        raise OperationError("Applying runtime paths changed Compose ownership or environment")
    for key, value in paths.items():
        if current["HostConfig"].get(key) != value:
            raise OperationError("The replacement runtime system paths differ: " + key)
    return current


def copy_state_file(source, destination):
    fd, temporary = tempfile.mkstemp(prefix=".current-state-", dir=destination.parent)
    try:
        with os.fdopen(fd, "wb") as output, source.open("rb") as input_file:
            shutil.copyfileobj(input_file, output, 1024 * 1024)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, destination)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def retain_development_state(args, configuration, report, report_file, current_image):
    """仅处理有本项目部署回执归属的历史，独立业务备份不参与清理。"""
    if configuration.get("development_retention") != "current_only":
        return None
    output = (args.project_dir / configuration.get("backup_directory", "backups")).resolve()
    output.mkdir(parents=True, exist_ok=True)
    state_dir = report_file.parent
    history = []
    terminal = {"deployed", "preflight_failed", "failed_before_start", "superseded"}
    for path in state_dir.glob("*.json"):
        if path == report_file or not re.fullmatch(r"\d{8}T\d{6}Z-[0-9a-f]{8}\.json", path.name):
            continue
        saved = json.loads(path.read_text())
        status = saved.get("status")
        recoverable = status == "failed_before_start" and not saved.get("original_service_restored")
        if ((status in terminal and not recoverable) or saved.get("resolved")) and "commit" in saved and "image" in saved:
            history.append((path, saved))
    report["retention_stage"] = "snapshot"
    write_json(report_file, report)
    current_configuration = json.loads(run(compose_args(args) + ["config", "--format", "json"], cwd=args.project_dir))
    current_compose = state_dir / "current.compose.json"
    write_json(current_compose, current_configuration)
    # 取更新后的完整一致快照；失败时原部署备份和镜像都保持。
    with tempfile.TemporaryDirectory(prefix=".current-state-", dir=output) as directory:
        snapshot_args = argparse.Namespace(**vars(args))
        snapshot_args.output = Path(directory)
        recovery_compose = report_file.with_suffix(".compose.json")
        snapshot_args.include = [path for path in args.include if path != recovery_compose] + [current_compose]
        latest = backup(snapshot_args, lock_held=True, emit_report=False)
        source = Path(directory) / latest["archive"]
        if sha256(source) != latest["sha256"]:
            raise OperationError("The current deployment snapshot digest changed")
        destination = output / "latest-deployment.tar"
        copy_state_file(source, destination)
        latest["archive"] = destination.name
        latest["retained"] = True
        write_json(output / "latest-deployment.json", latest)
    report["current_backup"] = latest
    report["post_snapshot_verification"] = wait_service(compose_args(args), args.project_dir, current_image, report["verification"])
    report["retention_stage"] = "cleanup"
    write_json(report_file, report)

    removed_archives = 0
    images = {report.get("previous_image_id")}
    references = {}
    for _, saved in history + [(report_file, report)]:
        images.update((saved.get("previous_image_id"), saved.get("image_id")))
        for image_id, reference in ((saved.get("previous_image_id"), saved.get("previous_image")),
                                    (saved.get("image_id"), saved.get("image"))):
            if image_id and reference:
                references.setdefault(image_id, set()).add(reference)
        for key in ("backup", "current_backup"):
            entry = saved.get(key) or {}
            name = entry.get("archive", "")
            if not name or Path(name).name != name or name == "latest-deployment.tar":
                continue
            archive = output / name
            if archive.is_file():
                archive.unlink()
                removed_archives += 1
            metadata = archive.with_suffix(".json")
            if metadata.is_file():
                metadata.unlink()
    containers = run(["docker", "ps", "--all", "--quiet"]).decode().split()
    used_images = set(run(["docker", "inspect", "--format", "{{.Image}}", *containers]).decode().split()) if containers else set()
    available = set(run(["docker", "image", "ls", "--no-trunc", "--quiet"]).decode().split())
    removed_images, retained_images, shared_images = 0, 0, 0
    for image in sorted(value for value in images if value and value != current_image):
        if image in used_images:
            retained_images += 1
            continue
        if image not in available:
            continue
        # 只解除本项目回执所属仓库的引用，保留其他应用共享的标签。
        tags = json.loads(run(["docker", "image", "inspect", "--format", "{{json .RepoTags}}", image])) or []
        digests = json.loads(run(["docker", "image", "inspect", "--format", "{{json .RepoDigests}}", image])) or []
        owned = references.get(image, set())
        repositories = {image_repository(value) for value in owned} - {None}
        for reference in tags + digests:
            if reference not in owned and image_repository(reference) not in repositories:
                continue
            if image not in set(run(["docker", "image", "ls", "--no-trunc", "--quiet"]).decode().split()):
                break
            run(["docker", "image", "rm", reference])
        if image in set(run(["docker", "image", "ls", "--no-trunc", "--quiet"]).decode().split()):
            remaining = docker_json("image", "inspect", image)[0]
            if remaining.get("RepoTags") or remaining.get("RepoDigests"):
                shared_images += 1
                continue
            run(["docker", "image", "rm", image])
        removed_images += 1
    # 清理结束后才收敛旧部署回执；在途任务和非部署备份均保留。
    for path, _ in history:
        recovery = path.with_suffix(".compose.json")
        if recovery.is_file():
            recovery.unlink()
        path.unlink()
    recovery = report_file.with_suffix(".compose.json")
    if recovery.is_file():
        recovery.unlink()
    if report.get("backup"):
        report["backup"]["retained"] = False
    report.pop("retention_stage", None)
    return {"policy": "current_only", "current_archive": "latest-deployment.tar",
            "removed_archives": removed_archives, "removed_images": removed_images,
            "preserved_in_use_images": retained_images, "preserved_shared_images": shared_images}


def image_repository(reference):
    if reference.startswith("sha256:"):
        return None
    repository = reference.split("@", 1)[0]
    if ":" in repository.rsplit("/", 1)[-1]:
        repository = repository.rsplit(":", 1)[0]
    return repository


def mounted_data(container):
    mounts = [item for item in container["Mounts"] if item["Destination"] == "/data"]
    if len(mounts) != 1:
        raise OperationError("Deployment requires one existing /data mount")
    item = mounts[0]
    return item["Type"], item["Source"], item.get("RW", True)


def configured_data(configuration):
    mounts = [item for item in configuration["services"]["hexclaw"].get("volumes", [])
              if item["target"] == "/data"]
    if len(mounts) != 1:
        raise OperationError("The Compose configuration must retain its existing /data mount")
    item = mounts[0]
    source = item["source"]
    if item["type"] == "volume":
        name = configuration["volumes"][source]["name"]
        source = docker_json("volume", "inspect", name)[0]["Mountpoint"]
    return item["type"], source, not item.get("read_only", False)


def only_container(command, project):
    ids = run(command + ["ps", "--all", "--quiet", "hexclaw"], cwd=project).decode().split()
    if len(ids) != 1:
        raise OperationError("Deployment requires exactly one HexClaw container")
    return docker_json("inspect", ids[0])[0]


def inspect_service(container):
    # 令牌只在容器内部读取并用于请求，不进入宿主参数、stdout 或部署记录。
    script = r'''
import hashlib, json, pathlib, sqlite3, sys, urllib.request, yaml
arguments = json.loads(sys.argv[1])
path = pathlib.Path.home() / '.hexclaw/hexclaw.yaml'
for index, arg in enumerate(arguments):
    if arg == '--config' and index + 1 < len(arguments): path = pathlib.Path(arguments[index + 1])
    elif arg.startswith('--config='): path = pathlib.Path(arg.split('=', 1)[1])
body = path.read_bytes()
cfg = yaml.safe_load(body)
server = cfg.get('server', {})
base = 'http://127.0.0.1:' + str(server.get('port', 16060))
def read(route, auth=False):
    req = urllib.request.Request(base + route)
    if auth: req.add_header('Authorization', 'Bearer ' + server['api_token'])
    with urllib.request.urlopen(req, timeout=5) as response: return json.load(response)
if read('/health').get('status') != 'healthy': raise RuntimeError('Not ready')
configuration = read('/api/v1/config', True)
identity = configuration.get('backend_id')
if not identity: raise RuntimeError('Missing backend identity')
documents = read('/api/v1/knowledge/documents', True)
if not isinstance(documents, dict): raise RuntimeError('Invalid knowledge response')
database = pathlib.Path(cfg.get('storage', {}).get('sqlite', {}).get('path') or str(pathlib.Path.home() / '.hexclaw/data.db')).expanduser().resolve()
with sqlite3.connect(database.as_uri() + '?mode=ro', uri=True, timeout=5) as connection:
    schema = connection.execute('SELECT COALESCE(MAX(version),0) FROM schema_migrations').fetchone()[0]
print(json.dumps({'backend_id': identity, 'config_sha256': hashlib.sha256(body).hexdigest(),
                  'protected_read': True, 'schema_version': schema}))
'''
    raw = run(["docker", "exec", container["Id"], "python3", "-c", script,
               json.dumps(container["Config"].get("Cmd") or [])])
    return json.loads(raw)


def wait_service(command, project, image_id, baseline):
    deadline = time.monotonic() + 120
    while True:
        container = only_container(command, project)
        if container["Image"] != image_id:
            raise OperationError("The started container does not use the requested image")
        try:
            state = inspect_service(container)
            if any(state[key] != baseline[key] for key in ("backend_id", "config_sha256", "protected_read")):
                raise OperationError("Backend identity or persistent configuration changed")
            return state
        except OperationError:
            if not container["State"]["Running"] or time.monotonic() >= deadline:
                raise
            time.sleep(1)


def deploy(args):
    args.project_dir = args.project_dir.resolve()
    settings_path = args.target_config.resolve()
    if not re.fullmatch(r"[0-9a-f]{40}", args.commit):
        raise OperationError("A full commit SHA is required")
    if not re.fullmatch(r"[^\s@]+@sha256:[0-9a-f]{64}", args.image):
        raise OperationError("An immutable image digest is required")
    args.include = [Path(value) for value in getattr(args, "include", [])]
    args.remote_host = None
    args.remote_dir = None

    with maintenance_lock(args.project_dir):
        configuration = json.loads(settings_path.read_text())
        image_file = args.project_dir / ".hexclaw-image.env"
        args.compose_file = [args.project_dir / item for item in configuration.get("compose_files", [])]
        args.env_file = [args.project_dir / item for item in configuration.get("env_files", [])]
        if not args.env_file and (args.project_dir / ".env").is_file():
            args.env_file.append(args.project_dir / ".env")
        if image_file.is_file():
            args.env_file.append(image_file)
        args.project_name = configuration.get("project_name")
        command = compose_args(args)
        original_files = list(args.compose_file) + list(args.env_file)
        state_dir = args.project_dir / ".hexclaw-deploy"
        state_dir.mkdir(exist_ok=True)
        pending = pending_deployment(state_dir)
        if pending:
            pending_file, saved = pending
            if (saved.get("status") != "deployed_retention_pending"
                    or saved.get("commit") != args.commit or saved.get("image") != args.image):
                raise OperationError("A previous deployment requires recovery; its data and recovery files are preserved")
            current = only_container(command, args.project_dir)
            target_image = docker_json("image", "inspect", args.image)[0]
            if mounted_data(current) != configured_data(json.loads(run(command + ["config", "--format", "json"], cwd=args.project_dir))):
                raise OperationError("The pending deployment no longer uses the configured data mount")
            saved["verification"] = wait_service(command, args.project_dir, target_image["Id"], saved["baseline"])
            args.output = args.project_dir / configuration.get("backup_directory", "backups")
            recovery_config = pending_file.with_suffix(".compose.json")
            args.include = [settings_path] + original_files + args.include
            if recovery_config.is_file():
                args.include.append(recovery_config)
            saved["original_actions_run"] = saved.get("original_actions_run", saved.get("actions_run"))
            saved["actions_run"] = args.run_id
            saved["resumed"] = True
            try:
                saved["retention"] = retain_development_state(args, configuration, saved, pending_file, target_image["Id"])
                saved["status"] = "deployed"
                write_json(pending_file, saved)
                atomic_bytes(state_dir / "current.json", (json.dumps(saved, ensure_ascii=False, indent=2) + "\n").encode())
                return saved
            except BaseException:
                saved["status"] = "deployed_retention_pending"
                write_json(pending_file, saved)
                raise

        old = only_container(command, args.project_dir)
        baseline = inspect_service(old)
        data_mount = mounted_data(old)
        if not data_mount[2]:
            raise OperationError("The existing data mount is read-only")
        frozen_configuration = json.loads(run(command + ["config", "--format", "json"], cwd=args.project_dir))
        if configured_data(frozen_configuration) != data_mount:
            raise OperationError("The Compose configuration no longer points to the running data mount")
        labels = old["Config"].get("Labels") or {}
        if frozen_configuration["name"] != labels.get("com.docker.compose.project"):
            raise OperationError("The Compose project does not match the existing container")
        desired_configuration = copy.deepcopy(frozen_configuration)
        desired_configuration["services"]["hexclaw"]["image"] = args.image
        desired_digest = configuration_digest(configuration, desired_configuration)
        tools_digest = deployment_tools_digest()
        current_file = state_dir / "current.json"
        if current_file.is_file():
            previous = json.loads(current_file.read_text())
            if (previous.get("status") == "deployed" and not previous.get("retention_stage")
                    and previous.get("commit") == args.commit and previous.get("image") == args.image
                    and previous.get("configuration_sha256") == desired_digest
                    and previous.get("tools_sha256") == tools_digest
                    and previous.get("verification", {}).get("backend_id") == baseline["backend_id"]):
                existing_image = docker_json("image", "inspect", args.image)[0]
                if old["Image"] == existing_image["Id"]:
                    previous["original_actions_run"] = previous.get("original_actions_run", previous.get("actions_run"))
                    previous["actions_run"] = args.run_id
                    previous["verification"] = baseline
                    previous["reused"] = True
                    write_json(current_file, previous)
                    return previous

        report_file = state_dir / (datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8] + ".json")
        report = {"commit": args.commit, "image": args.image, "actions_run": args.run_id,
                  "status": "preparing", "configuration_sha256": desired_digest, "tools_sha256": tools_digest}
        write_json(report_file, report)
        try:
            if current_target(configuration) != args.commit:
                report["status"] = "superseded"
                write_json(report_file, report)
                return report
            # 拉取、校验和清理共用维护锁，避免删除其他排队任务刚拉取的候选。
            run(["docker", "pull", args.image])
            target_image = docker_json("image", "inspect", args.image)[0]
            if (target_image["Config"].get("Labels") or {}).get("org.opencontainers.image.revision") != args.commit:
                raise OperationError("The image revision does not match the requested commit")
            report["image_id"] = target_image["Id"]
            frozen_configuration["services"]["hexclaw"]["image"] = old["Image"]
            recovery_config = report_file.with_suffix(".compose.json")
            write_json(recovery_config, frozen_configuration)
            args.include = [settings_path, recovery_config] + original_files + args.include
            if image_file.is_file() and image_file not in args.include:
                args.include.append(image_file)
            services = frozen_configuration.get("services", {})
            gateway_ids = run(command + ["ps", "--all", "--quiet", "gateway"], cwd=args.project_dir).decode().split() if "gateway" in services else []
            gateways = docker_json("inspect", *gateway_ids) if gateway_ids else []
            for option in services["hexclaw"].get("security_opt") or []:
                if option.startswith("seccomp="):
                    value = option.split("=", 1)[1]
                    if value not in {"unconfined", "builtin"} and not value.startswith("{"):
                        profile = Path(value)
                        if not profile.is_absolute():
                            profile = args.project_dir / profile
                        args.include.append(profile)
            deployment_paths(args, old, gateways)
            minimum_free = int(configuration.get("minimum_free_bytes", 0))
            if shutil.disk_usage(args.project_dir).free < minimum_free:
                raise OperationError("There is not enough free space for this deployment")
            previous_image_file = image_file.read_bytes() if image_file.exists() else None
            active_compose = state_dir / "current.compose.json"
            previous_compose = active_compose.read_bytes() if active_compose.exists() else None
            report.update(previous_image_id=old["Image"], previous_image=old["Config"]["Image"],
                          baseline=baseline, data_mount=list(data_mount))
            write_json(report_file, report)
            start_requested = False
            stopped = False
            try:
                report["status"] = "stopping"
                write_json(report_file, report)
                stopped = True
                with complete_service_command():
                    run(["docker", "stop", "--time", "60", old["Id"]])
                args.output = args.project_dir / configuration.get("backup_directory", "backups")
                report["backup"] = backup(args, lock_held=True, emit_report=False)
                if current_target(configuration) != args.commit:
                    with complete_service_command():
                        run(["docker", "start", old["Id"]])
                    wait_service(command, args.project_dir, old["Image"], baseline)
                    report["status"] = "superseded"
                    report["original_service_restored"] = True
                    write_json(report_file, report)
                    return report
                atomic_bytes(image_file, ("HEXCLAW_IMAGE=" + args.image + "\n").encode())
                if image_file not in args.include:
                    args.include.append(image_file)
                # 正常替换和恢复均使用锁内冻结的完整配置，不再解释可变 env 文件。
                write_json(active_compose, desired_configuration)
                args.compose_file = [active_compose]
                args.env_file = []
                args.project_name = desired_configuration["name"]
                command = compose_args(args)
                with complete_service_command():
                    run(command + ["up", "--no-start", "--no-build", "--no-deps", "--pull", "never", "--force-recreate", "hexclaw"], cwd=args.project_dir)
                candidate = only_container(command, args.project_dir)
                candidate = ensure_runtime_system_paths(configuration, candidate)
                if candidate["Image"] != target_image["Id"] or mounted_data(candidate) != data_mount:
                    raise OperationError("The replacement changed the image or persistent data mount")
                start_requested = True
                report["status"] = "starting"
                write_json(report_file, report)
                run(["docker", "start", candidate["Id"]])
                report["verification"] = wait_service(command, args.project_dir, target_image["Id"], baseline)
                report["status"] = "deployed"
                retention = retain_development_state(args, configuration, report, report_file, target_image["Id"])
                if retention is not None:
                    report["retention"] = retention
                write_json(report_file, report)
                atomic_bytes(current_file, (json.dumps(report, ensure_ascii=False, indent=2) + "\n").encode())
                return report
            except BaseException as error:
                report["error_type"] = type(error).__name__
                report["status"] = "recovery_required" if start_requested else "failed_before_start"
                if report.get("retention_stage"):
                    try:
                        wait_service(command, args.project_dir, target_image["Id"], baseline)
                        report["status"] = "deployed_retention_pending"
                    except (OperationError, OSError, ValueError):
                        pass
                if isinstance(error, OperationError):
                    report["error"] = str(error)
                if stopped and not start_requested:
                    atomic_bytes(image_file, ("HEXCLAW_IMAGE=" + old["Image"] + "\n").encode())
                    recovery_command = ["docker", "compose", "--project-directory", str(args.project_dir),
                                        "--project-name", frozen_configuration["name"], "--file", str(recovery_config)]
                    try:
                        with complete_service_command():
                            run(recovery_command + ["up", "--no-start", "--no-build", "--no-deps", "--pull", "never", "hexclaw"], cwd=args.project_dir)
                        recovered = ensure_runtime_system_paths(configuration, only_container(recovery_command, args.project_dir))
                        run(["docker", "start", recovered["Id"]])
                        wait_service(recovery_command, args.project_dir, old["Image"], baseline)
                        report["original_service_restored"] = True
                        if previous_image_file is None:
                            image_file.unlink()
                        else:
                            atomic_bytes(image_file, previous_image_file)
                        if previous_compose is None:
                            active_compose.unlink(missing_ok=True)
                        else:
                            atomic_bytes(active_compose, previous_compose)
                    except BaseException as recovery_error:
                        report["recovery_error_type"] = type(recovery_error).__name__
                        report["original_service_restored"] = False
                write_json(report_file, report)
                raise
        except BaseException as error:
            if report["status"] == "preparing":
                report["status"] = "preflight_failed"
                report["error_type"] = type(error).__name__
                if isinstance(error, OperationError):
                    report["error"] = str(error)
                write_json(report_file, report)
            raise


def main():
    os.umask(0o077)
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupted)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project-dir", type=Path, required=True)
    parser.add_argument("--target-config", type=Path, required=True)
    parser.add_argument("--image", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--run-id", required=True)
    args = parser.parse_args()
    try:
        print(json.dumps(deploy(args), ensure_ascii=False))
    except OperationError as error:
        print(f"Deployment failed: {error}", file=sys.stderr)
        return 1
    except (OSError, ValueError, KeyError, TypeError) as error:
        print(f"Deployment failed: {type(error).__name__}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("Deployment interrupted; inspect its persistent receipt", file=sys.stderr)
        return 130
    return 0


if __name__ == "__main__":
    sys.exit(main())
