#!/usr/bin/env python3
"""预检和同步已选择的 Hub 内容；恢复清单保存在独立暂存目录。"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import tempfile

ROOT = Path(__file__).resolve().parent.parent
EXCLUDED = {"chemistry-tutor", "physics-tutor", "essay-grader", "lesson-plan",
            "oral-english-partner", "study-planner"}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def version(data):
    match = re.search(rb'^version:\s*["\']?(\d+)\.(\d+)\.(\d+)["\']?\s*$', data, re.M)
    if not match:
        raise ValueError("Missing or unsupported skill version")
    return tuple(int(x) for x in match.groups())


def write_atomic(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    handle, temporary = tempfile.mkstemp(prefix=".hub-sync-", dir=path.parent)
    try:
        with os.fdopen(handle, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temporary, 0o644)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def save(path, document):
    write_atomic(path, (json.dumps(document, ensure_ascii=False, indent=2) + "\n").encode())


def restore(manifest, document):
    # 只恢复仍等于本轮候选的文件，保留中途发生的其他编辑。
    for item in reversed(document["files"]):
        target = Path(item["target"])
        current = target.read_bytes() if target.exists() else None
        current_digest = digest(current) if current is not None else None
        if current_digest == item["before_digest"]:
            continue
        if current_digest != item["candidate_digest"]:
            raise ValueError(f"Recovery would overwrite a later edit: {target}")
        if item["backup"]:
            backup = Path(item["backup"]).read_bytes()
            if digest(backup) != item["before_digest"]:
                raise ValueError(f"Backup digest mismatch: {target}")
            write_atomic(target, backup)
        else:
            target.unlink()
    document["state"] = "rolled_back"
    save(manifest, document)


def main():
    parser = argparse.ArgumentParser(description="Preview by default; apply explicitly selected skills/catalog after complete preflight.")
    parser.add_argument("hub", nargs="?", default=str(ROOT.parent / "hexclaw-hub"))
    parser.add_argument("--skills", nargs="+", help="Skill names to preview or apply")
    parser.add_argument("--catalog", action="store_true", help="Include both complete market catalogs")
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--report", type=Path, help="Write the preview or application manifest")
    parser.add_argument("--recover", type=Path, help="Restore only unchanged files from an application manifest")
    args = parser.parse_args()
    if args.recover:
        document = json.loads(args.recover.read_text())
        if document.get("state") not in {"prepared", "applying", "applied", "rolled_back"}:
            raise ValueError("Manifest has no recoverable application")
        restore(args.recover, document)
        print(json.dumps({"state": "rolled_back", "manifest": str(args.recover)}))
        return
    if args.apply and not args.skills and not args.catalog:
        raise ValueError("Apply requires an explicit --skills selection or --catalog")
    hub = Path(args.hub).resolve()
    index_bytes = (hub / "index.json").read_bytes()
    index = json.loads(index_bytes)
    entries = {}
    for entry in index["skills"]:
        name = entry["name"]
        if name in entries:
            raise ValueError(f"Duplicate skill in source index: {name}")
        entries[name] = entry
    eligible = {name for name, entry in entries.items() if "k12" in entry.get("tags", []) and name not in EXCLUDED}
    selected = sorted(set(args.skills) if args.skills else eligible)
    if set(selected) - eligible:
        raise ValueError("Selection contains an unknown or excluded K12 seed")
    candidates, preview = [], []
    for name in selected:
        entry = entries[name]
        if entry["file"] != f"skills/{name}.md":
            raise ValueError(f"Unexpected indexed skill file: {name}")
        source = (hub / entry["file"]).read_bytes()
        if digest(source) != entry.get("sha256") or len(source) != entry.get("size"):
            raise ValueError(f"Source does not match index digest/size: {name}")
        source_version = version(source)
        if ".".join(map(str, source_version)) != entry.get("version"):
            raise ValueError(f"Source version does not match index: {name}")
        target = ROOT / "scenarios/k12/skills" / f"{name}.md"
        old = target.read_bytes() if target.exists() else None
        target_version = version(old) if old is not None else None
        state = "unchanged" if old == source else "new" if old is None else "update"
        if old != source and target_version is not None and target_version >= source_version:
            state = "conflict"
        preview.append({"name": name, "source_version": entry["version"], "target_version": target_version,
                        "state": state, "source_digest": digest(source), "target_digest": digest(old) if old is not None else None})
        if state in {"new", "update"}:
            candidates.append((target, source))
    document = {"schema_version": 1, "state": "preview", "skills": preview, "catalog": args.catalog}
    if not args.apply:
        if args.report:
            save(args.report, document)
        print(json.dumps(document, ensure_ascii=False, indent=2))
        return
    if any(item["state"] == "conflict" for item in preview):
        raise ValueError("Embedded skill is ahead or differs at the same version; merge before applying")
    catalog_target = ROOT / "skill/hub/embed/index.json"
    if args.catalog:
        registry = (hub / "mcp-registry.json").read_bytes()
        json.loads(registry)
        candidates.extend([(catalog_target, index_bytes), (ROOT / "skill/hub/embed/mcp-registry.json", registry)])
    elif args.skills:
        current = json.loads(catalog_target.read_bytes())
        merged = {entry["name"]: entry for entry in current["skills"]}
        for name in selected:
            merged[name] = entries[name]
        current["skills"] = sorted(merged.values(), key=lambda entry: entry["name"])
        current["count"] = len(current["skills"])
        current["updated_at"] = index["updated_at"]
        candidates.append((catalog_target, (json.dumps(current, ensure_ascii=False, indent=2) + "\n").encode()))
    # 全部候选和备份准备成功后才替换；清单先落盘，支持中断后的显式恢复。
    stage = Path(tempfile.mkdtemp(prefix="hexclaw-hub-sync-"))
    manifest = args.report or stage / "manifest.json"
    document.update(state="prepared", files=[])
    for ordinal, (target, candidate) in enumerate(candidates):
        before = target.read_bytes() if target.exists() else None
        if before == candidate:
            continue
        backup = stage / f"{ordinal}.before" if before is not None else None
        if backup is not None:
            backup.write_bytes(before)
        prepared = stage / f"{ordinal}.candidate"
        prepared.write_bytes(candidate)
        document["files"].append({"target": str(target), "backup": str(backup) if backup else None,
                                  "candidate": str(prepared), "before_digest": digest(before) if before is not None else None,
                                  "candidate_digest": digest(candidate)})
    save(manifest, document)
    try:
        document["state"] = "applying"
        save(manifest, document)
        for item in document["files"]:
            target = Path(item["target"])
            current = target.read_bytes() if target.exists() else None
            if (digest(current) if current is not None else None) != item["before_digest"]:
                raise ValueError(f"Target changed after preflight: {target}")
            write_atomic(target, Path(item["candidate"]).read_bytes())
            if digest(target.read_bytes()) != item["candidate_digest"]:
                raise ValueError(f"Applied file digest mismatch: {target}")
        document["state"] = "applied"
        save(manifest, document)
    except BaseException:
        restore(manifest, document)
        raise
    print(json.dumps({"state": "applied", "files": len(document["files"]), "manifest": str(manifest)}))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError) as error:
        print(f"Hub synchronization failed: {error}", file=sys.stderr)
        sys.exit(1)
