"""Optional delivery step: hand a green clone to no-mistakes and end with a PR.

ATM's verdict stays local by default. With `--deliver no-mistakes` the worker commits
what the agent left in the clone, puts it on a branch, points the clone's `origin` at
the source repository's `origin`, and runs `no-mistakes axi run --yes` inside the clone.
no-mistakes does its own review, tests, push and PR; ATM only records the outcome.
"""

from __future__ import annotations

import re
import subprocess
import time
from datetime import datetime
from pathlib import Path
from uuid import uuid4

PR_URL = re.compile(r"https://github\.com/[\w.-]+/[\w.-]+/pull/\d+")
RUN_ID = re.compile(
    r"\b(?:run[_ ]id|run)[:=]?\s*([0-9A-HJKMNP-TV-Z]{26})\b", re.IGNORECASE
)
GIT_IDENTITY = [
    "-c",
    "user.name=atm",
    "-c",
    "user.email=atm@localhost",
    "-c",
    "core.hooksPath=/dev/null",
]


def branch_name(title: str, now: datetime | None = None) -> str:
    slug = re.sub(r"[^a-z0-9]+", "-", title.lower()).strip("-")[:40] or "fix"
    stamp = (now or datetime.now()).strftime("%Y%m%d-%H%M%S")
    return f"atm/{slug}-{stamp}-{uuid4().hex[:8]}"


def _git(cwd: str, *args: str, check: bool = True) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["git", *GIT_IDENTITY, *args],
        cwd=cwd,
        check=check,
        capture_output=True,
        text=True,
    )


def prepare_branch(
    worktree: str, source_repo: str, title: str, unit_number: int
) -> tuple[str, str]:
    """Commit the clone's remaining work, create the delivery branch and aim `origin` at the
    source repository's remote. Returns (branch, head_sha)."""
    remote = _git(source_repo, "remote", "get-url", "origin").stdout.strip()
    if not remote:
        raise subprocess.CalledProcessError(1, ["git", "remote", "get-url", "origin"])
    if not Path(remote).is_absolute() and not re.match(
        r"^(?:[\w+.-]+://|[^/:\s]+@[^/:]+:)", remote
    ):
        source_root = Path(
            _git(source_repo, "rev-parse", "--show-toplevel").stdout.strip()
        )
        remote = str((source_root / remote).resolve())
    branch = branch_name(title)
    _git(worktree, "checkout", "-q", "-b", branch)
    if _git(worktree, "status", "--porcelain", "-uall").stdout.strip():
        _git(worktree, "add", "-A", "--", ".", ":!.atm")
        _git(
            worktree,
            "commit",
            "-q",
            "--no-verify",
            "-m",
            f"atm unit {unit_number}: {title}"[:200],
        )
    _git(worktree, "remote", "set-url", "origin", remote)
    head = _git(worktree, "rev-parse", "HEAD").stdout.strip()
    return branch, head


def run_no_mistakes(
    worktree: str, intent: str, *, binary: str = "no-mistakes", timeout: int = 1800
) -> dict:
    """Run `no-mistakes axi run --yes --intent <intent>` in the clone and parse what it printed."""
    argv = [binary, "axi", "run", "--yes", "--intent", intent]
    started = time.monotonic()
    try:
        proc = subprocess.run(
            argv, cwd=worktree, capture_output=True, text=True, timeout=timeout
        )
        output, exit_code, timed_out = proc.stdout + proc.stderr, proc.returncode, False
    except subprocess.TimeoutExpired as exc:
        output = (exc.stdout or b"").decode(errors="replace") + (
            exc.stderr or b""
        ).decode(errors="replace")
        exit_code, timed_out = None, True
    except OSError as exc:
        output, exit_code, timed_out = str(exc), None, False
    pr = PR_URL.search(output)
    run_id = RUN_ID.search(output)
    return {
        "tool": "no-mistakes",
        "command": argv,
        "exit_code": exit_code,
        "timed_out": timed_out,
        "duration_seconds": round(time.monotonic() - started, 1),
        "pr_url": pr.group(0) if pr else None,
        "run_id": run_id.group(1) if run_id else None,
        "output_tail": output[-4000:],
    }


def deliver(
    worktree: str,
    source_repo: str,
    title: str,
    unit_number: int,
    *,
    binary: str = "no-mistakes",
    timeout: int = 1800,
) -> dict:
    """The whole step. `ok` is true only when no-mistakes exited 0 and printed a PR URL."""
    try:
        branch, head = prepare_branch(worktree, source_repo, title, unit_number)
    except subprocess.CalledProcessError as exc:
        return {
            "tool": "no-mistakes",
            "ok": False,
            "branch": None,
            "head_sha": None,
            "error": f"branch preparation failed: {(exc.stderr or exc.stdout or '').strip()}",
        }
    result = run_no_mistakes(worktree, title, binary=binary, timeout=timeout)
    result.update(
        branch=branch,
        head_sha=head,
        ok=result["exit_code"] == 0 and result["pr_url"] is not None,
    )
    return result
