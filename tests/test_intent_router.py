"""Tests for the deterministic intent router."""

from agentic_tdd_runner.intent_router import IntentRouter
from agentic_tdd_runner.runner_facts import RunnerFacts


def _facts():
    return RunnerFacts(
        test_runner="bun:test",
        test_command="bun test",
        typecheck_command="bun run typecheck",
        test_api_import='import { beforeEach, describe, expect, mock, test } from "bun:test";',
        recommended_test_file="src/twitch/handleResub.test.ts",
        source_file="src/twitch/client.ts",
        target_symbol="handleResub",
        nearby_tests=[],
        symbol_tests=[],
    )


def test_router_answers_from_runner_facts_for_missing_bun_test_globals():
    router = IntentRouter(_facts())
    router.observe_tool_result(
        "run_command",
        {"command": "bun run typecheck"},
        (
            "[Reactive typecheck]\n"
            "src/twitch/handleResub.test.ts(4,1): error TS2304: Cannot find name 'describe'.\n"
            "src/twitch/handleResub.test.ts(5,3): error TS2304: Cannot find name 'mock'.\n"
            "src/twitch/handleResub.test.ts(6,3): error TS2304: Cannot find name 'expect'.\n"
        ),
        applied=None,
    )

    decision = router.review_tool_call("read_file", {"path": "tsconfig.json"})

    assert decision is not None
    assert decision.event == "intent_router_answered"
    assert decision.data["intent"] == "inspect_test_framework"
    assert "RUNNER FACT ANSWER" in decision.message
    assert 'import { beforeEach, describe, expect, mock, test } from "bun:test";' in decision.message
    assert "src/twitch/handleResub.test.ts" in decision.message


def test_router_allows_edit_to_pending_test_file_then_clears_gate():
    router = IntentRouter(_facts())
    router.observe_tool_result(
        "run_command",
        {"command": "bun run typecheck"},
        (
            "[Reactive typecheck]\n"
            "src/twitch/handleResub.test.ts(4,1): error TS2304: Cannot find name 'test'.\n"
        ),
        applied=None,
    )

    allowed = router.review_tool_call(
        "str_replace_editor",
        {
            "path": "src/twitch/handleResub.test.ts",
            "old_str": 'import { handleResub } from "./client";',
            "new_str": (
                'import { test } from "bun:test";\n'
                'import { handleResub } from "./client";'
            ),
        },
    )
    assert allowed is None

    router.observe_tool_result(
        "str_replace_editor",
        {"path": "src/twitch/handleResub.test.ts"},
        "OK: replaced in src/twitch/handleResub.test.ts",
        applied=True,
    )

    assert router.review_tool_call(
        "run_command",
        {"command": "bun test src/twitch/handleResub.test.ts"},
    ) is None
