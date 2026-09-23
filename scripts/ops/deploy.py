#!/usr/bin/env python3
"""按固定镜像摘要更新单个 Compose 服务，保留部署回执和发布前完整备份。"""

import argparse
import datetime
import json
import os
from pathlib import Path
import re
import shutil
import signal
import sys
import tempfile
import time
import uuid

from backup import OperationError, backup, complete_service_command, compose_args, docker_json, maintenance_lock, run, write_json


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
    configuration = json.loads(settings_path.read_text())
    if not re.fullmatch(r"[0-9a-f]{40}", args.commit):
        raise OperationError("A full commit SHA is required")
    if not re.fullmatch(r"[^\s@]+@sha256:[0-9a-f]{64}", args.image):
        raise OperationError("An immutable image digest is required")
    state_dir = args.project_dir / ".hexclaw-deploy"
    state_dir.mkdir(exist_ok=True)
    report_file = state_dir / (datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + uuid.uuid4().hex[:8] + ".json")
    report = {"commit": args.commit, "image": args.image, "actions_run": args.run_id, "status": "preparing"}
    write_json(report_file, report)
    try:
        image_file = args.project_dir / ".hexclaw-image.env"
        args.compose_file = [args.project_dir / item for item in configuration.get("compose_files", [])]
        args.env_file = [args.project_dir / item for item in configuration.get("env_files", [])]
        if not args.env_file and (args.project_dir / ".env").is_file():
            args.env_file.append(args.project_dir / ".env")
        if image_file.is_file():
            args.env_file.append(image_file)
        args.project_name = configuration.get("project_name")
        command = compose_args(args)
        # 镜像下载失败不影响原容器；摘要和 OCI 提交一致后才进入停机区间。
        run(["docker", "pull", args.image])
        target_image = docker_json("image", "inspect", args.image)[0]
        if (target_image["Config"].get("Labels") or {}).get("org.opencontainers.image.revision") != args.commit:
            raise OperationError("The image revision does not match the requested commit")
        report["image_id"] = target_image["Id"]
        with maintenance_lock(args.project_dir):
            if current_target(configuration) != args.commit:
                report["status"] = "superseded"
                write_json(report_file, report)
                return report
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
            frozen_configuration["services"]["hexclaw"]["image"] = old["Image"]
            recovery_config = report_file.with_suffix(".compose.json")
            write_json(recovery_config, frozen_configuration)
            # 只检查运行所在文件系统，实际归档过程仍以写入结果为准。
            minimum_free = int(configuration.get("minimum_free_bytes", 0))
            if shutil.disk_usage(args.project_dir).free < minimum_free:
                raise OperationError("There is not enough free space for this deployment")
            previous_image_file = image_file.read_bytes() if image_file.exists() else None
            report.update(previous_image_id=old["Image"], previous_image=old["Config"]["Image"], baseline=baseline)
            write_json(report_file, report)
            start_requested = False
            stopped = False
            try:
                stopped = True
                with complete_service_command():
                    run(["docker", "stop", "--time", "60", old["Id"]])
                args.output = args.project_dir / configuration.get("backup_directory", "backups")
                args.include = [settings_path, recovery_config] + ([image_file] if image_file.exists() else [])
                args.remote_host = None
                args.remote_dir = None
                report["backup"] = backup(args, lock_held=True, emit_report=False)
                # 备份可能耗时，真正替换前再次核对分支，避免等待期间新提交已到达。
                if current_target(configuration) != args.commit:
                    report["status"] = "superseded"
                    with complete_service_command():
                        run(["docker", "start", old["Id"]])
                    write_json(report_file, report)
                    return report
                atomic_bytes(image_file, ("HEXCLAW_IMAGE=" + args.image + "\n").encode())
                if image_file not in args.env_file:
                    args.env_file.append(image_file)
                command = compose_args(args)
                with complete_service_command():
                    run(command + ["up", "--no-start", "--no-build", "--no-deps", "--pull", "never", "--force-recreate", "hexclaw"], cwd=args.project_dir)
                candidate = only_container(command, args.project_dir)
                if candidate["Image"] != target_image["Id"] or mounted_data(candidate) != data_mount:
                    raise OperationError("The replacement changed the image or persistent data mount")
                # 一旦发出 start，即可能已有迁移及副作用；失败后不能猜测数据库仍可回退。
                start_requested = True
                report["status"] = "starting"
                write_json(report_file, report)
                run(["docker", "start", candidate["Id"]])
                report["verification"] = wait_service(command, args.project_dir, target_image["Id"], baseline)
                report["status"] = "deployed"
                write_json(report_file, report)
                atomic_bytes(state_dir / "current.json", json.dumps(report, ensure_ascii=False, indent=2).encode() + b"\n")
                return report
            except BaseException as error:
                report["error_type"] = type(error).__name__
                report["status"] = "recovery_required" if start_requested else "failed_before_start"
                if isinstance(error, OperationError):
                    report["error"] = str(error)
                if stopped and not start_requested:
                    # 新程序未执行，原数据尚未迁移；先固定原本地镜像，再恢复同一 Compose 项目。
                    atomic_bytes(image_file, ("HEXCLAW_IMAGE=" + old["Image"] + "\n").encode())
                    if image_file not in args.env_file:
                        args.env_file.append(image_file)
                    recovery_command = ["docker", "compose", "--project-directory", str(args.project_dir),
                                        "--project-name", frozen_configuration["name"], "--file", str(recovery_config)]
                    try:
                        with complete_service_command():
                            run(recovery_command + ["up", "--detach", "--no-build", "--no-deps", "--pull", "never", "hexclaw"], cwd=args.project_dir)
                        wait_service(recovery_command, args.project_dir, old["Image"], baseline)
                        report["original_service_restored"] = True
                        # 保留原部署文件，镜像恢复使用的 ID 已记录到部署回执。
                        if previous_image_file is None:
                            image_file.unlink()
                        else:
                            atomic_bytes(image_file, previous_image_file)
                    except BaseException as recovery_error:
                        report["recovery_error_type"] = type(recovery_error).__name__
                        report["original_service_restored"] = False
                write_json(report_file, report)
                raise
    except BaseException as error:
        if report['status'] == 'preparing':
            report['status'] = 'preflight_failed'
            report['error_type'] = type(error).__name__
            if isinstance(error, OperationError):
                report['error'] = str(error)
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
