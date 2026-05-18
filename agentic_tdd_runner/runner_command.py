"""Focused test command resolution from runner bootstrap facts."""

from __future__ import annotations

import shlex
from typing import Any


DEFAULT_CONFIGURED_TEST_COMMAND = "bun test"


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

    if test_runner == "bun:test":
        return "bun test"
    if test_runner == "node:test":
        return "node --test"
    if test_runner == "vitest":
        return _package_runner_command(
            _report_value(report, "package_manager"),
            "vitest",
            ["run"],
        )
    if test_runner == "jest":
        return _package_runner_command(
            _report_value(report, "package_manager"),
            "jest",
            [],
        )
    return fallback


def runner_version_command(report: Any) -> list[str] | None:
    """Return a dry-run command that proves the detected JS runner is invocable."""
    test_runner = _report_value(report, "test_runner")
    if not test_runner or test_runner == "custom":
        return None

    if test_runner == "bun:test":
        return ["bun", "test", "--version"]
    if test_runner == "node:test":
        return ["node", "--version"]
    if test_runner == "vitest":
        return _package_runner_argv(
            _report_value(report, "package_manager"),
            "vitest",
            ["--version"],
        )
    if test_runner == "jest":
        return _package_runner_argv(
            _report_value(report, "package_manager"),
            "jest",
            ["--version"],
        )
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


def _package_runner_command(
    package_manager: str | None,
    runner: str,
    runner_args: list[str],
) -> str:
    return shlex.join(_package_runner_argv(package_manager, runner, runner_args))


def _package_runner_argv(
    package_manager: str | None,
    runner: str,
    runner_args: list[str],
) -> list[str]:
    if package_manager == "pnpm":
        return ["pnpm", "exec", runner, *runner_args]
    if package_manager == "yarn":
        return ["yarn", runner, *runner_args]
    if package_manager == "bun":
        return ["bun", "x", runner, *runner_args]
    return ["npm", "exec", "--", runner, *runner_args]
