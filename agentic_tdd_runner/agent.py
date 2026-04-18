#!/usr/bin/env python3
"""Agentic TDD runner — local LLM fixes bugs with tests."""

import argparse
import json
import os
import re
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

import requests

# --- Load .env if present ---
_env_path = Path.cwd() / ".env"
if _env_path.exists():
    for line in _env_path.read_text().splitlines():
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            key, _, value = line.partition("=")
            os.environ.setdefault(key.strip(), value.strip())

# --- Force unbuffered stdout ---
sys.stdout.reconfigure(line_buffering=True)
sys.stderr.reconfigure(line_buffering=True)

# --- Config (loaded from TOML, overridden by env vars / CLI args) ---
_CONFIG = None  # populated by main()
WORKDIR = os.environ.get("AGENT_WORKDIR", os.getcwd())
LOG_DIR = os.environ.get("AGENT_LOG_DIR", os.getcwd())
_last_run_exit_code: int | None = None
_file_read_cache: dict[Path, int] = {}

# --- Logging ---
_log_file = None

def init_log():
    global _log_file
    ts = datetime.now().strftime("%Y%m%d-%H%M%S")
    path = os.path.join(LOG_DIR, f"agent-{ts}.jsonl")
    os.makedirs(LOG_DIR, exist_ok=True)
    _log_file = open(path, "w")
    log("init", {"workdir": WORKDIR, "max_steps": _CONFIG["agent"]["max_steps"], "model": _CONFIG["llm"]["model"], "log": path})
    emit(f"LOG: {path}")
    return path

def log(event: str, data: dict):
    entry = {
        "ts": datetime.now(timezone.utc).isoformat(),
        "event": event,
        **data,
    }
    if _log_file:
        _log_file.write(json.dumps(entry, ensure_ascii=False) + "\n")
        _log_file.flush()

def emit(msg: str):
    print(msg, flush=True)


# Tools and prompts loaded from config/agent.toml + config/tools.json


def apply_mechanical_edits(edits: list[dict], workdir: str) -> int:
    """Apply pre_test_source_edits to files on disk. Returns count of edits applied."""
    applied = 0
    for edit in edits:
        try:
            full_path = _resolve_repo_path(edit["path"], workdir=workdir)
        except ValueError:
            emit(f"  [PREP] SKIP: path escapes workdir: {edit['path']}")
            continue
        try:
            content = full_path.read_text()
        except FileNotFoundError:
            emit(f"  [PREP] SKIP: {edit['path']} not found")
            continue
        except OSError as exc:
            emit(f"  [PREP] SKIP: cannot read {edit['path']}: {exc}")
            continue
        if edit["old"] not in content:
            emit(f"  [PREP] SKIP: old text not found in {edit['path']}")
            continue
        content = content.replace(edit["old"], edit["new"], 1)
        try:
            full_path.write_text(content)
        except OSError as exc:
            emit(f"  [PREP] SKIP: cannot write {edit['path']}: {exc}")
            continue
        emit(f"  [PREP] Applied edit to {edit['path']}")
        applied += 1
    return applied


def _quality_retry_feedback_message(quality_msg: str, test_file: str) -> dict:
    return {
        "role": "user",
        "content": (
            f"Verification already passed for {test_file}. Preserve the current fix behavior and "
            f"only address the residual quality issues below.\n\n{quality_msg}"
        ),
    }


def _compact_messages_after_quality_failure(messages: list[dict], quality_msg: str, test_file: str) -> list[dict]:
    _file_read_cache.clear()
    compacted: list[dict] = []
    if messages and messages[0].get("role") == "system":
        compacted.append(messages[0])

    issue_msg = next((msg for msg in messages[1:] if msg.get("role") == "user"), None)
    if issue_msg:
        compacted.append(issue_msg)

    compacted.append(_quality_retry_feedback_message(quality_msg, test_file))
    return compacted


def _llm_context_window_tokens() -> int:
    llm_cfg = (_CONFIG or {}).get("llm", {})
    raw_value = (
        llm_cfg.get("context_window_tokens")
        or llm_cfg.get("context_window")
        or llm_cfg.get("num_ctx")
        or 32768
    )
    try:
        return int(raw_value)
    except (TypeError, ValueError):
        return 32768


def _coerce_int(value) -> int | None:
    if isinstance(value, bool):
        return None
    if isinstance(value, int):
        return value
    if isinstance(value, float):
        return int(value)
    if isinstance(value, str):
        value = value.strip()
        if value.isdigit():
            return int(value)
    return None


def _should_compact_after_quality_failure(last_usage: dict | None) -> tuple[bool, dict]:
    quality_cfg = (_CONFIG or {}).get("quality", {})
    context_window = _llm_context_window_tokens()
    try:
        threshold_ratio = float(quality_cfg.get("compact_threshold_ratio", 0.85))
    except (TypeError, ValueError):
        threshold_ratio = 0.85
    min_headroom_tokens = _coerce_int(quality_cfg.get("compact_min_headroom_tokens"))
    if min_headroom_tokens is None:
        min_headroom_tokens = 2048
    prompt_tokens = _coerce_int((last_usage or {}).get("prompt_tokens"))

    info = {
        "context_window_tokens": context_window,
        "threshold_ratio": threshold_ratio,
        "min_headroom_tokens": min_headroom_tokens,
        "prompt_tokens": prompt_tokens,
    }

    if prompt_tokens is None:
        info["reason"] = "missing_prompt_tokens"
        return False, info

    headroom_tokens = context_window - prompt_tokens
    info["headroom_tokens"] = headroom_tokens
    info["prompt_ratio"] = round(prompt_tokens / context_window, 4) if context_window else None

    should_compact = (
        prompt_tokens >= int(context_window * threshold_ratio)
        or headroom_tokens <= min_headroom_tokens
    )
    info["reason"] = "near_context_limit" if should_compact else "enough_headroom"
    return should_compact, info


def _resolve_repo_path(path: str, workdir: str = None) -> Path:
    """Resolve a relative path within the workdir. Raises if it escapes."""
    repo_root = Path(workdir or WORKDIR).resolve()
    candidate = (repo_root / path).resolve()
    try:
        candidate.relative_to(repo_root)
    except ValueError as exc:
        raise ValueError(f"path escapes workdir: {path}") from exc
    return candidate


_ALLOWED_COMMANDS = frozenset({
    "git", "grep", "rg", "find", "ls", "cat", "head", "tail", "wc",
    "bun", "node", "npm", "npx", "pnpm", "yarn", "deno",
    "python", "python3", "pip", "pip3", "pytest",
    "echo", "sort", "uniq", "diff", "tr", "cut", "tee",
    "sed", "awk", "xargs", "dirname", "basename",
    "tree", "file", "which", "true", "false", "test",
})

# Flags that allow arbitrary code execution on otherwise safe binaries
_BLOCKED_FLAGS = {
    "python": {"-c"},
    "python3": {"-c"},
    "node": {"-e", "--eval"},
    "deno": {"eval"},
}


def _validate_command(command: str) -> None:
    """Validate that all commands in a pipeline/chain use allowed binaries."""
    import shlex
    if not command or not command.strip():
        raise ValueError("empty command")
    # Reject newlines — they bypass shell operator splitting
    if "\n" in command:
        raise ValueError("newlines not allowed in commands")
    # Split on shell operators to validate each sub-command
    parts = re.split(r"\s*(?:\|\||&&|[|;])\s*", command)
    for part in parts:
        part = part.strip()
        if not part:
            continue
        try:
            tokens = shlex.split(part)
        except ValueError:
            tokens = part.split()
        if not tokens:
            continue
        binary = os.path.basename(tokens[0])
        if binary not in _ALLOWED_COMMANDS:
            raise ValueError(
                f"command '{binary}' is not allowed. "
                f"Allowed: {', '.join(sorted(_ALLOWED_COMMANDS))}"
            )
        # Block dangerous flag combinations
        blocked = _BLOCKED_FLAGS.get(binary, set())
        if blocked:
            for token in tokens[1:]:
                if token in blocked:
                    raise ValueError(
                        f"flag '{token}' not allowed with '{binary}'"
                    )
        # Block env as a wrapper to run arbitrary binaries
        if binary == "env":
            # env VAR=val cmd or env cmd — validate the actual command too
            for token in tokens[1:]:
                if "=" in token:
                    continue  # env var assignment
                # This is the actual binary being wrapped
                wrapped = os.path.basename(token)
                if wrapped not in _ALLOWED_COMMANDS:
                    raise ValueError(
                        f"command '{wrapped}' (via env) is not allowed"
                    )
                # Check blocked flags for the wrapped binary too
                wrapped_blocked = _BLOCKED_FLAGS.get(wrapped, set())
                remaining = tokens[tokens.index(token) + 1:]
                for flag in remaining:
                    if flag in wrapped_blocked:
                        raise ValueError(
                            f"flag '{flag}' not allowed with '{wrapped}' (via env)"
                        )
                break


def execute_tool(name: str, args: dict) -> str:
    global _last_run_exit_code
    _last_run_exit_code = None
    try:
        if name == "read_file":
            full_path = _resolve_repo_path(args["path"])
            if os.path.isdir(full_path):
                entries = os.listdir(full_path)
                return "\n".join(sorted(entries))
            mtime_ns = full_path.stat().st_mtime_ns
            if _file_read_cache.get(full_path) == mtime_ns:
                return (
                    "File unchanged since last read. The content from the earlier read_file result "
                    "in this conversation is still current — refer to that instead of re-reading."
                )
            with open(full_path, "r") as f:
                content = f.read()
            _file_read_cache[full_path] = mtime_ns
            return content

        elif name == "run_command":
            _validate_command(args["command"])
            result = subprocess.run(
                args["command"],
                shell=True,
                cwd=WORKDIR,
                capture_output=True,
                text=True,
                timeout=_CONFIG["timeouts"]["tool_execution"],
            )
            _last_run_exit_code = result.returncode
            output = result.stdout + result.stderr
            return output if output.strip() else "(no output)"

        elif name == "str_replace_editor":
            full_path = _resolve_repo_path(args["path"])
            with open(full_path, "r") as f:
                content = f.read()
            old_str = args["old_str"]
            if old_str not in content:
                return f"ERROR: old_str not found in {args['path']}. Read the file first to get the exact text."
            if content.count(old_str) > 1:
                return f"ERROR: old_str appears {content.count(old_str)} times. Make it more specific."
            new_content = content.replace(old_str, args["new_str"], 1)
            with open(full_path, "w") as f:
                f.write(new_content)
            _file_read_cache.pop(full_path, None)
            result = f"OK: replaced in {args['path']}"
            return result + _reactive_typecheck_feedback(args["path"])

        elif name == "create_file":
            full_path = _resolve_repo_path(args["path"])
            if os.path.exists(full_path):
                return f"ERROR: {args['path']} already exists. Use str_replace_editor to modify it."
            os.makedirs(os.path.dirname(full_path), exist_ok=True)
            with open(full_path, "w") as f:
                f.write(args["content"])
            _file_read_cache.pop(full_path, None)
            result = f"OK: created {args['path']}"
            return result + _reactive_typecheck_feedback(args["path"]) + _reactive_test_feedback(args["path"])

        else:
            return f"ERROR: unknown tool {name}"

    except Exception as e:
        return f"ERROR: {type(e).__name__}: {e}"


def _reactive_typecheck_feedback(path: str) -> str:
    """Run typecheck after edit/create and return inline error summary."""
    from agentic_tdd_runner.languages import get_language
    import shlex
    lang = get_language(path)
    if not lang:
        return ""

    checks = detect_quality_tools(lang.name)
    typecheck = next((check for check in checks if check.get("name") == "typecheck"), None)
    if not typecheck:
        return ""
    if typecheck.get("reactive") is False:
        return "\n\n[Reactive typecheck] SKIPPED: project-wide fallback is too expensive; add scripts.typecheck to enable inline TS feedback"
    command = typecheck["command"]
    if "{changed_files}" in command:
        command = command.format(changed_files=shlex.quote(path))
    timeout_s = (_CONFIG or {}).get("timeouts", {}).get("tool_execution", 10)

    try:
        result = subprocess.run(
            command,
            shell=True,
            cwd=WORKDIR,
            capture_output=True,
            text=True,
            timeout=timeout_s,
        )
    except subprocess.TimeoutExpired:
        return "\n\n[Reactive typecheck] TIMEOUT: command timed out"
    except (OSError, UnicodeDecodeError) as e:
        return f"\n\n[Reactive typecheck] ERROR: {e}"

    if result.returncode == 0:
        return ""

    raw = (result.stdout or "") + (result.stderr or "")
    lines = [ln for ln in raw.splitlines() if ln.strip()]
    n_errors = sum(1 for ln in lines if "error" in ln.lower())
    sample = "\n".join(f"  {ln}" for ln in lines[:30])
    if not sample:
        sample = f"  {raw[:500]}"
    return f"\n\n[Reactive typecheck] {n_errors} errors:\n{sample}"


def _reactive_test_feedback(path: str) -> str:
    """Run tests after create_file on test files and return inline failure summary."""
    if not _is_test_file_path(path):
        return ""

    import shlex
    run_cmd = _test_runner_command_for_file(path)
    timeout_s = (_CONFIG or {}).get("timeouts", {}).get("test_run", 30)
    run_argv = shlex.split(run_cmd) + [path]

    try:
        result = subprocess.run(
            run_argv,
            cwd=WORKDIR,
            capture_output=True,
            text=True,
            timeout=timeout_s,
        )
    except subprocess.TimeoutExpired:
        return "\n\n[Reactive test] TIMEOUT: command timed out"
    except (OSError, UnicodeDecodeError) as e:
        return f"\n\n[Reactive test] ERROR: {e}"

    if result.returncode == 0:
        return ""

    raw = (result.stdout or "") + (result.stderr or "")
    sample_lines = [ln for ln in raw.splitlines() if ln.strip()][:5]
    sample = "\n".join(f"  {ln}" for ln in sample_lines) if sample_lines else f"  {raw[:200]}"
    return f"\n\n[Reactive test] failed:\n{sample}"


def _is_test_file_path(path: str) -> bool:
    from pathlib import PurePosixPath
    import fnmatch
    patterns = (_CONFIG or {}).get("runner", {}).get("test_file_patterns", [])
    name = PurePosixPath(path).name
    if not patterns:
        stem = PurePosixPath(path).stem.lower()
        if stem in {"test", "tests"}:
            return True
        return stem.startswith(("test_", "test-")) or stem.endswith(("_test", "-test", ".test"))
    return any(fnmatch.fnmatch(name, pattern) for pattern in patterns)


def _test_runner_command_for_file(path: str) -> str:
    from agentic_tdd_runner.languages import get_language
    lang = get_language(path)
    if lang and lang.runner == "pytest":
        return "python3 -m pytest"
    return (_CONFIG or {}).get("runner", {}).get("command", "bun test")


def _detect_package_manager(pkg: dict | None = None) -> str:
    """Detect the package manager. Checks packageManager field first, then lockfiles."""
    if pkg:
        pm_field = pkg.get("packageManager", "")
        if pm_field:
            name = pm_field.split("@")[0]
            if name in ("pnpm", "yarn", "bun", "npm"):
                return name
    lockfiles = {
        "pnpm-lock.yaml": "pnpm",
        "yarn.lock": "yarn",
        "bun.lock": "bun",
    }
    for filename, pm in lockfiles.items():
        if os.path.isfile(os.path.join(WORKDIR, filename)):
            return pm
    return "npm"


def detect_quality_tools(lang_name: str) -> list[dict]:
    """Detect quality tools from the target project's config files.

    Reads package.json (TypeScript) or pyproject.toml (Python) to discover
    which lint/format/typecheck tools are actually installed.
    """
    checks = []

    if lang_name == "typescript":
        pkg_path = os.path.join(WORKDIR, "package.json")
        if os.path.isfile(pkg_path):
            import json as _json
            try:
                with open(pkg_path) as f:
                    pkg = _json.load(f)
            except (OSError, ValueError):
                return checks

            scripts = pkg.get("scripts", {})
            dev_deps = pkg.get("devDependencies", {})
            deps = pkg.get("dependencies", {})
            all_deps = {**deps, **dev_deps}

            if "typecheck" in scripts:
                pm = _detect_package_manager(pkg)
                checks.append({"name": "typecheck", "command": f"{pm} run typecheck"})
            elif "typescript" in all_deps:
                checks.append({
                    "name": "typecheck",
                    "command": "npx tsc --noEmit",
                    "reactive": False,
                })

            if any(k.startswith("@biomejs/biome") for k in all_deps):
                checks.append({
                    "name": "lint",
                    "command": "npx biome check {changed_files}",
                    "fix": "npx biome check {changed_files} --fix",
                })
            elif "eslint" in all_deps:
                checks.append({
                    "name": "lint",
                    "command": "npx eslint {changed_files}",
                    "fix": "npx eslint {changed_files} --fix",
                })

            has_biome = any(k.startswith("@biomejs/biome") for k in all_deps)
            if not has_biome and "prettier" in all_deps:
                checks.append({
                    "name": "format",
                    "command": "npx prettier --check {changed_files}",
                    "fix": "npx prettier --write {changed_files}",
                })

    elif lang_name == "python":
        pyproject_path = os.path.join(WORKDIR, "pyproject.toml")
        has_ruff = False
        python_typecheck = None
        if os.path.isfile(pyproject_path):
            try:
                with open(pyproject_path, "rb") as f:
                    import tomllib
                    pyproject = tomllib.load(f)
                tool_cfg = pyproject.get("tool", {})
                has_ruff = "ruff" in tool_cfg
                if "mypy" in tool_cfg:
                    python_typecheck = "python3 -m mypy {changed_files}"
                elif "basedpyright" in tool_cfg:
                    python_typecheck = "npx basedpyright {changed_files}"
                elif "pyright" in tool_cfg:
                    python_typecheck = "npx pyright {changed_files}"
            except (OSError, ValueError):
                pass

        if not python_typecheck:
            if any(os.path.isfile(os.path.join(WORKDIR, name)) for name in ("mypy.ini", ".mypy.ini")):
                python_typecheck = "python3 -m mypy {changed_files}"
            elif os.path.isfile(os.path.join(WORKDIR, "basedpyrightconfig.json")):
                python_typecheck = "npx basedpyright {changed_files}"
            elif os.path.isfile(os.path.join(WORKDIR, "pyrightconfig.json")):
                python_typecheck = "npx pyright {changed_files}"

        if python_typecheck:
            checks.append({
                "name": "typecheck",
                "command": python_typecheck,
            })

        if has_ruff:
            checks.append({
                "name": "lint",
                "command": "python3 -m ruff check {changed_files}",
                "fix": "python3 -m ruff check {changed_files} --fix",
            })
            checks.append({
                "name": "format",
                "command": "python3 -m ruff format --check {changed_files}",
                "fix": "python3 -m ruff format {changed_files}",
            })

    return checks


def _run_git_capture(args: list[str], *, error_context: str) -> subprocess.CompletedProcess[str]:
    """Run a git command and raise with stderr if it fails."""
    result = subprocess.run(args, cwd=WORKDIR, capture_output=True, text=True)
    if result.returncode != 0:
        detail = (result.stderr or result.stdout or "").strip() or "no output captured"
        raise RuntimeError(f"{error_context} failed (rc={result.returncode}): {detail}")
    return result


def _get_changed_files() -> list[str]:
    """Get modified + staged + untracked files relative to WORKDIR."""
    diff = _run_git_capture(
        ["git", "diff", "--name-only"],
        error_context="changed-file discovery via `git diff --name-only`",
    )
    staged = _run_git_capture(
        ["git", "diff", "--cached", "--name-only"],
        error_context="changed-file discovery via `git diff --cached --name-only`",
    )
    untracked = _run_git_capture(
        ["git", "ls-files", "--others", "--exclude-standard"],
        error_context="changed-file discovery via `git ls-files --others --exclude-standard`",
    )
    files = set()
    for line in (diff.stdout + staged.stdout + untracked.stdout).splitlines():
        line = line.strip()
        if line and os.path.exists(os.path.join(WORKDIR, line)):
            files.add(line)
    return sorted(files)


def run_quality_checks(test_file: str) -> tuple[bool, str]:
    """Run quality checks on changed files. Returns (passed, message)."""
    from agentic_tdd_runner.languages import get_language
    import shlex

    quality_cfg = _CONFIG.get("quality", {})
    if not quality_cfg.get("enabled", False):
        return True, "Quality checks disabled"

    lang = get_language(test_file)
    if lang is None:
        return True, f"Quality checks skipped: no language plugin for {test_file!r}"

    lang_name = lang.name
    lang_cfg = quality_cfg.get(lang_name, {})
    checks = detect_quality_tools(lang_name) or lang_cfg.get("checks", [])
    forbidden = lang_cfg.get("forbidden", [])

    all_changed = _get_changed_files()
    if not all_changed:
        return True, "No changed files"

    changed = [f for f in all_changed if os.path.splitext(f)[1] in lang.extensions]
    if not changed:
        return True, "No changed files matching language"

    changed_str = " ".join(shlex.quote(path) for path in changed)
    failures = []

    for idx, check in enumerate(checks, 1):
        if not isinstance(check, dict):
            failures.append(
                f"[check #{idx}] invalid quality check config: expected mapping, got {type(check).__name__}"
            )
            continue

        check_name = check.get("name")
        if not isinstance(check_name, str) or not check_name.strip():
            failures.append(f"[check #{idx}] invalid quality check config: missing string 'name'")
            continue

        raw_cmd = check.get("command")
        if not isinstance(raw_cmd, str) or not raw_cmd.strip():
            failures.append(f"[{check_name}] invalid quality check config: missing string 'command'")
            continue

        raw_fix = check.get("fix", "")
        if raw_fix is None:
            raw_fix = ""
        if not isinstance(raw_fix, str):
            failures.append(f"[{check_name}] invalid quality check config: 'fix' must be a string")
            continue

        try:
            fix_cmd = raw_fix.replace("{changed_files}", changed_str)
            if fix_cmd:
                fix_result = subprocess.run(
                    fix_cmd,
                    shell=True,
                    cwd=WORKDIR,
                    capture_output=True,
                    text=True,
                    timeout=_CONFIG["timeouts"]["tool_execution"],
                )
                if fix_result.returncode != 0:
                    raw = (fix_result.stdout or "") + (fix_result.stderr or "")
                    lines = [ln for ln in raw.splitlines() if ln.strip()]
                    sample = "\n".join(f"  {ln}" for ln in lines[:5]) if lines else "  no output captured"
                    failures.append(
                        f"[{check_name}:fix] autofix failed (rc={fix_result.returncode}):\n{sample}"
                    )

            cmd = raw_cmd.replace("{changed_files}", changed_str)
            result = subprocess.run(
                cmd,
                shell=True,
                cwd=WORKDIR,
                capture_output=True,
                text=True,
                timeout=_CONFIG["timeouts"]["tool_execution"],
            )
            if result.returncode != 0:
                raw = (result.stdout or "") + (result.stderr or "")
                lines = [ln for ln in raw.splitlines() if ln.strip()]
                n_errors = sum(1 for ln in lines if "error" in ln.lower()) or 1
                sample = "\n".join(f"  {ln}" for ln in lines[:10]) if lines else f"  {raw[:200]}"
                failures.append(f"[{check_name}] {n_errors} errors:\n{sample}")
        except subprocess.TimeoutExpired:
            failures.append(f"[{check_name}] TIMEOUT: command timed out")
        except (OSError, UnicodeDecodeError) as e:
            failures.append(f"[{check_name}] ERROR: {e}")

    forbidden_by_file: dict[str, list[str]] = {}
    for f in changed:
        full = os.path.join(WORKDIR, f)
        if not os.path.isfile(full):
            continue
        try:
            with open(full, errors="replace") as fh:
                content = fh.read()
        except OSError:
            continue
        for pattern in forbidden:
            for i, line in enumerate(content.splitlines(), 1):
                if pattern in line:
                    forbidden_by_file.setdefault(f, []).append(f"  {f}:{i} '{pattern}'")

    for f, hits in forbidden_by_file.items():
        failures.append(f"[Forbidden] {f}: {len(hits)} forbidden patterns\n" + "\n".join(hits[:20]))

    if failures:
        details = "\n\n".join(failures)
        template = _CONFIG.get("prompt", {}).get("quality_failed", "QUALITY CHECK FAILED:\n\n{details}")
        return False, template.replace("{details}", details)

    return True, "All quality checks passed"


def truncate(text: str, max_chars: int = 0) -> str:
    if max_chars == 0:
        max_chars = _CONFIG["agent"]["max_tool_output"]
    if len(text) <= max_chars:
        return text
    half = max_chars // 2
    return text[:half] + f"\n\n... ({len(text) - max_chars} chars truncated) ...\n\n" + text[-half:]


def chat(messages: list) -> dict:
    llm = _CONFIG["llm"]
    payload = {
        "model": llm["model"],
        "messages": messages,
        "tools": _CONFIG["tools"],
        "temperature": llm.get("temperature", 0.6),
        "top_p": llm.get("top_p", 0.95),
        "top_k": llm.get("top_k", 20),
    }
    resp = requests.post(llm["url"], json=payload, timeout=_CONFIG["timeouts"]["llm_request"])
    resp.raise_for_status()
    return resp.json()


def find_test_file(hint: str | None = None) -> str | None:
    """Find the test file the agent created. Uses hint from cookbook if available."""
    import fnmatch
    if hint:
        hinted = os.path.join(WORKDIR, hint)
        if os.path.isfile(hinted):
            return hint
    patterns = _CONFIG["runner"]["test_file_patterns"]
    exclude = _CONFIG["runner"].get("exclude_dirs", [])
    for root, _dirs, files in os.walk(WORKDIR):
        if any(ex in root for ex in exclude):
            continue
        for f in files:
            if any(fnmatch.fnmatch(f, p) for p in patterns):
                full = os.path.join(root, f)
                rel = os.path.relpath(full, WORKDIR)
                result = _run_git_capture(
                    ["git", "ls-files", "--", rel],
                    error_context=f"test-file discovery via `git ls-files -- {rel}`",
                )
                if not result.stdout.strip():
                    return rel
    return None


def verify_red_green(test_file: str) -> tuple[bool, str]:
    """Verify red-green: test fails without fix, passes with fix.

    1. Stash current changes (with fix)
    2. Run test — should FAIL (red)
    3. Restore fix from stash
    4. Run test — should PASS (green)
    """
    from agentic_tdd_runner.languages import get_language
    lang = get_language(test_file)
    if lang and lang.runner == "pytest":
        run_cmd = "python3 -m pytest"
    else:
        run_cmd = _CONFIG["runner"]["command"]
    test_timeout = _CONFIG["timeouts"]["test_run"]

    emit("\n=== RED-GREEN VERIFICATION ===")

    # Preserve the test file (may be untracked) before stashing
    import shutil
    import tempfile
    test_full = os.path.join(WORKDIR, test_file)
    test_backup = None
    if os.path.isfile(test_full):
        test_backup = tempfile.mktemp(suffix=os.path.basename(test_file))
        shutil.copy2(test_full, test_backup)

    # Stash all changes including untracked (reverts source fix + new files)
    subprocess.run(["git", "stash", "--include-untracked"], cwd=WORKDIR, capture_output=True)

    import shlex
    run_argv = shlex.split(run_cmd)

    red_passed = False
    red_output = ""
    green_passed = False
    green_output = ""

    try:
        # Restore the test file so the red phase can run it
        if test_backup:
            os.makedirs(os.path.dirname(test_full), exist_ok=True)
            shutil.copy2(test_backup, test_full)

        emit("  [RED] Running test WITHOUT fix...")
        red_result = subprocess.run(
            [*run_argv, test_file],
            cwd=WORKDIR, capture_output=True, text=True, timeout=test_timeout,
        )
        red_passed = red_result.returncode == 0
        red_output = (red_result.stdout + red_result.stderr)[:500]
        emit(f"  [RED] exit={red_result.returncode} {'PASS (BAD!)' if red_passed else 'FAIL (good)'}")
        for line in red_output.split("\n")[:10]:
            emit(f"    {line}")
    finally:
        # Always clean up temp files and pop stash, even after exceptions
        if test_backup:
            if os.path.isfile(test_full):
                os.remove(test_full)
            if os.path.isfile(test_backup):
                os.remove(test_backup)
        subprocess.run(["git", "stash", "pop"], cwd=WORKDIR, capture_output=True)

    emit("  [GREEN] Running test WITH fix...")
    green_result = subprocess.run(
        [*run_argv, test_file],
        cwd=WORKDIR, capture_output=True, text=True, timeout=test_timeout,
    )
    green_passed = green_result.returncode == 0
    green_output = (green_result.stdout + green_result.stderr)[:500]
    emit(f"  [GREEN] exit={green_result.returncode} {'PASS (good)' if green_passed else 'FAIL (BAD!)'}")
    for line in green_output.split("\n")[:10]:
        emit(f"    {line}")

    log("verify", {
        "test_file": test_file,
        "red_passed": red_passed,
        "green_passed": green_passed,
        "red_output": red_output,
        "green_output": green_output,
    })

    if red_passed:
        return False, (
            f"REJECTED: Your test ({test_file}) passes even WITHOUT your source fix. "
            f"This means it doesn't test the real code — it probably uses local stub functions "
            f"instead of importing from the source. Rewrite the test to import the real "
            f"function and mock its dependencies properly."
        )

    if not green_passed:
        return False, (
            f"REJECTED: Your test ({test_file}) fails even WITH your source fix. "
            f"The test has errors. Fix them. Error: {green_output[:300]}"
        )

    return True, "VERIFIED: Test fails without fix, passes with fix. Real red-green."


def _is_test_pass(name: str, args: dict) -> bool:
    """Detect if a tool call was a test runner that exited 0."""
    if name != "run_command" or _last_run_exit_code != 0:
        return False
    cmd = str(args.get("command", "")).strip()
    if not cmd:
        return False

    configured_runner = ((_CONFIG or {}).get("runner", {}) or {}).get("command", "")
    if configured_runner and (cmd == configured_runner or cmd.startswith(f"{configured_runner} ")):
        return True

    import shlex
    try:
        tokens = shlex.split(cmd)
    except ValueError:
        tokens = cmd.split()

    for token in reversed(tokens[1:]):
        if _is_test_file_path(token):
            runner_cmd = _test_runner_command_for_file(token)
            return cmd == runner_cmd or cmd.startswith(f"{runner_cmd} ")

    return False


def _default_config_path():
    """Find config/agent.toml relative to the package."""
    pkg = Path(__file__).parent.parent / "config" / "agent.toml"
    if pkg.exists():
        return str(pkg)
    return None


def parse_args():
    parser = argparse.ArgumentParser(description="Agentic TDD runner — local LLM fixes bugs with tests")
    parser.add_argument("issue", help="Issue text, or path to a file containing the issue description")
    parser.add_argument("--source", type=str, help="Source file path relative to workdir (e.g. src/twitch/client.ts)")
    parser.add_argument("--symbol", type=str, help="Target function/method name (e.g. handleResub)")
    parser.add_argument("--workdir", type=str, default=WORKDIR, help="Project root directory (default: cwd)")
    parser.add_argument("--config", type=str, default=_default_config_path(), help="Path to agent.toml config file")
    parser.add_argument("--log-dir", type=str, default=LOG_DIR, help="Directory for JSONL logs (default: cwd)")
    return parser.parse_args()


def main():
    global _CONFIG, WORKDIR, LOG_DIR
    args = parse_args()

    from agentic_tdd_runner.config import load_config
    _CONFIG = load_config(args.config)

    WORKDIR = args.workdir
    LOG_DIR = args.log_dir

    log_path = init_log()

    # Issue can be inline text or a path to a file
    issue_text = args.issue
    if os.path.isfile(issue_text):
        with open(issue_text) as f:
            issue_text = f.read()

    # Build system prompt — inject cookbook if source/symbol provided
    system_prompt = _CONFIG["prompt"]["system"].strip()
    episode = None
    if args.source and args.symbol:
        from agentic_tdd_runner.cookbook import build_episode_context
        episode = build_episode_context(
            source_path=args.source,
            symbol=args.symbol,
            project_root=WORKDIR,
        )
        system_prompt = f"{system_prompt}\n\n{episode['cookbook_text']}"
        emit(f"[EPISODE] Built episode context for {args.symbol} in {args.source}")
        log("episode", {
            "source": args.source,
            "symbol": args.symbol,
            "test_file": episode["test_file"],
            "mechanical_edits": len(episode.get("pre_test_source_edits", [])),
        })

        edits = episode.get("pre_test_source_edits", [])
        if edits:
            applied = apply_mechanical_edits(edits, WORKDIR)
            emit(f"[PREP] Applied {applied}/{len(edits)} mechanical source edits")
            log("mechanical_edits", {"applied": applied, "total": len(edits)})

    if episode:
        phase1_msg = (
            f"Read {episode['source_file']} and understand the bug below. "
            f"Focus on the function `{episode['target_symbol']}`. "
            f"Then create a failing test in {episode['test_file']} that reproduces it.\n\n"
            f"Bug:\n{issue_text}"
        )
        messages = [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": phase1_msg},
        ]
    else:
        messages = [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": f"Fix this bug:\n\n{issue_text}"},
        ]

    emit(f"{'='*60}")
    max_steps = _CONFIG["agent"]["max_steps"]
    emit(f"AGENT START — max {max_steps} steps")
    emit(f"Workdir: {WORKDIR}")
    emit(f"Log: {log_path}")
    emit(f"{'='*60}")

    log("start", {"issue": issue_text[:200]})

    done_rejected = 0  # how many times we rejected DONE
    max_rejections = _CONFIG["verification"]["max_rejections"]
    last_usage: dict | None = None

    def try_complete(step: int, assistant_msg: dict | None = None) -> str:
        nonlocal done_rejected, last_usage
        test_hint = episode["test_file"] if episode else None
        test_file = find_test_file(test_hint)
        if not test_file:
            emit("  [WARN] No test file found — cannot verify")
            if assistant_msg:
                messages.append(assistant_msg)
            messages.append({
                "role": "user",
                "content": _CONFIG["prompt"]["no_test_found"],
            })
            return "no_test"

        verified, verify_msg = verify_red_green(test_file)
        emit(f"\n  [VERIFY] {verify_msg}")
        log("verify_result", {"verified": verified, "message": verify_msg, "test_file": test_file})

        if not verified:
            done_rejected += 1
            if done_rejected >= max_rejections:
                emit(f"\n  [GIVE UP] Rejected DONE {done_rejected} times. Stopping.")
                log("give_up", {"step": step, "done_rejected": done_rejected})
                return "give_up"
            if assistant_msg:
                messages.append(assistant_msg)
            messages.append({
                "role": "user",
                "content": verify_msg,
            })
            return "verify_fail"

        if _CONFIG.get("quality", {}).get("enabled", False):
            emit("\n=== QUALITY CHECKS ===")
            quality_ok, quality_msg = run_quality_checks(test_file)
            emit(f"  [QUALITY] {quality_msg}")
            log("quality_result", {
                "passed": quality_ok,
                "message": quality_msg[:500],
                "test_file": test_file,
            })
            if not quality_ok:
                should_compact, compact_info = _should_compact_after_quality_failure(last_usage)
                if should_compact:
                    before_count = len(messages)
                    messages[:] = _compact_messages_after_quality_failure(messages, quality_msg, test_file)
                    log("context_compacted", {
                        "reason": "quality_fail",
                        "before_messages": before_count,
                        "after_messages": len(messages),
                        "test_file": test_file,
                        **compact_info,
                    })
                else:
                    if assistant_msg:
                        messages.append(assistant_msg)
                    messages.append(_quality_retry_feedback_message(quality_msg, test_file))
                    log("context_preserved", {
                        "reason": "quality_fail",
                        "message_count": len(messages),
                        "test_file": test_file,
                        **compact_info,
                    })
                return "quality_fail"

            verified, verify_msg = verify_red_green(test_file)
            emit(f"\n  [RE-VERIFY] {verify_msg}")
            log("post_quality_verify_result", {
                "verified": verified,
                "message": verify_msg,
                "test_file": test_file,
            })
            if not verified:
                done_rejected += 1
                if done_rejected >= max_rejections:
                    emit(f"\n  [GIVE UP] Rejected DONE {done_rejected} times. Stopping.")
                    log("give_up", {"step": step, "done_rejected": done_rejected})
                    return "give_up"
                if assistant_msg:
                    messages.append(assistant_msg)
                messages.append({
                    "role": "user",
                    "content": verify_msg,
                })
                return "verify_fail"

        emit(f"\n{'='*60}")
        emit(f"AGENT DONE at step {step} — VERIFIED")
        emit(f"{'='*60}")
        diff = subprocess.run(["git", "diff"], cwd=WORKDIR, capture_output=True, text=True)
        emit(f"\n--- GIT DIFF ---\n{diff.stdout}")
        untracked = subprocess.run(["git", "ls-files", "--others", "--exclude-standard"], cwd=WORKDIR, capture_output=True, text=True)
        if untracked.stdout.strip():
            emit("\n--- NEW FILES ---")
            for f in untracked.stdout.strip().split("\n"):
                emit(f"  {f}")
                full = os.path.join(WORKDIR, f)
                try:
                    with open(full) as fh:
                        emit(fh.read())
                except OSError:
                    pass
        log("done", {"step": step, "verified": True})
        return "done"

    for step in range(max_steps):
        emit(f"\n>>> Step {step} — requesting LLM...")
        t0 = time.time()

        try:
            response = chat(messages)
        except Exception as e:
            emit(f"  ERROR: {e}")
            log("error", {"step": step, "error": str(e)})
            break

        elapsed = time.time() - t0

        choice = response["choices"][0]
        msg = choice["message"]
        finish = choice["finish_reason"]
        usage = response.get("usage", {})
        last_usage = usage
        timings = response.get("timings", {})
        thinking = msg.get("reasoning_content", "")

        log("step", {
            "step": step,
            "elapsed_s": round(elapsed, 1),
            "finish_reason": finish,
            "thinking": thinking,
            "content": msg.get("content", ""),
            "tool_calls": [
                {"name": tc["function"]["name"], "args": tc["function"]["arguments"]}
                for tc in msg.get("tool_calls", [])
            ],
            "usage": usage,
            "timings": {
                "prompt_ms": timings.get("prompt_ms"),
                "predicted_ms": timings.get("predicted_ms"),
                "prompt_per_second": timings.get("prompt_per_second"),
                "predicted_per_second": timings.get("predicted_per_second"),
            },
        })

        prompt_tok = usage.get("prompt_tokens", "?")
        comp_tok = usage.get("completion_tokens", "?")
        tok_s = timings.get("predicted_per_second", 0)
        emit(f"--- Step {step} | {elapsed:.1f}s | {prompt_tok}→{comp_tok} tok | {tok_s:.1f} tok/s | finish={finish} ---")

        if thinking:
            emit(f"  [THINK] ({len(thinking)} chars)")
            for line in thinking.strip().split("\n"):
                emit(f"    {line}")

        if msg.get("content"):
            emit(f"  [SAY] {msg['content']}")
            if "DONE" in msg["content"].upper():
                completion = try_complete(step, msg)
                if completion == "done":
                    return 0
                if completion == "give_up":
                    return 1
                continue

        # Append assistant message to history
        messages.append(msg)

        if finish == "tool_calls" and msg.get("tool_calls"):
            test_passed = False
            created_test = False
            for tc in msg["tool_calls"]:
                fn = tc["function"]
                name = fn["name"]
                try:
                    args = json.loads(fn["arguments"])
                except json.JSONDecodeError:
                    args = {}
                    emit(f"  WARNING: failed to parse args: {fn['arguments'][:200]}")

                args_preview = json.dumps(args, ensure_ascii=False)
                if len(args_preview) > 300:
                    args_preview = args_preview[:300] + "..."
                emit(f"  [TOOL] {name}({args_preview})")

                t1 = time.time()
                result = execute_tool(name, args)
                tool_elapsed = time.time() - t1
                result_truncated = truncate(result)

                log("tool", {
                    "step": step,
                    "name": name,
                    "args": args,
                    "result_chars": len(result),
                    "result_truncated": len(result) != len(result_truncated),
                    "elapsed_s": round(tool_elapsed, 3),
                    "result": result_truncated,
                })

                display = result_truncated
                if len(display) > 500:
                    display = display[:250] + f"\n  ... ({len(result)} chars total) ...\n" + display[-250:]
                emit(f"  [RESULT] ({len(result)} chars, {tool_elapsed:.2f}s)")
                for line in display.split("\n")[:20]:
                    emit(f"    {line}")
                if display.count("\n") > 20:
                    emit(f"    ... ({display.count(chr(10))} lines total)")

                messages.append({
                    "role": "tool",
                    "tool_call_id": tc["id"],
                    "content": result_truncated,
                })

                if (
                    name == "create_file"
                    and result.startswith("OK: created ")
                    and _is_test_file_path(args.get("path", ""))
                ):
                    created_test = True
                if _is_test_pass(name, args):
                    test_passed = True

            if episode and created_test:
                nudge = (
                    f"Good. Now run the test to confirm it fails, then fix "
                    f"{episode['source_file']} to make it pass. Say DONE when green."
                )
                messages.append({"role": "user", "content": nudge})
                emit("  [PHASE] Test created → injected run+fix nudge")
                log("phase_nudge", {"phase": "fix", "test_file": episode["test_file"]})

            if test_passed:
                emit("\n  [AUTO] Test pass detected — triggering verification pipeline")
                completion = try_complete(step)
                if completion == "done":
                    return 0
                if completion == "give_up":
                    return 1

        elif finish == "stop":
            if step > 3:
                messages.append({
                    "role": "user",
                    "content": _CONFIG["prompt"]["nudge"]
                })
                emit("  [NUDGE] Continue prompt injected")
        else:
            emit(f"  [FINISH] {finish}")

    emit(f"\n{'='*60}")
    emit(f"AGENT EXHAUSTED — {max_steps} steps without DONE")
    emit(f"{'='*60}")
    log("exhausted", {"steps": max_steps})
    return 1


if __name__ == "__main__":
    sys.exit(main())
