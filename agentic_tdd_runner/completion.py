"""Completion pipeline helpers for the agent runner."""

import os
import shutil
import subprocess
from collections.abc import Callable
from dataclasses import dataclass

MAX_UNTRACKED_PREVIEW_BYTES = 64 * 1024


@dataclass
class CompletionState:
    """Mutable completion counters shared across completion attempts."""

    done_rejected: int = 0
    quality_rejected: int = 0
    last_usage: dict | None = None


def compact_messages_after_quality_failure(
    messages: list[dict],
    quality_msg: str,
    test_file: str,
    *,
    clear_file_read_cache: Callable[[], None] | None = None,
) -> list[dict]:
    """Return a compact retry transcript after verification has already passed."""
    if clear_file_read_cache is not None:
        clear_file_read_cache()

    compacted: list[dict] = []
    start_idx = 0
    if messages and messages[0].get("role") == "system":
        compacted.append(messages[0])
        start_idx = 1

    issue_msg = next((msg for msg in messages[start_idx:] if msg.get("role") == "user"), None)
    if issue_msg:
        compacted.append(issue_msg)

    compacted.append(quality_retry_feedback_message(quality_msg, test_file))
    return compacted


def quality_retry_feedback_message(quality_msg: str, test_file: str) -> dict:
    """Build retry feedback for residual quality failures."""
    return {
        "role": "user",
        "content": (
            f"Verification already passed for {test_file}. Preserve the current fix behavior and "
            f"only address the residual quality issues below.\n\n{quality_msg}"
        ),
    }


def llm_context_window_tokens(config: dict) -> int:
    """Read the configured model context window."""
    llm_cfg = config.get("llm", {})
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


def coerce_int(value) -> int | None:
    """Coerce numeric usage metadata into an int."""
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


def should_compact_after_quality_failure(config: dict, last_usage: dict | None) -> tuple[bool, dict]:
    """Decide whether quality retry feedback should compact the transcript."""
    quality_cfg = config.get("quality", {})
    context_window = llm_context_window_tokens(config)
    try:
        threshold_ratio = float(quality_cfg.get("compact_threshold_ratio", 0.85))
    except (TypeError, ValueError):
        threshold_ratio = 0.85
    try:
        min_headroom_tokens = int(quality_cfg.get("compact_min_headroom_tokens", 2048))
    except (TypeError, ValueError):
        min_headroom_tokens = 2048
    prompt_tokens = coerce_int((last_usage or {}).get("prompt_tokens"))

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


def _verify_with_mechanical_edits(
    test_file: str,
    mechanical_edits: list[dict],
    verify_red_green: Callable,
) -> tuple[bool, str]:
    if mechanical_edits:
        return verify_red_green(test_file, mechanical_edits)
    return verify_red_green(test_file)


def max_quality_fix_rounds(config: dict) -> int:
    raw_value = config.get("quality", {}).get("max_fix_rounds", 3)
    try:
        return max(1, int(raw_value))
    except (TypeError, ValueError):
        return 3


def _read_file_preview(path: str, max_bytes: int = MAX_UNTRACKED_PREVIEW_BYTES) -> tuple[str, bool, bool]:
    with open(path, "rb") as fh:
        data = fh.read(max_bytes + 1)
    truncated = len(data) > max_bytes
    chunk = data[:max_bytes]
    binary = b"\x00" in chunk
    text = chunk.decode("utf-8", errors="replace").replace("\x00", "\\0")
    return text, truncated, binary


def try_complete(
    step: int,
    msg: dict,
    *,
    messages: list[dict],
    episode: dict | None,
    state: CompletionState,
    max_rejections: int,
    config: dict,
    workdir: str,
    emit: Callable[[str], None],
    log: Callable[[str, dict], None],
    find_test_file: Callable[[str | None], str | None],
    verify_red_green: Callable,
    run_quality_checks: Callable[[str], tuple[bool, str]],
    create_pr: Callable[[list[dict], dict, str, int], str | None],
    clear_file_read_cache: Callable[[], None] | None = None,
) -> str:
    """Run verify, quality, postamble, and optional PR creation."""
    test_file = find_test_file(episode.get("test_file") if episode else None)
    if not test_file:
        emit("  [WARN] No test file found — cannot verify")
        messages.append({"role": "user", "content": config["prompt"]["no_test_found"]})
        return "no_test"

    mechanical_edits = episode.get("pre_test_source_edits", []) if episode else []
    verified, verify_msg = _verify_with_mechanical_edits(
        test_file,
        mechanical_edits,
        verify_red_green,
    )
    emit(f"\n  [VERIFY] {verify_msg}")
    log("verify_result", {"verified": verified, "message": verify_msg, "test_file": test_file})

    if not verified:
        state.done_rejected += 1
        if state.done_rejected >= max_rejections:
            emit(f"\n  [GIVE UP] Rejected {state.done_rejected} times. Stopping.")
            log("give_up", {"step": step, "done_rejected": state.done_rejected})
            return "give_up"
        messages.append({"role": "user", "content": verify_msg})
        return "verify_fail"

    if config.get("quality", {}).get("enabled", False):
        emit("\n=== QUALITY CHECKS ===")
        quality_ok, quality_msg = run_quality_checks(test_file)
        emit(f"  [QUALITY] {quality_msg[:200]}")
        log("quality", {"passed": quality_ok, "message": quality_msg[:500]})

        if not quality_ok:
            state.quality_rejected += 1
            max_fix_rounds = max_quality_fix_rounds(config)
            if state.quality_rejected >= max_fix_rounds:
                emit(f"\n  [GIVE UP] Quality rejected {state.quality_rejected} times. Stopping.")
                log("give_up", {
                    "step": step,
                    "quality_rejected": state.quality_rejected,
                    "max_fix_rounds": max_fix_rounds,
                })
                return "give_up"
            emit(f"  [QUALITY] Round {state.quality_rejected} — feeding back to model")
            should_compact, compact_info = should_compact_after_quality_failure(
                config,
                state.last_usage,
            )
            if should_compact:
                before_count = len(messages)
                messages[:] = compact_messages_after_quality_failure(
                    messages,
                    quality_msg,
                    test_file,
                    clear_file_read_cache=clear_file_read_cache,
                )
                log("context_compacted", {
                    "reason": "quality_fail",
                    "before_messages": before_count,
                    "after_messages": len(messages),
                    "test_file": test_file,
                    **compact_info,
                })
            else:
                messages.append(quality_retry_feedback_message(quality_msg, test_file))
                log("context_preserved", {
                    "reason": "quality_fail",
                    "message_count": len(messages),
                    "test_file": test_file,
                    **compact_info,
                })
            return "quality_fail"

    if config.get("quality", {}).get("enabled", False):
        emit("\n=== POST-QUALITY VERIFICATION ===")
        mechanical_edits = episode.get("pre_test_source_edits", []) if episode else []
        verified, verify_msg = _verify_with_mechanical_edits(
            test_file,
            mechanical_edits,
            verify_red_green,
        )
        emit(f"  [RE-VERIFY] {verify_msg}")
        log("post_quality_verify_result", {
            "verified": verified,
            "message": verify_msg,
            "test_file": test_file,
        })
        if not verified:
            state.done_rejected += 1
            if state.done_rejected >= max_rejections:
                emit(f"\n  [GIVE UP] Rejected {state.done_rejected} times. Stopping.")
                log("give_up", {"step": step, "done_rejected": state.done_rejected})
                return "give_up"
            messages.append({"role": "user", "content": verify_msg})
            return "verify_fail"

    emit(f"\n{'='*60}")
    emit(f"AGENT DONE at step {step} — VERIFIED")
    emit(f"{'='*60}")
    log("done", {"step": step, "verified": True})
    try:
        command_timeout = config.get("timeouts", {}).get("tool_execution", 10)
        git_path = shutil.which("git")
        if git_path is None:
            raise OSError("git executable not found")
        diff = subprocess.run(
            [git_path, "diff"],
            cwd=workdir,
            capture_output=True,
            text=True,
            timeout=command_timeout,
        )
        emit(f"\n--- GIT DIFF ---\n{diff.stdout}")
        untracked = subprocess.run(
            [git_path, "ls-files", "--others", "--exclude-standard"],
            cwd=workdir,
            capture_output=True,
            text=True,
            timeout=command_timeout,
        )
        if untracked.stdout.strip():
            emit("\n--- NEW FILES ---")
            for path in untracked.stdout.strip().split("\n"):
                emit(f"  {path}")
                full = os.path.join(workdir, path)
                try:
                    preview, truncated, binary = _read_file_preview(full)
                    if binary:
                        emit("  [binary preview]")
                    emit(preview)
                    if truncated:
                        emit(f"  ...[truncated after {MAX_UNTRACKED_PREVIEW_BYTES} bytes]")
                except OSError:
                    pass
        if config.get("pr", {}).get("enabled", False):
            emit("\n=== PR CREATION ===")
            pr_url = create_pr(messages, msg, test_file, step)
            if pr_url:
                emit(f"  [PR] {pr_url}")
            else:
                emit("  [PR] Failed — diff printed above, create PR manually")
    except Exception as e:
        emit(f"  [POSTAMBLE] Failed: {e}")
        log("postamble_error", {"step": step, "error": str(e)})
    return "done"
