#!/usr/bin/env python3
"""执行跨仓 code_exec 行为合同，并输出与内部测试名称分离的结果。"""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess


parser = argparse.ArgumentParser()
parser.add_argument("--output", type=Path, required=True)
parser.add_argument("--events", type=Path)
args = parser.parse_args()
os.chdir(Path(__file__).resolve().parent.parent)
platform = subprocess.check_output(["go", "env", "GOOS"], text=True).strip()
behaviors = {
    "metadata": ["TestCodeExecSkill_Meta"],
    "tool-definition": ["TestCodeExecSkill_ToolDefinitionP0Fields"],
    "missing-packages": ["TestDetectMissingPackages"],
    "install-command": ["TestBuildInstallCommand"],
    "sandbox-capabilities": ["TestSandboxP0_StaticGapMatrix"],
}
listed = subprocess.check_output(["go", "test", "-list", "^Test", "./skill/builtin"], text=True).splitlines()
if platform != "windows":
    behaviors.update({"execute-success": ["TestCodeExecSkill_Execute_Success"],
                      "execute-nonzero": ["TestCodeExecSkill_Execute_NonZeroExit"],
                      "execute-timeout": ["TestCodeExecSkill_Execute_Timeout"]})
    if platform == "darwin":
        behaviors["authorized-directory"] = ["TestBug20260626_CodeExecReadsConnectorAuthorizedDir"]
    else:
        behaviors["go-module-files"] = ["TestCodeExecSkill_Execute_ModuleGoFiles"]
    behaviors["crawler-network-policy"] = [name for name in listed
        if name.startswith("TestCodeExecSkill_Execute_PythonCrawler")
        and not name.startswith("TestCodeExecSkill_Execute_PythonCrawlerPublicNetwork")]
    if os.environ.get("HEXCLAW_CODE_EXEC_LIVE_NETWORK") == "1":
        behaviors["crawler-public-network"] = [name for name in listed
            if name.startswith("TestCodeExecSkill_Execute_PythonCrawlerPublicNetwork")]
required = []
for behavior, tests in behaviors.items():
    if not tests or any(name not in listed for name in tests):
        raise SystemExit(f"Required code_exec behavior is missing: {behavior}")
    required.extend(tests)
pattern = "^(" + "|".join(re.escape(name) for name in required) + ")$"
args.output.parent.mkdir(parents=True, exist_ok=True)
events_path = args.events or args.output.with_suffix(".jsonl")
if args.events is None:
    with events_path.open("w") as stream:
        run = subprocess.run(["go", "test", "-json", "-count=1", "-timeout", "360s",
                              "./skill/builtin", "-run", pattern], stdout=stream, check=False)
    if run.returncode:
        print(events_path.read_text())
        raise SystemExit(run.returncode)
events = [json.loads(line) for line in events_path.read_text().splitlines() if line.strip()]
for name in required:
    if any(event.get("Action") == "skip" and
           (event.get("Test") == name or event.get("Test", "").startswith(name + "/")) for event in events):
        raise SystemExit(f"Required code_exec test was skipped: {name}")
    if sum(event.get("Action") == "pass" and event.get("Test") == name for event in events) != 1:
        raise SystemExit(f"Required code_exec test must pass exactly once: {name}")
result = {"schema": 1, "platform": platform,
          "behaviors": [{"id": name, "status": "pass"} for name in behaviors]}
args.output.write_text(json.dumps(result, indent=2) + "\n")
print(json.dumps(result))
