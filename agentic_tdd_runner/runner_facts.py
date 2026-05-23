"""Deterministic facts about the target project's test runner."""

from __future__ import annotations

import fnmatch
import json
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Any


BUN_TEST_API_IMPORT = 'import { beforeEach, describe, expect, mock, test } from "bun:test";'


@dataclass(frozen=True)
class RunnerFacts:
    """Small fact report the harness can answer without spending model tokens."""

    test_runner: str
    test_command: str
    typecheck_command: str | None
    test_api_import: str | None
    recommended_test_file: str | None
    source_file: str | None
    target_symbol: str | None
    nearby_tests: list[str]
    symbol_tests: list[str]
    source_line_range: dict | None = None
    test_api_facts: list[str] | None = None

    def to_prompt_section(self) -> str:
        source_dir = (
            PurePosixPath(self.source_file).parent.as_posix()
            if self.source_file
            else "."
        )
        lines = [
            "## Runner Facts",
            (
                "These facts were computed by the harness before the model ran. "
                "Use them before inspecting project config."
            ),
            f"- test runner: {self.test_runner}",
            f"- test command: {self.test_command}",
        ]
        if self.typecheck_command:
            lines.append(f"- typecheck command: {self.typecheck_command}")
        if self.test_api_import:
            lines.append(f"- test API import: `{self.test_api_import}`")
        if self.recommended_test_file:
            lines.append(f"- recommended regression test file: {self.recommended_test_file}")
        if self.source_line_range:
            start = self.source_line_range.get("start")
            end = self.source_line_range.get("end")
            source = self.source_line_range.get("source")
            if start and end and source == "definition":
                lines.append(f"- target source range: {self.source_file}:{start}-{end}")
        for fact in self.test_api_facts or []:
            lines.append(f"- test API fact: {fact}")
        nearby = ", ".join(self.nearby_tests) if self.nearby_tests else "none"
        lines.append(f"- nearby tests in {source_dir}: {nearby}")
        symbol = ", ".join(self.symbol_tests) if self.symbol_tests else "none"
        if self.target_symbol:
            lines.append(f"- existing tests mentioning {self.target_symbol}: {symbol}")
        return "\n".join(lines)

    def to_log_dict(self) -> dict:
        return {
            "test_runner": self.test_runner,
            "test_command": self.test_command,
            "typecheck_command": self.typecheck_command,
            "test_api_import": self.test_api_import,
            "recommended_test_file": self.recommended_test_file,
            "source_file": self.source_file,
            "target_symbol": self.target_symbol,
            "nearby_tests": list(self.nearby_tests),
            "symbol_tests": list(self.symbol_tests),
            "source_line_range": dict(self.source_line_range) if self.source_line_range else None,
            "test_api_facts": list(self.test_api_facts or []),
        }


def build_runner_facts(
    project_root: str,
    config: dict,
    *,
    episode: dict | None = None,
) -> RunnerFacts:
    """Build a reusable deterministic fact report for the current run."""
    root = Path(project_root)
    runner_cfg = config.get("runner", {}) if isinstance(config, dict) else {}
    test_runner = _episode_value(episode, "runner") or str(
        runner_cfg.get("framework") or "unknown"
    )
    test_command = str(runner_cfg.get("command") or "bun test")
    source_file = _episode_value(episode, "source_file")
    target_symbol = _episode_value(episode, "target_symbol")
    recommended_test_file = _episode_value(episode, "test_file")
    patterns = runner_cfg.get("test_file_patterns") or [
        "*.test.ts",
        "*.test.tsx",
        "*.test.js",
        "*.test.jsx",
        "test_*.py",
    ]
    default_exclude_dirs = {"node_modules", ".git", ".next", ".turbo", "build", "coverage", "dist"}
    exclude_dirs = default_exclude_dirs | set(runner_cfg.get("exclude_dirs") or [])

    test_files = _discover_test_files(root, patterns, exclude_dirs)
    nearby_tests = _nearby_tests(test_files, source_file)
    symbol_tests = _symbol_tests(root, test_files, target_symbol)

    return RunnerFacts(
        test_runner=test_runner,
        test_command=test_command,
        typecheck_command=_typecheck_command(root, config, test_runner),
        test_api_import=_test_api_import(test_runner),
        recommended_test_file=recommended_test_file,
        source_file=source_file,
        target_symbol=target_symbol,
        nearby_tests=nearby_tests,
        symbol_tests=symbol_tests,
        source_line_range=_episode_line_range(episode),
        test_api_facts=_test_api_facts(test_runner),
    )


def _episode_value(episode: dict | None, key: str) -> str | None:
    if not episode:
        return None
    value = episode.get(key)
    return value if isinstance(value, str) and value else None


def _episode_line_range(episode: dict | None) -> dict | None:
    if not episode:
        return None
    value = episode.get("function_line_range")
    if not isinstance(value, dict):
        return None
    start = value.get("start")
    end = value.get("end")
    source = value.get("source")
    if not isinstance(start, int) or not isinstance(end, int):
        return None
    return {"start": start, "end": end, "source": source}


def _test_api_facts(test_runner: str) -> list[str]:
    if test_runner != "bun:test":
        return []
    return [
        "Bun mock functions reset call history with `mockFn.mockClear()`; do not use `.mock.reset()`.",
        "When a module has import-time side effects, make `mock.module(...)` registrations happen before the target module is evaluated.",
    ]


def _discover_test_files(root: Path, patterns: list[str], exclude_dirs: set[str]) -> list[str]:
    files: list[str] = []
    if not root.exists():
        return files
    for path in root.rglob("*"):
        if not path.is_file():
            continue
        try:
            rel = path.relative_to(root)
        except ValueError:
            continue
        if any(part in exclude_dirs for part in rel.parts):
            continue
        rel_posix = rel.as_posix()
        if any(
            fnmatch.fnmatch(rel_posix, pattern) or fnmatch.fnmatch(path.name, pattern)
            for pattern in patterns
        ):
            files.append(rel_posix)
    return sorted(files)


def _nearby_tests(test_files: list[str], source_file: str | None) -> list[str]:
    if not source_file:
        return []
    source_dir = PurePosixPath(source_file).parent
    return [
        test_file
        for test_file in test_files
        if PurePosixPath(test_file).parent == source_dir
    ]


def _symbol_tests(root: Path, test_files: list[str], target_symbol: str | None) -> list[str]:
    if not target_symbol:
        return []
    matches: list[str] = []
    for test_file in test_files:
        rel = PurePosixPath(test_file)
        if target_symbol in rel.name:
            matches.append(test_file)
            continue
        try:
            text = (root / test_file).read_text(errors="ignore")
        except OSError:
            continue
        if target_symbol in text:
            matches.append(test_file)
    return sorted(dict.fromkeys(matches))


def _typecheck_command(root: Path, config: dict, test_runner: str) -> str | None:
    environment = config.get("environment", {}) if isinstance(config, dict) else {}
    if environment.get("run_typecheck") is False:
        return None
    scripts = _package_scripts(root)
    if "typecheck" not in scripts:
        return None
    if test_runner == "bun:test" or _uses_bun(root):
        return "bun run typecheck"
    if (root / "pnpm-lock.yaml").exists():
        return "pnpm typecheck"
    if (root / "yarn.lock").exists():
        return "yarn typecheck"
    return "npm run typecheck"


def _package_scripts(root: Path) -> dict[str, Any]:
    package_json = root / "package.json"
    try:
        data = json.loads(package_json.read_text())
    except (OSError, json.JSONDecodeError):
        return {}
    scripts = data.get("scripts")
    return scripts if isinstance(scripts, dict) else {}


def _uses_bun(root: Path) -> bool:
    package_json = root / "package.json"
    try:
        data = json.loads(package_json.read_text())
    except (OSError, json.JSONDecodeError):
        data = {}
    package_manager = data.get("packageManager") if isinstance(data, dict) else None
    return (
        (isinstance(package_manager, str) and package_manager.startswith("bun@"))
        or (root / "bun.lock").exists()
        or (root / "bun.lockb").exists()
    )


def _test_api_import(test_runner: str) -> str | None:
    if test_runner == "bun:test":
        return BUN_TEST_API_IMPORT
    return None
