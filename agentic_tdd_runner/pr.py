"""Pull request creation helpers for the agent runner."""

import os
import re
import subprocess
import textwrap
from collections.abc import Callable
from datetime import datetime
from pathlib import Path

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
    body = textwrap.dedent(
        f"""\
        ## Summary
        - Fix the reported behavior with a focused regression test.
        - Keep the change limited to the files touched by the agent run.

        ## Changed files
        {changed_section}

        ## Verification
        - Red/green verification passed.
        - Harness quality checks passed.
        """
    ).strip()
    return title, body


def resolve_pr_base_ref(base_branch: str, command_timeout: int, workdir: str) -> str | None:
    """Resolve the git ref the PR should be based on, preferring origin/<base>."""
    for ref in (f"origin/{base_branch}", base_branch):
        result = subprocess.run(
            ["git", "rev-parse", "--verify", ref],
            cwd=workdir,
            env=build_command_env(),
            capture_output=True,
            text=True,
            timeout=command_timeout,
        )
        if result.returncode == 0:
            return ref
    return None


def check_pr_base_hygiene(base_branch: str, command_timeout: int, workdir: str) -> tuple[bool, str]:
    """Require the run worktree to still be exactly on the configured PR base."""
    base_ref = resolve_pr_base_ref(base_branch, command_timeout, workdir)
    if not base_ref:
        return False, f"Refusing to create PR: could not resolve base ref for {base_branch}."

    result = subprocess.run(
        ["git", "rev-list", "--left-right", "--count", f"{base_ref}...HEAD"],
        cwd=workdir,
        env=build_command_env(),
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


def collect_pr_changed_files(workdir: str, command_timeout: int) -> set[str]:
    """Return dirty tracked/staged/untracked paths, including deleted files."""
    diff = subprocess.run(
        ["git", "diff", "--name-only"],
        cwd=workdir,
        env=build_command_env(),
        capture_output=True,
        text=True,
        timeout=command_timeout,
    )
    staged = subprocess.run(
        ["git", "diff", "--cached", "--name-only"],
        cwd=workdir,
        env=build_command_env(),
        capture_output=True,
        text=True,
        timeout=command_timeout,
    )
    untracked = subprocess.run(
        ["git", "ls-files", "--others", "--exclude-standard"],
        cwd=workdir,
        env=build_command_env(),
        capture_output=True,
        text=True,
        timeout=command_timeout,
    )
    return {
        path.strip()
        for path in (diff.stdout + staged.stdout + untracked.stdout).splitlines()
        if path.strip()
    }


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
        clean_base, base_info = check_pr_base_hygiene(base, pr_timeout, workdir)
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
        current_changed_files = collect_pr_changed_files(workdir, pr_timeout)
    except subprocess.TimeoutExpired as e:
        emit(f"  [PR] Command timed out: {e}")
        log("pr_error", {"stage": "changed_files", "error": str(e)})
        return None
    allowed_changed_files = current_changed_files - (baseline_changed_files or set())
    excluded_files = sorted(current_changed_files - allowed_changed_files)
    if excluded_files:
        log("pr_excluded_preexisting_changes", {"files": excluded_files})

    pr_messages = messages.copy()
    pr_messages.append(last_msg)
    pr_messages.append({
        "role": "user",
        "content": config["prompt"]["pr_prompt"],
    })

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
        title, body = build_pr_fallback(test_file, step, sorted(allowed_changed_files))
    if not title or not body:
        emit("  [PR] LLM returned incomplete PR content, using deterministic fallback")
        log("pr_content_fallback", {
            "reason": "incomplete_content",
            "raw_response": content[:500],
            "got_title": bool(title),
            "got_body": bool(body),
        })
        title, body = build_pr_fallback(test_file, step, sorted(allowed_changed_files))

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
