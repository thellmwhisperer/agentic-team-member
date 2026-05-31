"""Tests for harness artifact export."""

import json
from types import SimpleNamespace

from agentic_tdd_runner.artifacts import export_harness_artifacts
from agentic_tdd_runner.issue_intake import IssueContract
from agentic_tdd_runner.runner_facts import RunnerFacts


def _runner_facts():
    return RunnerFacts(
        test_runner="bun:test",
        test_command="bun test",
        typecheck_command="bun run typecheck",
        test_api_import='import { beforeEach, describe, expect, mock, test } from "bun:test";',
        recommended_test_file="src/events/processRenewal.test.ts",
        source_file="src/events/client.ts",
        target_symbol="processRenewal",
        nearby_tests=["src/events/client.test.ts"],
        symbol_tests=[],
    )


def _run_context():
    runner_facts = _runner_facts()
    episode = {
        "source_file": "src/events/client.ts",
        "target_symbol": "processRenewal",
        "test_file": "src/events/processRenewal.test.ts",
        "runner": "bun:test",
        "mocks_text": "mock.module('../logger', () => ({}));",
        "pre_test_source_edits": [{
            "path": "src/events/client.ts",
            "old": "function processRenewal(channel: string): void {",
            "new": "export function processRenewal(channel: string): void {",
        }],
        "conditional_source_edits": [{
            "path": "src/events/client.ts",
            "old": "function processRenewal(channel: string): void {",
            "new": "export function processRenewal(channel: string): void {",
        }],
        "cookbook_text": "## Mock Cookbook\nFULL COOKBOOK PAYLOAD\n",
        "runner_facts": runner_facts,
        "runner_facts_text": runner_facts.to_prompt_section(),
    }
    return SimpleNamespace(
        issue_contract=IssueContract(
            raw_text="raw issue text",
            model_text="## Symptom\nBug",
            source_hint="src/events/client.ts",
            symbol_hint="processRenewal",
            warnings=["dropped Fix approach before prompting"],
        ),
        issue_text="raw issue text",
        issue_text_for_model="## Symptom\nBug",
        source_path="src/events/client.ts",
        symbol="processRenewal",
        episode=episode,
        messages=[
            {
                "role": "system",
                "content": "SYSTEM PROMPT\n\n## Mock Cookbook\nFULL COOKBOOK PAYLOAD\n",
            },
            {"role": "user", "content": "USER PROMPT"},
        ],
        baseline_changed_files=set(),
    )


def test_export_harness_artifacts_writes_canonical_and_rendered_files(tmp_path):
    manifest = export_harness_artifacts(
        tmp_path,
        run_context=_run_context(),
        workdir="/repo/worktree",
        log_path="/logs/agent.jsonl",
    )

    assert (tmp_path / "manifest.json").exists()
    assert (tmp_path / "canonical" / "issue_contract.json").exists()
    assert (tmp_path / "canonical" / "episode.json").exists()
    assert (tmp_path / "canonical" / "runner_facts.json").exists()
    assert (tmp_path / "rendered" / "cookbook.md").read_text() == (
        "## Mock Cookbook\nFULL COOKBOOK PAYLOAD\n"
    )
    assert (tmp_path / "rendered" / "mocks.md").read_text() == (
        "mock.module('../logger', () => ({}));"
    )
    assert (tmp_path / "rendered" / "system_prompt.md").read_text().startswith("SYSTEM PROMPT")
    assert manifest["workdir"] == "/repo/worktree"
    assert manifest["log_path"] == "/logs/agent.jsonl"


def test_export_manifest_uses_refs_instead_of_repeating_payloads(tmp_path):
    manifest = export_harness_artifacts(
        tmp_path,
        run_context=_run_context(),
        workdir="/repo/worktree",
        log_path="/logs/agent.jsonl",
    )

    manifest_text = json.dumps(manifest, ensure_ascii=False)
    assert "FULL COOKBOOK PAYLOAD" not in manifest_text
    assert "function processRenewal(channel: string): void {" not in manifest_text
    assert "canonical/episode.json" in manifest_text
    assert "rendered/cookbook.md" in manifest_text
    assert "rendered/mocks.md" in manifest_text


def test_canonical_episode_replaces_rendered_payloads_with_file_refs(tmp_path):
    export_harness_artifacts(
        tmp_path,
        run_context=_run_context(),
        workdir="/repo/worktree",
        log_path="/logs/agent.jsonl",
    )

    episode = json.loads((tmp_path / "canonical" / "episode.json").read_text())

    assert "cookbook_text" not in episode
    assert "mocks_text" not in episode
    assert "runner_facts_text" not in episode
    assert "runner_facts" not in episode
    assert "conditional_source_edits" not in episode
    assert episode["conditional_source_edits_deduped_from"] == "pre_test_source_edits"
    assert episode["cookbook_ref"] == "rendered/cookbook.md"
    assert episode["mocks_ref"] == "rendered/mocks.md"
    assert episode["runner_facts_ref"] == "canonical/runner_facts.json"
    assert episode["pre_test_source_edits"][0]["old"] == (
        "function processRenewal(channel: string): void {"
    )


def test_canonical_episode_omits_refs_for_empty_rendered_payloads(tmp_path):
    context = _run_context()
    context.episode["cookbook_text"] = " \n"
    context.episode["mocks_text"] = ""
    context.episode["runner_facts_text"] = ""

    export_harness_artifacts(
        tmp_path,
        run_context=context,
        workdir="/repo/worktree",
        log_path="/logs/agent.jsonl",
    )

    episode = json.loads((tmp_path / "canonical" / "episode.json").read_text())

    assert "cookbook_ref" not in episode
    assert "mocks_ref" not in episode
    assert "runner_facts_text_ref" not in episode
    assert not (tmp_path / "rendered" / "cookbook.md").exists()
    assert not (tmp_path / "rendered" / "mocks.md").exists()
    assert not (tmp_path / "rendered" / "runner_facts.md").exists()


def test_export_jsonable_handles_mixed_sets(tmp_path):
    context = _run_context()
    context.episode["mixed_set"] = {1, "a"}

    export_harness_artifacts(
        tmp_path,
        run_context=context,
        workdir="/repo/worktree",
        log_path="/logs/agent.jsonl",
    )

    episode = json.loads((tmp_path / "canonical" / "episode.json").read_text())

    assert episode["mixed_set"] == [1, "a"]
