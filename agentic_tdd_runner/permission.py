"""Permission-driven runtime gates for the agent loop."""

from __future__ import annotations

import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable

from agentic_tdd_runner.compiler.parser import (
    _extract_target_snippet,
    _find_symbol_line_with_source,
)
from agentic_tdd_runner.cookbook import _find_function_end
from agentic_tdd_runner.languages import get_language


INTENTS = {
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
    contract_facts = _extract_callback_contract_facts(str(episode.get("cookbook_text") or ""))
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
        "runner_facts": runner_facts,
        "source_signature": _extract_source_signature(str(episode.get("cookbook_text") or "")),
        "source_snippet": _read_target_source_snippet(
            workdir=workdir,
            source_file=episode.get("source_file"),
            symbol=episode.get("target_symbol"),
        ),
        "source_imports": _read_relevant_imports_snippet(
            workdir=workdir,
            source_file=episode.get("source_file"),
            contract_facts=contract_facts,
        ),
        "source_seams": _extract_section_bullets(str(episode.get("cookbook_text") or ""), "### Test Seams"),
        "source_mocks": _extract_mock_modules(str(episode.get("cookbook_text") or "")),
        "module_mock_block": _extract_code_block_after(
            str(episode.get("cookbook_text") or ""),
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

    if intent == "understand_contract":
        facts = context.get("contract_facts") or []
        if facts:
            return PermissionReview(
                allowed=True,
                message=(
                    "HARNESS ANSWER: contract facts are already known. "
                    "Do not read source for this intent.\n"
                    + _bullet_block(facts)
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
            "Use the real callable contract and cover the acceptance bullets.",
            "Keep direct invocation and assertions inside the test, not shared setup.",
        ]
        signature = context.get("source_signature")
        if signature:
            lines.append(f"Target callable currently has source signature: `{signature}`.")
        contract_facts = context.get("contract_facts") or []
        if contract_facts:
            lines.append("Contract facts:")
            lines.extend(f"- {fact}" for fact in contract_facts)
        seams = context.get("source_seams") or []
        if seams:
            lines.append("Available test seams:")
            lines.extend(f"- {seam}" for seam in seams)
        mocks = context.get("source_mocks") or []
        if mocks:
            lines.append("Module mocks to use before source import:")
            lines.extend(f"- {mock}" for mock in mocks[:8])
        runner_facts = context.get("runner_facts") or []
        if runner_facts:
            lines.append("Runner facts:")
            lines.extend(f"- {fact}" for fact in runner_facts[:8])
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
                "Use deterministic runner/mock feedback. Do not edit source behavior in this phase."
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
            "Do not call read_file or rg for this edit; use the exact current target snippet below.",
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
    is_test_file_path: Callable[[str], bool],
) -> PermissionReview | None:
    if name == "ask_harness":
        return None
    if grant is None:
        return PermissionReview(
            allowed=False,
            message=(
                "PERMISSION REQUIRED: call ask_harness before using tools. "
                "Declare one intent: understand_contract, write_regression_test, "
                "repair_test_setup, edit_source, run_test, or done."
            ),
        )
    if grant == "read_contract":
        if name in {"read_file", "rg", "run_command"}:
            return None
    elif grant == "write_test":
        path = str(args.get("path") or "")
        if name in {"create_file", "str_replace_editor"} and _same_path(path, context.get("test_file")):
            return None
    elif grant == "write_source":
        path = str(args.get("path") or "")
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


def consume_grant(name: str, grant: str | None) -> str | None:
    if name == "ask_harness":
        return grant
    return None


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


def _import_needles(contract_facts: list[str]) -> list[str]:
    needles: list[str] = []
    for fact in contract_facts:
        if "tmi.js" in fact:
            needles.append("tmi.js")
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
    facts = "\n".join(context.get("contract_facts") or [])
    if (
        context.get("target_symbol") != "handleResub"
        or "tmi.js source emits `resub" not in facts
        or "msg-param-cumulative-months" not in facts
    ):
        return ""

    import_path = context.get("source_import_path") or "./client"
    mock_block = str(context.get("module_mock_block") or "").strip()
    spy_names = _extract_spy_names(mock_block)
    lines = [
        'import { beforeEach, describe, expect, mock, test } from "bun:test";',
        'import type { SubMethods, SubUserstate } from "tmi.js";',
        "",
    ]
    if mock_block:
        lines.extend(mock_block.splitlines())
        lines.append("")
    else:
        lines.extend([
            "// Keep mock.module(...) registrations above the dynamic source import.",
            "// Use the module mocks from the cookbook for logger, stream-summary, token, discord, and search.",
            "",
        ])

    lines.extend([
        f'type ClientModule = typeof import("{import_path}");',
        "type ResubHandler = (",
        "  channel: string,",
        "  username: string,",
        "  streakMonths: number,",
        "  message: string,",
        "  userstate: SubUserstate,",
        "  methods: SubMethods,",
        ") => void;",
        "",
        "let handleResub: ClientModule[\"handleResub\"];",
        "let setClientForTests: ClientModule[\"__setClientForTests\"];",
        "let setMemoryManagerForTests: ClientModule[\"__setMemoryManagerForTests\"];",
        "",
        "beforeEach(async () => {",
    ])
    if spy_names:
        lines.append(f"  for (const spy of [{', '.join(spy_names)}]) spy.mockClear();")
    else:
        lines.append("  // mockClear every *_spy from the cookbook here.")
    lines.extend([
        f'  const clientModule = await import("{import_path}");',
        "  handleResub = clientModule.handleResub;",
        "  setClientForTests = clientModule.__setClientForTests;",
        "  setMemoryManagerForTests = clientModule.__setMemoryManagerForTests;",
        "});",
        "",
        'test("uses cumulative resub months instead of streak months", () => {',
        '  const say = mock((_channel: string, _message: string): Promise<[string]> => Promise.resolve(["#channel"]));',
        '  const getEmote = mock((): string => "teseoLove");',
        "  setClientForTests({ say });",
        "  setMemoryManagerForTests({ getEmote });",
        "",
        "  const resubHandler: ResubHandler = handleResub;",
        "  const userstate: SubUserstate = {",
        '    "message-type": "resub",',
        '    "msg-param-streak-months": "0",',
        '    "msg-param-cumulative-months": "6",',
        "  };",
        '  const methods: SubMethods = { prime: false, plan: "1000", planName: "Tier 1" };',
        "",
        '  resubHandler("#channel", "username", 0, "", userstate, methods);',
        "",
        '  expect(say).toHaveBeenCalledWith("#channel", expect.stringContaining("6 meses"));',
        '  expect(streamSummaryManager_trackResub_spy).toHaveBeenCalledWith("username", 6);',
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


def _bullet_block(lines: list[str]) -> str:
    return "\n".join(f"- {line}" for line in lines)


def _same_path(left: str | None, right: str | None) -> bool:
    return (left or "").strip("./") == (right or "").strip("./")


def _looks_like_focused_test_command(command: str, *, context: dict) -> bool:
    test_file = str(context.get("test_file") or "")
    test_command = str(context.get("test_command") or "")
    if test_file and test_file in command:
        return True
    return bool(test_command and command.strip() == test_command)
