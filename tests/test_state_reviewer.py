"""Tests for deterministic bug-state review."""

from agentic_tdd_runner.state_reviewer import (
    BugStateReviewer,
    is_pending_forbidden_file_lookup,
)
from agentic_tdd_runner.runner_facts import RunnerFacts


def test_blocks_dependency_lookup_when_contract_is_already_known():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=True,
    )

    review = reviewer.review_tool_call(
        "run_command",
        {"command": 'rg "SubMethods" node_modules/tmi.js'},
        allow_dependency_contract_lookup=False,
    )

    assert review is not None
    assert review.event == "state_review_blocked"
    assert "CONTRACT" in review.message
    assert "already has concrete contract evidence" in review.message


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
            recommended_test_file="src/twitch/handleResub.test.ts",
            source_file="src/twitch/client.ts",
            target_symbol="handleResub",
            nearby_tests=[],
            symbol_tests=[],
        ),
    )
    reviewer.observe_tool_result(
        "run_command",
        {"command": "bun run typecheck"},
        (
            "[Reactive typecheck]\n"
            "src/twitch/handleResub.test.ts(4,1): error TS2304: Cannot find name 'describe'.\n"
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


def test_pending_forbidden_pattern_blocks_exploration_until_same_file_is_edited():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True},
        },
        contract_evidence_available=False,
    )
    reviewer.observe_tool_result(
        "create_file",
        {"path": "src/twitch/handleResub.test.ts"},
        (
            "OK: created src/twitch/handleResub.test.ts\n\n"
            "[Reactive forbidden] Potential quality issues detected before DONE:\n"
            "[Forbidden] src/twitch/handleResub.test.ts: 1 forbidden patterns\n"
            "  src/twitch/handleResub.test.ts:110 '{} as'"
        ),
        applied=True,
    )

    blocked = reviewer.review_tool_call(
        "run_command",
        {"command": 'rg "SubMethods" node_modules/tmi.js'},
        allow_dependency_contract_lookup=True,
    )
    allowed_edit = reviewer.review_tool_call(
        "str_replace_editor",
        {
            "path": "src/twitch/handleResub.test.ts",
            "old_str": "{} as SubMethods",
            "new_str": "methods",
        },
        allow_dependency_contract_lookup=True,
    )

    assert blocked is not None
    assert "QUALITY_REPAIR" in blocked.message
    assert "src/twitch/handleResub.test.ts" in blocked.message
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
        {"path": "src/twitch/handleResub.test.ts"},
        "[Reactive forbidden]\n  src/twitch/handleResub.test.ts:110 '{} as'",
        applied=True,
    )
    reviewer.observe_tool_result(
        "str_replace_editor",
        {"path": "src/twitch/handleResub.test.ts"},
        "OK: replaced in src/twitch/handleResub.test.ts",
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
        {"path": "src/twitch/handleResub.test.ts"},
        "[Reactive forbidden]\n  ./src/twitch/handleResub.test.ts:110 '{} as'",
        applied=True,
    )
    reviewer.observe_tool_result(
        "str_replace_editor",
        {"path": "src/twitch/../twitch/handleResub.test.ts"},
        "OK: replaced in src/twitch/handleResub.test.ts",
        applied=True,
    )

    review = reviewer.review_tool_call(
        "read_file",
        {"path": "src/twitch/handleResub.test.ts"},
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
        {"path": "src/twitch/handleResub.test.ts"},
        "[Reactive forbidden]\n  src/twitch/handleResub.test.ts:110 '{} as'",
        applied=True,
    )
    reviewer.observe_tool_result(
        "str_replace_editor",
        {"path": "src/twitch/handleResub.test.ts"},
        "ERROR: old_str not found",
        applied=False,
    )

    focused_read = reviewer.review_tool_call(
        "read_file",
        {"path": "./src/twitch/handleResub.test.ts"},
        allow_dependency_contract_lookup=True,
    )
    unrelated_read = reviewer.review_tool_call(
        "read_file",
        {"path": "src/twitch/client.ts"},
        allow_dependency_contract_lookup=True,
    )

    assert focused_read is None
    assert unrelated_read is not None
    assert "QUALITY_REPAIR" in unrelated_read.message


def test_pending_forbidden_file_lookup_accepts_direct_rg_and_sed_context():
    assert is_pending_forbidden_file_lookup(
        "rg",
        {"pattern": "{} as", "path": "./src/twitch/handleResub.test.ts"},
        "src/twitch/handleResub.test.ts",
    )
    assert is_pending_forbidden_file_lookup(
        "run_command",
        {"command": "sed -n '100,120p' src/twitch/handleResub.test.ts"},
        "src/twitch/handleResub.test.ts",
    )


def test_blocks_dependency_lookup_after_repair_budget_is_spent():
    reviewer = BugStateReviewer(
        {
            "state_reviewer": {"enabled": True, "max_dependency_contract_lookups": 1},
        },
        contract_evidence_available=True,
    )
    reviewer.observe_tool_result(
        "rg",
        {"pattern": "SubMethods", "path": "node_modules/tmi.js"},
        "resub(channel, username, months, message, userstate, methods)",
        applied=None,
    )

    review = reviewer.review_tool_call(
        "rg",
        {"pattern": "SubUserstate", "path": "node_modules/tmi.js"},
        allow_dependency_contract_lookup=True,
    )

    assert review is not None
    assert "CONTRACT_REPAIR" in review.message
    assert review.data["lookup_count"] == 1
    assert review.data["lookup_budget"] == 1
