"""Tests for agent tool execution helpers."""

from pathlib import Path
import subprocess

from agentic_tdd_runner.paths import resolve_repo_path
from agentic_tdd_runner.tools import (
    execute_tool,
    reactive_forbidden_feedback,
    reactive_test_feedback,
    tool_applied_status,
    tool_loop_signature,
)


def _execute_tool(name: str, args: dict, tmp_path, **overrides):
    exit_codes: list[int | None] = []
    config = overrides.pop("config", {"timeouts": {"tool_execution": 10, "test_run": 10}})
    cache = overrides.pop("cache", {})
    return execute_tool(
        name,
        args,
        workdir=str(tmp_path),
        config=config,
        file_read_cache=cache,
        resolve_repo_path=lambda path: resolve_repo_path(path, str(tmp_path)),
        validate_command=overrides.pop("validate_command", lambda _command: None),
        set_last_run_exit_code=overrides.pop("set_last_run_exit_code", exit_codes.append),
        detect_quality_tools=overrides.pop("detect_quality_tools", lambda _lang_name: []),
        typecheck_ownership_hint=overrides.pop("typecheck_ownership_hint", lambda *_args: None),
        is_test_file_path=overrides.pop("is_test_file_path", lambda path: path.endswith(".test.ts")),
        test_runner_command_for_file=overrides.pop("test_runner_command_for_file", lambda _path: "bun test"),
    ), exit_codes


def test_read_file_uses_mtime_cache(tmp_path):
    target = tmp_path / "src" / "file.ts"
    target.parent.mkdir()
    target.write_text("const x = 1;\n")
    cache: dict[Path, int] = {}

    first, _ = _execute_tool("read_file", {"path": "src/file.ts"}, tmp_path, cache=cache)
    second, _ = _execute_tool("read_file", {"path": "src/file.ts"}, tmp_path, cache=cache)

    assert first == "const x = 1;\n"
    assert "unchanged since last read" in second.lower()


def test_str_replace_invalidates_read_cache(tmp_path):
    target = tmp_path / "src" / "file.ts"
    target.parent.mkdir()
    target.write_text("const x = 1;\n")
    cache: dict[Path, int] = {}

    _execute_tool("read_file", {"path": "src/file.ts"}, tmp_path, cache=cache)
    result, _ = _execute_tool(
        "str_replace_editor",
        {
            "path": "src/file.ts",
            "old_str": "const x = 1;\n",
            "new_str": "const x = 2;\n",
        },
        tmp_path,
        cache=cache,
    )

    assert result == "OK: replaced in src/file.ts"
    assert cache == {}
    assert target.read_text() == "const x = 2;\n"


def test_run_command_updates_last_exit_code(tmp_path, monkeypatch):
    def fake_run(command, **kwargs):
        assert command == "git status"
        return subprocess.CompletedProcess(command, 7, stdout="", stderr="boom")

    monkeypatch.setattr("agentic_tdd_runner.tools.subprocess.run", fake_run)

    result, exit_codes = _execute_tool("run_command", {"command": "git status"}, tmp_path)

    assert result == "boom"
    assert exit_codes == [None, 7]


def test_reactive_test_feedback_returns_compact_failure(tmp_path, monkeypatch):
    def fake_run(command, **kwargs):
        assert command == ["bun", "test", "src/file.test.ts"]
        return subprocess.CompletedProcess(
            command,
            1,
            stdout="src/file.test.ts:\n10 | expect(true).toBe(false)\n",
            stderr="",
        )

    monkeypatch.setattr("agentic_tdd_runner.tools.subprocess.run", fake_run)

    result = reactive_test_feedback(
        "src/file.test.ts",
        workdir=str(tmp_path),
        config={"timeouts": {"test_run": 10}},
        is_test_file_path=lambda path: path.endswith(".test.ts"),
        test_runner_command_for_file=lambda _path: "bun test",
    )

    assert "[Reactive test]" in result
    assert "expect(true).toBe(false)" in result


def test_reactive_forbidden_feedback_reports_test_quality_issues(tmp_path):
    target = tmp_path / "src" / "file.test.ts"
    target.parent.mkdir()
    repeated_setup = "const client_say_spy = mock(() => undefined as never);"
    target.write_text(
        "\n".join([
            "import { mock } from 'bun:test';",
            repeated_setup,
            repeated_setup,
            "expect(client_say_spy).toHaveBeenCalled();",
        ])
    )

    result = reactive_forbidden_feedback(
        "src/file.test.ts",
        workdir=str(tmp_path),
        config={
            "quality": {
                "enabled": True,
                "typescript": {"forbidden": ["as never"]},
            },
        },
        is_test_file_path=lambda path: path.endswith(".test.ts"),
    )

    assert "[Reactive forbidden]" in result
    assert "2 forbidden patterns" in result
    assert "as never" in result
    assert "Duplicated setup" in result
    assert "client_say_spy" in result
    assert "beforeEach" in result


def test_create_file_includes_reactive_forbidden_feedback(tmp_path, monkeypatch):
    def fake_run(command, **kwargs):
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr("agentic_tdd_runner.tools.subprocess.run", fake_run)
    repeated_setup = "const client_say_spy = mock(() => undefined as never);"

    result, _ = _execute_tool(
        "create_file",
        {
            "path": "src/file.test.ts",
            "content": "\n".join([
                repeated_setup,
                repeated_setup,
                "expect(client_say_spy).toHaveBeenCalled();",
            ]),
        },
        tmp_path,
        config={
            "timeouts": {"tool_execution": 10, "test_run": 10},
            "quality": {
                "enabled": True,
                "typescript": {"forbidden": ["as never"]},
            },
        },
    )

    assert result.startswith("OK: created src/file.test.ts")
    assert "[Reactive forbidden]" in result
    assert "as never" in result


def test_str_replace_editor_includes_reactive_forbidden_feedback(tmp_path, monkeypatch):
    def fake_run(command, **kwargs):
        return subprocess.CompletedProcess(command, 0, stdout="", stderr="")

    monkeypatch.setattr("agentic_tdd_runner.tools.subprocess.run", fake_run)

    target = tmp_path / "src" / "file.test.ts"
    target.parent.mkdir()
    target.write_text("const ok = 1;\n")
    repeated_setup = "const client_say_spy = mock(() => undefined as never);"

    result, _ = _execute_tool(
        "str_replace_editor",
        {
            "path": "src/file.test.ts",
            "old_str": "const ok = 1;\n",
            "new_str": "\n".join([
                repeated_setup,
                repeated_setup,
                "expect(client_say_spy).toHaveBeenCalled();",
            ]),
        },
        tmp_path,
        config={
            "timeouts": {"tool_execution": 10, "test_run": 10},
            "quality": {
                "enabled": True,
                "typescript": {"forbidden": ["as never"]},
            },
        },
    )

    assert result.startswith("OK: replaced in src/file.test.ts")
    assert "[Reactive forbidden]" in result
    assert "as never" in result


def test_tool_status_and_loop_signature_helpers():
    assert tool_applied_status("str_replace_editor", "OK: replaced in src/file.ts") is True
    assert tool_applied_status("str_replace_editor", "ERROR: old_str not found") is False
    assert tool_applied_status("read_file", "contents") is None
    assert tool_loop_signature("read_file", {"path": "src/file.ts"}) == "read_file:src/file.ts"
    assert tool_loop_signature("run_command", {"command": "rg foo src"}) == "run_command:rg foo src"
    assert tool_loop_signature("run_command", {"command": "bun test"}) is None
