"""Shared helpers for deriving focused test command templates."""

from __future__ import annotations

import shlex
from typing import Any

TEST_FILE_SUFFIXES = (
    ".js",
    ".jsx",
    ".ts",
    ".tsx",
    ".mjs",
    ".cjs",
    ".mts",
    ".cts",
    ".py",
)


def custom_test_command_template(report: Any) -> str:
    """Return a focused command prefix for custom package test scripts."""
    test_command = _report_value(report, "test_command")
    if not test_command:
        return ""

    derived = strip_test_file_args(test_command)
    if derived and derived != test_command:
        return derived

    package_script = package_test_script_command(_report_value(report, "package_manager"))
    return package_script or test_command


def strip_test_file_args(command: str) -> str:
    try:
        parts = shlex.split(command)
    except ValueError:
        return command
    if not parts:
        return ""

    kept = [part for part in parts if not looks_like_test_path_arg(part)]
    return shlex.join(kept) if kept else command


def looks_like_test_path_arg(part: str) -> bool:
    if part.startswith("-") or "=" in part:
        return False
    looks_like_test_name = (
        ".test." in part
        or ".spec." in part
        or "test_" in part
        or "_test." in part
    )
    return looks_like_test_name and (
        any(ch in part for ch in ("/", "\\", "*"))
        or part.endswith(TEST_FILE_SUFFIXES)
    )


def package_test_script_command(package_manager: str | None) -> str:
    if package_manager == "npm":
        return "npm test --"
    if package_manager == "pnpm":
        return "pnpm test"
    if package_manager == "yarn":
        return "yarn test"
    if package_manager == "bun":
        return "bun test"
    return ""


def _report_value(report: Any, key: str) -> str | None:
    if report is None:
        return None
    value = report.get(key) if isinstance(report, dict) else getattr(report, key, None)
    return value if isinstance(value, str) and value else None
