"""Tests for the extracted completion pipeline."""

from agentic_tdd_runner import completion


def _base_config(quality_enabled=False):
    return {
        "quality": {"enabled": quality_enabled},
        "prompt": {"no_test_found": "no test"},
        "timeouts": {"tool_execution": 10},
        "pr": {"enabled": False},
        "llm": {"context_window_tokens": 32768},
    }


def test_try_complete_appends_no_test_feedback_when_test_is_missing():
    messages = [
        {"role": "system", "content": "system"},
        {"role": "user", "content": "bug"},
    ]
    state = completion.CompletionState()

    result = completion.try_complete(
        3,
        {"content": "DONE"},
        messages=messages,
        episode=None,
        state=state,
        max_rejections=3,
        config=_base_config(),
        workdir="/unused",
        emit=lambda msg: None,
        log=lambda event, data: None,
        find_test_file=lambda hint=None: None,
        verify_red_green=lambda test_file: (True, "verified"),
        run_quality_checks=lambda test_file: (True, "ok"),
        create_pr=lambda messages, msg, test_file, step: None,
    )

    assert result == "no_test"
    assert messages[-1] == {"role": "user", "content": "no test"}


def test_try_complete_compacts_quality_feedback_near_context_limit():
    messages = [
        {"role": "system", "content": "system prompt"},
        {"role": "user", "content": "Fix this bug:\n\nbug text"},
        {"role": "assistant", "content": None, "tool_calls": [{"id": "call_1"}]},
        {"role": "tool", "tool_call_id": "call_1", "content": "long output"},
    ]
    state = completion.CompletionState(last_usage={"prompt_tokens": 30000})
    logged = []
    cache_cleared = []

    result = completion.try_complete(
        4,
        {"content": "DONE"},
        messages=messages,
        episode=None,
        state=state,
        max_rejections=3,
        config=_base_config(quality_enabled=True),
        workdir="/unused",
        emit=lambda msg: None,
        log=lambda event, data: logged.append((event, data)),
        find_test_file=lambda hint=None: "src/file.test.ts",
        verify_red_green=lambda test_file: (True, "verified"),
        run_quality_checks=lambda test_file: (False, "QUALITY FAIL"),
        create_pr=lambda messages, msg, test_file, step: None,
        clear_file_read_cache=lambda: cache_cleared.append(True),
    )

    assert result == "quality_fail"
    assert state.quality_rejected == 1
    assert [msg["role"] for msg in messages] == ["system", "user", "user"]
    assert "Verification already passed for src/file.test.ts" in messages[2]["content"]
    assert "QUALITY FAIL" in messages[2]["content"]
    assert cache_cleared == [True]
    assert [event for event, _data in logged] == [
        "verify_result",
        "quality",
        "context_compacted",
    ]
