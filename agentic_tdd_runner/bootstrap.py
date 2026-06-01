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
    """Inputs prepared for the runtime loop.

    source_path and symbol are retained as resolved-target observability fields.
    """

    issue_text: str
    issue_contract: Any
    issue_text_for_model: str
    source_path: str | None
    symbol: str | None
    episode: dict | None
    messages: list[dict]
    baseline_changed_files: set[str]


@dataclass
class DiscoverySelection:
    """Selected target plus ranked alternatives for observability and prompting."""

    source_path: str
    symbol: str
    candidates: list[dict]


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
    apply_pre_test_edits: bool = True,
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

    manual_source = getattr(args, "source", None)
    manual_symbol = getattr(args, "symbol", None)
    if manual_source or manual_symbol:
        if not (manual_source and manual_symbol):
            raise SystemExit(
                "Partial --source/--symbol overrides are no longer supported. "
                "Deprecated debug overrides must provide both values. "
                "Prefer issue-only target discovery."
            )
        emit("[TARGET] Deprecated --source/--symbol override in use; prefer issue-only discovery")
        log("target_override_deprecated", {"source": manual_source, "symbol": manual_symbol})

    prepare_target_environment()
    baseline_changed_files = capture_run_baseline(
        workdir,
        config,
        collect_pr_changed_files=collect_pr_changed_files,
        log=log,
    )

    issue_text_for_model = issue_contract.model_text
    source_path = manual_source or issue_contract.source_hint
    symbol = manual_symbol or issue_contract.symbol_hint
    discovery_candidates: list[dict] = []
    if not (source_path and symbol) and discovery_enabled:
        discovery = discover_target_from_issue(
            issue_text_for_model,
            workdir=workdir,
            emit=emit,
            log=log,
        )
        source_path = discovery.source_path
        symbol = discovery.symbol
        discovery_candidates = discovery.candidates

    episode = build_episode_for_target(
        source_path,
        symbol,
        workdir=workdir,
        config=config,
        apply_mechanical_edits=apply_mechanical_edits,
        emit=emit,
        log=log,
        discovery_candidates=discovery_candidates,
        apply_pre_test_edits=apply_pre_test_edits,
    )
    if episode and config.get("runner"):
        from agentic_tdd_runner.runner_facts import build_runner_facts

        runner_facts = build_runner_facts(workdir, config, episode=episode)
        episode["runner_facts"] = runner_facts
        episode["runner_facts_text"] = runner_facts.to_prompt_section()
        log("runner_facts", runner_facts.to_log_dict())

    permission_driven = bool((config.get("agent", {}) or {}).get("permission_driven", False))
    messages = prompts.build_initial_messages(
        base_system_prompt=config["prompt"]["system"],
        issue_text_for_model=issue_text_for_model,
        episode=episode,
        permission_driven=permission_driven,
    )
    return RunBootstrapContext(
        issue_text=issue_text,
        issue_contract=issue_contract,
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
) -> DiscoverySelection:
    """Use semantic discovery to resolve source path and symbol."""
    from agentic_tdd_runner.discovery import load_or_build_semantic_index, rank_targets

    semantic_index = load_or_build_semantic_index(project_root=workdir)
    ranked = rank_targets(
        issue_text=issue_text_for_model,
        project_root=workdir,
        index=semantic_index,
        limit=5,
    )
    discovered = ranked[0] if ranked else None
    if not discovered:
        emit("[DISCOVERY] Could not determine target from issue")
        log(
            "discovery_failed",
            {"reason": "no_target", "candidates": len(semantic_index.get("candidates", []))},
        )
        raise SystemExit(
            "Could not determine target from issue. Improve the issue text or semantic index. "
            "--source/--symbol are deprecated debug overrides."
        )
    payload_keys = sorted(discovered.keys()) if isinstance(discovered, dict) else []
    source_path = discovered.get("source_path") if isinstance(discovered, dict) else None
    symbol = discovered.get("symbol") if isinstance(discovered, dict) else None
    if not (source_path and symbol):
        emit("[DISCOVERY] Discovery result missing target fields")
        log("discovery_failed", {"reason": "invalid_payload", "keys": payload_keys})
        raise SystemExit(
            "Could not determine target from issue. Improve the issue text or semantic index. "
            "--source/--symbol are deprecated debug overrides."
        )
    emit(f"[DISCOVERY] Selected {symbol} in {source_path}")
    candidate_log = [
        {
            "source": candidate.get("source_path"),
            "symbol": candidate.get("symbol"),
            "score": candidate.get("score"),
            "issue_shape": candidate.get("issue_shape"),
        }
        for candidate in ranked
    ]
    log("discovery_candidates", {"candidates": candidate_log})
    log("discovery", {"source": source_path, "symbol": symbol, "score": discovered.get("score")})
    return DiscoverySelection(source_path=source_path, symbol=symbol, candidates=ranked)


def build_episode_for_target(
    source_path: str | None,
    symbol: str | None,
    *,
    workdir: str,
    config: dict | None = None,
    apply_mechanical_edits: Callable[[list[dict], str], int],
    emit: Callable[[str], None],
    log: Callable[[str, dict], None],
    discovery_candidates: list[dict] | None = None,
    apply_pre_test_edits: bool = True,
) -> dict | None:
    """Build episode context and apply deterministic mechanical edits."""
    if not (source_path and symbol):
        return None

    from agentic_tdd_runner.cookbook import build_episode_context

    episode_kwargs = {
        "source_path": source_path,
        "symbol": symbol,
        "project_root": workdir,
    }
    runner_config = (config or {}).get("runner", {}) if isinstance(config, dict) else {}
    if isinstance(runner_config, dict) and runner_config:
        episode_kwargs["config"] = config
    episode = build_episode_context(**episode_kwargs)
    if discovery_candidates:
        episode["discovery_candidates"] = [
            {
                "source_path": candidate.get("source_path"),
                "symbol": candidate.get("symbol"),
                "score": candidate.get("score"),
                "issue_shape": candidate.get("issue_shape"),
            }
            for candidate in discovery_candidates
        ]
    emit(f"[EPISODE] Built episode context for {symbol} in {source_path}")
    log("episode", {
        "source": source_path,
        "symbol": symbol,
        "test_file": episode["test_file"],
        "mechanical_edits": len(episode.get("pre_test_source_edits", [])),
        "function_line_range": episode.get("function_line_range"),
    })

    edits = episode.get("pre_test_source_edits", [])
    if edits and apply_pre_test_edits:
        n = apply_mechanical_edits(edits, workdir)
        emit(f"[PREP] Applied {n}/{len(edits)} mechanical source edits")
        log("mechanical_edits", {"applied": n, "total": len(edits)})
    elif edits:
        emit(f"[PREP] Preview only: {len(edits)} mechanical source edits not applied")
        log("mechanical_edits_preview", {"total": len(edits)})
    return episode
