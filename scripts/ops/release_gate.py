#!/usr/bin/env python3
"""在发布前核对同一提交的主 CI 与真实部署结果，复用 GitHub 现有记录。"""

import argparse
import io
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
import zipfile


class GateError(Exception):
    pass


class ArtifactRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, response, code, message, headers, new_url):
        redirected = super().redirect_request(request, response, code, message, headers, new_url)
        if redirected and urllib.parse.urlsplit(request.full_url).netloc != urllib.parse.urlsplit(new_url).netloc:
            redirected.remove_header("Authorization")
        return redirected


class GitHub:
    def __init__(self, repository, token, api_url):
        self.repository = repository
        self.token = token
        self.api_url = api_url.rstrip("/")
        self.opener = urllib.request.build_opener(ArtifactRedirect())

    def get(self, path, *, binary=False):
        request = urllib.request.Request(self.api_url + "/repos/" + self.repository + path,
                                        headers={"Authorization": "Bearer " + self.token,
                                                 "Accept": "application/vnd.github+json",
                                                 "X-GitHub-Api-Version": "2022-11-28"})
        try:
            with self.opener.open(request, timeout=30) as response:
                body = response.read()
        except urllib.error.HTTPError as error:
            raise GateError("GitHub lookup failed with HTTP " + str(error.code)) from error
        except urllib.error.URLError as error:
            raise GateError("GitHub lookup could not be completed") from error
        return body if binary else json.loads(body)

    def pages(self, path, key):
        separator = "&" if "?" in path else "?"
        page = 1
        while True:
            items = self.get(path + separator + urllib.parse.urlencode({"per_page": 100, "page": page}))[key]
            yield from items
            if len(items) < 100:
                return
            page += 1


def check_release(client, tag):
    if not re.fullmatch(r"v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?", tag):
        raise GateError("A version tag is required")
    commit = client.get("/commits/" + urllib.parse.quote(tag, safe=""))["sha"]
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise GateError("The tag did not resolve to a full commit")
    comparison = client.get("/compare/" + commit + "...main")
    if comparison.get("status") not in {"ahead", "identical"}:
        raise GateError("The tag commit is not on main")
    ci_ids = set()
    query = urllib.parse.urlencode({"head_sha": commit, "event": "push", "branch": "main", "status": "completed"})
    for run in client.pages("/actions/workflows/ci.yml/runs?" + query, "workflow_runs"):
        if (run.get("head_sha") == commit and run.get("head_branch") == "main"
                and run.get("event") == "push" and run.get("conclusion") == "success"
                and run.get("head_repository", {}).get("full_name") == client.repository):
            ci_ids.add(str(run["id"]))
    if not ci_ids:
        raise GateError("The tag commit has no successful main push CI")

    name = "hexclaw-deployment-" + commit
    image_pattern = re.compile(re.escape("ghcr.io/" + client.repository.lower()) + r"@sha256:[0-9a-f]{64}")
    for artifact in client.pages("/actions/artifacts?" + urllib.parse.urlencode({"name": name}), "artifacts"):
        if artifact.get("name") != name or artifact.get("expired"):
            continue
        run_id = artifact.get("workflow_run", {}).get("id")
        if not run_id:
            continue
        run = client.get("/actions/runs/" + str(run_id))
        if (run.get("path", "").split("@", 1)[0] != ".github/workflows/deploy.yml"
                or run.get("event") != "workflow_run" or run.get("status") != "completed"
                or run.get("conclusion") != "success"
                or run.get("head_repository", {}).get("full_name") != client.repository):
            continue
        # 只读取已知结果文件，不展开归档内的其他文件。
        try:
            data = client.get("/actions/artifacts/" + str(artifact["id"]) + "/zip", binary=True)
            with zipfile.ZipFile(io.BytesIO(data)) as archive:
                result = json.loads(archive.read("deployment-result.json"))
        except (KeyError, ValueError, zipfile.BadZipFile) as error:
            raise GateError("The deployment result artifact is invalid") from error
        if (result.get("status") == "deployed" and result.get("commit") == commit
                and str(result.get("actions_run")) == str(run_id)
                and str(result.get("source_ci_run_id")) in ci_ids
                and result.get("environment") == "hexclaw-cloud"
                and image_pattern.fullmatch(result.get("image", ""))):
            return {"tag": tag, "commit": commit, "image": result["image"],
                    "ci_run_id": str(result["source_ci_run_id"]),
                    "deployment_run_id": str(run_id), "artifact_id": str(artifact["id"])}
    raise GateError("The tag commit has no matching finalized deployed result")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", default=os.environ.get("GITHUB_REPOSITORY"), required=not os.environ.get("GITHUB_REPOSITORY"))
    parser.add_argument("--tag", required=True)
    parser.add_argument("--api-url", default=os.environ.get("GITHUB_API_URL", "https://api.github.com"))
    args = parser.parse_args()
    token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN")
    if not token:
        parser.error("A GitHub token is required in GH_TOKEN or GITHUB_TOKEN")
    try:
        result = check_release(GitHub(args.repository, token, args.api_url), args.tag)
        output = os.environ.get("GITHUB_OUTPUT")
        if output:
            with open(output, "a") as destination:
                destination.write("commit=" + result["commit"] + "\n")
        print(json.dumps(result))
        return 0
    except (GateError, OSError, ValueError, KeyError, TypeError) as error:
        message = str(error) if isinstance(error, GateError) else type(error).__name__
        print("Release gate failed: " + message, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
