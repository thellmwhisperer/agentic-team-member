"""Tests for the deterministic intent router."""

from agentic_tdd_runner.intent_router import IntentRouter
from agentic_tdd_runner.runner_facts import RunnerFacts


def _facts():
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


def _jest_facts():
    return RunnerFacts(
        test_runner="jest",
        test_command="npm exec -- jest --runInBand --watchman=false --coverage=false",
        typecheck_command="npm run typecheck",
        test_api_import='import { beforeEach, describe, expect, jest, test } from "@jest/globals";',
        recommended_test_file="src/events/processRenewal.test.ts",
        source_file="src/events/client.ts",
        target_symbol="processRenewal",
        nearby_tests=[],
        symbol_tests=[],
    )


def _python_facts():
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


def test_router_does_not_apply_bun_answers_to_python_runner():
    router = IntentRouter(_python_facts())
    router.observe_tool_result(
        "run_command",
        {"command": "python3 -m pytest tests/test_worker.py"},
        (
            "tests/test_worker.py:4:1 - error TS2304: Cannot find name 'describe'.\n"
            "Property 'reset' does not exist on type 'MockFunctionState<() => void>'.\n"
        ),
        applied=None,
    )

    decision = router.review_tool_call("read_file", {"path": "pyproject.toml"})

    assert decision is None


def test_router_answers_from_runner_facts_for_missing_bun_test_globals():
    router = IntentRouter(_facts())
    router.observe_tool_result(
        "run_command",
        {"command": "bun run typecheck"},
        (
            "[Reactive typecheck]\n"
            "src/events/processRenewal.test.ts(4,1): error TS2304: Cannot find name 'describe'.\n"
            "src/events/processRenewal.test.ts(5,3): error TS2304: Cannot find name 'mock'.\n"
            "src/events/processRenewal.test.ts(6,3): error TS2304: Cannot find name 'expect'.\n"
        ),
        applied=None,
    )

    decision = router.review_tool_call("read_file", {"path": "tsconfig.json"})

    assert decision is not None
    assert decision.event == "intent_router_answered"
    assert decision.data["intent"] == "inspect_test_framework"
    assert "RUNNER FACT ANSWER" in decision.message
    assert 'import { beforeEach, describe, expect, mock, test } from "bun:test";' in decision.message
    assert "if that import is already present" in decision.message
    assert "src/events/processRenewal.test.ts" in decision.message


def test_router_answers_from_runner_facts_for_missing_jest_globals():
    router = IntentRouter(_jest_facts())
    router.observe_tool_result(
        "run_command",
        {"command": "npm run typecheck"},
        (
            "[Reactive typecheck]\n"
            "src/events/processRenewal.test.ts(4,1): error TS2304: Cannot find name 'describe'.\n"
            "src/events/processRenewal.test.ts(5,3): error TS2304: Cannot find name 'jest'.\n"
            "src/events/processRenewal.test.ts(6,3): error TS2304: Cannot find name 'expect'.\n"
        ),
        applied=None,
    )

    decision = router.review_tool_call("read_file", {"path": "jest.config.ts"})

    assert decision is not None
    assert decision.event == "intent_router_answered"
    assert decision.data["intent"] == "inspect_test_framework"
    assert 'import { beforeEach, describe, expect, jest, test } from "@jest/globals";' in decision.message
    assert "src/events/processRenewal.test.ts" in decision.message


def test_router_does_not_treat_jest_as_bun_global():
    router = IntentRouter(_facts())
    router.observe_tool_result(
        "run_command",
        {"command": "bun run typecheck"},
        (
            "[Reactive typecheck]\n"
            "src/events/processRenewal.test.ts(5,3): error TS2304: Cannot find name 'jest'.\n"
        ),
        applied=None,
    )

    assert router.review_tool_call("read_file", {"path": "tsconfig.json"}) is None


def test_router_matches_spec_files_and_pretty_tsc_missing_bun_globals():
    router = IntentRouter(_facts())
    router.observe_tool_result(
        "run_command",
        {"command": "bun run typecheck"},
        (
            "[Reactive typecheck]\n"
            "src/events/processRenewal.spec.ts:4:1 - error TS2304: Cannot find name 'describe'.\n"
        ),
        applied=None,
    )

    decision = router.review_tool_call("run_command", {"command": "bun test"})

    assert decision is not None
    assert decision.data["intent"] == "inspect_test_framework"
    assert decision.data["pending_test_file"] == "src/events/processRenewal.spec.ts"
    assert "describe" in decision.message


def test_router_allows_edit_to_pending_test_file_then_clears_gate():
    router = IntentRouter(_facts())
    router.observe_tool_result(
        "run_command",
        {"command": "bun run typecheck"},
        (
            "[Reactive typecheck]\n"
            "src/events/processRenewal.test.ts(4,1): error TS2304: Cannot find name 'test'.\n"
        ),
        applied=None,
    )

    allowed = router.review_tool_call(
        "str_replace_editor",
        {
            "path": "src/events/processRenewal.test.ts",
            "old_str": 'import { processRenewal } from "./client";',
            "new_str": (
                'import { test } from "bun:test";\n'
                'import { processRenewal } from "./client";'
            ),
        },
    )
    assert allowed is None

    router.observe_tool_result(
        "str_replace_editor",
        {"path": "src/events/processRenewal.test.ts"},
        "OK: replaced in src/events/processRenewal.test.ts",
        applied=True,
    )

    assert router.review_tool_call(
        "run_command",
        {"command": "bun test src/events/processRenewal.test.ts"},
    ) is None


def test_router_allows_apply_patch_to_pending_test_file_then_clears_gate():
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

    patch = (
        "*** Begin Patch\n"
        "*** Update File: src/twitch/handleResub.test.ts\n"
        "@@\n"
        "-import { handleResub } from \"./client\";\n"
        "+import { test } from \"bun:test\";\n"
        "+import { handleResub } from \"./client\";\n"
        "*** End Patch"
    )
    assert router.review_tool_call("apply_patch", {"patch": patch}) is None

    router.observe_tool_result(
        "apply_patch",
        {"patch": patch},
        "OK: applied patch (updated src/twitch/handleResub.test.ts)",
        applied=True,
    )

    assert router.review_tool_call(
        "run_command",
        {"command": "bun test src/twitch/handleResub.test.ts"},
    ) is None


def test_router_matches_pretty_tsc_missing_bun_globals():
    router = IntentRouter(_facts())
    router.observe_tool_result(
        "run_command",
        {"command": "bun run typecheck"},
        (
            "[Reactive typecheck]\n"
            "src/events/processRenewal.test.ts:4:1 - error TS2304: Cannot find name 'describe'.\n"
        ),
        applied=None,
    )

    decision = router.review_tool_call("run_command", {"command": "bun test"})

    assert decision is not None
    assert "RUNNER FACT ANSWER" in decision.message
    assert "describe" in decision.message


def test_router_answers_bun_mock_reset_type_error_before_more_tooling():
    router = IntentRouter(_facts())
    router.observe_tool_result(
        "create_file",
        {"path": "src/events/processRenewal.test.ts"},
        (
            "[Reactive typecheck]\n"
            "src/events/processRenewal.test.ts(22,18): error TS2339: "
            "Property 'reset' does not exist on type 'MockFunctionState<() => void>'.\n"
        ),
        applied=True,
    )

    decision = router.review_tool_call("run_command", {"command": "bun test"})

    assert decision is not None
    assert decision.event == "intent_router_answered"
    assert decision.data["intent"] == "fix_bun_mock_api"
    assert "mockClear()" in decision.message
    assert "src/events/processRenewal.test.ts" in decision.message


def test_router_answers_import_time_side_effect_before_more_exploration():
    router = IntentRouter(_facts())
    router.observe_tool_result(
        "run_command",
        {"command": "bun test src/events/processRenewal.test.ts"},
        (
            "[Reactive test] failed:\n"
            "  src/events/processRenewal.test.ts:\n"
            "  # Unhandled error between tests\n"
            "  error: deepseek requires an API key\n"
            "    at createProvider (/repo/node_modules/@example-org/llm/src/provider.ts:50:19)\n"
            "    at new MetricsSummaryManager (/repo/src/managers/metrics-summary.ts:31:21)\n"
            "    at getMetricsSummaryManager (/repo/src/managers/metrics-summary.ts:358:16)\n"
            "    at /repo/src/events/client.ts:62:30\n"
            "    at loadAndEvaluateModule (2:1)\n"
        ),
        applied=None,
    )

    decision = router.review_tool_call(
        "read_file",
        {"path": "src/managers/metrics-summary.ts"},
    )

    assert decision is not None
    assert decision.event == "intent_router_answered"
    assert decision.data["intent"] == "fix_import_time_side_effect"
    assert decision.data["pending_test_file"] == "src/events/processRenewal.test.ts"
    assert decision.data["import_path"] == "./client"
    assert "IMPORT-TIME SIDE EFFECT ANSWER" in decision.message
    assert "mock.module(...)" in decision.message
    assert "Prefer the repo's existing mock/import pattern" in decision.message
    assert "Do not inspect provider/env/singleton modules" in decision.message


def test_router_allows_edit_to_import_time_side_effect_test_then_clears_gate():
    router = IntentRouter(_facts())
    router.observe_tool_result(
        "run_command",
        {"command": "bun test src/events/processRenewal.test.ts"},
        (
            "[Reactive test] failed:\n"
            "  # Unhandled error between tests\n"
            "  error: missing config\n"
            "    at /repo/src/events/client.ts:62:30\n"
            "    at loadAndEvaluateModule (2:1)\n"
        ),
        applied=None,
    )

    allowed = router.review_tool_call(
        "str_replace_editor",
        {
            "path": "src/events/processRenewal.test.ts",
            "old_str": "import { processRenewal } from './client';",
            "new_str": "let subject: typeof import('./client');",
        },
    )
    assert allowed is None

    router.observe_tool_result(
        "str_replace_editor",
        {"path": "src/events/processRenewal.test.ts"},
        "OK: replaced in src/events/processRenewal.test.ts",
        applied=True,
    )

    assert router.review_tool_call(
        "run_command",
        {"command": "bun test src/events/processRenewal.test.ts"},
    ) is None
