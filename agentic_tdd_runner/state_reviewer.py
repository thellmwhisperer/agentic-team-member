"""Deterministic review of bug-fix loop state before tool execution."""

from __future__ import annotations

import posixpath
import re
import shlex
from dataclasses import dataclass
from pathlib import PurePosixPath

from agentic_tdd_runner.intent_router import IntentRouter
from agentic_tdd_runner.runner_facts import RunnerFacts


@dataclass(frozen=True)
class StateReview:
    """A deterministic decision that blocks an invalid next step."""

    event: str
    message: str
    data: dict


class BugStateReviewer:
    """Review model tool intent against the current bug-fix state.

    This is deliberately small: it does not try to replace the runner. It catches
    high-cost invalid moves and converts them into scoped feedback while keeping
    the OpenAI tool-call transcript valid.
    """

    def __init__(
        self,
        config: dict | None,
        *,
        contract_evidence_available: bool,
        runner_facts: RunnerFacts | None = None,
    ) -> None:
        cfg = (config or {}).get("state_reviewer", {})
        self.enabled = bool(cfg.get("enabled", False))
        self.contract_evidence_available = contract_evidence_available
        self.max_dependency_contract_lookups = int(
            cfg.get("max_dependency_contract_lookups", 1) or 0
        )
        self.dependency_contract_lookup_count = 0
        self.pending_forbidden_file: str | None = None
        self.pending_forbidden_patterns: list[str] = []
        self.intent_router = IntentRouter(runner_facts)

    def review_tool_call(
        self,
        name: str,
        args: dict,
        *,
        allow_dependency_contract_lookup: bool,
    ) -> StateReview | None:
        """Return a blocking review when a tool call violates state invariants."""
        intent_decision = self.intent_router.review_tool_call(name, args)
        if intent_decision:
            return StateReview(
                event=intent_decision.event,
                message=intent_decision.message,
                data=intent_decision.data,
            )

        if not self.enabled:
            return None

        if (
            self.pending_forbidden_file
            and is_exploratory_tool(name, args)
            and not is_pending_forbidden_file_lookup(name, args, self.pending_forbidden_file)
        ):
            return self._blocked(
                phase="QUALITY_REPAIR",
                reason=(
                    "A reactive forbidden-pattern finding is still open. "
                    f"The next valid move is an edit to `{self.pending_forbidden_file}`, "
                    "or a focused read of that same file for edit context."
                ),
                required_next=(
                    f"Read `{self.pending_forbidden_file}` only if you need exact old_str "
                    "context; otherwise use `str_replace_editor` on that file to remove "
                    "the forbidden pattern, then rerun the focused test or say DONE."
                ),
                data={
                    "tool": name,
                    "args": args,
                    "pending_file": self.pending_forbidden_file,
                    "patterns": list(self.pending_forbidden_patterns),
                },
            )

        if is_dependency_contract_lookup(name, args):
            if self.contract_evidence_available and not allow_dependency_contract_lookup:
                return self._blocked(
                    phase="CONTRACT",
                    reason=(
                        "The state already has concrete contract evidence from the "
                        "issue/cookbook. Dependency spelunking would reopen a frozen "
                        "fact instead of advancing the bug fix."
                    ),
                    required_next=(
                        "Use the known contract to write the regression test or patch. "
                        "Only inspect dependency types after reactive compiler/test "
                        "feedback contradicts the known contract."
                    ),
                    data={"tool": name, "args": args},
                )
            if (
                allow_dependency_contract_lookup
                and self.max_dependency_contract_lookups >= 0
                and self.dependency_contract_lookup_count >= self.max_dependency_contract_lookups
            ):
                return self._blocked(
                    phase="CONTRACT_REPAIR",
                    reason=(
                        "The dependency-contract lookup budget for this repair phase "
                        "has already been spent."
                    ),
                    required_next=(
                        "Use the compiler/test output already observed to make the "
                        "smallest edit. Do not continue searching dependency files."
                    ),
                    data={
                        "tool": name,
                        "args": args,
                        "lookup_count": self.dependency_contract_lookup_count,
                        "lookup_budget": self.max_dependency_contract_lookups,
                    },
                )

        return None

    def observe_tool_result(
        self,
        name: str,
        args: dict,
        result: str,
        *,
        applied: bool | None,
    ) -> None:
        """Update reviewer state after an executed tool call."""
        self.intent_router.observe_tool_result(name, args, result, applied=applied)

        if not self.enabled:
            return

        if is_dependency_contract_lookup(name, args):
            self.dependency_contract_lookup_count += 1

        forbidden_file, patterns = parse_reactive_forbidden(result)
        if forbidden_file:
            self.pending_forbidden_file = forbidden_file
            self.pending_forbidden_patterns = patterns
            return

        edited_path = (
            str(args.get("path") or "")
            if name in {"create_file", "str_replace_editor"}
            else ""
        )
        if (
            applied is True
            and normalize_review_path(edited_path)
            and normalize_review_path(edited_path) == normalize_review_path(self.pending_forbidden_file)
        ):
            self.pending_forbidden_file = None
            self.pending_forbidden_patterns = []

    def _blocked(
        self,
        *,
        phase: str,
        reason: str,
        required_next: str,
        data: dict,
    ) -> StateReview:
        message = (
            "STATE REVIEW BLOCKED\n"
            f"phase: {phase}\n"
            f"reason: {reason}\n"
            f"required_next: {required_next}"
        )
        return StateReview(
            event="state_review_blocked",
            message=message,
            data={"phase": phase, "reason": reason, "required_next": required_next, **data},
        )


def is_exploratory_tool(name: str, args: dict) -> bool:
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
        "grep", "rg", "find", "ls", "cat", "head", "tail", "sed", "awk",
        "wc", "sort", "uniq", "cut", "tr", "dirname", "basename", "tree",
        "file", "which", "test",
    }


def normalize_review_path(path: str | None) -> str:
    raw = str(path or "").strip()
    if not raw:
        return ""
    normalized = posixpath.normpath(PurePosixPath(raw).as_posix())
    if normalized == ".":
        return ""
    return normalized.removeprefix("./")


def is_pending_forbidden_file_lookup(name: str, args: dict, pending_file: str) -> bool:
    """Allow focused context reads for the file that must be repaired."""
    pending_norm = normalize_review_path(pending_file)
    if not pending_norm:
        return False

    if name == "read_file":
        return normalize_review_path(args.get("path")) == pending_norm

    if name == "rg":
        paths = args.get("path") or "."
        if isinstance(paths, str):
            paths = [paths]
        return any(normalize_review_path(str(path)) == pending_norm for path in paths)

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
    focused_tools = {"grep", "rg", "cat", "head", "tail", "sed", "awk"}
    if PurePosixPath(parts[0]).name not in focused_tools:
        return False
    return any(normalize_review_path(part) == pending_norm for part in parts[1:])


def is_dependency_contract_lookup(name: str, args: dict) -> bool:
    if name == "rg":
        paths = args.get("path") or "."
        if isinstance(paths, str):
            paths = [paths]
        return any("node_modules" in PurePosixPath(str(path)).parts for path in paths)

    if name != "run_command":
        return False
    command = args.get("command", "")
    if not isinstance(command, str) or "node_modules" not in command:
        return False
    try:
        parts = shlex.split(command)
    except ValueError:
        parts = command.split()
    if not parts:
        return False
    lookup_tools = {"rg", "grep", "find", "cat", "head", "tail", "sed", "awk", "ls"}
    return PurePosixPath(parts[0]).name in lookup_tools or "node_modules/@types" in command


def parse_reactive_forbidden(result: str) -> tuple[str | None, list[str]]:
    """Extract the first forbidden file + pattern list from reactive feedback."""
    if "[Reactive forbidden]" not in result and "[Forbidden]" not in result:
        return None, []

    hits = re.findall(r"^\s+([^:\n]+):\d+\s+'([^']+)'", result, flags=re.MULTILINE)
    if not hits:
        return None, []
    file_path = hits[0][0]
    patterns = [pattern for path, pattern in hits if path == file_path]
    return file_path, patterns
