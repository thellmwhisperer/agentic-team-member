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
_BUN_MOCK_RESET_RE = re.compile(
    r"(?P<file>[^\s:(]+\.test\.[tj]sx?)"
    r"(?:\(\d+,\d+\)|:\d+:\d+)?(?:\s*-\s*|:\s*)error TS\d+:\s*"
    r"Property ['\"]reset['\"] does not exist on type ['\"]?MockFunctionState",
)
_TEST_FILE_RE = re.compile(r"(?P<file>[^\s:]+\.test\.[tj]sx?)[:\s]")


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
        self.pending_mock_api_file: str | None = None
        self.pending_import_side_effect_file: str | None = None

    def review_tool_call(self, name: str, args: dict) -> RouterDecision | None:
        if self.pending_import_side_effect_file:
            if _is_edit_to_path(name, args, self.pending_import_side_effect_file):
                return None
            if _is_framework_lookup_or_premature_run(name, args) or _is_source_or_test_lookup(
                name, args
            ):
                import_path = _import_path_from_test_to_source(
                    self.pending_import_side_effect_file,
                    self.runner_facts.source_file if self.runner_facts else None,
                )
                source_file = self.runner_facts.source_file if self.runner_facts else None
                required_next = (
                    f"Edit `{self.pending_import_side_effect_file}`: replace the static import of "
                    f"the target module with a typed subject variable, register mocks first, then "
                    f"load the module with `await import('{import_path}')` inside `beforeEach`."
                )
                message = (
                    "IMPORT-TIME SIDE EFFECT ANSWER\n"
                    "intent: fix_import_time_side_effect\n"
                    "reason: the focused test failed while Bun was evaluating the target module, "
                    "before the test body could run. This is an import-order problem in the test, "
                    "not a reason to inspect provider/env/singleton modules.\n"
                    f"answer: mock dependencies with `mock.module(...)` before loading `{import_path}`; "
                    f"then use `let subject: typeof import('{import_path}');` and "
                    f"`subject = await import('{import_path}')` in `beforeEach`.\n"
                    "guardrail: Do not inspect provider/env/singleton modules; keep the fix "
                    "in the test harness.\n"
                    f"required_next: {required_next}"
                )
                return RouterDecision(
                    event="intent_router_answered",
                    message=message,
                    data={
                        "intent": "fix_import_time_side_effect",
                        "pending_test_file": self.pending_import_side_effect_file,
                        "source_file": source_file,
                        "import_path": import_path,
                        "required_next": required_next,
                        "tool": name,
                        "args": args,
                    },
                )

        if self.pending_mock_api_file:
            if _is_edit_to_path(name, args, self.pending_mock_api_file):
                return None
            if _is_framework_lookup_or_premature_run(name, args) or _is_source_or_test_lookup(name, args):
                required_next = (
                    f"Edit `{self.pending_mock_api_file}` and replace `.mock.reset()` "
                    "with `.mockClear()`, then rerun the focused test."
                )
                message = (
                    "RUNNER FACT ANSWER\n"
                    "intent: fix_bun_mock_api\n"
                    "reason: Bun mock call history is reset on the mock function, not through `.mock.reset()`.\n"
                    "answer: use `mockFn.mockClear()`.\n"
                    f"required_next: {required_next}"
                )
                return RouterDecision(
                    event="intent_router_answered",
                    message=message,
                    data={
                        "intent": "fix_bun_mock_api",
                        "pending_test_file": self.pending_mock_api_file,
                        "required_next": required_next,
                        "tool": name,
                        "args": args,
                    },
                )

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
            "note: if that import is already present, keep the next fix in the same test file "
            "and correct the local import/type issue before rerunning.\n"
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
        if applied is True and _is_edit_to_path(name, args, self.pending_mock_api_file):
            self.pending_mock_api_file = None
        if applied is True and _is_edit_to_path(
            name, args, self.pending_import_side_effect_file
        ):
            self.pending_import_side_effect_file = None

        test_file, missing = _parse_missing_test_globals(result)
        if test_file and missing:
            self.pending_test_file = test_file
            self.pending_missing_globals = missing

        mock_api_file = _parse_bun_mock_reset_file(result)
        if mock_api_file:
            self.pending_mock_api_file = mock_api_file

        import_side_effect_file = _parse_import_time_side_effect_file(
            name, args, result, self.runner_facts
        )
        if import_side_effect_file:
            self.pending_import_side_effect_file = import_side_effect_file


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


def _parse_bun_mock_reset_file(result: str) -> str | None:
    match = _BUN_MOCK_RESET_RE.search(result or "")
    return _normalize_path(match.group("file")) if match else None


def _parse_import_time_side_effect_file(
    name: str,
    args: dict,
    result: str,
    runner_facts: RunnerFacts | None,
) -> str | None:
    output = result or ""
    source_file = runner_facts.source_file if runner_facts else None
    if not source_file or source_file not in output:
        return None
    if "loadAndEvaluateModule" not in output:
        return None
    if "Unhandled error between tests" not in output and "[Reactive test]" not in output:
        return None
    return (
        _test_file_from_tool_result(name, args, output)
        or runner_facts.recommended_test_file
    )


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
    if any(
        PurePosixPath(part).name in {"package.json", "bunfig.toml"}
        for part in parts[1:]
    ):
        return True
    if any(PurePosixPath(part).name.startswith("tsconfig") for part in parts[1:]):
        return True
    return False


def _is_source_or_test_lookup(name: str, args: dict) -> bool:
    if name in {"read_file", "rg"}:
        return True
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
    return PurePosixPath(parts[0]).name in {
        "grep",
        "rg",
        "cat",
        "head",
        "tail",
        "sed",
        "awk",
    }


def _test_file_from_tool_result(name: str, args: dict, result: str) -> str | None:
    if name == "run_command":
        command = args.get("command", "")
        if isinstance(command, str):
            try:
                parts = shlex.split(command)
            except ValueError:
                parts = command.split()
            for part in parts:
                path = _normalize_path(part)
                if _is_test_file_path(path):
                    return path
    if name in {"create_file", "str_replace_editor"}:
        path = _normalize_path(args.get("path"))
        if _is_test_file_path(path):
            return path
    for match in _TEST_FILE_RE.finditer(result or ""):
        path = _normalize_path(match.group("file"))
        if _is_test_file_path(path):
            return path
    return None


def _is_test_file_path(path: str | None) -> bool:
    return bool(path and re.search(r"\.test\.[tj]sx?$", path))


def _import_path_from_test_to_source(
    test_file: str | None, source_file: str | None
) -> str:
    if not test_file or not source_file:
        return "./target"
    test_dir = PurePosixPath(_normalize_path(test_file)).parent.as_posix()
    source_without_ext = _strip_source_extension(_normalize_path(source_file))
    relative = posixpath.relpath(source_without_ext, test_dir or ".")
    if not relative.startswith("."):
        relative = f"./{relative}"
    return relative


def _strip_source_extension(path: str) -> str:
    for suffix in (".tsx", ".ts", ".jsx", ".js", ".mjs", ".cjs"):
        if path.endswith(suffix):
            return path[: -len(suffix)]
    return path


def _normalize_path(path: str | None) -> str:
    raw = str(path or "").strip()
    if not raw:
        return ""
    normalized = posixpath.normpath(PurePosixPath(raw).as_posix())
    if normalized == ".":
        return ""
    return normalized.removeprefix("./")
