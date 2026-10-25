#!/usr/bin/env python3
"""通过本地 HTTP 与真实 CLI 核对发布门禁，不连接 GitHub 或发布版本。"""

import copy
import http.server
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
import urllib.parse
import zipfile


ROOT = Path(__file__).resolve().parents[1]
COMMIT = "1" * 40
IMAGE = "ghcr.io/example/hexclaw@sha256:" + "a" * 64


class ReleaseGate(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="hexclaw-release-gate-")
        self.addCleanup(self.temporary.cleanup)
        self.output = Path(self.temporary.name) / "outputs"
        self.state = {
            "comparison": "ahead",
            "ci": [{"id": 11, "head_sha": COMMIT, "head_branch": "main", "event": "push",
                    "conclusion": "success", "head_repository": {"full_name": "example/hexclaw"}}],
            "artifacts": [{"id": 33, "name": "hexclaw-deployment-" + COMMIT,
                           "expired": False, "workflow_run": {"id": 22}}],
            # workflow_run 的默认分支 SHA 可以已经前进，真实候选身份由结果与来源 CI 绑定。
            "run": {"id": 22, "path": ".github/workflows/deploy.yml", "event": "workflow_run",
                    "status": "completed", "conclusion": "success", "head_sha": "2" * 40,
                    "head_repository": {"full_name": "example/hexclaw"}},
            "receipt": {"status": "deployed", "commit": COMMIT, "image": IMAGE,
                        "actions_run": "22", "source_ci_run_id": "11", "environment": "hexclaw-cloud"},
        }
        owner = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_GET(self):
                if self.headers.get("Authorization") != "Bearer fixture-only":
                    self.send_error(401)
                    return
                path = urllib.parse.urlsplit(self.path).path
                prefix = "/repos/example/hexclaw"
                status = 200
                if path == prefix + "/commits/v1.2.3":
                    body = {"sha": COMMIT}
                elif path == prefix + "/compare/" + COMMIT + "...main":
                    body = {"status": owner.state["comparison"]}
                elif path == prefix + "/actions/workflows/ci.yml/runs":
                    body = {"workflow_runs": owner.state["ci"]}
                elif path == prefix + "/actions/artifacts":
                    body = {"artifacts": owner.state["artifacts"]}
                elif path == prefix + "/actions/runs/22":
                    body = owner.state["run"]
                elif path == prefix + "/actions/artifacts/33/zip":
                    stream = io.BytesIO()
                    with zipfile.ZipFile(stream, "w") as archive:
                        archive.writestr("deployment-result.json", json.dumps(owner.state["receipt"]))
                    data = stream.getvalue()
                    self.send_response(200)
                    self.end_headers()
                    self.wfile.write(data)
                    return
                else:
                    body, status = {"message": "Not Found"}, 404
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(json.dumps(body).encode())

        self.server = http.server.HTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.stop_server)

    def stop_server(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)

    def invoke(self):
        env = dict(os.environ, GH_TOKEN="fixture-only", GITHUB_OUTPUT=str(self.output))
        return subprocess.run([sys.executable, str(ROOT / "release_gate.py"), "--repository", "example/hexclaw",
                               "--tag", "v1.2.3", "--api-url", "http://127.0.0.1:" + str(self.server.server_port)],
                              capture_output=True, text=True, env=env, timeout=15)

    def test_accepts_matching_main_ci_and_final_deployed_result(self):
        result = self.invoke()
        self.assertEqual(0, result.returncode, result.stderr)
        record = json.loads(result.stdout)
        self.assertEqual(COMMIT, record["commit"])
        self.assertEqual(IMAGE, record["image"])
        self.assertEqual("22", record["deployment_run_id"])
        self.assertEqual("commit=" + COMMIT + "\n", self.output.read_text())

    def test_rejects_failed_or_other_commit_ci(self):
        original = copy.deepcopy(self.state["ci"])
        for change in ({"conclusion": "failure"}, {"head_sha": "3" * 40}, {"head_branch": "feat/other"}):
            with self.subTest(change=change):
                self.state["ci"] = copy.deepcopy(original)
                self.state["ci"][0].update(change)
                self.assertNotEqual(0, self.invoke().returncode)
        self.assertFalse(self.output.exists())

    def test_rejects_skipped_unknown_or_retention_pending_result(self):
        for status in ("superseded", "starting", "recovery_required", "deployed_retention_pending", "failed_before_start"):
            with self.subTest(status=status):
                self.state["receipt"]["status"] = status
                self.assertNotEqual(0, self.invoke().returncode)
        self.assertFalse(self.output.exists())

    def test_rejects_missing_expired_or_failed_deployment(self):
        self.state["artifacts"][0]["expired"] = True
        self.assertNotEqual(0, self.invoke().returncode)
        self.state["artifacts"][0]["expired"] = False
        self.state["run"]["conclusion"] = "failure"
        self.assertNotEqual(0, self.invoke().returncode)
        self.state["artifacts"] = []
        self.assertNotEqual(0, self.invoke().returncode)
        self.assertFalse(self.output.exists())

    def test_rejects_mismatched_receipt_identity(self):
        original = copy.deepcopy(self.state["receipt"])
        for change in ({"commit": "4" * 40}, {"actions_run": "99"}, {"source_ci_run_id": "99"},
                       {"environment": "another-target"}, {"image": "ghcr.io/another/app@sha256:" + "a" * 64}):
            with self.subTest(change=change):
                self.state["receipt"] = dict(original, **change)
                self.assertNotEqual(0, self.invoke().returncode)
        self.assertFalse(self.output.exists())

    def test_rejects_commit_outside_main(self):
        self.state["comparison"] = "diverged"
        self.assertNotEqual(0, self.invoke().returncode)
        self.assertFalse(self.output.exists())


if __name__ == "__main__":
    unittest.main(verbosity=2)
