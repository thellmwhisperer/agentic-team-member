"""Pull request creation helpers for the agent runner."""

import json
import os
import re
import subprocess
from collections.abc import Callable
from datetime import datetime
from pathlib import Path

from agentic_tdd_runner import quality as _quality
from agentic_tdd_runner.shell import build_command_env


def parse_pr_content(content: str) -> tuple[str | None, str | None]:
    """Parse PR_TITLE and PR_BODY from an LLM response."""
    title = None
    body = None
    title_match = re.search(r"PR_TITLE:\s*(.+?)(?:\n|$)", content)
    if title_match:
        title = title_match.group(1).strip()[:70]
    body_match = re.search(r"PR_BODY:\s*(.+)", content, re.DOTALL)
    if body_match:
        body = body_match.group(1).strip()
    return title, body


def build_pr_fallback(test_file: str, step: int, changed_files: list[str]) -> tuple[str, str]:
    """Build deterministic PR content when LLM PR writing is unavailable."""
    stem = Path(test_file).name
    for suffix in (".test.ts", ".test.tsx", ".test.js", ".test.jsx", "_test.py", ".py"):
        if stem.endswith(suffix):
            stem = stem[: -len(suffix)]
            break
    title_subject = re.sub(r"[^A-Za-z0-9]+", " ", stem).strip().lower() or f"step {step}"
    title = f"fix: update {title_subject} behavior"[:70]
    changed_section = "\n".join(f"- `{path}`" for path in changed_files) or "- No changed files detected"
    body = (
        "## Summary\n\n"
        "- Fix the reported behavior with a focused regression test.\n"
        "- Keep the change limited to the files touched by the agent run.\n\n"
        "## Changed files\n\n"
        f"{changed_section}\n\n"
        "## Verification\n\n"
        "- Red/green verification passed.\n"
        "- Harness quality checks passed."
    )
    return title, body


def build_pr_content_messages(
    messages: list,
    last_msg: dict,
    *,
    pr_prompt: str,
    changed_files: list[str],
    diff_context: str = "",
) -> list[dict]:
    """Build a text-only prompt for PR copy generation.

    The main agent conversation contains tool-call and tool-result blocks. Bedrock
    Converse rejects those blocks when the request omits ``tools=``; PR copy
    generation does not need tool use, so keep only plain text context here.
    """
    pr_messages: list[dict] = []
    for message in messages:
        if message.get("role") != "system":
            continue
        system_text = _plain_message_content(message)
        if not system_text and isinstance(message.get("content"), dict):
            system_text = json.dumps(message["content"], sort_keys=True)
        if system_text:
            pr_messages.append({"role": "system", "content": system_text})
            break

    changed_section = "\n".join(f"- {path}" for path in changed_files) or "- No changed files detected"
    final_context = _plain_message_content(last_msg)
    diff_section = diff_context.strip() or "No diff context available."
    prompt = (
        f"{pr_prompt}\n\n"
        "Changed files:\n"
        f"{changed_section}\n\n"
        "Git diff:\n"
        f"{diff_section}\n\n"
        "Final agent message:\n"
        f"{final_context[:4000] if final_context else 'DONE'}"
    )
    pr_messages.append({"role": "user", "content": prompt})
    return pr_messages


def _plain_message_content(message: dict) -> str:
    content = message.get("content")
    if isinstance(content, str):
        return content.strip()
    if isinstance(content, list):
        parts = []
        for item in content:
            if isinstance(item, dict) and isinstance(item.get("text"), str):
                parts.append(item["text"])
        return "\n".join(parts).strip()
    return ""


def resolve_pr_base_ref(
    base_branch: str,
    command_timeout: int,
    workdir: str,
    *,
    command_env: dict[str, str] | None = None,
) -> str | None:
    """Resolve the git ref the PR should be based on, preferring origin/<base>."""
    env = command_env or build_command_env()
    for ref in (f"origin/{base_branch}", base_branch):
        result = subprocess.run(
            ["git", "rev-parse", "--verify", ref],
            cwd=workdir,
            env=env,
            capture_output=True,
            text=True,
            timeout=command_timeout,
        )
        if result.returncode == 0:
            return ref
    return None


def check_pr_base_hygiene(
    base_branch: str,
    command_timeout: int,
    workdir: str,
    *,
    command_env: dict[str, str] | None = None,
) -> tuple[bool, str]:
    """Require the run worktree to still be exactly on the configured PR base."""
    env = command_env or build_command_env()
    base_ref = resolve_pr_base_ref(
        base_branch,
        command_timeout,
        workdir,
        command_env=env,
    )
    if not base_ref:
        return False, f"Refusing to create PR: could not resolve base ref for {base_branch}."

    result = subprocess.run(
        ["git", "rev-list", "--left-right", "--count", f"{base_ref}...HEAD"],
        cwd=workdir,
        env=env,
        capture_output=True,
        text=True,
        timeout=command_timeout,
    )
    if result.returncode != 0:
        return False, (
            f"Refusing to create PR: could not compare HEAD against {base_ref}: "
            f"{result.stderr.strip()}"
        )

    counts = result.stdout.strip().split()
    if len(counts) != 2:
        return False, (
            f"Refusing to create PR: unexpected git ancestry output for {base_ref}: "
            f"{result.stdout.strip()}"
        )

    behind, ahead = counts
    if behind != "0" or ahead != "0":
        return False, (
            f"Refusing to create PR: current HEAD is not cleanly based on {base_ref} "
            f"(behind={behind}, ahead={ahead}). Start from a clean worktree based on the PR base."
        )

    return True, base_ref


def collect_pr_changed_files(
    workdir: str,
    command_timeout: int,
    *,
    command_env: dict[str, str] | None = None,
) -> set[str]:
    """Return dirty tracked/staged/untracked paths, including deleted files."""
    env = command_env or build_command_env()
    diff = subprocess.run(
        ["git", "diff", "--name-only"],
        cwd=workdir,
        env=env,
        capture_output=True,
        text=True,
        timeout=command_timeout,
    )
    staged = subprocess.run(
        ["git", "diff", "--cached", "--name-only"],
        cwd=workdir,
        env=env,
        capture_output=True,
        text=True,
        timeout=command_timeout,
    )
    untracked = subprocess.run(
        ["git", "ls-files", "--others", "--exclude-standard"],
        cwd=workdir,
        env=env,
        capture_output=True,
        text=True,
        timeout=command_timeout,
    )
    return {
        path.strip()
        for path in (diff.stdout + staged.stdout + untracked.stdout).splitlines()
        if path.strip()
    }


def collect_pr_diff_context(
    workdir: str,
    changed_files: list[str],
    command_timeout: int,
    *,
    command_env: dict[str, str] | None = None,
    max_chars: int = 12000,
) -> str:
    """Return a plain-text diff for PR copy generation."""
    if not changed_files:
        return ""
    env = command_env or build_command_env()
    sections: list[str] = []
    for args in (
        ["git", "diff", "--", *changed_files],
        ["git", "diff", "--cached", "--", *changed_files],
    ):
        result = subprocess.run(
            args,
            cwd=workdir,
            env=env,
            capture_output=True,
            text=True,
            timeout=command_timeout,
        )
        if result.stdout.strip():
            sections.append(result.stdout.strip())

    untracked = subprocess.run(
        ["git", "ls-files", "--others", "--exclude-standard"],
        cwd=workdir,
        env=env,
        capture_output=True,
        text=True,
        timeout=command_timeout,
    )
    untracked_files = {
        path.strip()
        for path in untracked.stdout.splitlines()
        if path.strip() in changed_files
    }
    for path in sorted(untracked_files):
        full_path = os.path.join(workdir, path)
        if not os.path.isfile(full_path):
            continue
        result = subprocess.run(
            ["git", "diff", "--no-index", "--no-ext-diff", "--", os.devnull, path],
            cwd=workdir,
            env=env,
            capture_output=True,
            text=True,
            timeout=command_timeout,
        )
        output = result.stdout.strip()
        if output:
            sections.append(output)

    diff_context = "\n\n".join(sections).strip()
    if len(diff_context) > max_chars:
        return f"{diff_context[:max_chars]}\n... [diff truncated]"
    return diff_context


def create_pr(
    messages: list,
    last_msg: dict,
    test_file: str,
    step: int,
    *,
    workdir: str,
    config: dict,
    emit: Callable[[str], None],
    log: Callable[[str, dict], None],
    chat: Callable[..., dict],
    get_changed_files_fn: Callable[[], list[str]],
    baseline_changed_files: set[str] | None = None,
) -> str | None:
    """Ask LLM for PR content, then create branch/commit/push/PR."""
    pr_cfg = config.get("pr", {})
    pr_timeout = config["timeouts"]["pr_create"]
    base = pr_cfg.get("base_branch", "main")
    prefix = pr_cfg.get("branch_prefix", "atm/fix-")
    command_env = build_command_env(config)

    log("pr_start", {"step": step, "base_branch": base})

    try:
        clean_base, base_info = check_pr_base_hygiene(
            base,
            pr_timeout,
            workdir,
            command_env=command_env,
        )
    except OSError as e:
        emit(f"  [PR] Tool missing: {e}")
        log("pr_error", {"stage": "base_hygiene", "error": str(e)})
        return None
    except subprocess.TimeoutExpired as e:
        emit(f"  [PR] Command timed out: {e}")
        log("pr_error", {"stage": "base_hygiene", "error": str(e)})
        return None

    if not clean_base:
        emit(f"  [PR] {base_info}")
        log("pr_error", {"stage": "base_hygiene", "error": base_info, "base_branch": base})
        return None

    try:
        current_changed_files = collect_pr_changed_files(
            workdir,
            pr_timeout,
            command_env=command_env,
        )
    except subprocess.TimeoutExpired as e:
        emit(f"  [PR] Command timed out: {e}")
        log("pr_error", {"stage": "changed_files", "error": str(e)})
        return None
    except OSError as e:
        emit(f"  [PR] Tool missing: {e}")
        log("pr_error", {"stage": "changed_files", "error": str(e)})
        return None
    allowed_changed_files = current_changed_files - (baseline_changed_files or set())
    target_source = (
        config.get("_runtime", {}).get("permission_context", {}).get("source_file")
    )
    target_violation = _quality.detect_pr_target_violation(
        sorted(allowed_changed_files), {"source_file": target_source}
    )
    if target_violation:
        emit(f"\n  [PR GATE] {target_violation}")
        log("pr_gate_fail", {
            "step": step,
            "reason": "target_not_touched",
            "target": target_source,
            "allowed_changed_files": sorted(allowed_changed_files),
        })
        return _quality.PR_TARGET_GATE_FAILED
    excluded_files = sorted(current_changed_files - allowed_changed_files)
    if excluded_files:
        log("pr_excluded_preexisting_changes", {"files": excluded_files})

    sorted_allowed_changed_files = sorted(allowed_changed_files)
    try:
        diff_context = collect_pr_diff_context(
            workdir,
            sorted_allowed_changed_files,
            pr_timeout,
            command_env=command_env,
        )
    except (OSError, subprocess.TimeoutExpired) as e:
        diff_context = ""
        log("pr_diff_context_error", {"error": str(e)})
    pr_messages = build_pr_content_messages(
        messages,
        last_msg,
        pr_prompt=config["prompt"]["pr_prompt"],
        changed_files=sorted_allowed_changed_files,
        diff_context=diff_context,
    )

    emit("  [PR] Generating title/body")
    log("pr_content_start", {"step": step})
    content = ""
    try:
        response = chat(pr_messages, include_tools=False)
        content = response["choices"][0]["message"].get("content", "")
        title, body = parse_pr_content(content)
    except Exception as e:
        emit(f"  [PR] LLM failed to generate PR content, using deterministic fallback: {e}")
        log("pr_content_fallback", {
            "reason": str(e),
            "raw_response": content[:500],
        })
        title, body = build_pr_fallback(test_file, step, sorted_allowed_changed_files)
    if not title or not body:
        emit("  [PR] LLM returned incomplete PR content, using deterministic fallback")
        log("pr_content_fallback", {
            "reason": "incomplete_content",
            "raw_response": content[:500],
            "got_title": bool(title),
            "got_body": bool(body),
        })
        title, body = build_pr_fallback(test_file, step, sorted_allowed_changed_files)

    timestamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    branch_name = f"{prefix}{timestamp}"
    log("pr_git_start", {
        "step": step,
        "branch": branch_name,
        "base_branch": base,
        "files": sorted(allowed_changed_files),
    })

    try:
        subprocess.run(
            ["git", "checkout", "-b", branch_name],
            cwd=workdir, env=command_env, capture_output=True, text=True,
            check=True, timeout=pr_timeout,
        )
        tracked = subprocess.run(
            ["git", "diff", "--name-only"],
            cwd=workdir, env=command_env, capture_output=True, text=True, timeout=pr_timeout,
        )
        staged = subprocess.run(
            ["git", "diff", "--cached", "--name-only"],
            cwd=workdir, env=command_env, capture_output=True, text=True, timeout=pr_timeout,
        )
        tracked_files = {
            f.strip() for f in (tracked.stdout + staged.stdout).splitlines()
            if f.strip() and f.strip() in allowed_changed_files
        }

        from agentic_tdd_runner.languages import get_language
        lang = get_language(test_file)
        extensions = lang.extensions if lang else [".ts", ".tsx", ".js", ".jsx"]
        untracked = subprocess.run(
            ["git", "ls-files", "--others", "--exclude-standard"],
            cwd=workdir, env=command_env, capture_output=True, text=True, timeout=pr_timeout,
        )
        untracked_files = {
            f.strip() for f in untracked.stdout.splitlines()
            if f.strip() and os.path.exists(os.path.join(workdir, f.strip()))
            and f.strip() in allowed_changed_files
            and os.path.splitext(f.strip())[1] in extensions
        }
        changed = sorted(tracked_files | untracked_files)
        if changed:
            subprocess.run(
                ["git", "add", "--", *changed],
                cwd=workdir, env=command_env, capture_output=True, text=True,
                check=True, timeout=pr_timeout,
            )
        subprocess.run(
            ["git", "commit", "-m", title, "-m", body],
            cwd=workdir, env=command_env, capture_output=True, text=True,
            check=True, timeout=pr_timeout,
        )
        subprocess.run(
            ["git", "push", "-u", "origin", branch_name],
            cwd=workdir, env=command_env, capture_output=True, text=True,
            check=True, timeout=pr_timeout,
        )
        result = subprocess.run(
            ["gh", "pr", "create", "--title", title, "--body", body, "--base", base],
            cwd=workdir, env=command_env, capture_output=True, text=True,
            check=True, timeout=pr_timeout,
        )
        pr_url = result.stdout.strip()
        log("pr", {"url": pr_url, "branch": branch_name, "title": title})
        log("pr_done", {"url": pr_url, "branch": branch_name, "title": title})
        return pr_url

    except subprocess.TimeoutExpired as e:
        emit(f"  [PR] Command timed out: {e.cmd}")
        log("pr_error", {"stage": "git_or_gh", "error": f"timeout: {e.cmd}"})
        return None
    except subprocess.CalledProcessError as e:
        emit(f"  [PR] Command failed: {e.stderr}")
        log("pr_error", {"stage": "git_or_gh", "error": str(e)})
        return None
    except OSError as e:
        emit(f"  [PR] Tool missing: {e}")
        log("pr_error", {"stage": "git_or_gh", "error": str(e)})
        return None
