"""Tests for runtime loop orchestration."""

import pytest

from agentic_tdd_runner import runtime
from agentic_tdd_runner.issue_intake import parse_issue_contract
from agentic_tdd_runner.runner_facts import RunnerFacts


def _tool_call_response(call_id: str, name: str, arguments: str) -> dict:
    return {
        "choices": [{
            "message": {
                "content": "",
                "tool_calls": [{
                    "id": call_id,
                    "function": {"name": name, "arguments": arguments},
                }],
            },
            "finish_reason": "tool_calls",
        }],
        "usage": {},
        "timings": {},
    }


def test_has_contract_evidence_matches_model_facing_contract_text():
    issue_text = """
    ## Callback contract
    tmi.js calls resub handlers with:
    ```
    resub(channel, username, months, message, userstate, methods)
    ```
    """

    assert runtime.has_contract_evidence(issue_text, episode=None) is True


def test_reporter_hypothesis_and_callback_registration_do_not_block_contract_lookup():
    issue_text = """Bug: handleResub reports 0 months

## Symptom
The bot reports 0 months for cumulative resubs.

## Root cause
tmi.js emits resub(channel, username, months, message, userstate, methods), but
handleResub only accepts three params and must read
userstate['msg-param-cumulative-months'].

## Expected behavior
The bot should report cumulative subscription months.
"""
    contract = parse_issue_contract(issue_text)
    episode = {
        "callback_registrations": [{"line": 115, "text": "client.on('resub', handleResub)"}],
        "cookbook_text": (
            "### Callback Contract Evidence\n"
            "- line 115: `client.on('resub', handleResub)`\n"
            "### Source Edits\n"
            "export function handleResub(...)\n"
        ),
    }

    assert runtime.has_contract_evidence(contract.model_text, episode=episode) is False


def test_dependency_backed_callback_fact_counts_as_contract_evidence():
    episode = {
        "callback_registrations": [{"line": 115, "text": "client.on('resub', handleResub)"}],
        "cookbook_text": (
            "### Callback Contract Evidence\n"
            "- line 115: `client.on('resub', handleResub)`\n"
            "- tmi.js source emits `resub(channel, username, streakMonths, msg, tags, methods)`.\n"
            "- The third argument is `streakMonths`, derived from `tags['msg-param-streak-months']`.\n"
        ),
    }

    assert runtime.has_contract_evidence("", episode=episode) is True


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


def test_permission_mode_blocks_tool_before_declared_intent(tmp_path):
    config = {
        "agent": {
            "max_steps": 1,
            "non_apply_step_warning_threshold": 0,
            "permission_driven": True,
        },
        "verification": {"max_rejections": 1},
        "runner": {"command": "bun test"},
    }
    logged = []

    def chat(_messages):
        return {
            "choices": [{
                "message": {
                    "content": "",
                    "tool_calls": [{
                        "id": "call_1",
                        "function": {
                            "name": "read_file",
                            "arguments": '{"path": "src/twitch/client.ts"}',
                        },
                    }],
                },
                "finish_reason": "tool_calls",
            }],
            "usage": {},
            "timings": {},
        }

    result = runtime.run_agent_loop(
        messages=[],
        episode={"source_file": "src/twitch/client.ts", "test_file": "src/twitch/client.test.ts"},
        issue_text="bug text",
        config=config,
        workdir=str(tmp_path),
        log_path=str(tmp_path / "log.jsonl"),
        emit=lambda _msg: None,
        log=lambda event, data: logged.append((event, data)),
        chat=chat,
        execute_tool=lambda _name, _args: (_ for _ in ()).throw(AssertionError("should not execute")),
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
    assert any(
        event == "permission_review" and data["tool"] == "read_file"
        for event, data in logged
    )
    assert any(
        event == "tool" and "PERMISSION REQUIRED" in data["result"]
        for event, data in logged
    )


def test_permission_mode_preserves_write_grant_across_informational_harness_answer(tmp_path):
    source = tmp_path / "src" / "twitch" / "client.ts"
    source.parent.mkdir(parents=True)
    source.write_text(
        "\n".join([
            "export function handleResub(channel: string, username: string, months: number): void {",
            "  logger.event('resub', { username, months });",
            "}",
        ])
    )
    config = {
        "agent": {
            "max_steps": 3,
            "non_apply_step_warning_threshold": 0,
            "permission_driven": True,
        },
        "verification": {"max_rejections": 1},
        "runner": {"command": "bun test"},
    }
    episode = {
        "source_file": "src/twitch/client.ts",
        "test_file": "src/twitch/handleResub.test.ts",
        "target_symbol": "handleResub",
        "cookbook_text": "\n".join([
            "### Callback Contract Evidence",
            "- line 115: `client.on('resub', handleResub)`",
            "- tmi.js source emits `resub(channel, username, streakMonths, msg, tags, methods)`.",
            "",
            "### Source Edits (apply before testing)",
            "OLD:",
            "function handleResub(channel: string, username: string, months: number): void {",
        ]),
    }
    calls = []
    executed = []

    def chat(_messages):
        calls.append(None)
        if len(calls) == 1:
            tool_name = "ask_harness"
            arguments = '{"intent": "write_regression_test", "question": "create test"}'
        elif len(calls) == 2:
            tool_name = "ask_harness"
            arguments = '{"intent": "understand_contract", "question": "What does handleResub do internally?"}'
        else:
            tool_name = "create_file"
            arguments = '{"path": "src/twitch/handleResub.test.ts", "content": "test"}'
        return {
            "choices": [{
                "message": {
                    "content": "",
                    "tool_calls": [{
                        "id": f"call_{len(calls)}",
                        "function": {"name": tool_name, "arguments": arguments},
                    }],
                },
                "finish_reason": "tool_calls",
            }],
            "usage": {},
            "timings": {},
        }

    runtime.run_agent_loop(
        messages=[],
        episode=episode,
        issue_text="bug text",
        config=config,
        workdir=str(tmp_path),
        log_path=str(tmp_path / "log.jsonl"),
        emit=lambda _msg: None,
        log=lambda _event, _data: None,
        chat=chat,
        execute_tool=lambda name, args: executed.append((name, args)) or "OK: created",
        truncate=lambda value: value,
        is_llm_timeout_error=lambda _exc: False,
        tool_applied_status=lambda name, _result: name == "create_file",
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

    assert executed == [
        ("create_file", {"path": "src/twitch/handleResub.test.ts", "content": "test"})
    ]


def test_permission_mode_accepts_target_challenge_and_reroutes_episode(tmp_path):
    wrong_source = tmp_path / "src" / "personality" / "sanitizer.ts"
    right_source = tmp_path / "src" / "twitch" / "client.ts"
    wrong_source.parent.mkdir(parents=True)
    right_source.parent.mkdir(parents=True)
    wrong_source.write_text(
        "\n".join([
            "export function wrapUserMessage(input: string): string {",
            "  return input.trim();",
            "}",
        ])
    )
    right_source.write_text(
        "\n".join([
            "export function handleMention(message: string): boolean {",
            "  return message.includes('@manolitozurrapa');",
            "}",
        ])
    )
    config = {
        "agent": {
            "max_steps": 3,
            "non_apply_step_warning_threshold": 0,
            "permission_driven": True,
        },
        "verification": {"max_rejections": 1},
        "runner": {
            "framework": "bun:test",
            "command": "bun test",
            "test_file_patterns": ["*.test.ts"],
            "exclude_dirs": [],
        },
    }
    episode = {
        "source_file": "src/personality/sanitizer.ts",
        "test_file": "src/personality/wrapUserMessage.test.ts",
        "target_symbol": "wrapUserMessage",
        "cookbook_text": "",
    }
    calls = []
    executed = []
    logged = []

    def chat(messages):
        calls.append(None)
        if len(calls) == 1:
            return {
                "choices": [{
                    "message": {
                        "content": "",
                        "tool_calls": [{
                            "id": "call_1",
                            "function": {
                                "name": "ask_harness",
                                "arguments": (
                                    '{"intent": "challenge_target", '
                                    '"source_file": "src/twitch/client.ts", '
                                    '"target_symbol": "handleMention", '
                                    '"evidence": "rg found mention dispatch in client.ts; sanitizer only trims text"}'
                                ),
                            },
                        }],
                    },
                    "finish_reason": "tool_calls",
                }],
                "usage": {},
                "timings": {},
            }
        if len(calls) == 2:
            assert "TARGET CHALLENGE ACCEPTED" in messages[-1]["content"]
            assert "src/twitch/client.ts::handleMention" in messages[-1]["content"]
            return {
                "choices": [{
                    "message": {
                        "content": "",
                        "tool_calls": [{
                            "id": "call_2",
                            "function": {
                                "name": "ask_harness",
                                "arguments": '{"intent": "write_regression_test"}',
                            },
                        }],
                    },
                    "finish_reason": "tool_calls",
                }],
                "usage": {},
                "timings": {},
            }
        assert "src/twitch/handleMention.test.ts" in messages[-1]["content"]
        assert "src/personality/wrapUserMessage.test.ts" not in messages[-1]["content"]
        return {
            "choices": [{
                "message": {
                    "content": "",
                    "tool_calls": [{
                        "id": "call_3",
                        "function": {
                            "name": "create_file",
                            "arguments": (
                                '{"path": "src/twitch/handleMention.test.ts", '
                                '"content": "test"}'
                            ),
                        },
                    }],
                },
                "finish_reason": "tool_calls",
            }],
            "usage": {},
            "timings": {},
        }

    runtime.run_agent_loop(
        messages=[],
        episode=episode,
        issue_text="@manolitozurrapa solo funciona al principio del mensaje",
        config=config,
        workdir=str(tmp_path),
        log_path=str(tmp_path / "log.jsonl"),
        emit=lambda _msg: None,
        log=lambda event, data: logged.append((event, data)),
        chat=chat,
        execute_tool=lambda name, args: executed.append((name, args)) or "OK: created",
        truncate=lambda value: value,
        is_llm_timeout_error=lambda _exc: False,
        tool_applied_status=lambda name, _result: name == "create_file",
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

    assert executed == [
        ("create_file", {"path": "src/twitch/handleMention.test.ts", "content": "test"})
    ]
    assert any(
        event == "target_challenge_accepted"
        and data["from"] == "src/personality/sanitizer.ts::wrapUserMessage"
        and data["to"] == "src/twitch/client.ts::handleMention"
        for event, data in logged
    )


def test_challenge_target_reroutes_without_permission_mode(tmp_path):
    wrong_source = tmp_path / "src" / "personality" / "sanitizer.ts"
    right_source = tmp_path / "src" / "twitch" / "client.ts"
    wrong_source.parent.mkdir(parents=True)
    right_source.parent.mkdir(parents=True)
    wrong_source.write_text(
        "\n".join([
            "export function wrapUserMessage(input: string): string {",
            "  return input.trim();",
            "}",
        ])
    )
    right_source.write_text(
        "\n".join([
            "export function handleMention(message: string): boolean {",
            "  return message.includes('@manolitozurrapa');",
            "}",
        ])
    )
    config = {
        "agent": {
            "max_steps": 2,
            "non_apply_step_warning_threshold": 0,
            "permission_driven": False,
        },
        "verification": {"max_rejections": 1},
        "runner": {
            "framework": "bun:test",
            "command": "bun test",
            "test_file_patterns": ["*.test.ts"],
            "exclude_dirs": [],
        },
    }
    episode = {
        "source_file": "src/personality/sanitizer.ts",
        "test_file": "src/personality/wrapUserMessage.test.ts",
        "target_symbol": "wrapUserMessage",
        "cookbook_text": "",
    }
    calls = []
    executed = []
    logged = []

    def chat(messages):
        calls.append(None)
        if len(calls) == 1:
            return _tool_call_response(
                "call_1",
                "ask_harness",
                (
                    '{"intent": "challenge_target", '
                    '"source_file": "src/twitch/client.ts", '
                    '"target_symbol": "handleMention", '
                    '"evidence": "rg found mention dispatch in client.ts; sanitizer only trims text"}'
                ),
            )
        assert "TARGET CHALLENGE ACCEPTED" in messages[-1]["content"]
        assert "src/twitch/client.ts::handleMention" in messages[-1]["content"]
        return {"choices": [{"message": {"content": "blocked"}, "finish_reason": "stop"}], "usage": {}, "timings": {}}

    runtime.run_agent_loop(
        messages=[],
        episode=episode,
        issue_text="@manolitozurrapa solo funciona al principio del mensaje",
        config=config,
        workdir=str(tmp_path),
        log_path=str(tmp_path / "log.jsonl"),
        emit=lambda _msg: None,
        log=lambda event, data: logged.append((event, data)),
        chat=chat,
        execute_tool=lambda name, args: executed.append((name, args)) or "ERROR: misconfigured",
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

    assert executed == []
    assert any(
        event == "target_challenge_accepted"
        and data["from"] == "src/personality/sanitizer.ts::wrapUserMessage"
        and data["to"] == "src/twitch/client.ts::handleMention"
        for event, data in logged
    )


def test_other_ask_harness_intents_without_permission_mode_get_useful_message(tmp_path):
    config = {
        "agent": {
            "max_steps": 2,
            "non_apply_step_warning_threshold": 0,
            "permission_driven": False,
        },
        "verification": {"max_rejections": 1},
    }
    executed = []
    tool_results = []

    def chat(messages):
        if not tool_results:
            return _tool_call_response(
                "call_1",
                "ask_harness",
                '{"intent": "write_regression_test", "question": "may I write a test?"}',
            )
        assert "HARNESS INTENT UNAVAILABLE" in messages[-1]["content"]
        return {"choices": [{"message": {"content": "blocked"}, "finish_reason": "stop"}], "usage": {}, "timings": {}}

    runtime.run_agent_loop(
        messages=[],
        episode=None,
        issue_text="bug",
        config=config,
        workdir=str(tmp_path),
        log_path=str(tmp_path / "log.jsonl"),
        emit=lambda _msg: None,
        log=lambda _event, _data: None,
        chat=chat,
        execute_tool=lambda name, args: executed.append((name, args)) or "ERROR: misconfigured",
        truncate=lambda value: tool_results.append(value) or value,
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

    assert executed == []
    assert "permission_driven" in tool_results[0]
    assert "misconfigured" not in tool_results[0]


def test_permission_mode_requires_challenge_after_search_finds_better_target(tmp_path):
    wrong_source = tmp_path / "src" / "personality" / "sanitizer.ts"
    right_source = tmp_path / "src" / "twitch" / "client.ts"
    wrong_source.parent.mkdir(parents=True)
    right_source.parent.mkdir(parents=True)
    wrong_source.write_text(
        "\n".join([
            "export function wrapUserMessage(input: string): string {",
            "  return input.trim();",
            "}",
        ])
    )
    right_source.write_text(
        "\n".join([
            "export function handleMessage(message: string): boolean {",
            "  const lower = message.toLowerCase();",
            "  const isMention = lower.startsWith('@manolitozurrapa');",
            "  return isMention;",
            "}",
        ])
    )
    config = {
        "agent": {
            "max_steps": 4,
            "non_apply_step_warning_threshold": 0,
            "permission_driven": True,
        },
        "verification": {"max_rejections": 1},
        "runner": {
            "framework": "bun:test",
            "command": "bun test",
            "test_file_patterns": ["*.test.ts"],
            "exclude_dirs": [],
        },
    }
    episode = {
        "source_file": "src/personality/sanitizer.ts",
        "test_file": "src/personality/wrapUserMessage.test.ts",
        "target_symbol": "wrapUserMessage",
        "cookbook_text": "",
    }
    calls = []
    logged = []

    def chat(messages):
        calls.append(messages[-1]["content"] if messages else "")
        if len(calls) == 1:
            return _tool_call_response(
                "call_1",
                "ask_harness",
                '{"intent": "understand_contract", "question": "search mention dispatch"}',
            )
        if len(calls) == 2:
            return _tool_call_response("call_2", "rg", '{"pattern": "@manolitozurrapa"}')
        if len(calls) == 3:
            return _tool_call_response("call_3", "ask_harness", '{"intent": "write_regression_test"}')
        assert "TARGET CHALLENGE REQUIRED" in messages[-1]["content"]
        assert '"target_symbol": "handleMessage"' in messages[-1]["content"]
        return {"choices": [{"message": {"content": "blocked"}, "finish_reason": "stop"}], "usage": {}, "timings": {}}

    runtime.run_agent_loop(
        messages=[],
        episode=episode,
        issue_text="@manolitozurrapa solo funciona al principio del mensaje",
        config=config,
        workdir=str(tmp_path),
        log_path=str(tmp_path / "log.jsonl"),
        emit=lambda _msg: None,
        log=lambda event, data: logged.append((event, data)),
        chat=chat,
        execute_tool=lambda name, _args: (
            "./src/twitch/client.ts:3:  const isMention = lower.startsWith('@manolitozurrapa');"
            if name == "rg"
            else "OK"
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

    assert any(
        event == "target_challenge_hint"
        and data["source_file"] == "src/twitch/client.ts"
        and data["target_symbol"] == "handleMessage"
        for event, data in logged
    )
    assert any(
        event == "target_challenge_required"
        and data["intent"] == "write_regression_test"
        for event, data in logged
    )


def test_permission_mode_requires_challenge_after_read_file_finds_better_target(tmp_path):
    wrong_source = tmp_path / "src" / "personality" / "sanitizer.ts"
    right_source = tmp_path / "src" / "twitch" / "client.ts"
    wrong_source.parent.mkdir(parents=True)
    right_source.parent.mkdir(parents=True)
    wrong_source.write_text(
        "\n".join([
            "export function wrapUserMessage(input: string): string {",
            "  return input.trim();",
            "}",
        ])
    )
    right_source.write_text(
        "\n".join([
            "export function connect(): void {",
            "  logger.info('ready');",
            "}",
            "",
            "export async function handleMessage(message: string): Promise<void> {",
            "  const botMention = '@manolitozurrapa';",
            "  const isMention = message.startsWith(botMention);",
            "  if (isMention) await respond(message);",
            "}",
        ])
    )
    config = {
        "agent": {
            "max_steps": 4,
            "non_apply_step_warning_threshold": 0,
            "permission_driven": True,
        },
        "verification": {"max_rejections": 1},
        "runner": {
            "framework": "bun:test",
            "command": "bun test",
            "test_file_patterns": ["*.test.ts"],
            "exclude_dirs": [],
        },
    }
    episode = {
        "source_file": "src/personality/sanitizer.ts",
        "test_file": "src/personality/wrapUserMessage.test.ts",
        "target_symbol": "wrapUserMessage",
        "cookbook_text": "",
    }
    calls = []
    logged = []

    def chat(messages):
        calls.append(messages[-1]["content"] if messages else "")
        if len(calls) == 1:
            return _tool_call_response("call_1", "ask_harness", '{"intent": "understand_contract"}')
        if len(calls) == 2:
            return _tool_call_response("call_2", "read_file", '{"path": "src/twitch/client.ts"}')
        if len(calls) == 3:
            return _tool_call_response("call_3", "ask_harness", '{"intent": "write_regression_test"}')
        assert "TARGET CHALLENGE REQUIRED" in messages[-1]["content"]
        assert '"source_file": "src/twitch/client.ts"' in messages[-1]["content"]
        assert '"target_symbol": "handleMessage"' in messages[-1]["content"]
        return {"choices": [{"message": {"content": "blocked"}, "finish_reason": "stop"}], "usage": {}, "timings": {}}

    runtime.run_agent_loop(
        messages=[],
        episode=episode,
        issue_text="@manolitozurrapa solo funciona al principio del mensaje",
        config=config,
        workdir=str(tmp_path),
        log_path=str(tmp_path / "log.jsonl"),
        emit=lambda _msg: None,
        log=lambda event, data: logged.append((event, data)),
        chat=chat,
        execute_tool=lambda name, _args: right_source.read_text() if name == "read_file" else "OK",
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

    assert any(
        event == "target_challenge_hint"
        and data["source_file"] == "src/twitch/client.ts"
        and data["target_symbol"] == "handleMessage"
        for event, data in logged
    )
    assert any(
        event == "target_challenge_required"
        and data["intent"] == "write_regression_test"
        for event, data in logged
    )


def test_target_challenge_does_not_apply_mechanical_edits_before_enrichment(tmp_path, monkeypatch):
    source = tmp_path / "src" / "client.ts"
    source.parent.mkdir(parents=True)
    source.write_text(
        "\n".join([
            "function handleMention(message: string): boolean {",
            "  return message.includes('@manolitozurrapa');",
            "}",
        ])
    )
    applied = []

    def failing_runner_facts(*_args, **_kwargs):
        raise RuntimeError("runner facts failed")

    monkeypatch.setattr(
        "agentic_tdd_runner.runner_facts.build_runner_facts",
        failing_runner_facts,
    )

    with pytest.raises(RuntimeError, match="runner facts failed"):
        runtime._build_rerouted_episode(
            {
                "source_file": "src/client.ts",
                "target_symbol": "handleMention",
                "evidence": "mention dispatch lives here",
            },
            current_episode={
                "source_file": "src/personality/sanitizer.ts",
                "target_symbol": "wrapUserMessage",
            },
            permission_context={
                "source_file": "src/personality/sanitizer.ts",
                "target_symbol": "wrapUserMessage",
            },
            config={"runner": {"command": "bun test"}},
            workdir=str(tmp_path),
            apply_mechanical_edits=lambda edits, workdir: applied.append((edits, workdir)) or len(edits),
            emit=lambda _msg: None,
            log=lambda _event, _data: None,
        )

    assert applied == []


def test_blocked_test_create_does_not_open_edit_source_gate(tmp_path):
    config = {
        "agent": {
            "max_steps": 2,
            "non_apply_step_warning_threshold": 0,
            "permission_driven": True,
        },
        "verification": {"max_rejections": 1},
        "runner": {"command": "bun test"},
    }
    episode = {
        "source_file": "src/client.ts",
        "test_file": "src/client.test.ts",
        "target_symbol": "handle",
        "cookbook_text": "",
    }
    calls = []
    executed = []
    logged = []

    def chat(_messages):
        calls.append(None)
        if len(calls) == 1:
            return {
                "choices": [{
                    "message": {
                        "content": "",
                        "tool_calls": [{
                            "id": "call_1",
                            "function": {
                                "name": "create_file",
                                "arguments": '{"path": "src/client.test.ts", "content": "test"}',
                            },
                        }],
                    },
                    "finish_reason": "tool_calls",
                }],
                "usage": {},
                "timings": {},
            }
        return {
            "choices": [{
                "message": {
                    "content": "",
                    "tool_calls": [{
                        "id": "call_2",
                        "function": {
                            "name": "ask_harness",
                            "arguments": '{"intent": "edit_source"}',
                        },
                    }],
                },
                "finish_reason": "tool_calls",
            }],
            "usage": {},
            "timings": {},
        }

    runtime.run_agent_loop(
        messages=[],
        episode=episode,
        issue_text="bug text",
        config=config,
        workdir=str(tmp_path),
        log_path=str(tmp_path / "log.jsonl"),
        emit=lambda _msg: None,
        log=lambda event, data: logged.append((event, data)),
        chat=chat,
        execute_tool=lambda name, args: executed.append((name, args)) or "OK",
        truncate=lambda value: value,
        is_llm_timeout_error=lambda _exc: False,
        tool_applied_status=lambda name, _result: name == "create_file",
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

    assert executed == []
    assert any(
        event == "tool" and "PERMISSION REQUIRED" in data["result"]
        for event, data in logged
    )
    assert any(
        event == "tool" and "create a focused failing regression test" in data["result"]
        for event, data in logged
    )


def test_denied_test_command_does_not_trigger_red_green_verification(tmp_path):
    config = {
        "agent": {
            "max_steps": 2,
            "non_apply_step_warning_threshold": 0,
            "permission_driven": True,
        },
        "verification": {"max_rejections": 1},
        "runner": {"command": "bun test"},
    }
    episode = {
        "source_file": "src/client.ts",
        "test_file": "src/client.test.ts",
        "target_symbol": "handle",
        "cookbook_text": "",
    }
    calls = []
    logged = []

    def chat(_messages):
        calls.append(None)
        if len(calls) == 1:
            return _tool_call_response(
                "call_1",
                "ask_harness",
                '{"intent": "write_regression_test"}',
            )
        return _tool_call_response(
            "call_2",
            "run_command",
            '{"command": "bun test src/client.test.ts"}',
        )

    def verify_red_green(*_args, **_kwargs):
        raise AssertionError("denied run_command must not trigger red/green verification")

    runtime.run_agent_loop(
        messages=[],
        episode=episode,
        issue_text="bug text",
        config=config,
        workdir=str(tmp_path),
        log_path=str(tmp_path / "log.jsonl"),
        emit=lambda _msg: None,
        log=lambda event, data: logged.append((event, data)),
        chat=chat,
        execute_tool=lambda _name, _args: "should not execute",
        truncate=lambda value: value,
        is_llm_timeout_error=lambda _exc: False,
        tool_applied_status=lambda _name, _result: None,
        tool_loop_signature=lambda _name, _args: None,
        tool_loop_warning_message=lambda signature: f"loop {signature}",
        non_apply_step_warning_message=lambda count: f"non-apply {count}",
        is_test_pass=lambda name, _args: name == "run_command",
        is_test_file_path=lambda path: path.endswith(".test.ts"),
        find_test_file=lambda _content: None,
        verify_red_green=verify_red_green,
        run_quality_checks=lambda _test_file: (True, "unused"),
        create_pr=lambda *_args, **_kwargs: None,
    )

    assert any(
        event == "tool" and "PERMISSION DENIED" in data["result"]
        for event, data in logged
    )


def test_permission_mode_marks_test_created_after_str_replace_edit(tmp_path):
    config = {
        "agent": {
            "max_steps": 4,
            "non_apply_step_warning_threshold": 0,
            "permission_driven": True,
        },
        "verification": {"max_rejections": 1},
        "runner": {"command": "bun test"},
    }
    episode = {
        "source_file": "src/client.ts",
        "test_file": "src/client.test.ts",
        "target_symbol": "handle",
        "cookbook_text": "",
    }
    calls = []
    executed = []
    logged = []

    def chat(_messages):
        calls.append(None)
        if len(calls) == 1:
            return _tool_call_response(
                "call_1",
                "ask_harness",
                '{"intent": "write_regression_test"}',
            )
        if len(calls) == 2:
            return _tool_call_response(
                "call_2",
                "str_replace_editor",
                '{"path": "src/client.test.ts", "old_str": "old", "new_str": "new"}',
            )
        if len(calls) == 3:
            return _tool_call_response(
                "call_3",
                "ask_harness",
                '{"intent": "run_test"}',
            )
        return _tool_call_response(
            "call_4",
            "run_command",
            '{"command": "bun test src/client.test.ts"}',
        )

    runtime.run_agent_loop(
        messages=[],
        episode=episode,
        issue_text="bug text",
        config=config,
        workdir=str(tmp_path),
        log_path=str(tmp_path / "log.jsonl"),
        emit=lambda _msg: None,
        log=lambda event, data: logged.append((event, data)),
        chat=chat,
        execute_tool=lambda name, args: executed.append((name, args)) or "OK",
        truncate=lambda value: value,
        is_llm_timeout_error=lambda _exc: False,
        tool_applied_status=lambda name, _result: name == "str_replace_editor",
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

    assert ("run_command", {"command": "bun test src/client.test.ts"}) in executed
    assert not any(
        event == "permission_review"
        and data["intent"] == "run_test"
        and data["allowed"] is False
        for event, data in logged
    )


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
