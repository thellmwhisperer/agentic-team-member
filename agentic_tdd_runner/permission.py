"""Permission-driven runtime gates for the agent loop."""

from __future__ import annotations

import re
import posixpath
import shlex
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from agentic_tdd_runner.compiler.parser import (
    _extract_target_snippet,
    _find_symbol_line_with_source,
)
from agentic_tdd_runner.cookbook import _find_function_end
from agentic_tdd_runner.languages import get_language


INTENTS = {
    "challenge_target",
    "understand_contract",
    "write_regression_test",
    "repair_test_setup",
    "edit_source",
    "run_test",
    "done",
}


@dataclass
class PermissionReview:
    allowed: bool
    message: str
    grant: str | None = None
    event: str = "permission_review"


_SEARCH_RESULT_RE = re.compile(
    r"^(?:\./)?(?P<path>[^:\n]+):(?P<line>\d+):(?P<text>.*)$"
)
_CONTROL_FLOW_SYMBOLS = {
    "if",
    "else",
    "for",
    "while",
    "switch",
    "catch",
    "do",
    "try",
    "finally",
    "return",
    "await",
}
_READ_ONLY_SHELL_COMMANDS = {"grep", "rg"}


def permission_enabled(config: dict) -> bool:
    return bool((config or {}).get("agent", {}).get("permission_driven", False))


def build_permission_context(
    *,
    episode: dict | None,
    config: dict,
    phase: str,
    test_file_created: bool,
    workdir: str | None = None,
) -> dict:
    episode = episode or {}
    runner_cfg = (config or {}).get("runner", {})
    cookbook_text = str(episode.get("cookbook_text") or "")
    contract_facts = _extract_callback_contract_facts(cookbook_text)
    repo_profile_facts = _extract_repo_profile_facts(cookbook_text)
    runner_facts = []
    runner_facts_text = str(episode.get("runner_facts_text") or "")
    for line in runner_facts_text.splitlines():
        stripped = line.strip()
        if stripped.startswith("- "):
            runner_facts.append(stripped[2:])
    return {
        "phase": phase,
        "test_file_created": test_file_created,
        "source_file": episode.get("source_file"),
        "test_file": episode.get("test_file"),
        "source_import_path": episode.get("source_import_path"),
        "target_symbol": episode.get("target_symbol"),
        "runner": episode.get("runner") or runner_cfg.get("framework"),
        "test_command": runner_cfg.get("command"),
        "contract_facts": contract_facts,
        "repo_profile_facts": repo_profile_facts,
        "runner_facts": runner_facts,
        "source_signature": _extract_source_signature(cookbook_text),
        "source_snippet": _read_target_source_snippet(
            workdir=workdir,
            source_file=episode.get("source_file"),
            symbol=episode.get("target_symbol"),
        ),
        "source_imports": _read_relevant_imports_snippet(
            workdir=workdir,
            source_file=episode.get("source_file"),
            contract_facts=contract_facts + repo_profile_facts,
        ),
        "referenced_type_shapes": _read_referenced_type_shapes(
            workdir=workdir,
            contract_facts=contract_facts,
        ),
        "test_setup_read_paths": _read_test_setup_dependency_paths(
            workdir=workdir,
            test_file=episode.get("test_file"),
        ),
        "source_mocks": _extract_mock_modules(cookbook_text),
        "module_mock_block": _extract_code_block_after(
            cookbook_text,
            "### Module Mocks (paste before source import)",
        ),
    }


def answer_harness(args: dict, context: dict) -> PermissionReview:
    intent = str(args.get("intent") or "").strip()
    if intent not in INTENTS:
        return PermissionReview(
            allowed=False,
            message=(
                "PERMISSION DENIED: unknown intent. Use one of: "
                + ", ".join(sorted(INTENTS))
            ),
        )

    if intent == "challenge_target":
        return PermissionReview(
            allowed=False,
            message=(
                "TARGET CHALLENGE REQUIRES RUNTIME REROUTE: provide `source_file`, "
                "`target_symbol`, and `evidence` to the runtime ask_harness handler."
            ),
            event="target_challenge_denied",
        )

    if context.get("target_challenge_hint"):
        return _target_challenge_required_review(context["target_challenge_hint"])

    if intent == "understand_contract":
        facts = context.get("contract_facts") or []
        if facts:
            type_shapes = _format_type_shapes(context.get("referenced_type_shapes") or [])
            shape_block = f"\nReferenced type shapes:\n{type_shapes}" if type_shapes else ""
            implementation_block = _implementation_answer_block(args, context)
            return PermissionReview(
                allowed=True,
                message=(
                    "HARNESS ANSWER: contract facts are already known. "
                    "Do not call read_file for this intent.\n"
                    + _bullet_block(facts)
                    + shape_block
                    + implementation_block
                    + "\nNext required action: call ask_harness with intent `write_regression_test`, "
                    "then create/edit only the granted test file."
                ),
                grant=None,
                event="permission_answered",
            )
        return PermissionReview(
            allowed=True,
            message=(
                "PERMISSION GRANTED: inspect one focused contract source. "
                "Read only the target registration, framework type/source lines, or compiler feedback needed."
            ),
            grant="read_contract",
            event="permission_granted",
        )

    if intent == "write_regression_test":
        lines = [
            f"PERMISSION GRANTED: create or edit only `{context.get('test_file')}`.",
            "Do not read source before writing the first regression test; the harness has supplied the needed target facts.",
            "You may read_file this same test path while repairing/resyncing the granted file.",
            "Use the real callable contract and cover the acceptance bullets.",
            "Keep direct invocation and assertions inside the test, not shared setup.",
            "Do not add production `__set...ForTests` setters. Prefer a public caller/registration path, module mock, or smallest pure helper/predicate when hard-wired dependencies block a direct test.",
        ]
        signature = context.get("source_signature")
        if signature:
            lines.append(f"Target callable currently has source signature: `{signature}`.")
        contract_facts = context.get("contract_facts") or []
        if contract_facts:
            lines.append("Contract facts:")
            lines.extend(f"- {fact}" for fact in contract_facts)
        mocks = context.get("source_mocks") or []
        if mocks:
            lines.append("Module mocks to use before source import:")
            lines.extend(f"- {mock}" for mock in mocks[:8])
        runner_facts = context.get("runner_facts") or []
        if runner_facts:
            lines.append("Runner facts:")
            lines.extend(f"- {fact}" for fact in runner_facts[:8])
        type_shapes = _format_type_shapes(context.get("referenced_type_shapes") or [])
        if type_shapes:
            lines.append("Referenced type shapes:")
            lines.append(type_shapes)
        skeleton = _build_regression_test_skeleton(context)
        if skeleton:
            lines.append("Suggested regression test skeleton:")
            lines.append("```ts")
            lines.append(skeleton)
            lines.append("```")
        return PermissionReview(
            allowed=True,
            message="\n".join(lines),
            grant="write_test",
            event="permission_granted",
        )

    if intent == "repair_test_setup":
        return PermissionReview(
            allowed=True,
            message=(
                f"PERMISSION GRANTED: repair setup in `{context.get('test_file')}` only. "
                "Use deterministic runner/mock feedback. You may read_file this same test path "
                "and modules explicitly mocked by this test. "
                "Do not edit source behavior in this phase."
            ),
            grant="write_test",
            event="permission_granted",
        )

    if intent == "edit_source":
        if not context.get("test_file_created"):
            return PermissionReview(
                allowed=False,
                message="PERMISSION DENIED: create a focused failing regression test before editing source.",
            )
        lines = [
            f"PERMISSION GRANTED: edit only `{context.get('source_file')}` for the requested behavior.",
            "Preserve existing public payload keys and fallback behavior unless the issue requires otherwise.",
            "You may read_file this same source path while resyncing the granted edit. Do not use broad rg.",
        ]
        snippet = context.get("source_snippet")
        if snippet:
            lines.append("Current target snippet for exact str_replace:")
            lines.append("```ts")
            lines.append(str(snippet))
            lines.append("```")
        imports = context.get("source_imports")
        if imports:
            lines.append("Existing relevant imports; extend these if the edit needs callback types:")
            lines.append("```ts")
            lines.append(str(imports))
            lines.append("```")
        contract_facts = context.get("contract_facts") or []
        if contract_facts:
            lines.append("Fix facts:")
            lines.extend(f"- {fact}" for fact in contract_facts)
        return PermissionReview(
            allowed=True,
            message="\n".join(lines),
            grant="write_source",
            event="permission_granted",
        )

    if intent == "run_test":
        if not context.get("test_file_created"):
            return PermissionReview(
                allowed=False,
                message="PERMISSION DENIED: no regression test has been created yet.",
            )
        return PermissionReview(
            allowed=True,
            message=(
                "PERMISSION GRANTED: run the focused test only. "
                f"Use `{context.get('test_command')}` with `{context.get('test_file')}`."
            ),
            grant="run_test",
            event="permission_granted",
        )

    return PermissionReview(
        allowed=True,
        message="PERMISSION GRANTED: say DONE only when focused test, red/green verification, and quality gates are satisfied.",
        grant="done",
        event="permission_granted",
    )


def review_tool_call(
    name: str,
    args: dict,
    *,
    grant: str | None,
    context: dict,
) -> PermissionReview | None:
    if name == "ask_harness":
        return None
    if context.get("target_challenge_hint"):
        return _target_challenge_required_review(context["target_challenge_hint"])
    if grant is None:
        return PermissionReview(
            allowed=False,
            message=(
                "PERMISSION REQUIRED: call ask_harness before using tools. "
                "Declare one intent: challenge_target, understand_contract, write_regression_test, "
                "repair_test_setup, edit_source, run_test, or done."
            ),
        )
    if grant != "done" and _is_read_only_tool_call(name, args):
        return None
    if grant == "read_contract":
        if name in {"read_file", "rg"}:
            return None
    elif grant == "write_test":
        path = str(args.get("path") or "")
        if (
            context.get("test_file_created")
            and name == "run_command"
            and _looks_like_focused_test_command(str(args.get("command") or ""), context=context)
        ):
            return None
        if name == "read_file" and (
            _same_path(path, context.get("test_file"))
            or _path_in_list(path, context.get("test_setup_read_paths") or [])
        ):
            return None
        if name in {"create_file", "str_replace_editor"} and _same_path(path, context.get("test_file")):
            return None
    elif grant == "write_source":
        path = str(args.get("path") or "")
        if name == "read_file" and _same_path(path, context.get("source_file")):
            return None
        if name == "str_replace_editor" and _same_path(path, context.get("source_file")):
            return None
    elif grant == "run_test":
        if name == "run_command" and _looks_like_focused_test_command(
            str(args.get("command") or ""),
            context=context,
        ):
            return None
    elif grant == "done":
        return PermissionReview(
            allowed=False,
            message="PERMISSION REQUIRED: say DONE in normal assistant text; do not call more tools.",
        )
    return PermissionReview(
        allowed=False,
        message=f"PERMISSION DENIED: `{name}` is not allowed under current grant `{grant}`.",
    )


def extract_target_challenge_hint(
    name: str,
    args: dict,
    result: str,
    context: dict,
    *,
    workdir: str,
) -> dict[str, str] | None:
    """Infer a pending target challenge from issue-relevant code evidence."""
    if name not in {"rg", "read_file"}:
        return None
    if not result.strip():
        return None

    active_source = _normalize_permission_path(str(context.get("source_file") or ""))
    active_symbol = str(context.get("target_symbol") or "").strip()
    if not active_source or not active_symbol:
        return None

    if name == "read_file":
        source_file = _normalize_permission_path(str(args.get("path") or ""))
        if (
            not source_file
            or _same_path(source_file, active_source)
            or _looks_like_test_file(source_file)
            or source_file.startswith("../")
            or "/../" in source_file
        ):
            return None
        path = Path(workdir) / source_file
        try:
            source_text = path.read_text()
            line_offset = 0
        except OSError:
            source_text = result
            view_range = args.get("view_range")
            line_offset = view_range[0] - 1 if _is_view_range(view_range) else 0
        for index, raw_line in enumerate(source_text.splitlines(), start=1):
            if not _line_matches_issue_terms(raw_line, context):
                continue
            line_no = line_offset + index
            target_symbol = _find_enclosing_symbol(source_text, index, source_file)
            if not target_symbol or target_symbol == active_symbol:
                continue
            return {
                "source_file": source_file,
                "target_symbol": target_symbol,
                "evidence": (
                    f"read_file found issue-relevant code in {source_file}:{line_no} inside "
                    f"{target_symbol} while the active target is "
                    f"{active_source}::{active_symbol}."
                ),
            }
        return None

    for raw_line in result.splitlines():
        match = _SEARCH_RESULT_RE.match(raw_line)
        if not match:
            continue
        source_file = _normalize_permission_path(match.group("path"))
        if not source_file or _same_path(source_file, active_source):
            continue
        if source_file.startswith("../") or "/../" in source_file:
            continue
        line_no = int(match.group("line"))
        path = Path(workdir) / source_file
        try:
            source_text = path.read_text()
        except OSError:
            continue

        target_symbol = _find_enclosing_symbol(source_text, line_no, source_file)
        if not target_symbol or target_symbol == active_symbol:
            continue

        return {
            "source_file": source_file,
            "target_symbol": target_symbol,
            "evidence": (
                f"rg found issue-relevant code in {source_file}:{line_no} inside "
                f"{target_symbol} while the active target is "
                f"{active_source}::{active_symbol}."
            ),
        }
    return None


def _is_view_range(value: Any) -> bool:
    return (
        isinstance(value, list)
        and len(value) == 2
        and all(isinstance(item, int) for item in value)
        and value[0] > 0
    )


def _looks_like_test_file(path: str) -> bool:
    normalized = _normalize_permission_path(path)
    filename = posixpath.basename(normalized)
    return bool(
        re.search(r"(^|[._-])(test|spec)\.[A-Za-z0-9]+$", filename)
        or normalized.endswith("__tests__")
        or "/__tests__/" in normalized
    )


def _line_matches_issue_terms(line: str, context: dict) -> bool:
    line_terms = set(_evidence_tokens(line))
    if not line_terms:
        return False
    issue_terms = set(_evidence_tokens(str(context.get("issue_text") or "")))
    if not issue_terms:
        return False
    return bool(line_terms & issue_terms)


def _evidence_tokens(text: str) -> list[str]:
    normalized = text.lower().replace("@", " ")
    tokens = re.findall(r"[a-z0-9_]{4,}", normalized)
    stop = {
        "const",
        "function",
        "return",
        "string",
        "boolean",
        "message",
        "export",
        "async",
        "await",
        "solo",
        "funciona",
        "principio",
    }
    return [token for token in tokens if token not in stop]


def _target_challenge_required_review(hint: dict[str, str]) -> PermissionReview:
    source_file = hint.get("source_file") or ""
    target_symbol = hint.get("target_symbol") or ""
    evidence = hint.get("evidence") or ""
    return PermissionReview(
        allowed=False,
        message=(
            "TARGET CHALLENGE REQUIRED: code evidence points at a different target. "
            "Call ask_harness with:\n"
            "{\n"
            '  "intent": "challenge_target",\n'
            f'  "source_file": "{source_file}",\n'
            f'  "target_symbol": "{target_symbol}",\n'
            f'  "evidence": "{_json_safe(evidence)}"\n'
            "}\n"
            "Do not continue writing tests or edits for the previous target."
        ),
        event="target_challenge_required",
    )


def _find_enclosing_symbol(source_text: str, line_no: int, source_path: str) -> str | None:
    lines = source_text.splitlines()
    if not lines or line_no < 1 or line_no > len(lines):
        return None
    lang = get_language(source_path)
    for index in range(line_no, 0, -1):
        symbol = _definition_symbol_from_line(lines[index - 1])
        if not symbol:
            continue
        end = _find_function_end(source_text, index, lang=lang) or index
        if index <= line_no <= end:
            return symbol
    return None


def _definition_symbol_from_line(line: str) -> str | None:
    patterns = (
        r"^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)\s*\(",
        r"^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=.*=>",
        r"^\s*(?:async\s+)?def\s+([A-Za-z_][\w]*)\s*\(",
        r"^\s*(?:public|private|protected|static|async|\s)*([A-Za-z_$][\w$]*)\s*\([^)]*\)\s*(?::[^{]+)?\{",
    )
    for pattern in patterns:
        match = re.search(pattern, line)
        if match:
            symbol = match.group(1)
            if symbol in _CONTROL_FLOW_SYMBOLS:
                return None
            return symbol
    return None


def consume_grant(
    name: str,
    args: dict | None,
    grant: str | None,
    context: dict | None = None,
) -> str | None:
    if name == "ask_harness":
        return grant
    if grant not in {None, "done"} and _is_read_only_tool_call(name, args or {}):
        return grant
    if (
        grant == "write_test"
        and (context or {}).get("test_file_created")
        and name == "run_command"
        and _looks_like_focused_test_command(str((args or {}).get("command") or ""), context=context or {})
    ):
        return grant
    if grant in {"write_test", "write_source"}:
        path = str((args or {}).get("path") or "")
        target = (context or {}).get("test_file") if grant == "write_test" else (context or {}).get("source_file")
        if name in {"read_file", "create_file", "str_replace_editor"} and _same_path(path, target):
            return grant
        if (
            grant == "write_test"
            and name == "read_file"
            and _path_in_list(path, (context or {}).get("test_setup_read_paths") or [])
        ):
            return grant
    return None


def merge_grant_after_harness_answer(
    current_grant: str | None,
    review: PermissionReview,
) -> str | None:
    if review.grant is not None:
        return review.grant
    if review.allowed and review.event == "permission_answered":
        return current_grant
    return None


def review_target_challenge(
    args: dict,
    context: dict,
    *,
    workdir: str,
) -> PermissionReview:
    """Validate a model request to move the episode to a different target."""
    source_file = _normalize_permission_path(str(args.get("source_file") or ""))
    target_symbol = str(args.get("target_symbol") or "").strip()
    evidence = str(args.get("evidence") or args.get("question") or "").strip()

    if not source_file or not target_symbol or not evidence:
        return PermissionReview(
            allowed=False,
            message=(
                "TARGET CHALLENGE DENIED: provide `source_file`, `target_symbol`, "
                "and concise code-derived `evidence`."
            ),
            event="target_challenge_denied",
        )
    if source_file.startswith("/") or source_file.startswith("../") or "/../" in source_file:
        return PermissionReview(
            allowed=False,
            message="TARGET CHALLENGE DENIED: source_file must be a relative repo path.",
            event="target_challenge_denied",
        )
    if _same_path(source_file, context.get("source_file")) and target_symbol == context.get("target_symbol"):
        retry = _target_challenge_retry_from_evidence(
            evidence,
            context=context,
            workdir=workdir,
        )
        if retry:
            return retry
        return PermissionReview(
            allowed=False,
            message="TARGET CHALLENGE DENIED: proposed target is already the active target.",
            event="target_challenge_denied",
        )

    path = Path(workdir) / source_file
    try:
        source_text = path.read_text()
    except OSError:
        return PermissionReview(
            allowed=False,
            message=f"TARGET CHALLENGE DENIED: `{source_file}` is not readable.",
            event="target_challenge_denied",
        )

    start, source = _find_challenge_symbol_line(source_text, target_symbol)
    if source not in {"definition", "method"} or not start:
        return PermissionReview(
            allowed=False,
            message=(
                f"TARGET CHALLENGE DENIED: `{target_symbol}` was not found as a "
                f"definition in `{source_file}`."
            ),
            event="target_challenge_denied",
        )

    return PermissionReview(
        allowed=True,
        message=(
            "TARGET CHALLENGE ACCEPTED: deterministic validation found "
            f"`{source_file}::{target_symbol}`. Reroute the episode before continuing."
        ),
        grant=None,
        event="target_challenge_accepted",
    )


def _find_challenge_symbol_line(source_text: str, symbol: str) -> tuple[int | None, str | None]:
    start, source = _find_symbol_line_with_source(source_text, symbol)
    if source == "definition" or not start:
        return start, source
    lines = source_text.splitlines()
    if 1 <= start <= len(lines) and _looks_like_method_definition(lines[start - 1], symbol):
        return start, "method"
    return start, source


def _target_challenge_retry_from_evidence(
    evidence: str,
    *,
    context: dict,
    workdir: str,
) -> PermissionReview | None:
    suggested = _suggest_target_from_evidence(
        evidence,
        active_source=_normalize_permission_path(str(context.get("source_file") or "")),
        active_symbol=str(context.get("target_symbol") or ""),
        workdir=workdir,
    )
    if not suggested:
        return None
    source_file, target_symbol = suggested
    return PermissionReview(
        allowed=False,
        message=(
            "TARGET CHALLENGE DENIED: your evidence names a different target, "
            "but the structured fields still point at the active target. "
            "Call ask_harness again with:\n"
            "{\n"
            '  "intent": "challenge_target",\n'
            f'  "source_file": "{source_file}",\n'
            f'  "target_symbol": "{target_symbol}",\n'
            f'  "evidence": "{_json_safe(evidence)}"\n'
            "}"
        ),
        event="target_challenge_denied",
    )


def _suggest_target_from_evidence(
    evidence: str,
    *,
    active_source: str,
    active_symbol: str,
    workdir: str,
) -> tuple[str, str] | None:
    for match in re.finditer(
        r"(?P<path>(?:[A-Za-z0-9_.-]+/)*[A-Za-z0-9_.-]+\.(?:ts|tsx|js|jsx|py))(?::(?P<line>\d+))?",
        evidence,
    ):
        source_file = _normalize_permission_path(match.group("path"))
        if (
            not source_file
            or _same_path(source_file, active_source)
            or source_file.startswith("../")
            or "/../" in source_file
        ):
            continue
        path = Path(workdir) / source_file
        try:
            source_text = path.read_text()
        except OSError:
            continue
        line_no = int(match.group("line")) if match.group("line") else None
        if line_no:
            target_symbol = _find_enclosing_symbol(source_text, line_no, source_file)
            if target_symbol and target_symbol != active_symbol:
                return source_file, target_symbol
        mentioned = _mentioned_symbols_in_source(source_text, evidence)
        for target_symbol in mentioned:
            if target_symbol != active_symbol:
                return source_file, target_symbol
    return None


def _mentioned_symbols_in_source(source_text: str, evidence: str) -> list[str]:
    symbols: list[str] = []
    for index, line in enumerate(source_text.splitlines(), start=1):
        symbol = _definition_symbol_from_line(line)
        if not symbol or not _symbol_mentioned_in_evidence(symbol, evidence):
            continue
        end = _find_function_end(source_text, index, lang=None) or index
        if index <= end:
            symbols.append(symbol)
    return symbols


def _symbol_mentioned_in_evidence(symbol: str, evidence: str) -> bool:
    return bool(re.search(rf"(?<![A-Za-z0-9_$]){re.escape(symbol)}(?![A-Za-z0-9_$])", evidence))


def _json_safe(text: str) -> str:
    return text.replace("\\", "\\\\").replace('"', '\\"')


def _looks_like_method_definition(line: str, symbol: str) -> bool:
    esc = re.escape(symbol)
    patterns = (
        rf"^\s*(?:public|private|protected|static|async|\s)*{esc}\s*\(",
        rf"^\s*(?:async\s+)?def\s+{esc}\s*\(",
    )
    return any(re.search(pattern, line) for pattern in patterns)


def _implementation_answer_block(args: dict, context: dict) -> str:
    question = str(args.get("question") or "")
    if not _asks_for_target_implementation(question):
        return ""

    snippet = str(context.get("source_snippet") or "").strip()
    if not snippet:
        return (
            "\nTarget implementation snippet: unavailable from deterministic context. "
            "Continue from the contract facts and runner feedback."
        )

    imports = str(context.get("source_imports") or "").strip()
    lines = [
        "\nTarget implementation snippet supplied by the harness:",
        "```ts",
        snippet,
        "```",
    ]
    if imports:
        lines.extend([
            "Relevant existing imports:",
            "```ts",
            imports,
            "```",
        ])
    return "\n".join(lines)


def _asks_for_target_implementation(question: str) -> bool:
    lowered = question.lower()
    implementation_terms = (
        "implement",
        "implementation",
        "internally",
        "body",
        "source",
        "what does",
        "why does",
        "how does",
        "current code",
        "actual code",
        "function do",
    )
    return any(term in lowered for term in implementation_terms)


def _extract_callback_contract_facts(cookbook_text: str) -> list[str]:
    facts: list[str] = []
    in_section = False
    for line in cookbook_text.splitlines():
        stripped = line.strip()
        if stripped == "### Callback Contract Evidence":
            in_section = True
            continue
        if in_section and stripped.startswith("### "):
            break
        if not in_section or not stripped.startswith("- "):
            continue
        body = stripped[2:]
        if body.startswith("Before writing"):
            continue
        facts.append(body)
    return facts


def _extract_repo_profile_facts(cookbook_text: str) -> list[str]:
    facts: list[str] = []
    in_section = False
    for line in cookbook_text.splitlines():
        stripped = line.strip()
        if stripped == "### Repo Profile Facts":
            in_section = True
            continue
        if in_section and stripped.startswith("### "):
            break
        if not in_section or not stripped.startswith("- "):
            continue
        body = stripped[2:]
        if body.startswith("Stable repo facts"):
            continue
        facts.append(body)
    return facts


def _extract_source_signature(cookbook_text: str) -> str:
    in_source_edits = False
    lines = cookbook_text.splitlines()
    for index, line in enumerate(lines):
        stripped = line.strip()
        if stripped == "### Source Edits (apply before testing)":
            in_source_edits = True
            continue
        if in_source_edits and stripped.startswith("### "):
            return ""
        if in_source_edits and stripped == "OLD:" and index + 1 < len(lines):
            candidate = lines[index + 1].strip()
            if candidate:
                return candidate
    return ""


def _read_target_source_snippet(
    *,
    workdir: str | None,
    source_file: Any,
    symbol: Any,
) -> str:
    if not workdir or not source_file or not symbol:
        return ""

    path = Path(workdir) / str(source_file)
    try:
        source_text = path.read_text()
    except OSError:
        return ""

    start, source = _find_symbol_line_with_source(source_text, str(symbol))
    if source != "definition" or not start:
        return ""

    lang = get_language(str(source_file))
    end = _find_function_end(source_text, start, lang=lang) or start
    return _extract_target_snippet(
        source_text,
        {"line_start": start, "line_end": end},
    ).strip()


def _read_relevant_imports_snippet(
    *,
    workdir: str | None,
    source_file: Any,
    contract_facts: list[str],
) -> str:
    if not workdir or not source_file:
        return ""

    path = Path(workdir) / str(source_file)
    try:
        source_text = path.read_text()
    except OSError:
        return ""

    needles = _import_needles(contract_facts)
    if not needles:
        return ""

    imports = _extract_import_declarations(source_text)
    relevant = [
        declaration
        for declaration in imports
        if any(needle in declaration for needle in needles)
    ]
    return "\n".join(relevant[:6]).strip()


def _read_referenced_type_shapes(
    *,
    workdir: str | None,
    contract_facts: list[str],
) -> list[dict[str, Any]]:
    if not workdir:
        return []

    shapes: list[dict[str, Any]] = []
    for module_name, type_names in _referenced_framework_types(contract_facts):
        type_text = _read_module_type_declarations(Path(workdir), module_name)
        if not type_text:
            continue
        for type_name in type_names:
            shape = _extract_type_shape(type_text, type_name)
            if shape:
                shape["module"] = module_name
                shapes.append(shape)
    return shapes


def _read_test_setup_dependency_paths(
    *,
    workdir: str | None,
    test_file: Any,
) -> list[str]:
    if not workdir or not test_file:
        return []

    test_path = Path(workdir) / str(test_file)
    try:
        test_text = test_path.read_text()
    except OSError:
        return []

    test_dir = posixpath.dirname(_normalize_permission_path(str(test_file)))
    paths: set[str] = set()
    for spec in _extract_mock_module_specs(test_text):
        if not spec.startswith(("./", "../")):
            continue
        candidate_base = _normalize_permission_path(posixpath.join(test_dir, spec))
        if not candidate_base or candidate_base.startswith("../") or "/../" in candidate_base:
            continue
        resolved = _resolve_repo_module_path(Path(workdir), candidate_base)
        if resolved:
            paths.add(resolved)
    return sorted(paths)


def _extract_mock_module_specs(test_text: str) -> list[str]:
    return [
        match.group("spec")
        for match in re.finditer(
            r"mock\.module\(\s*['\"](?P<spec>\.{1,2}/[^'\"]+)['\"]",
            test_text,
        )
    ]


def _resolve_repo_module_path(workdir: Path, candidate_base: str) -> str:
    candidates = [
        candidate_base,
        *[f"{candidate_base}{suffix}" for suffix in (".ts", ".tsx", ".js", ".jsx", ".mts", ".cts")],
        *[
            posixpath.join(candidate_base, f"index{suffix}")
            for suffix in (".ts", ".tsx", ".js", ".jsx", ".mts", ".cts")
        ],
    ]
    for candidate in candidates:
        if (workdir / candidate).is_file():
            return candidate
    return ""


def _referenced_framework_types(contract_facts: list[str]) -> list[tuple[str, list[str]]]:
    primitive_types = {"string", "number", "boolean", "void", "unknown", "object"}
    refs: dict[str, set[str]] = {}
    for fact in contract_facts:
        match = re.search(
            r"(?P<module>[\w@./-]+) type declarations expose `[^`]+\((?P<params>[^`]*)\)`",
            fact,
        )
        if not match:
            continue
        names = refs.setdefault(match.group("module"), set())
        for _param_name, type_name in _signature_param_types(match.group("params")):
            if type_name not in primitive_types:
                names.add(type_name)
    return [
        (module_name, sorted(type_names))
        for module_name, type_names in sorted(refs.items())
        if type_names
    ]


def _read_module_type_declarations(workdir: Path, module_name: str) -> str:
    candidates = [
        workdir / "node_modules" / "@types" / module_name / "index.d.ts",
        workdir / "node_modules" / module_name / "index.d.ts",
        workdir / "node_modules" / module_name / "types.d.ts",
    ]
    for candidate in candidates:
        if candidate.is_file():
            return candidate.read_text(errors="ignore")
    return ""


def _extract_type_shape(type_text: str, type_name: str) -> dict[str, Any]:
    interface_match = re.search(
        rf"\binterface\s+{re.escape(type_name)}\b[^\{{]*{{(?P<body>.*?)\n\s*}}",
        type_text,
        re.DOTALL,
    )
    if interface_match:
        fields = _extract_type_fields(interface_match.group("body"))
        return {"name": type_name, "kind": "interface", "fields": fields}

    alias_match = re.search(
        rf"\btype\s+{re.escape(type_name)}\s*=\s*(?P<body>[^;]+);",
        type_text,
        re.DOTALL,
    )
    if alias_match:
        return {
            "name": type_name,
            "kind": "type",
            "alias": re.sub(r"\s+", " ", alias_match.group("body")).strip(),
            "fields": [],
        }
    return {}


def _extract_type_fields(body: str) -> list[dict[str, str | bool]]:
    fields: list[dict[str, str | bool]] = []
    pattern = re.compile(
        r"^\s*(?P<name>['\"][^'\"]+['\"]|[A-Za-z_$][\w$]*)"
        r"(?P<optional>\?)?\s*:\s*(?P<type>[^;\n]+);?",
        re.MULTILINE,
    )
    for match in pattern.finditer(body):
        raw_name = match.group("name")
        name = raw_name[1:-1] if raw_name.startswith(("'", '"')) else raw_name
        fields.append({
            "name": name,
            "optional": bool(match.group("optional")),
            "type": re.sub(r"\s+", " ", match.group("type")).strip(),
        })
    return fields


def _format_type_shapes(shapes: list[dict[str, Any]]) -> str:
    lines: list[str] = []
    for shape in shapes:
        module_name = shape.get("module")
        type_name = shape.get("name")
        if shape.get("fields"):
            lines.append(f"- {module_name}.{type_name}:")
            for field in shape["fields"][:12]:
                marker = "?" if field.get("optional") else ""
                lines.append(f"  - {field['name']}{marker}: {field['type']}")
        elif shape.get("alias"):
            lines.append(f"- {module_name}.{type_name}: {shape['alias']}")
    return "\n".join(lines)


def _import_needles(contract_facts: list[str]) -> list[str]:
    needles: list[str] = []
    for fact in contract_facts:
        if "tmi.js" in fact:
            needles.append("tmi.js")
        module_match = re.search(r"uses module `(?P<module>[^`]+)`", fact)
        if module_match:
            needles.append(module_match.group("module"))
    return sorted(set(needles))


def _extract_import_declarations(source_text: str) -> list[str]:
    imports: list[str] = []
    lines = source_text.splitlines()
    index = 0
    while index < len(lines):
        line = lines[index]
        if not line.lstrip().startswith("import "):
            index += 1
            continue

        declaration = [line]
        while ";" not in lines[index] and index + 1 < len(lines):
            index += 1
            declaration.append(lines[index])
        imports.append("\n".join(declaration))
        index += 1
    return imports


def _extract_section_bullets(cookbook_text: str, section_title: str) -> list[str]:
    facts: list[str] = []
    in_section = False
    for line in cookbook_text.splitlines():
        stripped = line.strip()
        if stripped == section_title:
            in_section = True
            continue
        if in_section and stripped.startswith("### "):
            break
        if in_section and stripped.startswith("- "):
            facts.append(stripped[2:])
    return facts


def _extract_mock_modules(cookbook_text: str) -> list[str]:
    modules: list[str] = []
    for line in cookbook_text.splitlines():
        stripped = line.strip()
        if not stripped.startswith("mock.module("):
            continue
        modules.append(stripped)
    return modules


def _build_regression_test_skeleton(context: dict) -> str:
    """Build a compact, executable first-test shape when deterministic facts are enough."""
    facts = context.get("contract_facts") or []
    mock_block = str(context.get("module_mock_block") or "").strip()
    signature = str(context.get("source_signature") or "").strip()
    target_symbol = str(context.get("target_symbol") or "").strip()
    callback_params = _callback_params_from_contract_facts(facts)
    if not (facts and mock_block and signature and target_symbol):
        return ""

    import_path = context.get("source_import_path") or "./client"
    spy_names = _extract_spy_names(mock_block)
    type_imports = _extract_type_imports_from_contract_facts(facts)
    type_shapes = context.get("referenced_type_shapes") or []
    fixture_lines = _build_callback_fixture_lines(
        params=callback_params,
        facts=facts,
        type_shapes=type_shapes,
    )
    lines = [
        'import { beforeEach, describe, expect, mock, test } from "bun:test";',
    ]
    for module_name, names in type_imports:
        lines.append(f'import type {{ {", ".join(names)} }} from "{module_name}";')
    lines.append("")
    lines.extend(mock_block.splitlines())
    lines.append("")

    lines.extend([
        f'type ClientModule = typeof import("{import_path}");',
        f'type TargetHandler = ClientModule["{target_symbol}"];',
    ])
    if callback_params:
        lines.extend([
            "type CallbackContract = (",
            *[
                f"  {param_name}: {type_name},"
                for param_name, type_name in callback_params
            ],
            ") => void;",
        ])
    else:
        lines.append("type CallbackContract = TargetHandler;")
    lines.extend([
        "",
        "let targetHandler: TargetHandler;",
    ])
    lines.extend([
        "",
        "beforeEach(async () => {",
    ])
    if spy_names:
        lines.append(f"  for (const spy of [{', '.join(spy_names)}]) spy.mockClear();")
    else:
        lines.append("  // mockClear every *_spy from the cookbook here.")
    lines.extend([
        f'  const clientModule = await import("{import_path}");',
        f"  targetHandler = clientModule.{target_symbol};",
    ])
    lines.extend([
        "});",
        "",
        'test("covers the reported callback behavior", () => {',
        "  // Arrange issue-grounded doubles; do not add production test-only setters.",
    ])
    if fixture_lines:
        lines.extend(f"  {line}" if line else "" for line in fixture_lines)
    else:
        lines.append("  // Build typed callback arguments from the Callback Contract Evidence.")
    lines.extend([
        "  const callbackHandler: CallbackContract = targetHandler;",
        "  callbackHandler(" + ", ".join(_callback_argument_names(callback_params)) + ");",
        "  // Assert through the named *_spy variables and issue acceptance criteria.",
        '  throw new Error("replace skeleton comments with the focused failing regression");',
        "});",
    ])
    return "\n".join(lines)


def _extract_code_block_after(cookbook_text: str, section_title: str) -> str:
    in_section = False
    in_block = False
    lines: list[str] = []
    for line in cookbook_text.splitlines():
        stripped = line.strip()
        if stripped == section_title:
            in_section = True
            continue
        if in_section and not in_block and stripped.startswith("### "):
            break
        if in_section and stripped.startswith("```"):
            if in_block:
                return "\n".join(lines).strip()
            in_block = True
            continue
        if in_block:
            lines.append(line)
    return ""


def _extract_spy_names(mock_block: str) -> list[str]:
    names: list[str] = []
    for line in mock_block.splitlines():
        stripped = line.strip()
        if not stripped.startswith("const ") or "_spy" not in stripped:
            continue
        name = stripped.removeprefix("const ").split("=", 1)[0].strip()
        if name.endswith("_spy"):
            names.append(name)
    return names


def _extract_type_imports_from_contract_facts(facts: list[str]) -> list[tuple[str, list[str]]]:
    return _referenced_framework_types(facts)


def _callback_params_from_contract_facts(facts: list[str]) -> list[tuple[str, str]]:
    for fact in facts:
        match = re.search(
            r"[\w@./-]+ type declarations expose `[^`]+\((?P<params>[^`]*)\)`",
            fact,
        )
        if match:
            return _signature_param_types(match.group("params"))
    return []


def _signature_param_types(signature: str) -> list[tuple[str, str]]:
    pairs: list[tuple[str, str]] = []
    for param in signature.split(","):
        match = re.search(
            r"\b(?P<name>[A-Za-z_$][\w$]*)\s*:\s*(?P<type>[A-Za-z_$][\w$]*)\b",
            param,
        )
        if match:
            pairs.append((match.group("name"), match.group("type")))
    return pairs


def _callback_argument_names(params: list[tuple[str, str]]) -> list[str]:
    return [name for name, _type_name in params]


def _build_callback_fixture_lines(
    *,
    params: list[tuple[str, str]],
    facts: list[str],
    type_shapes: list[dict[str, Any]],
) -> list[str]:
    if not params:
        return []

    event_name = _event_name_from_contract_facts(facts)
    runtime_names = _runtime_arg_names_from_contract_facts(facts)
    shapes_by_name = {shape.get("name"): shape for shape in type_shapes}
    lines: list[str] = []
    for index, (param_name, type_name) in enumerate(params):
        shape = shapes_by_name.get(type_name)
        if shape and shape.get("fields"):
            lines.extend(_fixture_object_lines(param_name, type_name, shape, event_name=event_name, facts=facts))
            continue
        runtime_name = runtime_names[index] if index < len(runtime_names) else param_name
        lines.append(f"const {param_name}: {type_name} = {_sample_primitive_value(param_name, runtime_name, type_name, facts)};")
    return lines


def _fixture_object_lines(
    param_name: str,
    type_name: str,
    shape: dict[str, Any],
    *,
    event_name: str,
    facts: list[str],
) -> list[str]:
    fields = shape.get("fields") or []
    selected = _select_fixture_fields(fields, facts)
    if not selected:
        selected = [field for field in fields if not field.get("optional")][:3]
    if not selected:
        selected = fields[:3]
    lines = [f"const {param_name}: {type_name} = {{"]
    for field in selected:
        key = field["name"]
        value = _sample_field_value(str(key), str(field.get("type") or ""), event_name=event_name)
        rendered_key = key if re.match(r"^[A-Za-z_$][\w$]*$", str(key)) else f'"{key}"'
        lines.append(f"  {rendered_key}: {value},")
    lines.append("};")
    return lines


def _select_fixture_fields(fields: list[dict[str, Any]], facts: list[str]) -> list[dict[str, Any]]:
    selected: list[dict[str, Any]] = []
    fact_text = "\n".join(facts)
    for field in fields:
        name = str(field.get("name") or "")
        if name and (
            name in fact_text
            or name == "message-type"
        ):
            selected.append(field)
    if selected:
        return selected
    return [
        field for field in fields
        if _is_simple_fixture_field(str(field.get("type") or ""))
    ][:3]


def _is_simple_fixture_field(type_text: str) -> bool:
    return any(token in type_text for token in ["string", "boolean", "number"]) or re.match(r"^[A-Za-z_$][\w$]*Plan\b", type_text)


def _sample_primitive_value(param_name: str, runtime_name: str, type_name: str, facts: list[str]) -> str:
    name = f"{param_name} {runtime_name}".lower()
    if type_name == "string":
        if "channel" in name:
            return '"#channel"'
        if "user" in name:
            return '"username"'
        if "message" in name or "msg" in name:
            return '""'
        return '""'
    if type_name == "number":
        return "1"
    if type_name == "boolean":
        return "false"
    return "{}"


def _sample_field_value(field_name: str, type_text: str, *, event_name: str) -> str:
    if field_name == "message-type" and event_name:
        return f'"{event_name}"'
    if field_name == "planName":
        return '"Tier 1"'
    if field_name == "plan" or type_text.endswith("Plan"):
        return '"1000"'
    if "string" in type_text:
        return '""'
    if field_name == "prime" or "boolean" in type_text:
        return "false"
    if "number" in type_text:
        return "1"
    return '""'


def _event_name_from_contract_facts(facts: list[str]) -> str:
    for fact in facts:
        match = re.search(r"\.on\(['\"](?P<event>[^'\"]+)['\"]", fact)
        if match:
            return match.group("event")
        match = re.search(r"source emits `(?P<event>\w+)\(", fact)
        if match:
            return match.group("event")
    return ""


def _runtime_arg_names_from_contract_facts(facts: list[str]) -> list[str]:
    for fact in facts:
        match = re.search(r"source emits `\w+\((?P<params>[^`]*)\)`", fact)
        if match:
            return [part.strip() for part in match.group("params").split(",") if part.strip()]
    return []


def _bullet_block(lines: list[str]) -> str:
    return "\n".join(f"- {line}" for line in lines)


def _same_path(left: str | None, right: str | None) -> bool:
    left_text = str(left or "").strip()
    right_text = str(right or "").strip()
    if not left_text or not right_text:
        return False
    if left_text.startswith("/") != right_text.startswith("/"):
        return False
    return _normalize_permission_path(left_text) == _normalize_permission_path(right_text)


def _path_in_list(path: str | None, candidates: list[str]) -> bool:
    return any(_same_path(path, candidate) for candidate in candidates)


def _normalize_permission_path(path: str) -> str:
    while path.startswith("./"):
        path = path[2:]
    normalized = posixpath.normpath(path)
    return "" if normalized == "." else normalized


def _looks_like_focused_test_command(command: str, *, context: dict) -> bool:
    command = command.strip()
    command = _strip_innocuous_output_filters(command) or ""
    test_file = str(context.get("test_file") or "")
    test_command = str(context.get("test_command") or "")
    if not command or not test_command or _contains_shell_control(command):
        return False

    try:
        command_parts = shlex.split(command)
        test_command_parts = shlex.split(test_command)
    except ValueError:
        return False

    if not command_parts or not test_command_parts:
        return False
    if command_parts == test_command_parts:
        return True
    return (
        bool(test_file)
        and len(command_parts) == len(test_command_parts) + 1
        and command_parts[:len(test_command_parts)] == test_command_parts
        and _same_path(command_parts[-1], test_file)
    )


def _strip_innocuous_output_filters(command: str) -> str | None:
    """Ignore harmless stderr merge and head/tail suffixes for focused tests."""
    try:
        parts = shlex.split(command)
    except ValueError:
        return None
    if "|" in parts:
        pipe_index = len(parts) - 1 - parts[::-1].index("|")
        if not _looks_like_harmless_output_filter(parts[pipe_index + 1:]):
            return None
        parts = parts[:pipe_index]
    while parts and parts[-1] == "2>&1":
        parts.pop()
    if not parts:
        return None
    return shlex.join(parts)


def _looks_like_harmless_output_filter(parts: list[str]) -> bool:
    if not parts:
        return False
    binary = posixpath.basename(parts[0])
    if binary not in {"head", "tail"}:
        return False
    if len(parts) == 1:
        return True
    if len(parts) == 2 and re.fullmatch(r"-\d+", parts[1]):
        return True
    return len(parts) == 3 and parts[1] == "-n" and parts[2].isdigit()


def _is_read_only_tool_call(name: str, args: dict | None) -> bool:
    args = args or {}
    if name in {"read_file", "rg"}:
        return True
    if name != "run_command":
        return False
    return _looks_like_read_only_shell_command(str(args.get("command") or ""))


def _looks_like_read_only_shell_command(command: str) -> bool:
    command = command.strip()
    if not command or _contains_shell_control(command):
        return False
    try:
        parts = shlex.split(command)
    except ValueError:
        return False
    if not parts:
        return False
    binary = posixpath.basename(parts[0])
    return binary in _READ_ONLY_SHELL_COMMANDS


def _contains_shell_control(command: str) -> bool:
    return bool(re.search(r"[;&|<>`]|[$]\(|\r|\n", command))
