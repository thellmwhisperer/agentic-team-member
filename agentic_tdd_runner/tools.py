"""Tool execution helpers for the agent runner."""

import os
import shlex
import subprocess
from collections.abc import Callable, MutableMapping
from pathlib import Path


def tool_applied_status(name: str, result: str) -> bool | None:
    """Return whether a mutating tool call actually changed files."""
    if name not in {"str_replace_editor", "create_file"}:
        return None
    return result.startswith("OK:")


def tool_loop_signature(name: str, args: dict) -> str | None:
    """Return a stable signature for exploratory calls that can loop."""
    if name == "read_file":
        return f"read_file:{args.get('path', '')}"
    if name != "run_command":
        return None
    command = args.get("command", "")
    if not isinstance(command, str):
        return None
    try:
        parts = shlex.split(command)
    except ValueError:
        return None
    if not parts:
        return None
    exploratory = {
        "grep", "rg", "find", "ls", "cat", "head", "tail", "sed", "awk",
        "wc", "sort", "uniq", "cut", "tr", "dirname", "basename", "tree",
        "file", "which", "test",
    }
    if parts[0] not in exploratory:
        return None
    return f"run_command:{command}"


def tool_loop_warning_message(signature: str) -> str:
    """Build feedback for repeated exploratory tool calls."""
    return (
        "Loop warning: you have repeated the same exploratory tool call "
        f"({signature}) three times without editing files. Stop rereading and "
        "either make an edit, explain the blocker, or say DONE if the test already passes."
    )


def execute_tool(
    name: str,
    args: dict,
    *,
    workdir: str,
    config: dict,
    file_read_cache: MutableMapping[Path, int],
    resolve_repo_path: Callable[[str], Path],
    validate_command: Callable[[str], None],
    set_last_run_exit_code: Callable[[int | None], None],
    detect_quality_tools: Callable[[str], list[dict]],
    typecheck_ownership_hint: Callable[[str, str, list[str]], str | None],
    is_test_file_path: Callable[[str], bool],
    test_runner_command_for_file: Callable[[str], str],
) -> str:
    """Execute one model-requested tool call."""
    try:
        if name == "read_file":
            full_path = resolve_repo_path(args["path"])
            if os.path.isdir(full_path):
                entries = os.listdir(full_path)
                return "\n".join(sorted(entries))
            mtime_ns = full_path.stat().st_mtime_ns
            if file_read_cache.get(full_path) == mtime_ns:
                return "File unchanged since last read. The content from the earlier read_file result in this conversation is still current — refer to that instead of re-reading."
            with open(full_path, "r") as f:
                content = f.read()
            file_read_cache[full_path] = mtime_ns
            return content

        if name == "run_command":
            validate_command(args["command"])
            set_last_run_exit_code(None)
            result = subprocess.run(
                args["command"],
                shell=True,
                cwd=workdir,
                capture_output=True,
                text=True,
                timeout=config["timeouts"]["tool_execution"],
            )
            set_last_run_exit_code(result.returncode)
            output = result.stdout + result.stderr
            return output if output.strip() else "(no output)"

        if name == "str_replace_editor":
            full_path = resolve_repo_path(args["path"])
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
            file_read_cache.pop(full_path, None)
            result = f"OK: replaced in {args['path']}"
            return (
                result
                + reactive_typecheck_feedback(
                    args["path"],
                    workdir=workdir,
                    config=config,
                    detect_quality_tools=detect_quality_tools,
                    typecheck_ownership_hint=typecheck_ownership_hint,
                )
                + reactive_forbidden_feedback(
                    args["path"],
                    workdir=workdir,
                    config=config,
                    is_test_file_path=is_test_file_path,
                )
            )

        if name == "create_file":
            full_path = resolve_repo_path(args["path"])
            if os.path.exists(full_path):
                return f"ERROR: {args['path']} already exists. Use str_replace_editor to modify it."
            os.makedirs(os.path.dirname(full_path), exist_ok=True)
            with open(full_path, "w") as f:
                f.write(args["content"])
            file_read_cache.pop(full_path, None)
            result = f"OK: created {args['path']}"
            return (
                result
                + reactive_typecheck_feedback(
                    args["path"],
                    workdir=workdir,
                    config=config,
                    detect_quality_tools=detect_quality_tools,
                    typecheck_ownership_hint=typecheck_ownership_hint,
                )
                + reactive_test_feedback(
                    args["path"],
                    workdir=workdir,
                    config=config,
                    is_test_file_path=is_test_file_path,
                    test_runner_command_for_file=test_runner_command_for_file,
                )
                + reactive_forbidden_feedback(
                    args["path"],
                    workdir=workdir,
                    config=config,
                    is_test_file_path=is_test_file_path,
                )
            )

        return f"ERROR: unknown tool {name}"

    except Exception as e:
        return f"ERROR: {type(e).__name__}: {e}"


def reactive_typecheck_feedback(
    path: str,
    *,
    workdir: str,
    config: dict,
    detect_quality_tools: Callable[[str], list[dict]],
    typecheck_ownership_hint: Callable[[str, str, list[str]], str | None],
) -> str:
    """Run the detected typecheck immediately after editing source files."""
    from agentic_tdd_runner.languages import get_language

    lang = get_language(path)
    if not lang:
        return ""

    checks = detect_quality_tools(lang.name)
    typecheck = next((check for check in checks if check.get("name") == "typecheck"), None)
    if not typecheck:
        return ""
    timeout_s = (config or {}).get("timeouts", {}).get("tool_execution", 10)

    try:
        result = subprocess.run(
            typecheck["command"],
            shell=True,
            cwd=workdir,
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
    ownership_hint = typecheck_ownership_hint("typecheck", raw, [path])
    if ownership_hint:
        sample = f"{sample}\n  {ownership_hint}"
    return f"\n\n[Reactive typecheck] {n_errors} errors:\n{sample}"


def reactive_test_feedback(
    path: str,
    *,
    workdir: str,
    config: dict,
    is_test_file_path: Callable[[str], bool],
    test_runner_command_for_file: Callable[[str], str],
) -> str:
    """Run the just-created test file immediately and return compact failure feedback."""
    if not is_test_file_path(path):
        return ""

    run_cmd = test_runner_command_for_file(path)
    timeout_s = (config or {}).get("timeouts", {}).get("test_run", 30)
    run_argv = shlex.split(run_cmd) + [path]

    try:
        result = subprocess.run(
            run_argv,
            cwd=workdir,
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


def reactive_forbidden_feedback(
    path: str,
    *,
    workdir: str,
    config: dict,
    is_test_file_path: Callable[[str], bool],
) -> str:
    """Return fast feedback for deterministic test quality violations."""
    if not is_test_file_path(path):
        return ""

    quality_cfg = (config or {}).get("quality", {})
    if not quality_cfg.get("enabled", False):
        return ""

    from agentic_tdd_runner.languages import get_language
    from agentic_tdd_runner.quality import (
        format_duplicated_setup_finding,
        partition_duplicated_test_lines,
    )

    lang = get_language(path)
    lang_name = lang.name if lang else "typescript"
    forbidden = quality_cfg.get(lang_name, {}).get("forbidden", [])

    full_path = Path(workdir) / path
    try:
        file_text = full_path.read_text(errors="replace")
    except OSError as e:
        return f"\n\n[Reactive forbidden] ERROR: {e}"

    failures: list[str] = []
    forbidden_hits: list[str] = []
    for pattern in forbidden:
        for line_no, line in enumerate(file_text.splitlines(), 1):
            if pattern in line:
                forbidden_hits.append(f"  {path}:{line_no} '{pattern}'")
    if forbidden_hits:
        sample = "\n".join(forbidden_hits[:5])
        failures.append(
            f"[Forbidden] {path}: {len(forbidden_hits)} forbidden patterns\n{sample}"
        )

    setup_dupes, _ambiguous_dupes = partition_duplicated_test_lines(file_text)
    if setup_dupes:
        failures.append(format_duplicated_setup_finding(path, setup_dupes))

    if not failures:
        return ""

    details = "\n\n".join(failures)
    return (
        "\n\n[Reactive forbidden] Potential quality issues detected before DONE:\n"
        f"{details}"
    )
