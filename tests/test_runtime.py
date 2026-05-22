"""Tests for runtime loop orchestration."""

from agentic_tdd_runner import runtime


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
