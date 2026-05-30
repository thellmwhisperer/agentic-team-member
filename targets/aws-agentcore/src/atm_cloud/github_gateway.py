from __future__ import annotations

import json
import os
import urllib.request
from typing import Any


class UrlLibTransport:
    def request_json(
        self,
        method: str,
        url: str,
        *,
        headers: dict[str, str] | None = None,
        payload: dict[str, Any] | None = None,
    ) -> Any:
        data = json.dumps(payload).encode("utf-8") if payload is not None else None
        request = urllib.request.Request(url, data=data, headers=headers or {}, method=method)
        with urllib.request.urlopen(request, timeout=30) as response:
            raw = response.read().decode("utf-8")
            return json.loads(raw) if raw else {}


class GitHubGatewayClient:
    """Narrow GitHub API client intended to sit behind AgentCore Gateway."""

    def __init__(
        self,
        *,
        token: str,
        api_base: str = "https://api.github.com",
        transport: Any | None = None,
    ):
        self.token = token
        self.api_base = api_base.rstrip("/")
        self.transport = transport or UrlLibTransport()

    def get_issue(self, *, repo: str, issue_number: int) -> dict[str, Any]:
        _validate_repo_allowed(repo)
        return self._request("GET", f"/repos/{repo}/issues/{issue_number}")

    def create_branch(self, *, repo: str, base: str, branch: str) -> dict[str, Any]:
        _validate_repo_allowed(repo)
        _validate_atm_branch(branch)
        base_ref = self._request("GET", f"/repos/{repo}/git/ref/heads/{base}")
        sha = base_ref["object"]["sha"]
        response = self._request(
            "POST",
            f"/repos/{repo}/git/refs",
            {"ref": f"refs/heads/{branch}", "sha": sha},
        )
        return {"branch": branch, "sha": response.get("object", {}).get("sha", sha), "raw": response}

    def commit_files(
        self,
        *,
        repo: str,
        branch: str,
        message: str,
        files: list[dict[str, str]],
    ) -> dict[str, Any]:
        _validate_repo_allowed(repo)
        _validate_atm_branch(branch)
        head_ref = self._request("GET", f"/repos/{repo}/git/ref/heads/{branch}")
        parent_sha = head_ref["object"]["sha"]
        parent = self._request("GET", f"/repos/{repo}/git/commits/{parent_sha}")
        base_tree = parent["tree"]["sha"]
        tree_entries = []
        for item in files:
            path = _required(item, "path")
            content = _required(item, "content")
            blob = self._request(
                "POST",
                f"/repos/{repo}/git/blobs",
                {"content": content, "encoding": "utf-8"},
            )
            tree_entries.append(
                {"path": path, "mode": "100644", "type": "blob", "sha": blob["sha"]}
            )
        tree = self._request(
            "POST",
            f"/repos/{repo}/git/trees",
            {"base_tree": base_tree, "tree": tree_entries},
        )
        commit = self._request(
            "POST",
            f"/repos/{repo}/git/commits",
            {"message": message, "tree": tree["sha"], "parents": [parent_sha]},
        )
        self._request(
            "PATCH",
            f"/repos/{repo}/git/refs/heads/{branch}",
            {"sha": commit["sha"]},
        )
        return {"commit_sha": commit["sha"], "branch": branch, "files": [item["path"] for item in files]}

    def open_pr(self, *, repo: str, head: str, base: str, title: str, body: str) -> dict[str, Any]:
        _validate_repo_allowed(repo)
        _validate_atm_branch(head)
        response = self._request(
            "POST",
            f"/repos/{repo}/pulls",
            {
                "head": head,
                "base": base,
                "title": title,
                "body": body,
            },
        )
        return {"pr_url": response.get("html_url"), "raw": response}

    def comment_issue(self, *, repo: str, issue_number: int, body: str) -> dict[str, Any]:
        _validate_repo_allowed(repo)
        response = self._request(
            "POST",
            f"/repos/{repo}/issues/{issue_number}/comments",
            {"body": body},
        )
        return {"comment_url": response.get("html_url"), "raw": response}

    def _request(self, method: str, path: str, payload: dict[str, Any] | None = None) -> Any:
        return self.transport.request_json(
            method,
            f"{self.api_base}{path}",
            headers={
                "authorization": f"Bearer {self.token}",
                "accept": "application/vnd.github+json",
                "x-github-api-version": "2022-11-28",
                "user-agent": "atm-aws-agentcore-gateway",
            },
            payload=payload,
        )


def resolve_tool_name(event: dict[str, Any], context: Any) -> str:
    if event.get("tool_name"):
        return str(event["tool_name"])
    test_context = event.get("__context__", {})
    if isinstance(test_context, dict) and test_context.get("bedrockagentcoreToolName"):
        return str(test_context["bedrockagentcoreToolName"])
    custom = getattr(getattr(context, "client_context", None), "custom", None)
    if isinstance(custom, dict) and custom.get("bedrockagentcoreToolName"):
        return str(custom["bedrockagentcoreToolName"])
    raise ValueError("could not resolve AgentCore Gateway tool name")


def dispatch_tool(tool_name: str, payload: dict[str, Any], client: GitHubGatewayClient) -> dict[str, Any]:
    if tool_name == "github_get_issue":
        return client.get_issue(repo=_required(payload, "repo"), issue_number=int(_required(payload, "issue_number")))
    if tool_name == "github_create_branch":
        return client.create_branch(
            repo=_required(payload, "repo"),
            base=_required(payload, "base"),
            branch=_required(payload, "branch"),
        )
    if tool_name == "github_commit_files":
        files = _required(payload, "files")
        if not isinstance(files, list) or not files:
            raise ValueError("files must be a non-empty list")
        return client.commit_files(
            repo=_required(payload, "repo"),
            branch=_required(payload, "branch"),
            message=_required(payload, "message"),
            files=files,
        )
    if tool_name == "github_open_pr":
        return client.open_pr(
            repo=_required(payload, "repo"),
            head=_required(payload, "head"),
            base=_required(payload, "base"),
            title=_required(payload, "title"),
            body=_required(payload, "body"),
        )
    if tool_name == "github_comment_issue":
        return client.comment_issue(
            repo=_required(payload, "repo"),
            issue_number=int(_required(payload, "issue_number")),
            body=_required(payload, "body"),
        )
    raise ValueError(f"unsupported GitHub tool: {tool_name}")


def lambda_handler(event: dict[str, Any], context: Any) -> dict[str, Any]:
    try:
        tool_name = resolve_tool_name(event, context)
        token = load_github_token()
        result = dispatch_tool(tool_name, event, GitHubGatewayClient(token=token))
        return {"statusCode": 200, "body": json.dumps(result)}
    except (KeyError, ValueError) as exc:
        return {"statusCode": 400, "body": json.dumps({"error": str(exc)})}
    except Exception as exc:
        return {"statusCode": 500, "body": json.dumps({"error": str(exc)})}


def _required(payload: dict[str, Any], key: str) -> Any:
    value = payload.get(key)
    if value in (None, ""):
        raise ValueError(f"missing required field: {key}")
    return value


def load_github_token() -> str:
    token = os.environ.get("GITHUB_TOKEN")
    if token:
        return token
    secret_arn = os.environ.get("GITHUB_TOKEN_SECRET_ARN")
    if not secret_arn:
        raise KeyError("GITHUB_TOKEN or GITHUB_TOKEN_SECRET_ARN")
    import boto3

    raw = boto3.client("secretsmanager").get_secret_value(SecretId=secret_arn)["SecretString"]
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        return raw
    return parsed.get("token") or next(iter(parsed.values()))


def _validate_repo_allowed(repo: str) -> None:
    allowlist = os.environ.get("GITHUB_REPO_ALLOWLIST")
    if not allowlist:
        return
    allowed = {item.strip() for item in allowlist.split(",") if item.strip()}
    if repo not in allowed:
        raise ValueError(f"repo is not allowlisted: {repo}")


def _validate_atm_branch(branch: str) -> None:
    prefix = os.environ.get("ATM_BRANCH_PREFIX", "atm-agentcore/")
    if not branch.startswith(prefix):
        raise ValueError(f"branch must start with {prefix}")
