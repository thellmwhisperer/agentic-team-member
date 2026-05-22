"""Tests for runtime loop orchestration."""

from agentic_tdd_runner import runtime
from agentic_tdd_runner.runner_facts import RunnerFacts


def test_has_contract_evidence_matches_real_handle_resub_issue_text():
    issue_text = """
    ## Root cause
    The function receives `streakMonths` (3rd param from tmi.js) but treats it
    as cumulative months. The tmi.js `resub` event signature is:
    ```
    resub(channel, username, months, message, userstate, methods)
    ```
    Cumulative months are in `userstate['msg-param-cumulative-months']`, but
    the function only accepts 3 params.
    """

    assert runtime.has_contract_evidence(issue_text, episode=None) is True


def test_reactive_test_feedback_reopens_dependency_contract_lookup_gate(tmp_path):
    config = {
        "agent": {"max_steps": 2, "non_apply_step_warning_threshold": 0},
        "verification": {"max_rejections": 1},
    }
    runtime_states = []

    def chat(_messages):
        runtime_states.append(dict(config["_runtime"]))
        if len(runtime_states) == 1:
            return {
                "choices": [{
                    "message": {
                        "content": "",
                        "tool_calls": [{
                            "id": "call_1",
                            "function": {"name": "run_command", "arguments": "{}"},
                        }],
                    },
                    "finish_reason": "tool_calls",
                }],
                "usage": {},
                "timings": {},
            }
        return {
            "choices": [{
                "message": {"content": "Still working"},
                "finish_reason": "stop",
            }],
            "usage": {},
            "timings": {},
        }

    result = runtime.run_agent_loop(
        messages=[],
        episode=None,
        issue_text="callback contract: use userstate",
        config=config,
        workdir=str(tmp_path),
        log_path=str(tmp_path / "log.jsonl"),
        emit=lambda _msg: None,
        log=lambda _event, _data: None,
        chat=chat,
        execute_tool=lambda _name, _args: "[Reactive test] failed: expected callback argument",
        truncate=lambda value: value,
        is_llm_timeout_error=lambda _exc: False,
        tool_applied_status=lambda _name, _result: None,
        tool_loop_signature=lambda _name, _args: None,
        tool_loop_warning_message=lambda signature: f"loop {signature}",
        non_apply_step_warning_message=lambda count: f"non-apply {count}",
        is_test_pass=lambda _name, _args: False,
        is_test_file_path=lambda path: path.endswith(".test.ts"),
        find_test_file=lambda _content: None,
        verify_red_green=lambda *_args, **_kwargs: (False, "unused"),
        run_quality_checks=lambda _test_file: (True, "unused"),
        create_pr=lambda *_args, **_kwargs: None,
    )

    assert result == 1
    assert runtime_states[0]["allow_dependency_contract_lookup"] is False
    assert runtime_states[1]["allow_dependency_contract_lookup"] is True


def test_state_reviewer_blocks_tool_execution_and_returns_scoped_feedback(tmp_path):
    config = {
        "agent": {"max_steps": 2, "non_apply_step_warning_threshold": 0},
        "verification": {"max_rejections": 1},
        "state_reviewer": {"enabled": True},
    }
    executed = []
    logged = []

    def chat(messages):
        if not messages:
            return {
                "choices": [{
                    "message": {
                        "content": "",
                        "tool_calls": [{
                            "id": "call_1",
                            "function": {
                                "name": "run_command",
                                "arguments": '{"command": "rg \\"SubMethods\\" node_modules/tmi.js"}',
                            },
                        }],
                    },
                    "finish_reason": "tool_calls",
                }],
                "usage": {},
                "timings": {},
            }
        assert messages[-1]["role"] == "tool"
        assert "STATE REVIEW BLOCKED" in messages[-1]["content"]
        assert "CONTRACT" in messages[-1]["content"]
        return {
            "choices": [{
                "message": {"content": "Still working"},
                "finish_reason": "stop",
            }],
            "usage": {},
            "timings": {},
        }

    result = runtime.run_agent_loop(
        messages=[],
        episode=None,
        issue_text="callback contract: use userstate['msg-param-cumulative-months']",
        config=config,
        workdir=str(tmp_path),
        log_path=str(tmp_path / "log.jsonl"),
        emit=lambda _msg: None,
        log=lambda event, data: logged.append((event, data)),
        chat=chat,
        execute_tool=lambda name, args: executed.append((name, args)) or "should not execute",
        truncate=lambda value: value,
        is_llm_timeout_error=lambda _exc: False,
        tool_applied_status=lambda _name, _result: None,
        tool_loop_signature=lambda _name, _args: None,
        tool_loop_warning_message=lambda signature: f"loop {signature}",
        non_apply_step_warning_message=lambda count: f"non-apply {count}",
        is_test_pass=lambda _name, _args: False,
        is_test_file_path=lambda path: path.endswith(".test.ts"),
        find_test_file=lambda _content: None,
        verify_red_green=lambda *_args, **_kwargs: (False, "unused"),
        run_quality_checks=lambda _test_file: (True, "unused"),
        create_pr=lambda *_args, **_kwargs: None,
    )

    assert result == 1
    assert executed == []
    assert any(event == "state_review_blocked" for event, _data in logged)


def test_intent_router_answers_known_runner_fact_as_tool_result(tmp_path):
    config = {
        "agent": {"max_steps": 3, "non_apply_step_warning_threshold": 0},
        "verification": {"max_rejections": 1},
        "state_reviewer": {"enabled": True},
    }
    episode = {
        "runner_facts": RunnerFacts(
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
    }
    executed = []
    logged = []

    def chat(messages):
        if not messages:
            return {
                "choices": [{
                    "message": {
                        "content": "",
                        "tool_calls": [{
                            "id": "call_1",
                            "function": {
                                "name": "run_command",
                                "arguments": '{"command": "bun run typecheck"}',
                            },
                        }],
                    },
                    "finish_reason": "tool_calls",
                }],
                "usage": {},
                "timings": {},
            }
        if len(executed) == 1 and messages[-1]["role"] == "tool":
            return {
                "choices": [{
                    "message": {
                        "content": "",
                        "tool_calls": [{
                            "id": "call_2",
                            "function": {
                                "name": "read_file",
                                "arguments": '{"path": "tsconfig.json"}',
                            },
                        }],
                    },
                    "finish_reason": "tool_calls",
                }],
                "usage": {},
                "timings": {},
            }
        assert messages[-1]["role"] == "tool"
        assert "RUNNER FACT ANSWER" in messages[-1]["content"]
        return {
            "choices": [{
                "message": {"content": "Still working"},
                "finish_reason": "stop",
            }],
            "usage": {},
            "timings": {},
        }

    result = runtime.run_agent_loop(
        messages=[],
        episode=episode,
        issue_text="Bug: missing import",
        config=config,
        workdir=str(tmp_path),
        log_path=str(tmp_path / "log.jsonl"),
        emit=lambda _msg: None,
        log=lambda event, data: logged.append((event, data)),
        chat=chat,
        execute_tool=lambda name, args: executed.append((name, args)) or (
            "[Reactive typecheck]\n"
            "src/twitch/handleResub.test.ts(4,1): error TS2304: Cannot find name 'describe'.\n"
        ),
        truncate=lambda value: value,
        is_llm_timeout_error=lambda _exc: False,
        tool_applied_status=lambda _name, _result: None,
        tool_loop_signature=lambda _name, _args: None,
        tool_loop_warning_message=lambda signature: f"loop {signature}",
        non_apply_step_warning_message=lambda count: f"non-apply {count}",
        is_test_pass=lambda _name, _args: False,
        is_test_file_path=lambda path: path.endswith(".test.ts"),
        find_test_file=lambda _content: None,
        verify_red_green=lambda *_args, **_kwargs: (False, "unused"),
        run_quality_checks=lambda _test_file: (True, "unused"),
        create_pr=lambda *_args, **_kwargs: None,
    )

    assert result == 1
    assert executed == [("run_command", {"command": "bun run typecheck"})]
    assert any(event == "intent_router_answered" for event, _data in logged)
