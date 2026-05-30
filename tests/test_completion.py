"""Tests for the extracted completion pipeline."""

import subprocess

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
    def should_not_be_called(*_args, **_kwargs):
        raise AssertionError("should not be called when test file is missing")

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
        verify_red_green=should_not_be_called,
        run_quality_checks=should_not_be_called,
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


def test_try_complete_gives_up_after_max_quality_rounds():
    messages = [
        {"role": "system", "content": "system prompt"},
        {"role": "user", "content": "Fix this bug:\n\nbug text"},
    ]
    state = completion.CompletionState(quality_rejected=1)
    config = _base_config(quality_enabled=True)
    config["quality"]["max_fix_rounds"] = 2
    emitted = []
    logged = []

    result = completion.try_complete(
        7,
        {"content": "DONE"},
        messages=messages,
        episode=None,
        state=state,
        max_rejections=3,
        config=config,
        workdir="/unused",
        emit=emitted.append,
        log=lambda event, data: logged.append((event, data)),
        find_test_file=lambda hint=None: "src/file.test.ts",
        verify_red_green=lambda test_file: (True, "verified"),
        run_quality_checks=lambda test_file: (False, "QUALITY FAIL"),
        create_pr=lambda messages, msg, test_file, step: None,
    )

    assert result == "give_up"
    assert state.quality_rejected == 2
    assert messages == [
        {"role": "system", "content": "system prompt"},
        {"role": "user", "content": "Fix this bug:\n\nbug text"},
    ]
    assert any("Quality rejected 2 times" in msg for msg in emitted)
    assert logged[-1] == (
        "give_up",
        {"step": 7, "quality_rejected": 2, "max_fix_rounds": 2},
    )


def test_try_complete_reads_untracked_files_with_replacement_encoding(tmp_path, monkeypatch):
    bad_file = tmp_path / "bad.bin"
    bad_file.write_bytes(b"valid\n\xff\n")
    emitted = []
    logged = []

    def fake_run(cmd, **kwargs):
        if cmd == ["git", "diff"]:
            return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
        if cmd == ["git", "ls-files", "--others", "--exclude-standard"]:
            return subprocess.CompletedProcess(cmd, 0, stdout="bad.bin\n", stderr="")
        raise AssertionError(f"unexpected command: {cmd}")

    monkeypatch.setattr(completion.shutil, "which", lambda name: "git")
    monkeypatch.setattr(completion.subprocess, "run", fake_run)

    result = completion.try_complete(
        5,
        {"content": "DONE"},
        messages=[{"role": "system", "content": "system"}],
        episode=None,
        state=completion.CompletionState(),
        max_rejections=3,
        config=_base_config(),
        workdir=str(tmp_path),
        emit=emitted.append,
        log=lambda event, data: logged.append((event, data)),
        find_test_file=lambda hint=None: "src/file.test.ts",
        verify_red_green=lambda test_file: (True, "verified"),
        run_quality_checks=lambda test_file: (True, "ok"),
        create_pr=lambda messages, msg, test_file, step: None,
    )

    assert result == "done"
    assert any("bad.bin" in msg for msg in emitted)
    assert any("valid" in msg for msg in emitted)
    assert any("\ufffd" in msg for msg in emitted)
    assert "postamble_error" not in [event for event, _data in logged]


def test_read_file_preview_bounds_large_binary_content(tmp_path):
    binary_file = tmp_path / "large.bin"
    binary_file.write_bytes(b"ab\x00cd")

    text, truncated, binary = completion._read_file_preview(str(binary_file), max_bytes=4)

    assert text == "ab\\0c"
    assert truncated is True
    assert binary is True


def test_compaction_preserves_issue_without_system_message():
    compacted = completion.compact_messages_after_quality_failure(
        [
            {"role": "user", "content": "Fix this bug"},
            {"role": "assistant", "content": "working"},
        ],
        "QUALITY FAIL",
        "src/file.test.ts",
    )

    assert [msg["role"] for msg in compacted] == ["user", "user"]
    assert compacted[0]["content"] == "Fix this bug"
    assert "QUALITY FAIL" in compacted[1]["content"]


def test_should_compact_uses_defaults_for_malformed_quality_thresholds():
    should_compact, info = completion.should_compact_after_quality_failure(
        {
            "llm": {"context_window_tokens": 32768},
            "quality": {
                "compact_threshold_ratio": "not-a-number",
                "compact_min_headroom_tokens": "nope",
            },
        },
        {"prompt_tokens": 30000},
    )

    assert should_compact is True
    assert info["threshold_ratio"] == 0.85
    assert info["min_headroom_tokens"] == 2048
