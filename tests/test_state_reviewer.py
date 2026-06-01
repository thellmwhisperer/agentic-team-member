"""Tests for deterministic bug-state review."""

from agentic_tdd_runner.state_reviewer import (
    BugStateReviewer,
    is_pending_forbidden_file_lookup,
)
from agentic_tdd_runner.runner_facts import RunnerFacts


def _ts_runner_facts():
    return RunnerFacts(
        test_runner="bun:test",
        test_command="bun test",
        typecheck_command="bun run typecheck",
        test_api_import='import { beforeEach, describe, expect, mock, test } from "bun:test";',
        recommended_test_file="src/events/processRenewal.test.ts",
        source_file="src/events/client.ts",
        target_symbol="processRenewal",
        nearby_tests=[],
        symbol_tests=[],
    )


def _python_runner_facts():
    return RunnerFacts(
        test_runner="pytest",
        test_command="python3 -m pytest",
        typecheck_command=None,
        test_api_import=None,
        recommended_test_file="tests/test_worker.py",
        source_file="src/worker.py",
        target_symbol="process",
        nearby_tests=[],
        symbol_tests=[],
    )


def test_blocks_dependency_lookup_when_contract_is_already_known():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=True,
        runner_facts=_ts_runner_facts(),
    )

    review = reviewer.review_tool_call(
        "run_command",
        {"command": 'rg "DeliveryOptions" node_modules/@example/event-bus'},
        allow_dependency_contract_lookup=False,
    )

    assert review is not None
    assert review.event == "state_review_blocked"
    assert "CONTRACT" in review.message
    assert "already has concrete contract evidence" in review.message


def test_dependency_lookup_without_language_context_is_not_treated_as_js():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=True,
    )

    review = reviewer.review_tool_call(
        "run_command",
        {"command": 'rg "DeliveryOptions" node_modules/@example/event-bus'},
        allow_dependency_contract_lookup=False,
    )

    assert review is None


def test_python_dependency_lookup_does_not_inherit_node_modules_contract_rule():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=True,
        runner_facts=_python_runner_facts(),
    )

    review = reviewer.review_tool_call(
        "run_command",
        {"command": 'rg "DeliveryOptions" node_modules/@example/event-bus'},
        allow_dependency_contract_lookup=False,
    )

    assert review is None


def test_reviewer_routes_known_runner_fact_answers_before_tool_execution():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=False,
        runner_facts=RunnerFacts(
            test_runner="bun:test",
            test_command="bun test",
            typecheck_command="bun run typecheck",
            test_api_import='import { beforeEach, describe, expect, mock, test } from "bun:test";',
            recommended_test_file="src/events/processRenewal.test.ts",
            source_file="src/events/client.ts",
            target_symbol="processRenewal",
            nearby_tests=[],
            symbol_tests=[],
        ),
    )
    reviewer.observe_tool_result(
        "run_command",
        {"command": "bun run typecheck"},
        (
            "[Reactive typecheck]\n"
            "src/events/processRenewal.test.ts(4,1): error TS2304: Cannot find name 'describe'.\n"
        ),
        applied=None,
    )

    review = reviewer.review_tool_call(
        "read_file",
        {"path": "tsconfig.json"},
        allow_dependency_contract_lookup=True,
    )

    assert review is not None
    assert review.event == "intent_router_answered"
    assert "RUNNER FACT ANSWER" in review.message


def test_reviewer_routes_runner_facts_even_when_state_reviewer_is_disabled():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": False},
        },
        contract_evidence_available=False,
        runner_facts=RunnerFacts(
            test_runner="bun:test",
            test_command="bun test",
            typecheck_command="bun run typecheck",
            test_api_import='import { beforeEach, describe, expect, mock, test } from "bun:test";',
            recommended_test_file="src/events/processRenewal.test.ts",
            source_file="src/events/client.ts",
            target_symbol="processRenewal",
            nearby_tests=[],
            symbol_tests=[],
        ),
    )
    reviewer.observe_tool_result(
        "run_command",
        {"command": "bun run typecheck"},
        (
            "[Reactive typecheck]\n"
            "src/events/processRenewal.test.ts(4,1): error TS2304: Cannot find name 'describe'.\n"
        ),
        applied=None,
    )

    review = reviewer.review_tool_call(
        "read_file",
        {"path": "tsconfig.json"},
        allow_dependency_contract_lookup=True,
    )

    assert review is not None
    assert review.event == "intent_router_answered"


def test_pending_forbidden_pattern_blocks_exploration_until_same_file_is_edited():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=False,
    )
    reviewer.observe_tool_result(
        "create_file",
        {"path": "src/events/processRenewal.test.ts"},
        (
            "OK: created src/events/processRenewal.test.ts\n\n"
            "[Reactive forbidden] Potential quality issues detected before DONE:\n"
            "[Forbidden] src/events/processRenewal.test.ts: 1 forbidden patterns\n"
            "  src/events/processRenewal.test.ts:110 '{} as'"
        ),
        applied=True,
    )

    blocked = reviewer.review_tool_call(
        "run_command",
        {"command": 'rg "DeliveryOptions" node_modules/@example/event-bus'},
        allow_dependency_contract_lookup=True,
    )
    allowed_edit = reviewer.review_tool_call(
        "str_replace_editor",
        {
            "path": "src/events/processRenewal.test.ts",
            "old_str": "{} as DeliveryOptions",
            "new_str": "methods",
        },
        allow_dependency_contract_lookup=True,
    )

    assert blocked is not None
    assert "QUALITY_REPAIR" in blocked.message
    assert "src/events/processRenewal.test.ts" in blocked.message
    assert allowed_edit is None


def test_successful_edit_clears_pending_forbidden_pattern_when_feedback_is_clean():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=False,
    )
    reviewer.observe_tool_result(
        "create_file",
        {"path": "src/events/processRenewal.test.ts"},
        "[Reactive forbidden]\n  src/events/processRenewal.test.ts:110 '{} as'",
        applied=True,
    )
    reviewer.observe_tool_result(
        "str_replace_editor",
        {"path": "src/events/processRenewal.test.ts"},
        "OK: replaced in src/events/processRenewal.test.ts",
        applied=True,
    )

    review = reviewer.review_tool_call(
        "rg",
        {"pattern": "DeliveryOptions", "path": "node_modules/@example/event-bus"},
        allow_dependency_contract_lookup=True,
    )

    assert review is None


def test_successful_apply_patch_clears_pending_forbidden_pattern():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=False,
    )
    reviewer.observe_tool_result(
        "create_file",
        {"path": "src/twitch/handleResub.test.ts"},
        "[Reactive forbidden]\n  src/twitch/handleResub.test.ts:110 '{} as'",
        applied=True,
    )
    patch = (
        "*** Begin Patch\n"
        "*** Update File: src/twitch/handleResub.test.ts\n"
        "@@\n"
        "-{} as SubMethods\n"
        "+methods\n"
        "*** End Patch"
    )
    reviewer.observe_tool_result(
        "apply_patch",
        {"patch": patch},
        "OK: applied patch (updated src/twitch/handleResub.test.ts)",
        applied=True,
    )

    review = reviewer.review_tool_call(
        "rg",
        {"pattern": "SubMethods", "path": "node_modules/tmi.js"},
        allow_dependency_contract_lookup=True,
    )

    assert review is None


def test_successful_edit_clears_pending_forbidden_pattern_with_normalized_path():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=False,
    )
    reviewer.observe_tool_result(
        "create_file",
        {"path": "src/events/processRenewal.test.ts"},
        "[Reactive forbidden]\n  ./src/events/processRenewal.test.ts:110 '{} as'",
        applied=True,
    )
    reviewer.observe_tool_result(
        "str_replace_editor",
        {"path": "src/events/../events/processRenewal.test.ts"},
        "OK: replaced in src/events/processRenewal.test.ts",
        applied=True,
    )

    review = reviewer.review_tool_call(
        "read_file",
        {"path": "src/events/processRenewal.test.ts"},
        allow_dependency_contract_lookup=True,
    )

    assert review is None


def test_pending_forbidden_allows_focused_read_after_failed_edit():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=False,
    )
    reviewer.observe_tool_result(
        "create_file",
        {"path": "src/events/processRenewal.test.ts"},
        "[Reactive forbidden]\n  src/events/processRenewal.test.ts:110 '{} as'",
        applied=True,
    )
    reviewer.observe_tool_result(
        "str_replace_editor",
        {"path": "src/events/processRenewal.test.ts"},
        "ERROR: old_str not found",
        applied=False,
    )

    focused_read = reviewer.review_tool_call(
        "read_file",
        {"path": "./src/events/processRenewal.test.ts"},
        allow_dependency_contract_lookup=True,
    )
    unrelated_read = reviewer.review_tool_call(
        "read_file",
        {"path": "src/events/client.ts"},
        allow_dependency_contract_lookup=True,
    )

    assert focused_read is None
    assert unrelated_read is not None
    assert "QUALITY_REPAIR" in unrelated_read.message


def test_pending_forbidden_file_lookup_accepts_direct_rg_and_sed_context():
    assert is_pending_forbidden_file_lookup(
        "rg",
        {"pattern": "{} as", "path": "./src/events/processRenewal.test.ts"},
        "src/events/processRenewal.test.ts",
    )
    assert is_pending_forbidden_file_lookup(
        "run_command",
        {"command": "sed -n '100,120p' src/events/processRenewal.test.ts"},
        "src/events/processRenewal.test.ts",
    )


def test_blocks_dependency_lookup_after_repair_budget_is_spent():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True, "max_dependency_contract_lookups": 1},
        },
        contract_evidence_available=True,
        runner_facts=_ts_runner_facts(),
    )
    reviewer.observe_tool_result(
        "rg",
        {"pattern": "DeliveryOptions", "path": "node_modules/@example/event-bus"},
        "renewal(channel, username, months, message, eventPayload, methods)",
        applied=None,
    )

    review = reviewer.review_tool_call(
        "rg",
        {"pattern": "RenewalEventPayload", "path": "node_modules/@example/event-bus"},
        allow_dependency_contract_lookup=True,
    )

    assert review is not None
    assert "CONTRACT_REPAIR" in review.message
    assert review.data["lookup_count"] == 1
    assert review.data["lookup_budget"] == 1


def test_blocks_repeated_target_source_reads_after_source_context_was_observed():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=False,
        runner_facts=RunnerFacts(
            test_runner="bun:test",
            test_command="bun test",
            typecheck_command="bun run typecheck",
            test_api_import='import { beforeEach, describe, expect, mock, test } from "bun:test";',
            recommended_test_file="src/events/processRenewal.test.ts",
            source_file="src/events/client.ts",
            target_symbol="processRenewal",
            nearby_tests=[],
            symbol_tests=[],
        ),
    )
    reviewer.observe_tool_result(
        "read_file",
        {"path": "src/events/client.ts"},
        "export function processRenewal() {}\n",
        applied=None,
    )

    reread = reviewer.review_tool_call(
        "run_command",
        {"command": "sed -n '120,170p' src/events/client.ts"},
        allow_dependency_contract_lookup=False,
    )

    assert reread is not None
    assert reread.event == "state_review_blocked"
    assert "SOURCE_CONTEXT" in reread.message
    assert "already been read `src/events/client.ts`" in reread.message


def test_allows_target_source_reread_after_source_edit():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=False,
        runner_facts=RunnerFacts(
            test_runner="bun:test",
            test_command="bun test",
            typecheck_command="bun run typecheck",
            test_api_import='import { beforeEach, describe, expect, mock, test } from "bun:test";',
            recommended_test_file="src/events/processRenewal.test.ts",
            source_file="src/events/client.ts",
            target_symbol="processRenewal",
            nearby_tests=[],
            symbol_tests=[],
        ),
    )
    reviewer.observe_tool_result(
        "read_file",
        {"path": "src/events/client.ts"},
        "export function processRenewal() {}\n",
        applied=None,
    )
    reviewer.observe_tool_result(
        "str_replace_editor",
        {"path": "src/events/client.ts"},
        "OK: replaced in src/events/client.ts",
        applied=True,
    )

    reread = reviewer.review_tool_call(
        "read_file",
        {"path": "./src/events/client.ts"},
        allow_dependency_contract_lookup=False,
    )

    assert reread is None


def test_allows_target_source_reread_after_reactive_feedback_points_at_source():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=False,
        runner_facts=RunnerFacts(
            test_runner="bun:test",
            test_command="bun test",
            typecheck_command="bun run typecheck",
            test_api_import='import { beforeEach, describe, expect, mock, test } from "bun:test";',
            recommended_test_file="src/events/processRenewal.test.ts",
            source_file="src/events/client.ts",
            target_symbol="processRenewal",
            nearby_tests=[],
            symbol_tests=[],
        ),
    )
    reviewer.observe_tool_result(
        "read_file",
        {"path": "src/events/client.ts"},
        "export function processRenewal() {}\n",
        applied=None,
    )
    reviewer.observe_tool_result(
        "run_command",
        {"command": "bun test src/events/processRenewal.test.ts"},
        "[Reactive test] failed:\n  at src/events/client.ts:42:7",
        applied=None,
    )

    reread = reviewer.review_tool_call(
        "run_command",
        {"command": "sed -n '38,46p' src/events/client.ts"},
        allow_dependency_contract_lookup=False,
    )

    assert reread is None


def test_allows_target_source_reread_after_compacted_test_failure_points_at_source():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=False,
        runner_facts=RunnerFacts(
            test_runner="bun:test",
            test_command="bun test",
            typecheck_command="bun run typecheck",
            test_api_import='import { beforeEach, describe, expect, mock, test } from "bun:test";',
            recommended_test_file="src/events/processRenewal.test.ts",
            source_file="src/events/client.ts",
            target_symbol="processRenewal",
            nearby_tests=[],
            symbol_tests=[],
        ),
    )
    reviewer.observe_tool_result(
        "read_file",
        {"path": "src/events/client.ts"},
        "export function processRenewal() {}\n",
        applied=None,
    )
    reviewer.observe_tool_result(
        "run_command",
        {"command": "bun test src/events/processRenewal.test.ts"},
        "[run_command test failure: exit 1]\n  at src/events/client.ts:42:7",
        applied=None,
    )

    reread = reviewer.review_tool_call(
        "run_command",
        {"command": "sed -n '38,46p' src/events/client.ts"},
        allow_dependency_contract_lookup=False,
    )

    assert reread is None
