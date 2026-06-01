"""Apply Codex-style file patches inside a repository workdir."""

from __future__ import annotations

import posixpath
import re
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path


class ApplyPatchError(ValueError):
    """Raised when an apply_patch payload cannot be parsed or applied."""


@dataclass(frozen=True)
class HunkLine:
    prefix: str
    text: str


@dataclass(frozen=True)
class PatchHunk:
    headers: tuple[str, ...]
    lines: tuple[HunkLine, ...]


@dataclass(frozen=True)
class PatchOperation:
    kind: str
    path: str
    lines: tuple[str, ...] = ()
    hunks: tuple[PatchHunk, ...] = ()
    move_to: str | None = None


@dataclass(frozen=True)
class ApplyPatchOutcome:
    changed_paths: tuple[str, ...]
    deleted_paths: tuple[str, ...]
    summary: tuple[str, ...]


_FILE_HEADERS = (
    "*** Add File: ",
    "*** Delete File: ",
    "*** Update File: ",
)
_UNIFIED_HUNK_HEADER_RE = re.compile(
    r"^(?:@@\s*)?-\d+(?:,\d+)?\s+\+\d+(?:,\d+)?(?:\s+@@(?:\s+.*)?|\s+.*)?$"
)


def apply_patch_touched_paths(patch_text: str) -> tuple[str, ...]:
    """Return normalized repo-relative paths mentioned by a patch."""
    paths: list[str] = []
    for op in parse_apply_patch(patch_text):
        paths.append(op.path)
        if op.move_to:
            paths.append(op.move_to)
    return tuple(dict.fromkeys(paths))


def apply_patch_to_workdir(
    patch_text: str,
    *,
    workdir: str,
    resolve_repo_path: Callable[[str], Path],
) -> ApplyPatchOutcome:
    """Apply a patch atomically at file granularity."""
    operations = parse_apply_patch(patch_text)
    if not operations:
        raise ApplyPatchError("patch must include at least one file operation")

    planned: dict[str, tuple[list[str], bool] | None] = {}
    summaries: list[str] = []
    changed_paths: list[str] = []
    deleted_paths: list[str] = []

    def full_path(rel_path: str) -> Path:
        return resolve_repo_path(rel_path)

    def exists_in_plan_or_disk(rel_path: str) -> bool:
        if rel_path in planned:
            return planned[rel_path] is not None
        return full_path(rel_path).exists()

    def current_text(rel_path: str) -> tuple[list[str], bool]:
        if rel_path in planned:
            state = planned[rel_path]
            if state is None:
                raise ApplyPatchError(f"{rel_path} was deleted earlier in this patch")
            lines, final_newline = state
            return list(lines), final_newline

        path = full_path(rel_path)
        if not path.exists():
            raise ApplyPatchError(f"{rel_path} does not exist")
        if not path.is_file():
            raise ApplyPatchError(f"{rel_path} is not a file")
        text = path.read_text()
        return text.splitlines(), text.endswith("\n")

    def ensure_deletable_file(rel_path: str) -> None:
        if rel_path in planned:
            if planned[rel_path] is None:
                raise ApplyPatchError(f"{rel_path} does not exist")
            return
        path = full_path(rel_path)
        if not path.exists():
            raise ApplyPatchError(f"{rel_path} does not exist")
        if not path.is_file():
            raise ApplyPatchError(f"{rel_path} is not a file")

    for op in operations:
        if op.kind == "add":
            if exists_in_plan_or_disk(op.path):
                raise ApplyPatchError(f"{op.path} already exists")
            planned[op.path] = (list(op.lines), True)
            changed_paths.append(op.path)
            summaries.append(f"added {op.path}")
            continue

        if op.kind == "delete":
            ensure_deletable_file(op.path)
            planned[op.path] = None
            deleted_paths.append(op.path)
            summaries.append(f"deleted {op.path}")
            continue

        if op.kind == "update":
            lines, final_newline = current_text(op.path)
            updated = _apply_hunks(lines, op.hunks, path=op.path)
            destination = op.move_to or op.path
            if op.move_to and op.move_to != op.path and exists_in_plan_or_disk(op.move_to):
                raise ApplyPatchError(f"{op.move_to} already exists")
            if op.move_to and op.move_to != op.path:
                ensure_deletable_file(op.path)
                planned[op.path] = None
                deleted_paths.append(op.path)
                summaries.append(f"moved {op.path} -> {op.move_to}")
            else:
                summaries.append(f"updated {op.path}")
            planned[destination] = (updated, final_newline)
            changed_paths.append(destination)
            continue

        raise ApplyPatchError(f"unknown operation kind {op.kind}")

    for rel_path, state in planned.items():
        path = full_path(rel_path)
        if state is None:
            try:
                path.unlink()
            except FileNotFoundError:
                pass
            continue
        lines, final_newline = state
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(_join_lines(lines, final_newline))

    return ApplyPatchOutcome(
        changed_paths=tuple(dict.fromkeys(changed_paths)),
        deleted_paths=tuple(dict.fromkeys(deleted_paths)),
        summary=tuple(summaries),
    )


def parse_apply_patch(patch_text: str) -> tuple[PatchOperation, ...]:
    """Parse a Codex-style patch payload."""
    if not isinstance(patch_text, str):
        raise ApplyPatchError("patch must be a string")

    lines = patch_text.splitlines()
    if not lines or lines[0] != "*** Begin Patch":
        raise ApplyPatchError("patch must start with *** Begin Patch")
    if lines[-1:] != ["*** End Patch"]:
        raise ApplyPatchError("patch must end with *** End Patch")

    operations: list[PatchOperation] = []
    index = 1
    while index < len(lines) - 1:
        line = lines[index]
        if line.startswith("*** Add File: "):
            path = _normalize_patch_path(line.removeprefix("*** Add File: "))
            index += 1
            add_lines: list[str] = []
            while index < len(lines) - 1 and not _is_file_header(lines[index]):
                if not lines[index].startswith("+"):
                    raise ApplyPatchError(f"add file line for {path} must start with +")
                add_lines.append(lines[index][1:])
                index += 1
            operations.append(PatchOperation(kind="add", path=path, lines=tuple(add_lines)))
            continue

        if line.startswith("*** Delete File: "):
            path = _normalize_patch_path(line.removeprefix("*** Delete File: "))
            operations.append(PatchOperation(kind="delete", path=path))
            index += 1
            continue

        if line.startswith("*** Update File: "):
            path = _normalize_patch_path(line.removeprefix("*** Update File: "))
            index += 1
            move_to: str | None = None
            if index < len(lines) - 1 and lines[index].startswith("*** Move to: "):
                move_to = _normalize_patch_path(lines[index].removeprefix("*** Move to: "))
                index += 1

            hunks: list[PatchHunk] = []
            pending_headers: list[str] = []
            while index < len(lines) - 1 and not _is_file_header(lines[index]):
                if not lines[index].startswith("@@"):
                    raise ApplyPatchError(f"update hunk for {path} must start with @@")
                header = _normalize_hunk_header(lines[index][2:].strip())
                index += 1
                hunk_lines: list[HunkLine] = []

                while index < len(lines) - 1:
                    current = lines[index]
                    if current == "*** End of File":
                        index += 1
                        break
                    if current.startswith("@@") or _is_file_header(current):
                        break
                    if current == "":
                        hunk_lines.append(HunkLine(prefix=" ", text=""))
                        index += 1
                        continue
                    if current[0] not in {" ", "-", "+"}:
                        raise ApplyPatchError(
                            f"hunk line for {path} must start with space, -, or +"
                        )
                    hunk_lines.append(HunkLine(prefix=current[0], text=current[1:]))
                    index += 1

                if hunk_lines:
                    headers = tuple([*pending_headers, header] if header else pending_headers)
                    hunks.append(PatchHunk(headers=headers, lines=tuple(hunk_lines)))
                    pending_headers = []
                elif header:
                    pending_headers.append(header)
                else:
                    raise ApplyPatchError(f"empty hunk for {path}")

            if pending_headers:
                raise ApplyPatchError(f"context header for {path} has no hunk lines")
            if not hunks and not move_to:
                raise ApplyPatchError(f"update file {path} must include at least one hunk")
            operations.append(
                PatchOperation(
                    kind="update",
                    path=path,
                    hunks=tuple(hunks),
                    move_to=move_to,
                )
            )
            continue

        raise ApplyPatchError(f"unexpected patch line: {line}")

    return tuple(operations)


def _is_file_header(line: str) -> bool:
    return any(line.startswith(header) for header in _FILE_HEADERS)


def _normalize_hunk_header(header: str) -> str:
    stripped = header.strip()
    if stripped.endswith("@@"):
        stripped = stripped[:-2].strip()
    if _UNIFIED_HUNK_HEADER_RE.match(stripped):
        return ""
    return stripped


def _normalize_patch_path(raw_path: str) -> str:
    path = raw_path.strip()
    if not path:
        raise ApplyPatchError("file path cannot be empty")
    if path.startswith("/"):
        raise ApplyPatchError("file path must be relative")
    normalized = posixpath.normpath(path)
    if normalized == "." or normalized.startswith("../") or "/../" in normalized:
        raise ApplyPatchError("file path must stay inside the repository")
    return normalized


def _apply_hunks(lines: list[str], hunks: tuple[PatchHunk, ...], *, path: str) -> list[str]:
    updated = list(lines)
    cursor = 0
    for hunk in hunks:
        old_lines = [line.text for line in hunk.lines if line.prefix in {" ", "-"}]
        new_lines = [line.text for line in hunk.lines if line.prefix in {" ", "+"}]
        if not old_lines:
            raise ApplyPatchError(f"hunk for {path} needs context or removed lines")
        location = _find_hunk_location(updated, old_lines, hunk.headers, cursor, path=path)
        updated[location : location + len(old_lines)] = new_lines
        cursor = location + len(new_lines)
    return updated


def _find_hunk_location(
    lines: list[str],
    old_lines: list[str],
    headers: tuple[str, ...],
    cursor: int,
    *,
    path: str,
) -> int:
    for mode in ("exact", "rstrip", "strip"):
        candidates = [
            index
            for index in range(cursor, len(lines) - len(old_lines) + 1)
            if _window_matches(lines[index : index + len(old_lines)], old_lines, mode)
        ]
        candidates = _filter_by_headers(lines, candidates, headers)
        if len(candidates) == 1:
            return candidates[0]
        if len(candidates) > 1:
            raise ApplyPatchError(
                f"hunk for {path} matched {len(candidates)} locations; add more context"
            )
    header_text = f" near {' / '.join(headers)}" if headers else ""
    raise ApplyPatchError(f"hunk for {path}{header_text} did not match file content")


def _window_matches(window: list[str], old_lines: list[str], mode: str) -> bool:
    if len(window) != len(old_lines):
        return False
    if mode == "exact":
        return window == old_lines
    if mode == "rstrip":
        return [line.rstrip() for line in window] == [line.rstrip() for line in old_lines]
    return [line.strip() for line in window] == [line.strip() for line in old_lines]


def _filter_by_headers(
    lines: list[str],
    candidates: list[int],
    headers: tuple[str, ...],
) -> list[int]:
    if not headers:
        return candidates
    filtered: list[int] = []
    for candidate in candidates:
        search_start = 0
        ok = True
        for header in headers:
            header_index = _find_header_before(lines, header, search_start, candidate)
            if header_index is None:
                ok = False
                break
            search_start = header_index + 1
        if ok:
            filtered.append(candidate)
    return filtered


def _find_header_before(
    lines: list[str],
    header: str,
    start: int,
    end: int,
) -> int | None:
    needle = header.strip()
    if not needle:
        return None
    for index in range(start, end + 1):
        haystack = lines[index].strip()
        if needle in haystack:
            return index
    return None


def _join_lines(lines: list[str], final_newline: bool) -> str:
    text = "\n".join(lines)
    if final_newline:
        return f"{text}\n"
    return text
