from __future__ import annotations

import os
import shutil
import socket
import subprocess
import sys
import time
from pathlib import Path
import tomllib

DEFAULT_PROXY_HOST = "127.0.0.1"
DEFAULT_PROXY_PORT = 11435
DEFAULT_BASE_CONFIG = "/app/config/agent.toml"
DEFAULT_AGENTCORE_CONFIG = "/tmp/atm-agentcore/agentcore-agent.toml"

_PROXY_PROCESS: subprocess.Popen | None = None


def configure_model_runner_from_env() -> subprocess.Popen | None:
    """Prepare the ATM harness to call a local OpenAI-compatible Bedrock proxy."""
    if not env_bool("ATM_ENABLE_BEDROCK_PROXY", default=False):
        return None

    host = os.environ.get("ATM_PROXY_HOST", DEFAULT_PROXY_HOST)
    port = int(os.environ.get("ATM_PROXY_PORT", str(DEFAULT_PROXY_PORT)))
    model_id = os.environ.get("ATM_BEDROCK_MODEL_ID")
    if not model_id:
        raise KeyError("ATM_BEDROCK_MODEL_ID is required when ATM_ENABLE_BEDROCK_PROXY=true")
    model_name = bedrock_model_id(model_id)
    proxy_url = f"http://{host}:{port}/v1/chat/completions"

    source_config = Path(os.environ.get("ATM_BASE_CONFIG", DEFAULT_BASE_CONFIG))
    target_config = Path(os.environ.get("ATM_CONFIG", DEFAULT_AGENTCORE_CONFIG))
    render_agentcore_config(
        source_config,
        target_config,
        model=model_name,
        url=proxy_url,
        base_branch=os.environ.get("ATM_BASE_BRANCH"),
        branch_prefix=os.environ.get("ATM_BRANCH_PREFIX"),
        permission_driven=env_bool_or_none("ATM_PERMISSION_DRIVEN"),
        max_steps=env_int_or_none("ATM_MAX_STEPS"),
        disable_local_judges=env_bool("ATM_DISABLE_LOCAL_JUDGES", default=True),
    )
    os.environ["ATM_CONFIG"] = str(target_config)

    return ensure_bedrock_proxy(host=host, port=port, model=model_name)


def bedrock_model_id(model_id: str) -> str:
    model = model_id.strip()
    if not model:
        raise ValueError("model_id cannot be empty")
    for prefix in ("bedrock/converse/", "bedrock/"):
        if model.startswith(prefix):
            return model.removeprefix(prefix)
    return model


def render_agentcore_config(
    source: Path,
    target: Path,
    *,
    model: str,
    url: str,
    base_branch: str | None = None,
    branch_prefix: str | None = None,
    permission_driven: bool | None = None,
    max_steps: int | None = None,
    disable_local_judges: bool = True,
) -> None:
    """Write a cloud-safe ATM config derived from the full harness config."""
    agent_replacements = {}
    if permission_driven is not None:
        agent_replacements["permission_driven"] = "true" if permission_driven else "false"
    if max_steps is not None:
        agent_replacements["max_steps"] = str(max_steps)

    pr_replacements = {}
    if base_branch:
        pr_replacements["base_branch"] = _toml_string(base_branch)
    if branch_prefix:
        pr_replacements["branch_prefix"] = _toml_string(branch_prefix)

    text = source.read_text()
    rendered = _replace_toml_keys(
        text,
        replacements={
            "agent": agent_replacements,
            "llm": {
                "url": _toml_string(url),
                "model": _toml_string(model),
            },
            "quality.duplicated_setup_judge": {
                "enabled": "false" if disable_local_judges else "true",
            },
            "pr": pr_replacements,
        },
    )
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(rendered)
    _copy_referenced_tools_file(source, target)


def ensure_bedrock_proxy(*, host: str, port: int, model: str) -> subprocess.Popen:
    global _PROXY_PROCESS
    if _PROXY_PROCESS is not None and _PROXY_PROCESS.poll() is None:
        return _PROXY_PROCESS

    env = os.environ.copy()
    command = [
        os.environ.get("ATM_PROXY_PYTHON", sys.executable),
        "-m",
        os.environ.get("ATM_PROXY_MODULE", "runtime.bedrock_openai_proxy"),
        "--host",
        host,
        "--port",
        str(port),
        "--model",
        model,
    ]
    region = os.environ.get("AWS_REGION") or os.environ.get("AWS_DEFAULT_REGION")
    if region:
        command.extend(["--region", region])
    _PROXY_PROCESS = subprocess.Popen(command, env=env)
    wait_for_tcp(host, port, timeout=float(os.environ.get("ATM_PROXY_STARTUP_TIMEOUT", "45")))
    returncode = _PROXY_PROCESS.poll()
    if returncode is not None:
        raise RuntimeError(f"Bedrock proxy process exited before becoming ready: {returncode}")
    return _PROXY_PROCESS


def wait_for_tcp(host: str, port: int, *, timeout: float) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with socket.create_connection((host, port), timeout=1):
                return
        except OSError:
            time.sleep(0.25)
    raise RuntimeError(f"Bedrock proxy did not start on {host}:{port} within {timeout:.0f}s")


def env_bool(name: str, *, default: bool) -> bool:
    value = os.environ.get(name)
    if value is None:
        return default
    return value.strip().lower() in {"1", "true", "yes", "on"}


def env_bool_or_none(name: str) -> bool | None:
    value = os.environ.get(name)
    if value is None or not value.strip():
        return None
    return value.strip().lower() in {"1", "true", "yes", "on"}


def env_int_or_none(name: str) -> int | None:
    value = os.environ.get(name)
    if value is None or not value.strip():
        return None
    try:
        result = int(value)
    except ValueError as exc:
        raise ValueError(f"{name} must be an integer, got {value!r}") from exc
    if result <= 0:
        raise ValueError(f"{name} must be a positive integer, got {result}")
    return result


def _replace_toml_keys(text: str, replacements: dict[str, dict[str, str]]) -> str:
    lines = text.splitlines()
    current_section: str | None = None
    output: list[str] = []
    pending = {
        section: dict(section_replacements)
        for section, section_replacements in replacements.items()
        if section_replacements
    }

    def flush_missing_section_keys(section: str | None) -> None:
        section_replacements = pending.get(section or "")
        if not section_replacements:
            return
        for key, value in section_replacements.items():
            output.append(f"{key} = {value}")
        section_replacements.clear()

    for line in lines:
        stripped = line.strip()
        if stripped.startswith("[") and stripped.endswith("]"):
            flush_missing_section_keys(current_section)
            current_section = stripped.strip("[]")
            output.append(line)
            continue

        section_replacements = pending.get(current_section or "")
        if section_replacements:
            key = stripped.split("=", 1)[0].strip() if "=" in stripped else ""
            if key in section_replacements and not stripped.startswith("#"):
                indent = line[: len(line) - len(line.lstrip())]
                output.append(f"{indent}{key} = {section_replacements.pop(key)}")
                continue

        output.append(line)

    flush_missing_section_keys(current_section)

    return "\n".join(output) + ("\n" if text.endswith("\n") else "")


def _toml_string(value: str) -> str:
    escaped = value.replace("\\", "\\\\").replace('"', '\\"')
    return f'"{escaped}"'


def _copy_referenced_tools_file(source: Path, target: Path) -> None:
    tools_ref = _read_tools_ref(source)
    source_dir = source.parent.resolve()
    target_dir = target.parent.resolve()
    source_tools = (source_dir / tools_ref).resolve()
    target_tools = (target_dir / tools_ref).resolve()
    source_tools.relative_to(source_dir)
    target_tools.relative_to(target_dir)
    if source_tools == target_tools:
        return
    target_tools.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source_tools, target_tools)


def _read_tools_ref(source: Path) -> str:
    with source.open("rb") as handle:
        config = tomllib.load(handle)
    tools_config = config.get("tools", {}) or {}
    if isinstance(tools_config, dict):
        return str(tools_config.get("file", "tools.json"))
    return "tools.json"
