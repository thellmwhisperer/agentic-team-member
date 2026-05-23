"""Deterministic intent routing before expensive or noisy tool calls."""

from __future__ import annotations

import posixpath
import re
import shlex
from dataclasses import dataclass
from pathlib import PurePosixPath

from agentic_tdd_runner.runner_facts import RunnerFacts


_BUN_TEST_GLOBALS = {"beforeEach", "describe", "expect", "mock", "test"}
_MISSING_NAME_RE = re.compile(
    r"(?P<file>[^\s:(]+\.(?:test|spec)\.[tj]sx?)"
    r"(?:\(\d+,\d+\)|:\d+:\d+)?(?:\s*-\s*|:\s*)error TS\d+:\s*"
    r"Cannot find name ['\"](?P<name>[A-Za-z_]\w*)['\"]"
)


@dataclass(frozen=True)
class RouterDecision:
    """A deterministic answer to a model intent."""

    event: str
    message: str
    data: dict


class IntentRouter:
    """Answer known runner/setup questions before the model burns tool calls."""

    def __init__(self, runner_facts: RunnerFacts | None) -> None:
        self.runner_facts = runner_facts
        self.pending_test_file: str | None = None
        self.pending_missing_globals: set[str] = set()

    def review_tool_call(self, name: str, args: dict) -> RouterDecision | None:
        if not (self.runner_facts and self.pending_test_file and self.pending_missing_globals):
            return None
        if _is_edit_to_path(name, args, self.pending_test_file):
            return None
        if not self.runner_facts.test_api_import:
            return None
        if not _is_framework_lookup_or_premature_run(name, args):
            return None
        missing = ", ".join(sorted(self.pending_missing_globals))
        required_next = (
            f"Add `{self.runner_facts.test_api_import}` to "
            f"`{self.pending_test_file}`, then rerun the focused test."
        )
        message = (
            "RUNNER FACT ANSWER\n"
            "intent: inspect_test_framework\n"
            f"reason: `{missing}` are test API globals provided by "
            f"{self.runner_facts.test_runner}; this is a missing import, not a project-config gap.\n"
            f"answer: {self.runner_facts.test_api_import}\n"
            f"required_next: {required_next}"
        )
        return RouterDecision(
            event="intent_router_answered",
            message=message,
            data={
                "intent": "inspect_test_framework",
                "pending_test_file": self.pending_test_file,
                "missing_globals": sorted(self.pending_missing_globals),
                "required_next": required_next,
                "tool": name,
                "args": args,
            },
        )

    def observe_tool_result(
        self,
        name: str,
        args: dict,
        result: str,
        *,
        applied: bool | None,
    ) -> None:
        if applied is True and _is_edit_to_path(name, args, self.pending_test_file):
            self.pending_test_file = None
            self.pending_missing_globals.clear()
            return

        test_file, missing = _parse_missing_test_globals(result)
        if test_file and missing:
            self.pending_test_file = test_file
            self.pending_missing_globals = missing


def _parse_missing_test_globals(result: str) -> tuple[str | None, set[str]]:
    hits: dict[str, set[str]] = {}
    for match in _MISSING_NAME_RE.finditer(result or ""):
        missing = match.group("name")
        if missing not in _BUN_TEST_GLOBALS:
            continue
        path = _normalize_path(match.group("file"))
        hits.setdefault(path, set()).add(missing)
    if not hits:
        return None, set()
    test_file = sorted(hits, key=lambda path: (-len(hits[path]), path))[0]
    return test_file, hits[test_file]


def _is_edit_to_path(name: str, args: dict, target_path: str | None) -> bool:
    if not target_path or name not in {"create_file", "str_replace_editor"}:
        return False
    return _normalize_path(args.get("path")) == _normalize_path(target_path)


def _is_framework_lookup_or_premature_run(name: str, args: dict) -> bool:
    if name == "read_file":
        path = _normalize_path(args.get("path"))
        if not path:
            return False
        filename = PurePosixPath(path).name
        return (
            "node_modules" in PurePosixPath(path).parts
            or filename in {"package.json", "bunfig.toml"}
            or filename.startswith("tsconfig")
            or filename.startswith(("vitest.config", "jest.config"))
        )

    if name == "rg":
        paths = args.get("path") or "."
        if isinstance(paths, str):
            paths = [paths]
        return any("node_modules" in PurePosixPath(str(path)).parts for path in paths)

    if name != "run_command":
        return False
    command = args.get("command", "")
    if not isinstance(command, str):
        return False
    try:
        parts = shlex.split(command)
    except ValueError:
        parts = command.split()
    if not parts:
        return False
    executable = PurePosixPath(parts[0]).name
    if executable in {"bun", "npm", "pnpm", "yarn"}:
        return True
    if any("node_modules" in PurePosixPath(part).parts for part in parts[1:]):
        return True
    if any(PurePosixPath(part).name in {"package.json", "bunfig.toml"} for part in parts[1:]):
        return True
    if any(PurePosixPath(part).name.startswith("tsconfig") for part in parts[1:]):
        return True
    return False


def _normalize_path(path: str | None) -> str:
    raw = str(path or "").strip()
    if not raw:
        return ""
    normalized = posixpath.normpath(PurePosixPath(raw).as_posix())
    if normalized == ".":
        return ""
    return normalized.removeprefix("./")
