"""Tool execution helpers for the agent runner."""

import os
import re
import shlex
import subprocess
from collections.abc import Callable, MutableMapping
from pathlib import Path

from agentic_tdd_runner.shell import build_command_env


def tool_applied_status(name: str, result: str) -> bool | None:
    """Return whether a mutating tool call actually changed files."""
    if name not in {"str_replace_editor", "create_file"}:
        return None
    return result.startswith("OK:")


def tool_loop_signature(name: str, args: dict) -> str | None:
    """Return a stable signature for exploratory calls that can loop."""
    if name == "read_file":
        return f"read_file:{args.get('path', '')}"
    if name == "rg":
        pattern = args.get("pattern") or args.get("query") or args.get("term") or ""
        path = args.get("path") or "."
        glob = args.get("glob") or ""
        return f"rg:{pattern}:{path}:{glob}"
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


def non_apply_step_warning_message(count: int) -> str:
    """Build feedback for tool-call turns that do not make code changes."""
    return (
        "Progress warning: you have spent "
        f"{count} consecutive tool-calling steps without a successful edit. "
        "Stop broad exploration. Use the issue, the files you already read, and "
        "the latest tool output to either make a focused edit, create the failing "
        "test, or say DONE if the bug is already fixed and tested."
    )


def is_blocked_dependency_contract_lookup(command: str, config: dict) -> bool:
    """Block stale dependency spelunking when concrete contract evidence exists."""
    runtime = (config or {}).get("_runtime", {})
    if not runtime.get("block_dependency_contract_lookup", False):
        return False
    if runtime.get("allow_dependency_contract_lookup", False):
        return False
    if "node_modules" not in command:
        return False
    try:
        parts = shlex.split(command)
    except ValueError:
        parts = command.split()
    if not parts:
        return False
    lookup_tools = {"rg", "grep", "find", "cat", "head", "tail", "sed", "awk", "ls"}
    return parts[0] in lookup_tools or "node_modules/@types" in command


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
    log: Callable[[str, dict], None] | None = None,
) -> str:
    """Execute one model-requested tool call."""
    try:
        if name == "ask_harness":
            return (
                "ERROR: ask_harness is handled by the runtime permission controller. "
                "If you see this, the runtime is misconfigured."
            )

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

        if name == "rg":
            paths = rg_paths_from_args(args)
            for path in paths:
                resolve_repo_path(path)
            command = build_rg_command(args)
            command_text = shlex.join(command)
            set_last_run_exit_code(None)
            if is_blocked_dependency_contract_lookup(command_text, config):
                if log:
                    log("dependency_contract_lookup_blocked", {
                        "command": command_text,
                        "runtime": dict((config or {}).get("_runtime", {})),
                    })
                return (
                    "BLOCKED: dependency contract lookup is disabled for this step "
                    "because the issue/cookbook already provided concrete contract "
                    "evidence. Use that evidence plus source and reactive compiler/test "
                    "feedback. Dependency lookup is allowed again after reactive "
                    "feedback contradicts the known contract."
                )
            validate_command(command_text)
            result = subprocess.run(
                command,
                shell=False,
                cwd=workdir,
                env=build_command_env(config),
                capture_output=True,
                text=True,
                timeout=config["timeouts"]["tool_execution"],
            )
            set_last_run_exit_code(result.returncode)
            output = (result.stdout or "") + (result.stderr or "")
            if output.strip():
                return output
            if result.returncode == 1:
                return "(no matches)"
            return "(no output)"

        if name == "run_command":
            set_last_run_exit_code(None)
            if is_blocked_dependency_contract_lookup(args["command"], config):
                if log:
                    log("dependency_contract_lookup_blocked", {
                        "command": args["command"],
                        "runtime": dict((config or {}).get("_runtime", {})),
                    })
                return (
                    "BLOCKED: dependency contract lookup is disabled for this step "
                    "because the issue/cookbook already provided concrete contract "
                    "evidence. Use that evidence plus source and reactive compiler/test "
                    "feedback. Dependency lookup is allowed again after reactive "
                    "feedback contradicts the known contract."
                )
            validate_command(args["command"])
            result = subprocess.run(
                args["command"],
                shell=True,
                cwd=workdir,
                env=build_command_env(config),
                capture_output=True,
                text=True,
                timeout=config["timeouts"]["tool_execution"],
            )
            set_last_run_exit_code(result.returncode)
            output = result.stdout + result.stderr
            if (
                output.strip()
                and result.returncode != 0
                and _is_test_run_command(args["command"])
            ):
                return _compact_run_command_test_failure(output, result.returncode)
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


def rg_paths_from_args(args: dict) -> list[str]:
    """Return normalized repo-relative paths for an rg invocation."""
    path = args.get("path") or "."
    if isinstance(path, list):
        paths = [str(item) for item in path if str(item).strip()]
    else:
        paths = [str(path)]
    return paths or ["."]


def rg_needs_no_ignore(paths: list[str]) -> bool:
    """Return true when the caller explicitly targets commonly ignored paths."""
    ignored_parts = {
        ".git",
        ".next",
        ".turbo",
        "build",
        "coverage",
        "dist",
        "node_modules",
        "vendor",
    }
    for path in paths:
        if path == ".":
            continue
        parts = Path(path).parts
        if any(part in ignored_parts for part in parts):
            return True
    return False


def build_rg_command(args: dict) -> list[str]:
    """Build a safe ripgrep argv from model-supplied args."""
    pattern = args.get("pattern") or args.get("query") or args.get("term")
    if not isinstance(pattern, str) or not pattern.strip():
        raise ValueError("rg requires a non-empty pattern")

    paths = rg_paths_from_args(args)
    command = ["rg", "--line-number", "--no-heading"]
    if args.get("case_sensitive") is False:
        command.append("--ignore-case")
    if rg_needs_no_ignore(paths):
        command.append("--no-ignore")
    glob = args.get("glob")
    if isinstance(glob, str) and glob.strip():
        command.extend(["--glob", glob])
    command.append(pattern)
    command.extend(paths)
    return command


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
            env=build_command_env(config),
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
            env=build_command_env(config),
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
    sample_lines = _compact_test_failure_lines(raw)
    sample = "\n".join(f"  {ln}" for ln in sample_lines) if sample_lines else f"  {raw[:200]}"
    return f"\n\n[Reactive test] failed:\n{sample}"


def _compact_test_failure_lines(raw: str, *, max_lines: int = 16) -> list[str]:
    lines = [ln for ln in raw.splitlines() if ln.strip()]
    if len(lines) <= max_lines:
        return lines

    error_index = next(
        (
            index
            for index, line in enumerate(lines)
            if re.search(r"\b(error|fail|exception|expected|received)\b", line, flags=re.IGNORECASE)
        ),
        None,
    )
    if error_index is None:
        return lines[:max_lines]

    start = max(0, error_index - 2)
    end = min(len(lines), error_index + max_lines - 2)
    sample = lines[start:end]
    if start > 0:
        sample.insert(0, "... earlier output omitted ...")
    if end < len(lines):
        sample.append("... later output omitted ...")
    return sample[:max_lines]


def _compact_run_command_test_failure(raw: str, returncode: int) -> str:
    sample_lines = _compact_test_failure_lines(raw)
    sample = "\n".join(sample_lines) if sample_lines else raw[:200]
    return f"[run_command test failure: exit {returncode}]\n{sample}"


def _is_test_run_command(command: str) -> bool:
    try:
        parts = shlex.split(command)
    except ValueError:
        parts = command.split()
    if not parts:
        return False

    executable = Path(parts[0]).name
    if executable in {"pytest"}:
        return True
    if executable.startswith("python") and "-m" in parts and "pytest" in parts:
        return True
    if executable == "bun" and len(parts) > 1 and parts[1] == "test":
        return True
    if _is_package_manager_test_command(executable, parts):
        return True
    return any(re.search(r"\.test\.[tj]sx?$|test_.*\.py$", part) for part in parts[1:])


def _is_package_manager_test_command(executable: str, parts: list[str]) -> bool:
    if executable not in {"npm", "pnpm", "yarn"}:
        return False
    return (len(parts) >= 2 and parts[1] == "test") or (
        len(parts) >= 3 and parts[1] == "run" and parts[2] == "test"
    )


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
