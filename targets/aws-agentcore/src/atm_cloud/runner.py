from __future__ import annotations

import os
import re
import subprocess
import sys
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Protocol

from .job import AtmJob


@dataclass(frozen=True)
class CommandResult:
    exit_code: int
    stdout: str
    stderr: str
    metadata: dict[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class RunEvent:
    phase: str
    status: str
    metadata: dict[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class RunResult:
    status: str
    job: AtmJob
    events: list[RunEvent]
    command: CommandResult


class MemoryClient(Protocol):
    def query(self, **kwargs: Any) -> Any:
        ...

    def store(self, **kwargs: Any) -> Any:
        ...


class Harness(Protocol):
    def run(self, job: AtmJob, context: Any) -> CommandResult:
        ...


class NoopMemoryClient:
    """Memory adapter used when optional durable memory is not configured."""

    def query(self, **kwargs: Any) -> Any:
        return {"items": [], "skipped": True}

    def store(self, **kwargs: Any) -> Any:
        return {"skipped": True}


class SubprocessAtmHarness:
    """Adapter that invokes the existing ATM harness as a subprocess."""

    def __init__(
        self,
        *,
        python: str = "python",
        module: str = "agentic_tdd_runner.agent",
        repo_root: str | None = None,
        timeout: int = 1800,
        github_token: str | None = None,
    ):
        self.python = python
        self.module = module
        self.repo_root = repo_root
        self.timeout = timeout
        self.github_token = github_token

    def run(self, job: AtmJob, context: Any) -> CommandResult:
        run_nonce = os.environ.get("ATM_RUN_NONCE") or f"{job.run_id}-{int(time.time())}"
        workdir = os.environ.get("ATM_TARGET_WORKDIR", f"/tmp/atm-agentcore/runs/{run_nonce}")
        run_root = os.environ.get("ATM_RUN_ROOT", f"/tmp/atm-agentcore/worktrees/{run_nonce}")
        log_dir = _run_log_dir(run_nonce)
        repo_root = self.repo_root or ensure_github_repo_cache(job.repo, github_token=self.github_token)
        env = _subprocess_env(self.github_token)
        command = [
            self.python,
            "-m",
            self.module,
            "--github-repo",
            job.repo,
            "--issue-number",
            str(job.issue_number),
            "--base-ref",
            job.base_branch,
            "--workdir",
            workdir,
            "--run-root",
            run_root,
            "--log-dir",
            log_dir,
        ]
        command.extend(["--repo", repo_root])
        if job.source:
            command.extend(["--source", job.source])
        if job.symbol:
            command.extend(["--symbol", job.symbol])
        config_ref = job.config_ref or os.environ.get("ATM_CONFIG")
        if config_ref:
            command.extend(["--config", config_ref])

        if _env_bool("ATM_STREAM_HARNESS_OUTPUT", default=False):
            completed = _run_streaming(command, timeout=self.timeout, env=env)
        else:
            completed = subprocess.run(
                command,
                capture_output=True,
                text=True,
                timeout=self.timeout,
                check=False,
                env=env,
            )
        return CommandResult(
            exit_code=completed.returncode,
            stdout=completed.stdout,
            stderr=completed.stderr,
            metadata={"command": command, "log_dir": log_dir},
        )


def ensure_github_repo_cache(
    repo: str,
    *,
    cache_root: str | None = None,
    github_token: str | None = None,
) -> str:
    root = Path(cache_root or os.environ.get("ATM_REPO_CACHE_ROOT", "/tmp/atm-agentcore/repos"))
    repo_path = root / _safe_repo_cache_name(repo)
    if (repo_path / ".git").is_dir():
        _best_effort_fetch(repo_path, github_token=github_token)
        return str(repo_path)
    if repo_path.exists() and any(repo_path.iterdir()):
        raise RuntimeError(f"repo cache path exists and is not a git repo: {repo_path}")

    repo_path.parent.mkdir(parents=True, exist_ok=True)
    result = subprocess.run(
        ["gh", "repo", "clone", repo, str(repo_path)],
        capture_output=True,
        text=True,
        timeout=int(os.environ.get("ATM_REPO_CLONE_TIMEOUT_SECONDS", "300")),
        check=False,
        env=_subprocess_env(github_token),
    )
    if result.returncode != 0:
        detail = (result.stderr or result.stdout or "").strip()
        raise RuntimeError(f"could not clone GitHub repo {repo}: {detail}")
    return str(repo_path)


def _best_effort_fetch(repo_path: Path, *, github_token: str | None = None) -> None:
    subprocess.run(
        ["git", "-C", str(repo_path), "fetch", "origin", "--prune"],
        capture_output=True,
        text=True,
        timeout=int(os.environ.get("ATM_REPO_FETCH_TIMEOUT_SECONDS", "120")),
        check=False,
        env=_subprocess_env(github_token),
    )


def _safe_repo_cache_name(repo: str) -> str:
    return re.sub(r"[^A-Za-z0-9_.-]+", "__", repo).strip("_")


def _run_log_dir(run_nonce: str) -> str:
    root = Path(os.environ.get("ATM_LOG_DIR", "/tmp/atm-agentcore/logs"))
    return str(root / _safe_path_segment(run_nonce))


def _safe_path_segment(value: str) -> str:
    return re.sub(r"[^A-Za-z0-9_.-]+", "-", value).strip("-") or "run"


def _env_bool(name: str, *, default: bool) -> bool:
    value = os.environ.get(name)
    if value is None:
        return default
    return value.strip().lower() in {"1", "true", "yes", "on"}


def _subprocess_env(github_token: str | None) -> dict[str, str] | None:
    if not github_token:
        return None
    env = os.environ.copy()
    env["GH_TOKEN"] = github_token
    env["GITHUB_TOKEN"] = github_token
    return env


def _run_streaming(
    command: list[str],
    *,
    timeout: int,
    env: dict[str, str] | None = None,
) -> subprocess.CompletedProcess[str]:
    process = subprocess.Popen(
        command,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        bufsize=1,
        env=env,
    )
    stdout_parts: list[str] = []
    stderr_parts: list[str] = []

    stdout_thread = threading.Thread(
        target=_pump_stream,
        args=(process.stdout, stdout_parts, ""),
        daemon=True,
    )
    stderr_thread = threading.Thread(
        target=_pump_stream,
        args=(process.stderr, stderr_parts, "[atm stderr] "),
        daemon=True,
    )
    stdout_thread.start()
    stderr_thread.start()
    try:
        returncode = process.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        process.kill()
        stdout_thread.join(timeout=1)
        stderr_thread.join(timeout=1)
        raise
    stdout_thread.join(timeout=1)
    stderr_thread.join(timeout=1)
    return subprocess.CompletedProcess(
        command,
        returncode,
        stdout="".join(stdout_parts),
        stderr="".join(stderr_parts),
    )


def _pump_stream(stream: Any, sink: list[str], prefix: str) -> None:
    if stream is None:
        return
    for line in stream:
        sink.append(line)
        sys.stdout.write(f"{prefix}{line}")
        sys.stdout.flush()


class AtmCloudRunner:
    def __init__(self, *, memory: MemoryClient, harness: Harness):
        self.memory = memory
        self.harness = harness

    def run(self, job: AtmJob) -> RunResult:
        events: list[RunEvent] = []
        try:
            context = self.memory.query(
                query=f"latest handoff for {job.repo}",
                project=job.roca_project,
                limit=5,
            )
            events.append(RunEvent("memory_context", "succeeded", {"project": job.roca_project}))
        except Exception as exc:
            context = {"items": []}
            events.append(
                RunEvent(
                    "memory_context",
                    "failed",
                    {"project": job.roca_project, "error": str(exc)},
                )
            )

        if job.mode == "dry_run":
            command = CommandResult(
                exit_code=0,
                stdout=f"Dry run validated ATM AWS AgentCore wiring for {job.repo} issue #{job.issue_number}.",
                stderr="",
                metadata={"dry_run": True},
            )
            events.append(RunEvent("dry_run", "succeeded", {"repo": job.repo}))
        else:
            try:
                command = self.harness.run(job, context)
                command = _with_extracted_pr_url(command)
                harness_status = "succeeded" if command.exit_code == 0 else "failed"
                events.append(
                    RunEvent(
                        "atm_harness",
                        harness_status,
                        {"exit_code": command.exit_code, **command.metadata},
                    )
                )
            except Exception as exc:
                command = CommandResult(
                    exit_code=1,
                    stdout="",
                    stderr=str(exc),
                    metadata={"exception": exc.__class__.__name__},
                )
                events.append(
                    RunEvent(
                        "atm_harness",
                        "failed",
                        {"exit_code": command.exit_code, "error": str(exc), **command.metadata},
                    )
                )

        run_status = "succeeded" if command.exit_code == 0 else "failed"
        metadata = {
            **job.to_metadata(),
            "exit_code": command.exit_code,
            **command.metadata,
        }
        content = _handoff_content(job, run_status, command)
        try:
            self.memory.store(
                layer="handoff",
                project=job.roca_project,
                source_agent="atm-aws-agentcore",
                content=content,
                metadata=metadata,
            )
            events.append(RunEvent("memory_handoff", "succeeded", {"project": job.roca_project}))
        except Exception as exc:
            run_status = "failed"
            events.append(
                RunEvent(
                    "memory_handoff",
                    "failed",
                    {"project": job.roca_project, "error": str(exc)},
                )
            )
        return RunResult(status=run_status, job=job, events=events, command=command)


def _handoff_content(job: AtmJob, status: str, command: CommandResult) -> str:
    pr_url = command.metadata.get("pr_url")
    summary = [
        f"ATM AWS AgentCore run {job.run_id} {status} for {job.repo} issue #{job.issue_number}.",
        f"Mode: {job.mode}. Branch: {job.branch_name}. Exit code: {command.exit_code}.",
    ]
    if pr_url:
        summary.append(f"PR: {pr_url}.")
    if status != "succeeded":
        failure = _failure_excerpt(command)
        if failure:
            summary.append(f"Failure: {failure}")
    return "\n".join(summary)


def _failure_excerpt(command: CommandResult) -> str:
    detail = (command.stderr or command.stdout or "").strip()
    return detail[-1000:]


def _with_extracted_pr_url(command: CommandResult) -> CommandResult:
    if command.metadata.get("pr_url"):
        return command
    pr_url = _extract_pr_url(command.stdout, command.stderr)
    if not pr_url:
        return command
    return CommandResult(
        exit_code=command.exit_code,
        stdout=command.stdout,
        stderr=command.stderr,
        metadata={**command.metadata, "pr_url": pr_url},
    )


def _extract_pr_url(stdout: str, stderr: str) -> str | None:
    text = f"{stdout}\n{stderr}"
    match = re.search(r"https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/\d+", text)
    return match.group(0) if match else None
