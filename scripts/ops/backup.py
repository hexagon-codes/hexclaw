#!/usr/bin/env python3
"""单实例 Compose 一致备份与隔离恢复，不启动恢复副本的业务进程。"""

import argparse
import contextlib
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shlex
import signal
import subprocess
import sys
import tarfile
import tempfile
import uuid


class OperationError(Exception):
    pass


def run(argv, *, stdout=subprocess.PIPE, stdin=None, cwd=None):
    result = subprocess.run(argv, stdin=stdin, stdout=stdout, stderr=subprocess.PIPE, cwd=cwd)
    if result.returncode:
        # 命令或 stderr 可能含私有配置，不回显到调度日志。
        raise OperationError(f"{argv[0]} operation failed (exit {result.returncode})")
    return result.stdout


@contextlib.contextmanager
def complete_service_command():
    # Docker 守护进程可能在 CLI 退出后继续 stop/create；等命令完成后再处理中断，
    # 避免恢复 start 与尚未完成的 stop 交错，最终把原服务留在停止状态。
    previous = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
    try:
        yield
    finally:
        signal.pthread_sigmask(signal.SIG_SETMASK, previous)


def docker_json(*args):
    return json.loads(run(["docker", *args]))


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


@contextlib.contextmanager
def maintenance_lock(project):
    # 与后续部署工具共用锁文件；锁随进程退出释放，不按 PID 猜测清理。
    with (project / ".hexclaw-maintenance.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        yield


def compose_args(args):
    command = ["docker", "compose", "--project-directory", str(args.project_dir)]
    for filename in args.compose_file:
        command += ["--file", str(filename.resolve())]
    for filename in args.env_file:
        command += ["--env-file", str(filename.resolve())]
    if args.project_name:
        command += ["--project-name", args.project_name]
    return command


def write_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def deployment_paths(args, container, gateway):
    labels = container["Config"].get("Labels") or {}
    paths = list(args.include) + list(args.env_file)
    environment = args.project_dir / ".env"
    if environment.exists():
        paths.append(environment)
    configured = labels.get("com.docker.compose.project.config_files", "")
    paths += [Path(value) for value in configured.split(",") if value]
    paths += args.compose_file
    # 网关证书及配置位于 HOME 之外，按当前容器实际挂载一并保存。
    for item in gateway:
        paths += [Path(mount["Source"]) for mount in item["Mounts"] if mount["Type"] == "bind"]
    unique = []
    for path in paths:
        path = path.absolute()
        if path not in unique:
            if not path.exists():
                raise OperationError("A deployment file required by the running service is missing")
            unique.append(path)
    return unique


def upload_snapshot(snapshot, digest, host, directory):
    # 归档与服务恢复已完成后才传输；上传失败不会让原服务一直停机。
    target = directory.rstrip("/") + "/" + snapshot.name
    temporary = target + ".partial"
    quoted = shlex.quote
    script = (
        "set -eu; umask 077; "
        f"mkdir -p -- {quoted(directory)}; cat > {quoted(temporary)}; "
        f"actual=$(sha256sum -- {quoted(temporary)}); "
        f"test \"${{actual%% *}}\" = {quoted(digest)}; "
        f"mv -- {quoted(temporary)} {quoted(target)}; "
        f"printf '%s\\n' {quoted(digest)}"
    )
    with snapshot.open("rb") as source:
        actual = run(["ssh", "--", host, script], stdin=source).decode().strip()
    if actual != digest:
        raise OperationError("Remote backup digest does not match")


def backup(args, *, lock_held=False, emit_report=True):
    args.project_dir = args.project_dir.resolve()
    args.output = args.output.resolve()
    args.output.mkdir(parents=True, exist_ok=True)
    command = compose_args(args)
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    snapshot_id = f"hexclaw-{stamp}-{uuid.uuid4().hex[:8]}"
    report_path = args.output / (snapshot_id + ".json")
    report = {"snapshot_id": snapshot_id, "local_complete": False,
              "service_restored": False, "remote_complete": False}
    try:
        with (contextlib.nullcontext() if lock_held else maintenance_lock(args.project_dir)):
            ids = run(command + ["ps", "--all", "--quiet", "hexclaw"], cwd=args.project_dir).decode().split()
            if len(ids) != 1:
                raise OperationError("Backup requires exactly one existing HexClaw container")
            container = docker_json("inspect", ids[0])[0]
            if not any(mount["Destination"] == "/data" for mount in container["Mounts"]):
                raise OperationError("The running deployment has no /data persistence mount")
            image = container["Image"]
            was_running = bool(container["State"]["Running"])
            configuration = run(command + ["config", "--format", "json"], cwd=args.project_dir)
            services = json.loads(configuration).get("services", {})
            gateway_ids = (run(command + ["ps", "--all", "--quiet", "gateway"], cwd=args.project_dir).decode().split()
                           if "gateway" in services else [])
            gateways = docker_json("inspect", *gateway_ids) if gateway_ids else []
            paths = deployment_paths(args, container, gateways)
            with tempfile.TemporaryDirectory(prefix=snapshot_id + "-", dir=args.output) as temporary:
                work = Path(temporary)
                home = work / "home.tar.gz"
                stopped = False
                try:
                    if was_running:
                        # 即使 stop 被中断也进入恢复路径，不能遗留已停下的实例。
                        stopped = True
                        with complete_service_command():
                            run(["docker", "stop", "--time", "60", ids[0]])
                    state = docker_json("inspect", ids[0])[0]["State"]
                    if state["Running"]:
                        raise OperationError("HexClaw is still writing; no snapshot was taken")
                    with home.open("wb") as output:
                        run(["docker", "run", "--rm", "--network", "none", "--volumes-from", ids[0] + ":ro",
                             "--entrypoint", "tar", image, "-C", "/data", "-czf", "-", "."], stdout=output)
                    with tarfile.open(work / "deployment.tar.gz", "w:gz") as archive:
                        for index, path in enumerate(paths):
                            archive.add(path, arcname=f"files/{index}/{path.name}")
                    (work / "compose.json").write_bytes(configuration)
                    write_json(work / "deployment-paths.json", [str(path) for path in paths])
                finally:
                    if stopped:
                        # 只启动原容器，不重新解释可能已变动的 .env 或可变镜像标签。
                        with complete_service_command():
                            run(["docker", "start", ids[0]])
                        if not docker_json("inspect", ids[0])[0]["State"]["Running"]:
                            raise OperationError("The original HexClaw container did not restart")
                    report["service_restored"] = True
                    report["original_service_running"] = was_running
                    write_json(report_path, report)
                # 停机区间仅包含复制；哈希和归档整理在原服务恢复后完成。
                with tarfile.open(home, "r:gz") as archive:
                    for _ in archive:
                        pass
                files = {path.name: sha256(path) for path in work.iterdir() if path.is_file()}
                write_json(work / "manifest.json", {"schema": 1, "snapshot_id": snapshot_id,
                           "image_id": image, "image_reference": container["Config"]["Image"], "files": files})
                snapshot = args.output / (snapshot_id + ".tar")
                staging = snapshot.with_suffix(".partial")
                with tarfile.open(staging, "w") as archive:
                    for path in work.iterdir():
                        archive.add(path, arcname=path.name)
                staging.replace(snapshot)
                digest = sha256(snapshot)
                report.update(local_complete=True, sha256=digest, archive=snapshot.name)
                write_json(report_path, report)
        if args.remote_host:
            upload_snapshot(snapshot, digest, args.remote_host, args.remote_dir)
            report["remote_complete"] = True
            report["remote_target"] = args.remote_host + ":" + args.remote_dir.rstrip("/") + "/" + snapshot.name
            write_json(report_path, report)
        if emit_report:
            print(json.dumps(report, ensure_ascii=False))
        return report
    except BaseException as error:
        report["error_type"] = type(error).__name__
        if isinstance(error, OperationError):
            report["error"] = str(error)
        write_json(report_path, report)
        raise


def restore(args):
    # 只恢复到全新隔离卷，不启动 HexClaw、IM 或任务消费者。
    if sha256(args.archive) != args.sha256:
        raise OperationError("Backup digest does not match")
    names = run(["docker", "volume", "ls", "--format", "{{.Name}}"]).decode().splitlines()
    if args.volume in names:
        raise OperationError("Restore requires a new volume; existing data will not be replaced")
    with tempfile.TemporaryDirectory(prefix="hexclaw-restore-") as temporary:
        work = Path(temporary)
        with tarfile.open(args.archive, "r:") as archive:
            manifest_file = archive.extractfile("manifest.json")
            if manifest_file is None:
                raise OperationError("Backup manifest is missing")
            manifest = json.load(manifest_file)
            if manifest.get("schema") != 1 or "home.tar.gz" not in manifest.get("files", {}):
                raise OperationError("Backup manifest is incompatible")
            # 按已知成员读取，不向宿主展开归档中的路径。
            for filename, expected in manifest["files"].items():
                source = archive.extractfile(filename)
                if source is None:
                    raise OperationError("Backup member is missing")
                destination = work / hashlib.sha256(filename.encode()).hexdigest()
                with destination.open("wb") as output:
                    for chunk in iter(lambda: source.read(1024 * 1024), b""):
                        output.write(chunk)
                if sha256(destination) != expected:
                    raise OperationError("Backup member digest does not match")
                if filename == "home.tar.gz":
                    home = destination
        # 恢复镜像必须已存在，避免在恢复阶段静默换用另一份可变镜像。
        image = manifest["image_id"]
        docker_json("image", "inspect", image)
        restore_id = uuid.uuid4().hex
        run(["docker", "volume", "create", "--label", "hexclaw.restore.id=" + restore_id, args.volume])
        volume = docker_json("volume", "inspect", args.volume)[0]
        if (volume.get("Labels") or {}).get("hexclaw.restore.id") != restore_id:
            raise OperationError("The restore volume was created by another operation")
        with home.open("rb") as source:
            run(["docker", "run", "--rm", "--network", "none", "-i", "--mount",
                 f"type=volume,source={args.volume},target=/data", "--entrypoint", "tar", image,
                 "-C", "/data", "-xzf", "-"], stdin=source)
        print(json.dumps({"restored": True, "volume": args.volume, "service_started": False}))


def main():
    os.umask(0o077)
    # 调度器正常终止仍执行 finally 恢复；SIGKILL 和主机断电由运维状态记录核实。
    def interrupted(_signum, _frame):
        raise KeyboardInterrupt
    signal.signal(signal.SIGTERM, interrupted)
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="action", required=True)
    create = commands.add_parser("backup")
    create.add_argument("--project-dir", type=Path, required=True)
    create.add_argument("--output", type=Path, required=True)
    create.add_argument("--compose-file", type=Path, action="append", default=[])
    create.add_argument("--env-file", type=Path, action="append", default=[])
    create.add_argument("--project-name")
    create.add_argument("--include", type=Path, action="append", default=[])
    create.add_argument("--remote-host")
    create.add_argument("--remote-dir")
    recover = commands.add_parser("restore")
    recover.add_argument("--archive", type=Path, required=True)
    recover.add_argument("--sha256", required=True)
    recover.add_argument("--volume", required=True)
    args = parser.parse_args()
    if args.action == "backup" and bool(args.remote_host) != bool(args.remote_dir):
        parser.error("--remote-host and --remote-dir must be provided together")
    try:
        (backup if args.action == "backup" else restore)(args)
    except OperationError as error:
        print(f"Backup operation failed: {error}", file=sys.stderr)
        return 1
    except (OSError, ValueError, KeyError, TypeError, tarfile.TarError) as error:
        print(f"Backup operation failed: {type(error).__name__}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("Backup operation interrupted", file=sys.stderr)
        return 130
    return 0


if __name__ == "__main__":
    sys.exit(main())
