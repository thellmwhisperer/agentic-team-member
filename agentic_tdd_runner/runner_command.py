"""Focused test command resolution from runner bootstrap facts."""

from __future__ import annotations

from typing import Any

from agentic_tdd_runner.languages import plugins


DEFAULT_CONFIGURED_TEST_COMMAND = ""


def effective_test_command_template(
    report: Any,
    configured_command: str | None = None,
) -> str:
    """Return the focused-test command prefix for a discovered JS runner.

    The caller appends the test file path. Unknown, custom, or ambiguous runner
    facts deliberately fall back to the configured command instead of guessing.
    """
    fallback = _configured_test_command(configured_command)
    test_runner = _report_value(report, "test_runner")
    if not test_runner or test_runner == "custom":
        return fallback

    for plugin in plugins():
        if not _supports_test_runner(plugin, test_runner):
            continue
        command_fn = getattr(plugin, "effective_test_command_template", None)
        if callable(command_fn):
            command = command_fn(report, configured_command)
            return command or fallback
    return fallback


def runner_version_command(report: Any) -> list[str] | None:
    """Return a presence-check command that proves the detected JS runner is invocable."""
    test_runner = _report_value(report, "test_runner")
    if not test_runner or test_runner == "custom":
        return None

    for plugin in plugins():
        if not _supports_test_runner(plugin, test_runner):
            continue
        version_fn = getattr(plugin, "runner_version_command", None)
        if callable(version_fn):
            return version_fn(report)
    return None


def _configured_test_command(command: str | None) -> str:
    if isinstance(command, str) and command.strip():
        return command.strip()
    return DEFAULT_CONFIGURED_TEST_COMMAND


def _report_value(report: Any, key: str) -> str | None:
    if report is None:
        return None
    value = report.get(key) if isinstance(report, dict) else getattr(report, key, None)
    return value if isinstance(value, str) and value else None


def _supports_test_runner(plugin: object, test_runner: str) -> bool:
    supports_fn = getattr(plugin, "supports_test_runner", None)
    return callable(supports_fn) and bool(supports_fn(test_runner))
