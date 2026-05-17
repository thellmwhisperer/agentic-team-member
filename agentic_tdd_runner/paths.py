"""Path and test-file helpers for the agent runner."""

import fnmatch
import os
import re
import subprocess
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


def _git_status_entry_step(status_code: str) -> int:
    return 2 if any(marker in status_code for marker in ("R", "C")) else 1


def find_test_file(hint: str | None, workdir: str, config: dict) -> str | None:
    """Find the test file the agent created. Uses hint from cookbook if available."""
    if hint:
        try:
            hinted = resolve_repo_path(hint, workdir)
        except ValueError:
            hinted = None
        if hinted and hinted.is_file():
            return os.path.relpath(hinted, workdir)

    status_result = subprocess.run(
        ["git", "status", "--porcelain", "-z"],
        cwd=workdir,
        capture_output=True,
        text=False,
    )
    if status_result.returncode == 0:
        changed_test_files = []
        exclude_prefixes = [
            PurePosixPath(ex).parts
            for ex in config["runner"].get("exclude_dirs", [])
        ]
        entries = [
            entry
            for entry in status_result.stdout.decode("utf-8", errors="surrogateescape").split("\0")
            if entry
        ]
        idx = 0
        while idx < len(entries):
            raw_line = entries[idx]
            if len(raw_line) < 4:
                idx += 1
                continue
            status_code = raw_line[:2]
            rel = raw_line[3:]
            rel_parts = PurePosixPath(rel).parts
            step = _git_status_entry_step(status_code)
            if any(rel_parts[:len(prefix)] == prefix for prefix in exclude_prefixes):
                idx += step
                continue
            if not is_test_file_path(rel, config):
                idx += step
                continue
            try:
                resolved = resolve_repo_path(rel, workdir)
            except ValueError:
                idx += step
                continue
            if resolved.is_file():
                changed_test_files.append(os.path.relpath(resolved, workdir))
            idx += step
        if changed_test_files:
            return sorted(dict.fromkeys(changed_test_files))[0]

    patterns = config["runner"]["test_file_patterns"]
    exclude = config["runner"].get("exclude_dirs", [])
    for root, _dirs, files in os.walk(workdir):
        if any(ex in root for ex in exclude):
            continue
        for filename in files:
            if any(fnmatch.fnmatch(filename, pattern) for pattern in patterns):
                full = os.path.join(root, filename)
                rel = os.path.relpath(full, workdir)
                result = subprocess.run(
                    ["git", "ls-files", "--", rel],
                    cwd=workdir,
                    capture_output=True,
                    text=True,
                )
                if not result.stdout.strip():
                    return rel
    return None
