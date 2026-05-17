"""Path and test-file helpers for the agent runner."""

import fnmatch
import re
from pathlib import Path, PurePosixPath


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
    from agentic_tdd_runner.languages import get_language

    lang = get_language(path)
    if lang and lang.runner == "pytest":
        return "python3 -m pytest"
    return (config or {}).get("runner", {}).get("command", "bun test")
