from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path
from urllib.parse import urlparse

from atm_cloud.github_gateway import load_github_token
from atm_cloud.job import AtmJob
from atm_cloud.roca import RocaMcpClient
from atm_cloud.runner import AtmCloudRunner, NoopMemoryClient, SubprocessAtmHarness

try:
    from runtime.model_runner import configure_model_runner_from_env
except ImportError:  # pragma: no cover - supports direct script execution.
    from model_runner import configure_model_runner_from_env

try:
    from bedrock_agentcore.runtime import BedrockAgentCoreApp
except ImportError:  # Local tests/dev can import handle without AgentCore installed.
    BedrockAgentCoreApp = None


def build_runner_from_env() -> AtmCloudRunner:
    github_token = configure_runtime_environment()
    mcp_url = os.environ.get("ROCA_CLOUD_MCP_URL")
    memory = RocaMcpClient(mcp_url, load_roca_token()) if mcp_url else NoopMemoryClient()
    harness = SubprocessAtmHarness(
        python=os.environ.get("ATM_PYTHON", "python"),
        module=os.environ.get("ATM_HARNESS_MODULE", "agentic_tdd_runner.agent"),
        repo_root=os.environ.get("ATM_SOURCE_REPO"),
        timeout=int(os.environ.get("ATM_TIMEOUT_SECONDS", "1800")),
        github_token=github_token,
    )
    return AtmCloudRunner(memory=memory, harness=harness)


def configure_runtime_environment() -> str | None:
    github_token = configure_github_auth_from_env()
    configure_git_identity_from_env()
    configure_model_runner_from_env()
    return github_token


def configure_github_auth_from_env() -> str | None:
    token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN")
    if not token and not os.environ.get("GITHUB_TOKEN_SECRET_ARN"):
        return None
    if not token:
        token = load_github_token()
    configure_github_packages_auth(token)
    configure_git_https_auth(token)
    return token


def configure_github_packages_auth(token: str) -> None:
    if not _env_bool("ATM_ENABLE_GITHUB_PACKAGES_AUTH", default=False):
        return

    scope = os.environ.get("ATM_NPM_SCOPE")
    if not scope:
        raise KeyError("ATM_NPM_SCOPE is required when ATM_ENABLE_GITHUB_PACKAGES_AUTH=true")
    registry = os.environ.get("ATM_NPM_REGISTRY", "https://npm.pkg.github.com")
    registry_host = urlparse(registry).netloc
    if not registry_host:
        raise ValueError(f"invalid npm registry URL: {registry}")

    npmrc_path = Path(os.environ.get("ATM_NPMRC_PATH", str(Path.home() / ".npmrc")))
    npmrc_path.parent.mkdir(parents=True, exist_ok=True)
    npmrc_path.write_text(
        f"{scope}:registry={registry}\n"
        f"//{registry_host}/:_authToken={token}\n"
    )
    npmrc_path.chmod(0o600)
    os.environ.setdefault("NPM_CONFIG_USERCONFIG", str(npmrc_path))


def configure_git_https_auth(token: str) -> None:
    if os.environ.get("ATM_ENABLE_GIT_HTTPS_AUTH", "true").lower() not in {"1", "true", "yes", "on"}:
        return

    token_path = Path(
        os.environ.get(
            "ATM_GIT_TOKEN_FILE",
            str(Path(tempfile.gettempdir()) / "atm-agentcore-git-token"),
        )
    )
    token_path.parent.mkdir(parents=True, exist_ok=True)
    token_path.write_text(token)
    token_path.chmod(0o600)
    os.environ.setdefault("ATM_GIT_TOKEN_FILE", str(token_path))

    askpass_path = Path(
        os.environ.get(
            "ATM_GIT_ASKPASS_PATH",
            str(Path(tempfile.gettempdir()) / "atm-agentcore-git-askpass.sh"),
        )
    )
    askpass_path.parent.mkdir(parents=True, exist_ok=True)
    askpass_path.write_text(
        "#!/bin/sh\n"
        "if [ -z \"${ATM_GIT_TOKEN_FILE:-}\" ] || [ ! -r \"$ATM_GIT_TOKEN_FILE\" ]; then\n"
        "  exit 1\n"
        "fi\n"
        "case \"$1\" in\n"
        "  *Username*) printf '%s\\n' \"x-access-token\" ;;\n"
        "  *Password*) cat \"$ATM_GIT_TOKEN_FILE\" ;;\n"
        "  *) cat \"$ATM_GIT_TOKEN_FILE\" ;;\n"
        "esac\n"
    )
    askpass_path.chmod(0o700)
    os.environ.setdefault("GIT_ASKPASS", str(askpass_path))
    os.environ.setdefault("GIT_TERMINAL_PROMPT", "0")


def configure_git_identity_from_env() -> None:
    name = os.environ.get("ATM_GIT_USER_NAME", "ATM AgentCore")
    email = os.environ.get("ATM_GIT_USER_EMAIL", "atm-agentcore@example.invalid")
    os.environ.setdefault("GIT_AUTHOR_NAME", name)
    os.environ.setdefault("GIT_AUTHOR_EMAIL", email)
    os.environ.setdefault("GIT_COMMITTER_NAME", name)
    os.environ.setdefault("GIT_COMMITTER_EMAIL", email)
    subprocess.run(["git", "config", "--global", "user.name", name], check=False)
    subprocess.run(["git", "config", "--global", "user.email", email], check=False)


def load_roca_token() -> str:
    token = os.environ.get("ROCA_CLOUD_API_TOKEN")
    if token:
        return token
    secret_arn = os.environ.get("ROCA_CLOUD_API_TOKEN_SECRET_ARN")
    if not secret_arn:
        raise KeyError("ROCA_CLOUD_API_TOKEN or ROCA_CLOUD_API_TOKEN_SECRET_ARN")
    import boto3

    raw = boto3.client("secretsmanager").get_secret_value(SecretId=secret_arn)["SecretString"]
    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        return raw
    return parsed.get("token") or next(iter(parsed.values()))


def _env_bool(name: str, *, default: bool) -> bool:
    value = os.environ.get(name)
    if value is None:
        return default
    return value.strip().lower() in {"1", "true", "yes", "on"}


def handle(payload: dict | None) -> dict:
    job = AtmJob.from_dict(payload or {})
    result = build_runner_from_env().run(job)
    return {
        "status": result.status,
        "run_id": job.run_id,
        "repo": job.repo,
        "issue_number": job.issue_number,
        "events": [
            {"phase": event.phase, "status": event.status, "metadata": event.metadata}
            for event in result.events
        ],
    }


if BedrockAgentCoreApp is not None:
    app = BedrockAgentCoreApp()

    @app.entrypoint
    async def invoke(payload=None):
        return handle(payload)


def main() -> int:
    if BedrockAgentCoreApp is not None:
        app.run()
        return 0
    payload = json.loads(sys.stdin.read() or "{}")
    print(json.dumps(handle(payload)))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
