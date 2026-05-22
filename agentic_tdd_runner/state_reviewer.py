"""Deterministic review of bug-fix loop state before tool execution."""

from __future__ import annotations

import re
import shlex
from dataclasses import dataclass
from pathlib import PurePosixPath


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

    def __init__(self, config: dict | None, *, contract_evidence_available: bool) -> None:
        cfg = (config or {}).get("state_reviewer", {})
        self.enabled = bool(cfg.get("enabled", False))
        self.contract_evidence_available = contract_evidence_available
        self.max_dependency_contract_lookups = int(
            cfg.get("max_dependency_contract_lookups", 1) or 0
        )
        self.dependency_contract_lookup_count = 0
        self.pending_forbidden_file: str | None = None
        self.pending_forbidden_patterns: list[str] = []

    def review_tool_call(
        self,
        name: str,
        args: dict,
        *,
        allow_dependency_contract_lookup: bool,
    ) -> StateReview | None:
        """Return a blocking review when a tool call violates state invariants."""
        if not self.enabled:
            return None

        if self.pending_forbidden_file and is_exploratory_tool(name, args):
            return self._blocked(
                phase="QUALITY_REPAIR",
                reason=(
                    "A reactive forbidden-pattern finding is still open. "
                    f"The next valid move is an edit to `{self.pending_forbidden_file}`, "
                    "not more exploration."
                ),
                required_next=(
                    f"Use `str_replace_editor` on `{self.pending_forbidden_file}` "
                    "to remove the forbidden pattern, then rerun the focused test or say DONE."
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

    def observe_tool_result(self, name: str, args: dict, result: str, *, applied: bool | None) -> None:
        """Update reviewer state after an executed tool call."""
        if not self.enabled:
            return

        if is_dependency_contract_lookup(name, args):
            self.dependency_contract_lookup_count += 1

        forbidden_file, patterns = parse_reactive_forbidden(result)
        if forbidden_file:
            self.pending_forbidden_file = forbidden_file
            self.pending_forbidden_patterns = patterns
            return

        edited_path = str(args.get("path") or "") if name in {"create_file", "str_replace_editor"} else ""
        if applied is True and edited_path and edited_path == self.pending_forbidden_file:
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
