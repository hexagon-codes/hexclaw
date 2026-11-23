#!/usr/bin/env python3
"""按固定镜像摘要更新单个 Compose 服务，保留部署回执和发布前完整备份。"""

import argparse
from contextlib import nullcontext
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
import tarfile
import tempfile
import time
import uuid
import urllib.parse

from backup import OperationError, backup, complete_service_command, compose_args, deployment_paths, docker_json, maintenance_lock, run, sha256, write_json


LEGACY_ARCHIVE_NAME = re.compile(r"hexclaw-\d{8}T\d{6}Z-[0-9a-f]{8}\.tar")
MAX_BACKUP_METADATA = 1024 * 1024


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


def previous_image_reference(container, configured_image):
    """仅认领冻结配置中确实指向旧容器镜像的引用，保留其他仓库的共享标签。"""
    if configured_image and image_repository(configured_image) is not None:
        image = docker_json("image", "inspect", container["Image"])[0]
        references = (image.get("RepoTags") or []) + (image.get("RepoDigests") or [])
        if configured_image in references:
            return configured_image
    return container["Config"]["Image"]


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


def receipt_backup_names(receipt):
    return {(receipt.get(key) or {}).get("archive") for key in ("backup", "current_backup")} - {None, ""}


def legacy_backup_directories(project, configuration, output, data_mount):
    configured = configuration.get("legacy_backup_directories", [])
    if not isinstance(configured, list) or any(not isinstance(path, str) or not path.strip() for path in configured):
        raise OperationError("legacy_backup_directories must be a list of directory paths")
    if not configured:
        return []
    data_path = Path(data_mount[1]).resolve()
    directories = sorted({(project / path).resolve() for path in configured})
    for directory in directories:
        for current in (output, data_path):
            if directory == current or directory in current.parents or current in directory.parents:
                raise OperationError("A legacy backup directory overlaps current snapshots or data")
        if directory.exists() and not directory.is_dir():
            raise OperationError("A legacy backup path is not a directory")
    return [path for path in directories if path.is_dir()]


def read_archive_metadata(archive, name):
    member = archive.getmember(name)
    if not member.isfile() or member.size > MAX_BACKUP_METADATA:
        raise ValueError("Invalid backup metadata")
    with archive.extractfile(member) as source:
        return source.read()


def is_completed_project_backup(path, sidecar, project_name, data_reference):
    if path.is_symlink() or sidecar.is_symlink() or not path.is_file() or not sidecar.is_file():
        return False
    try:
        if sidecar.stat().st_size > MAX_BACKUP_METADATA:
            return False
        receipt = json.loads(sidecar.read_bytes())
        if (receipt["snapshot_id"] != path.stem or receipt["archive"] != path.name
                or receipt["local_complete"] is not True or receipt["service_restored"] is not True
                or not re.fullmatch(r"[0-9a-f]{64}", receipt["sha256"])):
            return False
        with tarfile.open(path, "r:") as archive:
            manifest = json.loads(read_archive_metadata(archive, "manifest.json"))
            compose_bytes = read_archive_metadata(archive, "compose.json")
            payload = ("home.tar.gz", "deployment.tar.gz", "deployment-paths.json")
            if (manifest["schema"] != 1 or manifest["snapshot_id"] != path.stem
                    or manifest["files"]["compose.json"] != hashlib.sha256(compose_bytes).hexdigest()
                    or any(name not in manifest["files"] or not archive.getmember(name).isfile() for name in payload)):
                return False
        compose = json.loads(compose_bytes)
        return compose["name"] == project_name and configured_data_reference(compose)[:2] == data_reference
    except (ValueError, KeyError, TypeError, tarfile.TarError, OperationError):
        # 无法证实属于该部署的归档保留给运维确认。
        return False


def remove_legacy_backups(project, directories, current_configuration, protected_archives):
    if not directories:
        return 0
    project_name = current_configuration["name"]
    data_reference = configured_data_reference(current_configuration)[:2]
    removed = 0
    for directory in directories:
        # 当前部署锁已持有，旧目录的生产者还可能使用旧项目目录的锁。
        parent = directory.parent
        lock = nullcontext() if parent == project else maintenance_lock(parent, blocking=False)
        with lock:
            pending = pending_deployment(parent / ".hexclaw-deploy") if parent != project else None
            protected = protected_archives | (receipt_backup_names(pending[1]) if pending else set())
            for archive in sorted(directory.glob("hexclaw-*.tar")):
                if archive.name in protected or not LEGACY_ARCHIVE_NAME.fullmatch(archive.name):
                    continue
                sidecar = archive.with_suffix(".json")
                if is_completed_project_backup(archive, sidecar, project_name, data_reference):
                    archive.unlink()
                    sidecar.unlink()
                    removed += 1
    return removed


def history_for_retention(state_dir, report_file=None):
    history = []
    protected_archives = {"latest-deployment.tar"}
    terminal = {"deployed", "preflight_failed", "failed_before_start", "superseded"}
    for path in state_dir.glob("*.json"):
        if path == report_file or not re.fullmatch(r"\d{8}T\d{6}Z-[0-9a-f]{8}\.json", path.name):
            continue
        saved = json.loads(path.read_text())
        status = saved.get("status")
        recoverable = status == "failed_before_start" and not saved.get("original_service_restored")
        if ((status in terminal and not recoverable) or saved.get("resolved")) and "commit" in saved and "image" in saved:
            history.append((path, saved))
        else:
            protected_archives.update(receipt_backup_names(saved))
    return history, protected_archives


def retain_development_state(args, configuration, report, report_file, current_image):
    """清理部署回执及显式归属的旧部署备份，保留未决恢复材料。"""
    if configuration.get("development_retention") != "current_only":
        return None
    output = (args.project_dir / configuration.get("backup_directory", "backups")).resolve()
    output.mkdir(parents=True, exist_ok=True)
    state_dir = report_file.parent
    history, protected_archives = history_for_retention(state_dir, report_file)
    report["retention_stage"] = "snapshot"
    write_json(report_file, report)
    legacy_directories = legacy_backup_directories(args.project_dir, configuration, output, report.get("data_mount"))
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
    report["post_snapshot_verification"] = wait_service(
        compose_args(args), args.project_dir, current_image, report["verification"], accepted_mount=report["data_mount"],
    )
    report["retention_stage"] = "cleanup"
    write_json(report_file, report)

    removed_legacy_archives = remove_legacy_backups(
        args.project_dir, legacy_directories, current_configuration, protected_archives,
    )
    removed_archives = removed_legacy_archives
    images = {report.get("previous_image_id")}
    references = {}
    for _, saved in history + [(report_file, report)]:
        images.update((saved.get("previous_image_id"), saved.get("image_id")))
        for image_id, reference in ((saved.get("previous_image_id"), saved.get("previous_image")),
                                    (saved.get("image_id"), saved.get("image"))):
            if image_id and reference:
                references.setdefault(image_id, set()).add(reference)
        for name in receipt_backup_names(saved):
            if Path(name).name != name or name in protected_archives:
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
            remove_image_reference(reference)
        if image in set(run(["docker", "image", "ls", "--no-trunc", "--quiet"]).decode().split()):
            remaining = docker_json("image", "inspect", image)[0]
            if remaining.get("RepoTags") or remaining.get("RepoDigests"):
                shared_images += 1
                continue
            # 旧镜像可能仍是新镜像的父层；无法删除不等于部署失败，保留并如实计数。
            if not remove_image_reference(image):
                shared_images += 1
                continue
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
            "removed_archives": removed_archives, "removed_legacy_archives": removed_legacy_archives,
            "removed_images": removed_images,
            "preserved_in_use_images": retained_images, "preserved_shared_images": shared_images}


def remove_image_reference(reference):
    """删除不再使用的旧镜像引用；父层依赖或共享引用导致无法删除时返回 False。"""
    try:
        run(["docker", "image", "rm", reference])
        return True
    except OperationError:
        return False


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


def configured_data_reference(configuration):
    mounts = [item for item in configuration["services"]["hexclaw"].get("volumes", [])
              if item["target"] == "/data"]
    if len(mounts) != 1:
        raise OperationError("The Compose configuration must retain its existing /data mount")
    item = mounts[0]
    source = item["source"]
    if item["type"] == "volume":
        source = configuration["volumes"][source]["name"]
    return item["type"], source, not item.get("read_only", False)


def configured_data(configuration):
    mount_type, source, writable = configured_data_reference(configuration)
    if mount_type == "volume":
        source = docker_json("volume", "inspect", source)[0]["Mountpoint"]
    return mount_type, source, writable


def only_container(command, project):
    ids = run(command + ["ps", "--all", "--quiet", "hexclaw"], cwd=project).decode().split()
    if len(ids) != 1:
        raise OperationError("Deployment requires exactly one HexClaw container")
    return docker_json("inspect", ids[0])[0]


def inspect_service(container, accepted_config_sha=None):
    # 令牌只在容器内部读取并用于请求，不进入宿主参数、stdout 或部署记录。
    script = r'''
import hashlib, json, pathlib, sqlite3, sys, urllib.request, yaml
arguments = json.loads(sys.argv[1])
accepted_config_sha = sys.argv[2]
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
config_sha = hashlib.sha256(body).hexdigest()
llm_revision = cfg.get('llm', {}).get('config_revision', 0)
if accepted_config_sha and config_sha != accepted_config_sha:
    if read('/api/v1/config/llm', True).get('config_revision') != llm_revision:
        raise RuntimeError('The running LLM configuration does not match the saved revision')
runtime = [str(path.resolve()), str(database), cfg.get('storage', {}).get('driver', 'sqlite'),
           server.get('host', '0.0.0.0'), server.get('port', 16060), server.get('api_token')]
print(json.dumps({'backend_id': identity, 'config_sha256': config_sha,
                  'runtime_config_sha256': hashlib.sha256(json.dumps(runtime).encode()).hexdigest(),
                  'llm_revision': llm_revision, 'protected_read': True, 'schema_version': schema}))
'''
    raw = run(["docker", "exec", container["Id"], "python3", "-c", script,
               json.dumps(container["Config"].get("Cmd") or []), accepted_config_sha or ""])
    return json.loads(raw)


def wait_service(command, project, image_id, baseline, *, accepted_mount=None):
    deadline = time.monotonic() + 120
    while True:
        container = only_container(command, project)
        if container["Image"] != image_id:
            raise OperationError("The started container does not use the requested image")
        if accepted_mount is not None and mounted_data(container) != tuple(accepted_mount):
            raise OperationError("The accepted service no longer uses the original data mount")
        try:
            state = inspect_service(container, baseline["config_sha256"] if accepted_mount is not None else None)
            if any(state[key] != baseline[key] for key in ("backend_id", "protected_read")):
                raise OperationError("Backend identity or persistent configuration changed")
            if accepted_mount is not None and state["schema_version"] != baseline["schema_version"]:
                raise OperationError("The accepted service schema version changed")
            if state["config_sha256"] != baseline["config_sha256"]:
                # 接管前必须与旧配置字节一致；接管后只承认现用服务已读回的正常配置修订。
                if (accepted_mount is None or state["runtime_config_sha256"] != baseline.get("runtime_config_sha256")
                        or state["llm_revision"] <= baseline.get("llm_revision", state["llm_revision"])):
                    raise OperationError("Backend identity or persistent configuration changed")
            return state
        except OperationError:
            if not container["State"]["Running"] or time.monotonic() >= deadline:
                raise
            time.sleep(1)


def deployment_compose(args, configuration, image_file):
    args.compose_file = [args.project_dir / item for item in configuration.get("compose_files", [])]
    args.env_file = [args.project_dir / item for item in configuration.get("env_files", [])]
    if not args.env_file and (args.project_dir / ".env").is_file():
        args.env_file.append(args.project_dir / ".env")
    if image_file.is_file():
        args.env_file.append(image_file)
    args.project_name = configuration.get("project_name")
    return compose_args(args)


def deployment_predecessor(args, settings_path, image_file):
    expected = getattr(args, "expected_previous_image_id", None)
    if expected is not None:
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", expected):
            raise OperationError("A full Docker image ID is required for the expected previous image")
        return expected
    # 锁外只记录交接意图；实际配置和容器仍须在获得维护锁后重新读取。
    command = deployment_compose(args, json.loads(settings_path.read_text()), image_file)
    try:
        return only_container(command, args.project_dir)["Image"]
    except OperationError:
        # Compose 正在重建的短窗口可能暂时没有容器；使用上一次已确认的镜像。
        current_file = args.project_dir / ".hexclaw-deploy" / "current.json"
        if current_file.is_file():
            saved = json.loads(current_file.read_text())
            if saved.get("status") == "deployed" and re.fullmatch(r"sha256:[0-9a-f]{64}", saved.get("image_id") or ""):
                return saved["image_id"]
        raise


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

    image_file = args.project_dir / ".hexclaw-image.env"
    expected_previous_image = deployment_predecessor(args, settings_path, image_file)
    with maintenance_lock(args.project_dir):
        configuration = json.loads(settings_path.read_text())
        command = deployment_compose(args, configuration, image_file)
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
            accepted = saved.get("verification")
            saved["verification"] = wait_service(
                command, args.project_dir, target_image["Id"], accepted or saved["baseline"],
                accepted_mount=saved.get("data_mount") if accepted else None,
            )
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
        # 恢复快照保留实际容器的回收器状态，避免原 Compose 文件落后于已部署配置。
        frozen_configuration["services"]["hexclaw"]["init"] = old["HostConfig"].get("Init") is True
        desired_configuration = copy.deepcopy(frozen_configuration)
        desired_configuration["services"]["hexclaw"]["image"] = args.image
        # 为既有部署补齐容器回收器，其余冻结的运行配置保持原值。
        desired_configuration["services"]["hexclaw"]["init"] = True
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
                if old["Image"] == existing_image["Id"] and old["HostConfig"].get("Init") is True:
                    if configuration.get("development_retention") == "current_only":
                        output = (args.project_dir / configuration.get("backup_directory", "backups")).resolve()
                        directories = legacy_backup_directories(args.project_dir, configuration, output, data_mount)
                        if directories:
                            _, protected_archives = history_for_retention(state_dir)
                            removed = remove_legacy_backups(
                                args.project_dir, directories, frozen_configuration, protected_archives,
                            )
                            if removed:
                                previous["retention"]["removed_legacy_archives"] += removed
                                previous["retention"]["removed_archives"] += removed
                    previous["original_actions_run"] = previous.get("original_actions_run", previous.get("actions_run"))
                    previous["actions_run"] = args.run_id
                    previous["verification"] = baseline
                    previous["reused"] = True
                    write_json(current_file, previous)
                    return previous

        report_file = state_dir / (datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8] + ".json")
        report = {"commit": args.commit, "image": args.image, "actions_run": args.run_id,
                  "expected_previous_image_id": expected_previous_image,
                  "status": "preparing", "configuration_sha256": desired_digest, "tools_sha256": tools_digest}
        write_json(report_file, report)
        try:
            if old["Image"] != expected_previous_image:
                report.update(status="superseded", superseded_reason="previous_image_changed",
                              observed_previous_image_id=old["Image"])
                write_json(report_file, report)
                return report
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
            previous_image = previous_image_reference(old, frozen_configuration["services"]["hexclaw"].get("image"))
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
            report.update(previous_image_id=old["Image"], previous_image=previous_image,
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
                if report.get("retention_stage") and report.get("verification"):
                    try:
                        wait_service(command, args.project_dir, target_image["Id"], report["verification"],
                                     accepted_mount=data_mount)
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
    parser.add_argument("--expected-previous-image-id", help="Docker image ID observed before this handoff")
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
