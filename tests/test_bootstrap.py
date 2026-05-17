"""Tests for agent bootstrap helpers."""

from types import SimpleNamespace

import pytest

from agentic_tdd_runner.bootstrap import (
    build_episode_for_target,
    capture_run_baseline,
    discover_target_from_issue,
    prepare_run_context,
    prepare_workdir,
)
from agentic_tdd_runner.environment import WorktreeReport


def test_prepare_workdir_uses_explicit_workdir_without_repo(tmp_path):
    args = SimpleNamespace(repo=None, workdir=str(tmp_path / "target"))

    context = prepare_workdir(args, str(tmp_path / "default"))

    assert context.repo is None
    assert context.workdir == str(tmp_path / "target")
    assert context.worktree_report is None


def test_prepare_workdir_materializes_repo_worktree(tmp_path, monkeypatch):
    report = WorktreeReport(
        repo=str(tmp_path / "repo"),
        workdir=str(tmp_path / "run"),
        base_ref="origin/main",
        command=["git", "worktree", "add"],
    )
    calls = []

    def fake_prepare_run_worktree(*args, **kwargs):
        calls.append((args, kwargs))
        return report

    monkeypatch.setattr(
        "agentic_tdd_runner.environment.prepare_run_worktree",
        fake_prepare_run_worktree,
    )
    args = SimpleNamespace(
        repo=str(tmp_path / "repo"),
        workdir=None,
        base_ref="origin/main",
        run_root=str(tmp_path),
    )

    context = prepare_workdir(args, str(tmp_path / "default"))

    assert context.repo == str(tmp_path / "repo")
    assert context.workdir == str(tmp_path / "run")
    assert context.worktree_report is report
    assert calls == [((str(tmp_path / "repo"),), {
        "workdir": None,
        "base_ref": "origin/main",
        "run_root": str(tmp_path),
    })]


def test_prepare_run_context_rejects_issue_before_environment_prep(tmp_path):
    logged = []

    def fail_if_called():
        raise AssertionError("environment prep should not run")

    args = SimpleNamespace(issue="unused", source=None, symbol=None)

    with pytest.raises(SystemExit, match="Issue rejected"):
        prepare_run_context(
            args,
            repo=None,
            workdir=str(tmp_path),
            config={"prompt": {"system": "system"}, "timeouts": {"tool_execution": 10}},
            load_issue_text=lambda _args, _repo: "## Expected behavior\nAdd `userstate: any`",
            prepare_target_environment=fail_if_called,
            apply_mechanical_edits=lambda _edits, _workdir: 0,
            collect_pr_changed_files=lambda _workdir, _timeout: set(),
            discovery_enabled=False,
            emit=lambda _msg: None,
            log=lambda event, data: logged.append((event, data)),
        )

    assert [event for event, _data in logged] == ["issue_rejected"]


def test_prepare_run_context_uses_issue_hints_and_builds_messages(tmp_path, monkeypatch):
    episode_calls = []
    mechanical_calls = []
    logged = []
    issue = """Bug: handleResub reports 0 months

## Where
`src/twitch/client.ts` -> `handleResub()`

## Symptom
The bot reports 0 months.
"""

    def fake_episode(**kwargs):
        episode_calls.append(kwargs)
        return {
            "source_file": kwargs["source_path"],
            "target_symbol": kwargs["symbol"],
            "test_file": "src/twitch/client.test.ts",
            "pre_test_source_edits": [
                {"path": kwargs["source_path"], "old": "function handleResub", "new": "export function handleResub"},
            ],
            "function_line_range": {"start": 7, "end": 12, "source": "definition"},
            "cookbook_text": "## Cookbook\n",
        }

    monkeypatch.setattr("agentic_tdd_runner.cookbook.build_episode_context", fake_episode)
    args = SimpleNamespace(issue="unused", source=None, symbol=None)

    context = prepare_run_context(
        args,
        repo=None,
        workdir=str(tmp_path),
        config={"prompt": {"system": "system"}, "timeouts": {"tool_execution": 10}},
        load_issue_text=lambda _args, _repo: issue,
        prepare_target_environment=lambda: None,
        apply_mechanical_edits=lambda edits, workdir: mechanical_calls.append((edits, workdir)) or len(edits),
        collect_pr_changed_files=lambda _workdir, _timeout: {"preexisting.ts"},
        discovery_enabled=True,
        emit=lambda _msg: None,
        log=lambda event, data: logged.append((event, data)),
    )

    assert context.source_path == "src/twitch/client.ts"
    assert context.symbol == "handleResub"
    assert context.baseline_changed_files == {"preexisting.ts"}
    assert episode_calls == [{
        "source_path": "src/twitch/client.ts",
        "symbol": "handleResub",
        "project_root": str(tmp_path),
    }]
    assert mechanical_calls[0][1] == str(tmp_path)
    assert context.messages[0]["content"] == "system\n\n## Cookbook\n"
    assert "Focus on the function `handleResub` (lines 7-12)" in context.messages[1]["content"]
    assert [event for event, _data in logged] == [
        "issue_intake",
        "run_baseline_dirty_files",
        "episode",
        "mechanical_edits",
    ]


def test_prepare_run_context_handles_missing_source_symbol_attrs(tmp_path):
    args = SimpleNamespace(issue="unused")

    context = prepare_run_context(
        args,
        repo=None,
        workdir=str(tmp_path),
        config={"prompt": {"system": "system"}, "timeouts": {"tool_execution": 10}},
        load_issue_text=lambda _args, _repo: "Bug: the command reports the wrong total",
        prepare_target_environment=lambda: None,
        apply_mechanical_edits=lambda _edits, _workdir: 0,
        collect_pr_changed_files=lambda _workdir, _timeout: set(),
        discovery_enabled=False,
        emit=lambda _msg: None,
        log=lambda _event, _data: None,
    )

    assert context.source_path is None
    assert context.symbol is None
    assert context.episode is None
    assert context.messages[0]["content"] == "system"
    assert "Bug: the command reports the wrong total" in context.messages[1]["content"]


def test_capture_run_baseline_logs_preexisting_dirty_files(tmp_path):
    logged = []

    baseline = capture_run_baseline(
        str(tmp_path),
        {"timeouts": {"tool_execution": 7}},
        collect_pr_changed_files=lambda workdir, timeout: {"b.ts", "a.ts"} if timeout == 7 else set(),
        log=lambda event, data: logged.append((event, data)),
    )

    assert baseline == {"a.ts", "b.ts"}
    assert logged == [("run_baseline_dirty_files", {"files": ["a.ts", "b.ts"]})]


@pytest.mark.parametrize(
    ("payload", "keys"),
    [
        ({"source_path": "src/app.ts"}, ["source_path"]),
        (["src/app.ts", "handle"], []),
    ],
)
def test_discover_target_from_issue_rejects_invalid_payload(tmp_path, monkeypatch, payload, keys):
    emitted = []
    logged = []

    monkeypatch.setattr(
        "agentic_tdd_runner.discovery.load_or_build_semantic_index",
        lambda project_root: {"candidates": [{}, {}]},
    )
    monkeypatch.setattr(
        "agentic_tdd_runner.discovery.discover_target",
        lambda **_kwargs: payload,
    )

    with pytest.raises(SystemExit, match="Could not determine source/symbol"):
        discover_target_from_issue(
            "Bug: totals are wrong",
            workdir=str(tmp_path),
            emit=emitted.append,
            log=lambda event, data: logged.append((event, data)),
        )

    assert emitted == ["[DISCOVERY] Discovery result missing source/symbol"]
    assert logged == [("discovery_failed", {"reason": "invalid_payload", "keys": keys})]


def test_build_episode_for_target_returns_none_without_target(tmp_path):
    assert build_episode_for_target(
        None,
        "target",
        workdir=str(tmp_path),
        apply_mechanical_edits=lambda _edits, _workdir: 0,
        emit=lambda _msg: None,
        log=lambda _event, _data: None,
    ) is None
