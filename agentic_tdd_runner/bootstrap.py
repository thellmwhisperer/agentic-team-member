"""Bootstrap helpers for preparing an agent run before the runtime loop."""

import subprocess
from dataclasses import dataclass
from typing import Any, Callable

from agentic_tdd_runner import prompts


@dataclass
class WorkdirContext:
    """Resolved working directory and optional generated worktree report."""

    repo: str | None
    workdir: str
    worktree_report: Any | None = None


@dataclass
class RunBootstrapContext:
    """Inputs prepared for the runtime loop."""

    issue_text: str
    issue_text_for_model: str
    source_path: str | None
    symbol: str | None
    episode: dict | None
    messages: list[dict]
    baseline_changed_files: set[str]


def prepare_workdir(args, default_workdir: str) -> WorkdirContext:
    """Resolve the run workdir, materializing an isolated worktree when requested."""
    repo = getattr(args, "repo", None)
    if not repo:
        return WorkdirContext(
            repo=None,
            workdir=getattr(args, "workdir", None) or default_workdir,
        )

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
    return WorkdirContext(
        repo=repo,
        workdir=worktree_report.workdir,
        worktree_report=worktree_report,
    )


def prepare_run_context(
    args,
    *,
    repo: str | None,
    workdir: str,
    config: dict,
    load_issue_text: Callable[[Any, str], str],
    prepare_target_environment: Callable[[], None],
    apply_mechanical_edits: Callable[[list[dict], str], int],
    collect_pr_changed_files: Callable[[str, int], set[str]],
    discovery_enabled: bool,
    emit: Callable[[str], None],
    log: Callable[[str, dict], None],
) -> RunBootstrapContext:
    """Prepare issue, target, episode, and initial messages for a run."""
    issue_lookup_repo = repo or workdir
    issue_text = load_issue_text(args, issue_lookup_repo)

    from agentic_tdd_runner.issue_intake import parse_issue_contract

    issue_contract = parse_issue_contract(issue_text)
    if issue_contract.rejected:
        emit(f"[ISSUE] REJECTED: {issue_contract.rejection_reason}")
        log("issue_rejected", issue_contract.to_log_dict())
        raise SystemExit(f"Issue rejected: {issue_contract.rejection_reason}")
    log("issue_intake", issue_contract.to_log_dict())

    prepare_target_environment()
    baseline_changed_files = capture_run_baseline(
        workdir,
        config,
        collect_pr_changed_files=collect_pr_changed_files,
        log=log,
    )

    issue_text_for_model = issue_contract.model_text
    source_path = args.source or issue_contract.source_hint
    symbol = args.symbol or issue_contract.symbol_hint
    if not (source_path and symbol) and discovery_enabled:
        source_path, symbol = discover_target_from_issue(
            issue_text_for_model,
            workdir=workdir,
            emit=emit,
            log=log,
        )

    episode = build_episode_for_target(
        source_path,
        symbol,
        workdir=workdir,
        apply_mechanical_edits=apply_mechanical_edits,
        emit=emit,
        log=log,
    )

    messages = prompts.build_initial_messages(
        base_system_prompt=config["prompt"]["system"],
        issue_text_for_model=issue_text_for_model,
        episode=episode,
    )
    return RunBootstrapContext(
        issue_text=issue_text,
        issue_text_for_model=issue_text_for_model,
        source_path=source_path,
        symbol=symbol,
        episode=episode,
        messages=messages,
        baseline_changed_files=baseline_changed_files,
    )


def capture_run_baseline(
    workdir: str,
    config: dict,
    *,
    collect_pr_changed_files: Callable[[str, int], set[str]],
    log: Callable[[str, dict], None],
) -> set[str]:
    """Capture dirty files that existed before the model can edit the tree."""
    try:
        command_timeout = config.get("timeouts", {}).get("tool_execution", 10)
        baseline = collect_pr_changed_files(workdir, command_timeout)
    except subprocess.TimeoutExpired as exc:
        raise SystemExit(f"Could not capture run dirty baseline: {exc}") from exc
    if baseline:
        log("run_baseline_dirty_files", {"files": sorted(baseline)})
    return baseline


def discover_target_from_issue(
    issue_text_for_model: str,
    *,
    workdir: str,
    emit: Callable[[str], None],
    log: Callable[[str, dict], None],
) -> tuple[str, str]:
    """Use semantic discovery to resolve source path and symbol."""
    from agentic_tdd_runner.discovery import discover_target, load_or_build_semantic_index

    semantic_index = load_or_build_semantic_index(project_root=workdir)
    discovered = discover_target(
        issue_text=issue_text_for_model,
        project_root=workdir,
        index=semantic_index,
    )
    if not discovered:
        emit("[DISCOVERY] Could not determine source/symbol from issue")
        log(
            "discovery_failed",
            {"reason": "no_target", "candidates": len(semantic_index.get("candidates", []))},
        )
        raise SystemExit(
            "Could not determine source/symbol from issue. "
            "Pass --source and --symbol or improve the semantic index."
        )
    source_path = discovered["source_path"]
    symbol = discovered["symbol"]
    emit(f"[DISCOVERY] Selected {symbol} in {source_path}")
    log("discovery", {"source": source_path, "symbol": symbol, "score": discovered.get("score")})
    return source_path, symbol


def build_episode_for_target(
    source_path: str | None,
    symbol: str | None,
    *,
    workdir: str,
    apply_mechanical_edits: Callable[[list[dict], str], int],
    emit: Callable[[str], None],
    log: Callable[[str, dict], None],
) -> dict | None:
    """Build episode context and apply deterministic mechanical edits."""
    if not (source_path and symbol):
        return None

    from agentic_tdd_runner.cookbook import build_episode_context

    episode = build_episode_context(
        source_path=source_path,
        symbol=symbol,
        project_root=workdir,
    )
    emit(f"[EPISODE] Built episode context for {symbol} in {source_path}")
    log("episode", {
        "source": source_path,
        "symbol": symbol,
        "test_file": episode["test_file"],
        "mechanical_edits": len(episode.get("pre_test_source_edits", [])),
        "function_line_range": episode.get("function_line_range"),
    })

    edits = episode.get("pre_test_source_edits", [])
    if edits:
        n = apply_mechanical_edits(edits, workdir)
        emit(f"[PREP] Applied {n}/{len(edits)} mechanical source edits")
        log("mechanical_edits", {"applied": n, "total": len(edits)})
    return episode
