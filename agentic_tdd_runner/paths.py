"""Path and test-file helpers for the agent runner."""

import fnmatch
import re
from pathlib import Path, PurePosixPath

from agentic_tdd_runner.runner_authority import override_detected_runner
from agentic_tdd_runner.languages import language_for
from agentic_tdd_runner.runner_command import (
    _configured_test_command,
    effective_test_command_template,
    js_test_command_template,
)

def resolve_repo_path(path: str, workdir: str) -> Path:
    """Resolve a relative path within the workdir. Raises if it escapes."""
    repo_root = Path(workdir).resolve()
    candidate = (repo_root / path).resolve()
    try:
        candidate.relative_to(repo_root)
    except ValueError as exc:
        raise ValueError(f"path escapes workdir: {path}") from exc
    return candidate


def is_test_file_path(path: str, config: dict | None = None) -> bool:
    patterns = (config or {}).get("runner", {}).get("test_file_patterns", [])
    name = PurePosixPath(path).name
    if not patterns:
        lower_name = name.lower()
        return bool(re.search(r"(^test(?:[_\.-]|$)|(?:[_\.-]test)(?:[_\.-]|$))", lower_name))
    return any(fnmatch.fnmatch(name, pattern) for pattern in patterns)


def test_runner_command_for_file(path: str, config: dict | None = None) -> str:
    runner_config = (config or {}).get("runner", {}) or {}
    language = language_for(path)
    if language == "python":
        return "python3 -m pytest"
    if language == "typescript":
        return _js_test_command(runner_config)
    if override_detected_runner(runner_config) and runner_config.get("command"):
        return str(runner_config.get("command"))
    return effective_test_command_template(
        runner_config.get("bootstrap"),
        runner_config.get("command"),
    )


def _js_test_command(runner_config: dict) -> str:
    configured = _configured_test_command(runner_config.get("command"))
    if override_detected_runner(runner_config):
        configured_runner = runner_config.get("framework")
        if configured:
            return configured
        if isinstance(configured_runner, str) and configured_runner:
            return js_test_command_template({"test_runner": configured_runner}, None)
    detected = js_test_command_template(runner_config.get("bootstrap"), configured)
    return detected or configured or "bun test"


