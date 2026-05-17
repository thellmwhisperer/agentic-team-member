#!/usr/bin/env python3
"""Agentic TDD runner — local LLM fixes bugs with tests."""

import argparse
import json
import os
import re
import shlex
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

from agentic_tdd_runner import completion as _completion
from agentic_tdd_runner import llm as _llm
from agentic_tdd_runner import pr as _pr
from agentic_tdd_runner import quality as _quality
from agentic_tdd_runner import tools as _tools
from agentic_tdd_runner.paths import (
    find_test_file as _find_test_file_impl,
    is_test_file_path as _is_test_file_path_impl,
    resolve_repo_path as _resolve_repo_path_impl,
    test_runner_command_for_file as _test_runner_command_for_file_impl,
)
from agentic_tdd_runner.shell import (
    has_shell_command_substitution as _has_shell_command_substitution,
    split_shell_segments as _split_shell_segments,
    validate_command as _validate_command,
)
from agentic_tdd_runner.verification import (
    is_invalid_red_phase_failure as _is_invalid_red_phase_failure,
    mechanical_edit_paths as _mechanical_edit_paths_impl,
    verification_infra_error as _verification_infra_error,
    verify_red_green as _verify_red_green_impl,
)

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
LOG_DIR = os.environ.get("AGENT_LOG_DIR") or os.environ.get("ATM_LOG_DIR") or os.getcwd()
_last_run_exit_code: int | None = None
_file_read_cache: dict[Path, int] = {}  # keyed by st_mtime_ns for deterministic invalidation
_RUN_BASELINE_CHANGED_FILES: set[str] | None = None

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
            full_path = _resolve_repo_path(edit["path"], workdir)
        except ValueError:
            emit(f"  [PREP] SKIP: path escapes workdir: {edit['path']}")
            continue
        try:
            content = full_path.read_text()
        except FileNotFoundError:
            emit(f"  [PREP] SKIP: {edit['path']} not found")
            continue
        if edit["old"] not in content:
            emit(f"  [PREP] SKIP: old text not found in {edit['path']}")
            continue
        content = content.replace(edit["old"], edit["new"], 1)
        full_path.write_text(content)
        emit(f"  [PREP] Applied edit to {edit['path']}")
        applied += 1
    return applied


def _is_llm_timeout_error(exc: Exception) -> bool:
    return _llm.is_llm_timeout_error(exc)


def _compact_messages_after_quality_failure(messages: list[dict], quality_msg: str, test_file: str) -> list[dict]:
    return _completion.compact_messages_after_quality_failure(
        messages,
        quality_msg,
        test_file,
        clear_file_read_cache=_file_read_cache.clear,
    )


def _quality_retry_feedback_message(quality_msg: str, test_file: str) -> dict:
    return _completion.quality_retry_feedback_message(quality_msg, test_file)


def _llm_context_window_tokens() -> int:
    return _completion.llm_context_window_tokens(_CONFIG or {})


def _coerce_int(value) -> int | None:
    return _completion.coerce_int(value)


def _should_compact_after_quality_failure(last_usage: dict | None) -> tuple[bool, dict]:
    return _completion.should_compact_after_quality_failure(_CONFIG or {}, last_usage)


def _tool_applied_status(name: str, result: str) -> bool | None:
    return _tools.tool_applied_status(name, result)


def _tool_loop_signature(name: str, args: dict) -> str | None:
    return _tools.tool_loop_signature(name, args)


def _tool_loop_warning_message(signature: str) -> str:
    return _tools.tool_loop_warning_message(signature)


def _resolve_repo_path(path: str, workdir: str = None) -> Path:
    return _resolve_repo_path_impl(path, workdir or WORKDIR)


def _set_last_run_exit_code(value: int | None) -> None:
    global _last_run_exit_code
    _last_run_exit_code = value


def execute_tool(name: str, args: dict) -> str:
    return _tools.execute_tool(
        name,
        args,
        workdir=WORKDIR,
        config=_CONFIG,
        file_read_cache=_file_read_cache,
        resolve_repo_path=_resolve_repo_path,
        validate_command=_validate_command,
        set_last_run_exit_code=_set_last_run_exit_code,
        detect_quality_tools=detect_quality_tools,
        typecheck_ownership_hint=_typecheck_ownership_hint,
        is_test_file_path=_is_test_file_path,
        test_runner_command_for_file=_test_runner_command_for_file,
    )


def _reactive_typecheck_feedback(path: str) -> str:
    return _tools.reactive_typecheck_feedback(
        path,
        workdir=WORKDIR,
        config=_CONFIG,
        detect_quality_tools=detect_quality_tools,
        typecheck_ownership_hint=_typecheck_ownership_hint,
    )


def _reactive_test_feedback(path: str) -> str:
    return _tools.reactive_test_feedback(
        path,
        workdir=WORKDIR,
        config=_CONFIG,
        is_test_file_path=_is_test_file_path,
        test_runner_command_for_file=_test_runner_command_for_file,
    )


def _is_test_file_path(path: str) -> bool:
    return _is_test_file_path_impl(path, _CONFIG)


def _test_runner_command_for_file(path: str) -> str:
    return _test_runner_command_for_file_impl(path, _CONFIG)


def truncate(text: str, max_chars: int = 0) -> str:
    if max_chars == 0:
        max_chars = _CONFIG["agent"]["max_tool_output"]
    if len(text) <= max_chars:
        return text
    half = max_chars // 2
    return text[:half] + f"\n\n... ({len(text) - max_chars} chars truncated) ...\n\n" + text[-half:]


def chat(messages: list, include_tools: bool = True) -> dict:
    return _llm.chat_completion(messages, _CONFIG, include_tools=include_tools)


def find_test_file(hint: str | None = None) -> str | None:
    return _find_test_file_impl(hint, WORKDIR, _CONFIG)


def _mechanical_edit_paths(mechanical_edits: list[dict] | None, workdir: str) -> list[str]:
    return _mechanical_edit_paths_impl(mechanical_edits, workdir)


def verify_red_green(test_file: str, mechanical_edits: list[dict] | None = None) -> tuple[bool, str]:
    return _verify_red_green_impl(
        test_file,
        workdir=WORKDIR,
        config=_CONFIG,
        emit=emit,
        log=log,
        apply_mechanical_edits=apply_mechanical_edits,
        mechanical_edits=mechanical_edits,
    )


def _detect_package_manager(pkg: dict | None = None) -> str:
    return _quality.detect_package_manager(WORKDIR, pkg)


def detect_quality_tools(lang_name: str) -> list[dict]:
    return _quality.detect_quality_tools(lang_name, WORKDIR)


def _get_changed_files() -> list[str]:
    return _quality.get_changed_files(WORKDIR)


def run_quality_checks(test_file: str) -> tuple[bool, str]:
    return _quality.run_quality_checks(
        test_file,
        workdir=WORKDIR,
        config=_CONFIG,
        log=log,
        is_test_file_path=_is_test_file_path,
        detect_quality_tools_fn=detect_quality_tools,
        get_changed_files_fn=_get_changed_files,
    )


def _is_obvious_assert_line(line: str) -> bool:
    return _quality.is_obvious_assert_line(line)


def _is_obvious_setup_line(line: str) -> bool:
    return _quality.is_obvious_setup_line(line)


def _is_obvious_act_line(line: str) -> bool:
    return _quality.is_obvious_act_line(line)


def _partition_duplicated_test_lines(file_text: str) -> tuple[list[str], list[str]]:
    return _quality.partition_duplicated_test_lines(file_text)


def _typecheck_ownership_hint(check_name: str, raw_output: str, changed_files: list[str]) -> str | None:
    return _quality.typecheck_ownership_hint(check_name, raw_output, changed_files)


def _extract_side_effect_call(line: str) -> tuple[str, tuple[str, ...]] | None:
    return _quality.extract_side_effect_call(line)


def _extract_object_literal_keys(body: str) -> tuple[str, ...]:
    return _quality.extract_object_literal_keys(body)


def _detect_side_effect_shape_changes(changed_files: list[str]) -> list[str]:
    return _quality.detect_side_effect_shape_changes(changed_files, WORKDIR, _is_test_file_path)


def _find_matching_brace(text: str, open_index: int) -> int:
    return _quality.find_matching_brace(text, open_index)


def _extract_param_names(params_text: str) -> list[str]:
    return _quality.extract_param_names(params_text)


def _iter_function_bodies(text: str):
    return _quality.iter_function_bodies(text)


def _body_parses_external_metadata(body: str) -> bool:
    return _quality.body_parses_external_metadata(body)


def _body_has_invalid_metadata_fallback(body: str, original_param: str) -> bool:
    return _quality.body_has_invalid_metadata_fallback(body, original_param)


def _detect_parsed_metadata_without_original_fallback(changed_files: list[str]) -> list[str]:
    return _quality.detect_parsed_metadata_without_original_fallback(
        changed_files, WORKDIR, _is_test_file_path,
    )


def _detect_empty_object_type_assertions(changed_files: list[str]) -> list[str]:
    return _quality.detect_empty_object_type_assertions(changed_files, WORKDIR)


def _build_duplicated_setup_judge_prompt(file_path: str, file_text: str, duplicated_lines: list[str]) -> str:
    return _quality.build_duplicated_setup_judge_prompt(file_path, file_text, duplicated_lines)


def _judge_duplicated_setup(file_path: str, file_text: str, duplicated_lines: list[str]) -> bool | None:
    return _quality.judge_duplicated_setup(
        file_path,
        file_text,
        duplicated_lines,
        config=_CONFIG,
        log=log,
    )


def _parse_pr_content(content: str) -> tuple[str | None, str | None]:
    return _pr.parse_pr_content(content)


def _build_pr_fallback(test_file: str, step: int) -> tuple[str, str]:
    return _pr.build_pr_fallback(test_file, step, _get_changed_files())


def _resolve_pr_base_ref(base_branch: str, command_timeout: int) -> str | None:
    return _pr.resolve_pr_base_ref(base_branch, command_timeout, WORKDIR)


def _check_pr_base_hygiene(base_branch: str, command_timeout: int) -> tuple[bool, str]:
    return _pr.check_pr_base_hygiene(base_branch, command_timeout, WORKDIR)


def create_pr(messages: list, last_msg: dict, test_file: str, step: int) -> str | None:
    return _pr.create_pr(
        messages,
        last_msg,
        test_file,
        step,
        workdir=WORKDIR,
        config=_CONFIG,
        emit=emit,
        log=log,
        chat=chat,
        get_changed_files_fn=_get_changed_files,
        baseline_changed_files=_RUN_BASELINE_CHANGED_FILES,
    )


def _is_test_pass(name: str, args: dict) -> bool:
    """Detect if a tool call was a test runner that exited 0."""
    if name != "run_command" or _last_run_exit_code != 0:
        return False
    cmd = str(args.get("command", "")).strip()
    if not cmd:
        return False
    try:
        tokens = shlex.split(cmd)
    except ValueError:
        tokens = cmd.split()
    if not tokens:
        return False

    def _matches_runner(runner_cmd: str) -> bool:
        try:
            runner_tokens = shlex.split(runner_cmd)
        except ValueError:
            runner_tokens = runner_cmd.split()
        return bool(runner_tokens) and tokens[:len(runner_tokens)] == runner_tokens

    configured_runner = ((_CONFIG or {}).get("runner", {}) or {}).get("command", "")
    if configured_runner and _matches_runner(configured_runner):
        return True

    for token in reversed(tokens[1:]):
        if _is_test_file_path(token) and _matches_runner(_test_runner_command_for_file(token)):
            return True

    return False


def _default_config_path():
    """Find config/agent.toml relative to the package."""
    env_config = os.environ.get("AGENT_CONFIG") or os.environ.get("ATM_CONFIG")
    if env_config:
        return env_config
    pkg = Path(__file__).parent.parent / "config" / "agent.toml"
    if pkg.exists():
        return str(pkg)
    return None


def _default_log_dir():
    return os.environ.get("AGENT_LOG_DIR") or os.environ.get("ATM_LOG_DIR") or LOG_DIR


def _github_repo_slug_from_remote_url(remote_url: str) -> str | None:
    remote_url = remote_url.strip()
    patterns = [
        r"^https://github\.com/([^/\s]+/[^/\s]+?)(?:\.git)?/?$",
        r"^git@github\.com:([^/\s]+/[^/\s]+?)(?:\.git)?$",
        r"^ssh://git@github\.com/([^/\s]+/[^/\s]+?)(?:\.git)?/?$",
    ]
    for pattern in patterns:
        match = re.match(pattern, remote_url)
        if match:
            return match.group(1)
    return None


def _github_repo_slug_from_worktree(repo_path: str) -> str:
    try:
        result = subprocess.run(
            ["git", "remote", "get-url", "origin"],
            cwd=repo_path,
            capture_output=True,
            text=True,
            timeout=30,
        )
    except FileNotFoundError as exc:
        raise SystemExit("git is required for GitHub issue lookup") from exc
    except subprocess.TimeoutExpired as exc:
        raise SystemExit("git remote lookup timed out during GitHub issue lookup") from exc
    if result.returncode != 0:
        raise SystemExit(f"Could not resolve origin remote for GitHub issue lookup: {result.stderr.strip()}")
    slug = _github_repo_slug_from_remote_url(result.stdout)
    if not slug:
        raise SystemExit(f"Origin remote is not a supported GitHub URL: {result.stdout.strip()}")
    return slug


def _load_issue_text(args, repo_path: str) -> str:
    issue_number = getattr(args, "issue_number", None)
    if issue_number:
        repo_slug = getattr(args, "github_repo", None) or _github_repo_slug_from_worktree(repo_path)
        try:
            result = subprocess.run(
                ["gh", "issue", "view", str(issue_number), "--repo", repo_slug, "--json", "title,body"],
                capture_output=True,
                text=True,
                timeout=60,
            )
        except FileNotFoundError as exc:
            raise SystemExit("GitHub CLI 'gh' is required for --issue-number") from exc
        except subprocess.TimeoutExpired as exc:
            raise SystemExit(f"GitHub issue lookup timed out for #{issue_number}") from exc
        if result.returncode != 0:
            raise SystemExit(f"Could not load GitHub issue #{issue_number}: {result.stderr.strip()}")
        try:
            payload = json.loads(result.stdout)
        except ValueError as exc:
            raise SystemExit(f"Could not parse GitHub issue #{issue_number} JSON") from exc
        title = str(payload.get("title") or "").strip()
        body = str(payload.get("body") or "").strip()
        return f"{title}\n\n{body}".strip()

    issue_text = getattr(args, "issue", None)
    if not issue_text:
        raise SystemExit("Pass an issue path/text or --issue-number")
    if os.path.isfile(issue_text):
        with open(issue_text) as f:
            return f.read()
    return issue_text


def _environment_prep_enabled() -> bool:
    return bool(((_CONFIG or {}).get("environment", {}) or {}).get("enabled", False))


def _discovery_enabled() -> bool:
    return bool(((_CONFIG or {}).get("discovery", {}) or {}).get("enabled", False))


def _format_environment_report(report) -> str:
    parts = [f"project={report.project_type}"]
    if report.package_manager:
        parts.append(f"package_manager={report.package_manager}")
    if report.install_command:
        parts.append(f"install={' '.join(report.install_command)}")
    if report.preflight_commands:
        commands = [" ".join(command) for command in report.preflight_commands]
        parts.append(f"preflight={commands}")
    return ", ".join(parts)


def prepare_target_environment() -> None:
    """Run deterministic repo setup before discovery/cookbook/model calls."""
    if not _environment_prep_enabled():
        return

    from agentic_tdd_runner.environment import EnvironmentPrepError, prepare_environment

    emit("[ENV] Preparing target environment")
    try:
        report = prepare_environment(WORKDIR, _CONFIG)
    except EnvironmentPrepError as exc:
        emit(f"[ENV] FAILED: {exc}")
        log("environment_failed", exc.report.to_log_dict())
        raise SystemExit(f"Target environment is not ready: {exc}") from exc

    emit(f"[ENV] Ready: {_format_environment_report(report)}")
    log("environment_ready", report.to_log_dict())


def parse_args():
    parser = argparse.ArgumentParser(description="Agentic TDD runner — local LLM fixes bugs with tests")
    parser.add_argument("issue", nargs="?", help="Issue text, or path to a file containing the issue description")
    parser.add_argument("--issue-number", type=int, help="GitHub issue number to load from the target repo")
    parser.add_argument("--github-repo", type=str, help="GitHub repo slug for --issue-number, e.g. owner/repo")
    parser.add_argument("--repo", type=str, help="Existing git repo to materialize into an isolated run worktree")
    parser.add_argument("--base-ref", type=str, default="main", help="Git ref used when creating a run worktree")
    parser.add_argument("--run-root", type=str, help="Directory for generated run worktrees (default: REPO/.worktree)")
    parser.add_argument("--source", type=str, help="Source file path relative to workdir (e.g. src/twitch/client.ts)")
    parser.add_argument("--symbol", type=str, help="Target function/method name (e.g. handleResub)")
    parser.add_argument("--workdir", type=str, help="Project root directory, or destination when --repo is used")
    parser.add_argument("--config", type=str, default=_default_config_path(), help="Path to agent.toml config file")
    parser.add_argument("--log-dir", type=str, default=_default_log_dir(), help="Directory for JSONL logs (default: cwd)")
    return parser.parse_args()


def main():
    global _CONFIG, WORKDIR, LOG_DIR, _RUN_BASELINE_CHANGED_FILES
    args = parse_args()

    from agentic_tdd_runner.config import load_config
    _CONFIG = load_config(args.config)

    worktree_report = None
    repo = getattr(args, "repo", None)
    if repo:
        from agentic_tdd_runner.environment import WorktreePrepError, prepare_run_worktree

        try:
            worktree_report = prepare_run_worktree(
                repo,
                workdir=getattr(args, "workdir", None),
                base_ref=getattr(args, "base_ref", "main"),
                run_root=getattr(args, "run_root", None),
            )
        except WorktreePrepError as exc:
            raise SystemExit(f"Could not prepare run worktree: {exc}") from exc
        WORKDIR = worktree_report.workdir
    else:
        WORKDIR = getattr(args, "workdir", None) or WORKDIR
    LOG_DIR = args.log_dir

    log_path = init_log()
    if worktree_report:
        emit(f"[WORKTREE] Ready: {worktree_report.workdir}")
        log("worktree_ready", worktree_report.to_log_dict())

    issue_lookup_repo = repo or WORKDIR
    issue_text = _load_issue_text(args, issue_lookup_repo)

    from agentic_tdd_runner.issue_intake import parse_issue_contract
    issue_contract = parse_issue_contract(issue_text)
    if issue_contract.rejected:
        emit(f"[ISSUE] REJECTED: {issue_contract.rejection_reason}")
        log("issue_rejected", issue_contract.to_log_dict())
        raise SystemExit(f"Issue rejected: {issue_contract.rejection_reason}")
    log("issue_intake", issue_contract.to_log_dict())

    prepare_target_environment()
    try:
        command_timeout = _CONFIG.get("timeouts", {}).get("tool_execution", 10)
        _RUN_BASELINE_CHANGED_FILES = _pr.collect_pr_changed_files(WORKDIR, command_timeout)
        if _RUN_BASELINE_CHANGED_FILES:
            log("run_baseline_dirty_files", {"files": sorted(_RUN_BASELINE_CHANGED_FILES)})
    except subprocess.TimeoutExpired as exc:
        raise SystemExit(f"Could not capture run dirty baseline: {exc}") from exc

    issue_text_for_model = issue_contract.model_text
    source_path = args.source or issue_contract.source_hint
    symbol = args.symbol or issue_contract.symbol_hint
    if not (source_path and symbol) and _discovery_enabled():
        from agentic_tdd_runner.discovery import discover_target, load_or_build_semantic_index

        semantic_index = load_or_build_semantic_index(project_root=WORKDIR)
        discovered = discover_target(issue_text=issue_text_for_model, project_root=WORKDIR, index=semantic_index)
        if not discovered:
            emit("[DISCOVERY] Could not determine source/symbol from issue")
            log("discovery_failed", {"reason": "no_target", "candidates": len(semantic_index.get("candidates", []))})
            raise SystemExit(
                "Could not determine source/symbol from issue. "
                "Pass --source and --symbol or improve the semantic index."
            )
        source_path = discovered["source_path"]
        symbol = discovered["symbol"]
        emit(f"[DISCOVERY] Selected {symbol} in {source_path}")
        log("discovery", {"source": source_path, "symbol": symbol, "score": discovered.get("score")})

    # Build system prompt — inject cookbook if source/symbol provided
    system_prompt = _CONFIG["prompt"]["system"].strip()
    episode = None
    if source_path and symbol:
        from agentic_tdd_runner.cookbook import build_episode_context
        episode = build_episode_context(
            source_path=source_path,
            symbol=symbol,
            project_root=WORKDIR,
        )
        system_prompt = f"{system_prompt}\n\n{episode['cookbook_text']}"
        emit(f"[EPISODE] Built episode context for {symbol} in {source_path}")
        log("episode", {
            "source": source_path,
            "symbol": symbol,
            "test_file": episode["test_file"],
            "mechanical_edits": len(episode.get("pre_test_source_edits", [])),
            "function_line_range": episode.get("function_line_range"),
        })

        # Apply mechanical edits (export, seams) before the agent loop
        edits = episode.get("pre_test_source_edits", [])
        if edits:
            n = apply_mechanical_edits(edits, WORKDIR)
            emit(f"[PREP] Applied {n}/{len(edits)} mechanical source edits")
            log("mechanical_edits", {"applied": n, "total": len(edits)})

    if episode:
        rng = episode.get("function_line_range") or {}
        # Only emit the line hint when the parser matched a real definition
        # pattern (def / function / const|let|var). A 'fallback' match means we
        # only located the symbol as a bare word — could be a comment or call
        # site — so the number would misdirect the agent.
        line_hint = (
            f" (lines {rng['start']}-{rng['end']})"
            if rng.get("source") == "definition" and rng.get("start") and rng.get("end")
            else ""
        )
        phase1_msg = (
            f"Read {episode['source_file']} and understand the bug below. "
            f"Focus on the function `{episode['target_symbol']}`{line_hint}. "
            f"Then create a failing test in {episode['test_file']} that reproduces it.\n\n"
            f"Bug:\n{issue_text_for_model}"
        )
        messages = [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": phase1_msg},
        ]
    else:
        messages = [
            {"role": "system", "content": system_prompt},
            {"role": "user", "content": f"Fix this bug:\n\n{issue_text_for_model}"},
        ]

    emit(f"{'='*60}")
    max_steps = _CONFIG["agent"]["max_steps"]
    emit(f"AGENT START — max {max_steps} steps")
    emit(f"Workdir: {WORKDIR}")
    emit(f"Log: {log_path}")
    emit(f"{'='*60}")

    log("start", {"issue": issue_text})

    completion_state = _completion.CompletionState()
    max_rejections = _CONFIG["verification"]["max_rejections"]
    recent_exploratory_signatures: list[str] = []

    # --- Completion pipeline: verify, quality, done ---
    # Returns: "done" | "quality_fail" | "verify_fail" | "give_up" | "no_test"
    def try_complete(step, msg: dict):
        return _completion.try_complete(
            step,
            msg,
            messages=messages,
            episode=episode,
            state=completion_state,
            max_rejections=max_rejections,
            config=_CONFIG,
            workdir=WORKDIR,
            emit=emit,
            log=log,
            find_test_file=find_test_file,
            verify_red_green=verify_red_green,
            run_quality_checks=run_quality_checks,
            create_pr=create_pr,
            clear_file_read_cache=_file_read_cache.clear,
        )

    for step in range(max_steps):
        emit(f"\n>>> Step {step}/{max_steps} — requesting LLM...")
        t0 = time.time()

        try:
            response = chat(messages)
        except Exception as e:
            if _is_llm_timeout_error(e):
                emit(f"  LLM TIMEOUT: {e}")
                log("llm_timeout", {"step": step, "error": str(e)})
                return 1
            emit(f"  ERROR: {e}")
            log("error", {"step": step, "error": str(e)})
            break

        elapsed = time.time() - t0

        choice = response["choices"][0]
        msg = choice["message"]
        finish = choice["finish_reason"]
        usage = response.get("usage", {})
        completion_state.last_usage = usage
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
            # OpenAI's tool_calls API requires every assistant(tool_calls) to be
            # followed by a contiguous run of role=tool messages — one per call.
            # If loop detection fires mid-iteration, buffer the warning here and
            # append it AFTER the loop, so siblings stay contiguous instead of
            # producing assistant→tool→user→tool (an invalid transcript).
            loop_warning = None
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
                applied = _tool_applied_status(name, result)
                loop_signature = _tool_loop_signature(name, args)

                log("tool", {
                    "step": step,
                    "name": name,
                    "args": args,
                    "applied": applied,
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

                if applied is True:
                    recent_exploratory_signatures.clear()
                elif loop_signature:
                    recent_exploratory_signatures.append(loop_signature)
                    recent_exploratory_signatures[:] = recent_exploratory_signatures[-3:]
                    if (
                        len(recent_exploratory_signatures) == 3
                        and len(set(recent_exploratory_signatures)) == 1
                    ):
                        loop_warning = _tool_loop_warning_message(loop_signature)
                        log("loop_detected", {
                            "step": step,
                            "signature": loop_signature,
                            "count": 3,
                        })
                        recent_exploratory_signatures.clear()
                # No else: failed edits and unrelated tools leave the streak
                # intact. Only a successful edit (applied is True) breaks it,
                # because only a successful edit represents actual progress.

                if _is_test_pass(name, args):
                    test_passed = True

            if loop_warning is not None:
                messages.append({"role": "user", "content": loop_warning})

            # --- PHASE NUDGE: test file created → nudge to run + fix ---
            if episode:
                created_test = False
                for tc in msg.get("tool_calls", []):
                    try:
                        fn = tc["function"]
                        if fn["name"] != "create_file":
                            continue
                        tc_args = json.loads(fn["arguments"])
                    except (json.JSONDecodeError, KeyError, TypeError):
                        continue
                    if _is_test_file_path(str(tc_args.get("path", ""))):
                        created_test = True
                        break
                if created_test:
                    nudge = (
                        f"Good. Now run the test to confirm it fails, then fix "
                        f"{episode['source_file']} to make it pass. Say DONE when green."
                    )
                    messages.append({"role": "user", "content": nudge})
                    emit("  [PHASE] Test created → injected run+fix nudge")
                    log("phase_nudge", {"phase": "fix", "test_file": episode["test_file"]})

            # --- AUTO-TRIGGER: test passed → verify → quality → done ---
            if test_passed:
                emit("\n  [AUTO] Test pass detected — triggering verification pipeline")
                completion = try_complete(step, msg)
                if completion == "done":
                    return 0
                if completion == "give_up":
                    return 1
                # quality_fail, verify_fail, no_test → continue loop

        elif finish == "stop":
            if step > 3:
                messages.append({
                    "role": "user",
                    "content": _CONFIG["prompt"]["nudge"].replace("{step}", str(step)).replace("{max_steps}", str(max_steps))
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
