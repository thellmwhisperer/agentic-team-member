"""Pull request creation helpers for the agent runner."""

import os
import re
import subprocess
import textwrap
from collections.abc import Callable
from datetime import datetime
from pathlib import Path


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
) -> str | None:
    """Ask LLM for PR content, then create branch/commit/push/PR."""
    pr_cfg = config.get("pr", {})
    pr_timeout = config["timeouts"]["pr_create"]
    base = pr_cfg.get("base_branch", "main")
    prefix = pr_cfg.get("branch_prefix", "atm/fix-")

    try:
        clean_base, base_info = check_pr_base_hygiene(base, pr_timeout, workdir)
    except OSError as e:
        emit(f"  [PR] Tool missing: {e}")
        log("pr_error", {"error": str(e)})
        return None
    except subprocess.TimeoutExpired as e:
        emit(f"  [PR] Command timed out: {e}")
        log("pr_error", {"error": str(e)})
        return None

    if not clean_base:
        emit(f"  [PR] {base_info}")
        log("pr_error", {"error": base_info, "base_branch": base})
        return None

    pr_messages = messages.copy()
    pr_messages.append(last_msg)
    pr_messages.append({
        "role": "user",
        "content": config["prompt"]["pr_prompt"],
    })

    try:
        response = chat(pr_messages, include_tools=False)
        content = response["choices"][0]["message"].get("content", "")
        title, body = parse_pr_content(content)
    except Exception as e:
        emit(f"  [PR] LLM failed to generate PR content, using deterministic fallback: {e}")
        log("pr_content_fallback", {"reason": str(e)})
        title, body = build_pr_fallback(test_file, step, get_changed_files_fn())
    if not title or not body:
        emit("  [PR] LLM returned incomplete PR content, using deterministic fallback")
        log("pr_content_fallback", {"reason": "incomplete_content"})
        title, body = build_pr_fallback(test_file, step, get_changed_files_fn())

    timestamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    branch_name = f"{prefix}{timestamp}"

    try:
        subprocess.run(
            ["git", "checkout", "-b", branch_name],
            cwd=workdir, capture_output=True, text=True, check=True, timeout=pr_timeout,
        )
        tracked = subprocess.run(
            ["git", "diff", "--name-only"],
            cwd=workdir, capture_output=True, text=True, timeout=pr_timeout,
        )
        staged = subprocess.run(
            ["git", "diff", "--cached", "--name-only"],
            cwd=workdir, capture_output=True, text=True, timeout=pr_timeout,
        )
        tracked_files = {
            f.strip() for f in (tracked.stdout + staged.stdout).splitlines()
            if f.strip()
        }

        from agentic_tdd_runner.languages import get_language
        lang = get_language(test_file)
        extensions = lang.extensions if lang else [".ts", ".tsx", ".js", ".jsx"]
        untracked = subprocess.run(
            ["git", "ls-files", "--others", "--exclude-standard"],
            cwd=workdir, capture_output=True, text=True, timeout=pr_timeout,
        )
        untracked_files = {
            f.strip() for f in untracked.stdout.splitlines()
            if f.strip() and os.path.exists(os.path.join(workdir, f.strip()))
            and os.path.splitext(f.strip())[1] in extensions
        }
        changed = sorted(tracked_files | untracked_files)
        if changed:
            subprocess.run(
                ["git", "add", "--", *changed],
                cwd=workdir, capture_output=True, text=True, check=True, timeout=pr_timeout,
            )
        subprocess.run(
            ["git", "commit", "-m", title, "-m", body],
            cwd=workdir, capture_output=True, text=True, check=True, timeout=pr_timeout,
        )
        subprocess.run(
            ["git", "push", "-u", "origin", branch_name],
            cwd=workdir, capture_output=True, text=True, check=True, timeout=pr_timeout,
        )
        result = subprocess.run(
            ["gh", "pr", "create", "--title", title, "--body", body, "--base", base],
            cwd=workdir, capture_output=True, text=True, check=True, timeout=pr_timeout,
        )
        pr_url = result.stdout.strip()
        log("pr", {"url": pr_url, "branch": branch_name, "title": title})
        return pr_url

    except subprocess.TimeoutExpired as e:
        emit(f"  [PR] Command timed out: {e.cmd}")
        log("pr_error", {"error": f"timeout: {e.cmd}"})
        return None
    except subprocess.CalledProcessError as e:
        emit(f"  [PR] Command failed: {e.stderr}")
        log("pr_error", {"error": str(e)})
        return None
    except OSError as e:
        emit(f"  [PR] Tool missing: {e}")
        log("pr_error", {"error": str(e)})
        return None
