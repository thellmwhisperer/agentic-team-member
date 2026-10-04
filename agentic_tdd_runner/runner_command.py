"""Focused test command resolution from runner bootstrap facts."""

from __future__ import annotations

import shlex
from typing import Any

from agentic_tdd_runner.test_command_templates import custom_test_command_template


DEFAULT_CONFIGURED_TEST_COMMAND = ""


def effective_test_command_template(
    report: Any,
    configured_command: str | None = None,
) -> str:
    """Return the focused-test command prefix for a discovered runner.

    The caller appends the test file path. Unknown, custom, or ambiguous runner
    facts deliberately fall back to the configured command instead of guessing.
    """
    if _report_value(report, "test_runner") == "pytest":
        return "python3 -m pytest"
    return js_test_command_template(report, configured_command)


def js_test_command_template(report: Any, configured_command: str | None = None) -> str:
    fallback = _configured_test_command(configured_command)
    test_runner = _report_value(report, "test_runner")
    if not test_runner:
        return fallback
    if test_runner == "custom":
        return custom_test_command_template(report) or fallback
    if test_runner == "bun:test":
        return "bun test"
    if test_runner == "node:test":
        return "node --test"
    if test_runner == "vitest":
        return shlex.join(_package_runner_argv(_report_value(report, "package_manager"), "vitest", ["run"]))
    if test_runner == "jest":
        return shlex.join(
            _package_runner_argv(_report_value(report, "package_manager"), "jest", _jest_runner_args(report))
        )
    return fallback


def runner_version_command(report: Any) -> list[str] | None:
    """Return a presence-check command that proves the detected runner is invocable."""
    test_runner = _report_value(report, "test_runner")
    if test_runner == "pytest":
        return ["python3", "-m", "pytest", "--version"]
    if test_runner == "bun:test":
        return ["bun", "--version"]
    if test_runner == "node:test":
        return ["node", "--version"]
    if test_runner in ("vitest", "jest"):
        return _package_runner_argv(_report_value(report, "package_manager"), test_runner, ["--version"])
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


def _jest_runner_args(report: Any) -> list[str]:
    args = ["--runInBand", "--watchman=false", "--coverage=false"]
    test_config_path = _report_value(report, "test_config_path")
    if test_config_path:
        args.extend(["--config", test_config_path])
    return args


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
