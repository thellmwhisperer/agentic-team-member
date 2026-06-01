"""Permission-driven runtime gates for the agent loop."""

from __future__ import annotations

import re
import posixpath
import shlex
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from agentic_tdd_runner.compiler.parser import (
    _extract_target_snippet,
    _find_symbol_line_with_source,
)
from agentic_tdd_runner.apply_patch import ApplyPatchError, apply_patch_touched_paths
from agentic_tdd_runner.cookbook import _find_function_end
from agentic_tdd_runner.languages import get_language
from agentic_tdd_runner.runner_authority import override_detected_runner
from agentic_tdd_runner.runner_command import effective_test_command_template


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


def _language_for_permission_context(*, source_file: Any, test_file: Any) -> object | None:
    for path in (test_file, source_file):
        if not isinstance(path, str) or not path:
            continue
        language = get_language(path)
        if language:
            return language
    return None


def _language_capability(
    language: object | None,
    capability: str,
    *args,
    default,
    **kwargs,
):
    fn = getattr(language, capability, None)
    if callable(fn):
        return fn(*args, **kwargs)
    return default


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
    runner_facts_obj = episode.get("runner_facts")
    bootstrap = runner_cfg.get("bootstrap") if isinstance(runner_cfg, dict) else None
    override_detected = override_detected_runner(runner_cfg)
    runner = (
        _value_from(runner_facts_obj, "test_runner")
        or (runner_cfg.get("framework") if override_detected else None)
        or _value_from(bootstrap, "test_runner")
        or episode.get("runner")
        or runner_cfg.get("framework")
    )
    test_command = (
        _value_from(runner_facts_obj, "test_command")
        or (runner_cfg.get("command") if override_detected else None)
        or effective_test_command_template(bootstrap, None)
        or runner_cfg.get("command")
    )
    source_file = episode.get("source_file")
    test_file = episode.get("test_file")
    language = _language_for_permission_context(source_file=source_file, test_file=test_file)
    runner_facts = []
    runner_facts_text = str(episode.get("runner_facts_text") or "")
    for line in runner_facts_text.splitlines():
        stripped = line.strip()
        if stripped.startswith("- "):
            runner_facts.append(stripped[2:])
    return {
        "phase": phase,
        "test_file_created": test_file_created,
        "source_file": source_file,
        "test_file": test_file,
        "source_import_path": episode.get("source_import_path"),
        "target_symbol": episode.get("target_symbol"),
        "runner": runner,
        "language_name": getattr(language, "name", None),
        "test_command": test_command,
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
        "referenced_type_shapes": _language_capability(
            language,
            "referenced_type_shapes",
            workdir=workdir,
            contract_facts=contract_facts,
            default=[],
        ),
        "test_setup_read_paths": _language_capability(
            language,
            "test_setup_dependency_paths",
            workdir=workdir,
            test_file=test_file,
            default=[],
        ),
        "source_mocks": _language_capability(
            language,
            "extract_mock_modules",
            cookbook_text,
            default=[],
        ),
        "module_mock_block": _extract_code_block_after(
            cookbook_text,
            "### Module Mocks (paste before source import)",
        ),
    }


def _value_from(container: object, key: str) -> str | None:
    value = container.get(key) if isinstance(container, Mapping) else getattr(container, key, None)
    return value.strip() if isinstance(value, str) and value.strip() else None


def _bun_test_guidance_conflict(args: dict, context: dict) -> PermissionReview | None:
    runner = str(context.get("runner") or "").strip()
    if not runner or runner == "bun:test":
        return None

    payload = _write_payload_text(args)
    if "bun:test" not in payload and "mock.module(" not in payload:
        return None
    return PermissionReview(
        allowed=False,
        message=(
            "PERMISSION DENIED: detected runner is "
            f"`{runner}`, so do not create Bun-specific test scaffolding. "
            "Use Runner Facts and the project test runner instead."
        ),
    )


def _write_payload_text(args: dict) -> str:
    return "\n".join(str(args.get(field) or "") for field in ("content", "new_str", "patch"))


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
            language = _language_for_permission_context(
                source_file=context.get("source_file"),
                test_file=context.get("test_file"),
            )
            code_fence_fn = getattr(language, "code_fence", None)
            code_fence = code_fence_fn() if callable(code_fence_fn) else ""
            lines.append("Suggested regression test skeleton:")
            lines.append(f"```{code_fence}")
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
            conflict = _bun_test_guidance_conflict(args, context)
            if conflict:
                return conflict
            return None
        if name == "apply_patch" and _patch_touches_only(args, context.get("test_file")):
            conflict = _bun_test_guidance_conflict(args, context)
            if conflict:
                return conflict
            return None
    elif grant == "write_source":
        path = str(args.get("path") or "")
        if name == "read_file" and _same_path(path, context.get("source_file")):
            return None
        if name == "str_replace_editor" and _same_path(path, context.get("source_file")):
            return None
        if name == "apply_patch" and _patch_touches_only(args, context.get("source_file")):
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
        if name == "apply_patch" and _patch_touches_only(args or {}, target):
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
        module_fact_match = re.search(
            r"^(?P<module>[\w@./-]+)\s+(?:source emits|type declarations expose)\s+`",
            fact,
        )
        if module_fact_match:
            needles.append(module_fact_match.group("module"))
        module_match = re.search(r"uses module `(?P<module>[^`]+)`", fact)
        if module_match:
            needles.append(module_match.group("module"))
        if "expected imports:" in fact:
            imports_part = fact.split("expected imports:", 1)[1]
            imports_part = imports_part.split("; applies when", 1)[0]
            needles.extend(re.findall(r"`([^`]+)`", imports_part))
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


def _build_regression_test_skeleton(context: dict) -> str:
    """Ask the active language plugin for a compact first-test shape."""
    language = _language_for_permission_context(
        source_file=context.get("source_file"),
        test_file=context.get("test_file"),
    )
    skeleton_fn = getattr(language, "regression_test_skeleton", None)
    if callable(skeleton_fn):
        return str(skeleton_fn(context) or "")
    return ""


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


def _patch_touches_only(args: dict, target: str | None) -> bool:
    if not target:
        return False
    try:
        paths = apply_patch_touched_paths(str(args.get("patch") or ""))
    except ApplyPatchError:
        return False
    return bool(paths) and all(_same_path(path, target) for path in paths)


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
