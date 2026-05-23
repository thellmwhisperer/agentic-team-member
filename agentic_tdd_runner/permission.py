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
        "referenced_type_shapes": _read_referenced_type_shapes(
            workdir=workdir,
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
            type_shapes = _format_type_shapes(context.get("referenced_type_shapes") or [])
            shape_block = f"\nReferenced type shapes:\n{type_shapes}" if type_shapes else ""
            red_case = _red_case_guidance_block(context)
            implementation_block = _implementation_answer_block(args, context)
            return PermissionReview(
                allowed=True,
                message=(
                    "HARNESS ANSWER: contract facts are already known. "
                    "Do not call read_file for this intent.\n"
                    + _bullet_block(facts)
                    + shape_block
                    + red_case
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
        type_shapes = _format_type_shapes(context.get("referenced_type_shapes") or [])
        if type_shapes:
            lines.append("Referenced type shapes:")
            lines.append(type_shapes)
        red_case_notes = _red_case_guidance(context)
        if red_case_notes:
            lines.append("Regression red-case guidance:")
            lines.extend(f"- {note}" for note in red_case_notes)
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
                "Use deterministic runner/mock feedback. You may read_file this same test path. "
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
        if name == "read_file" and _same_path(path, context.get("test_file")):
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


def consume_grant(
    name: str,
    args: dict | None,
    grant: str | None,
    context: dict | None = None,
) -> str | None:
    if name == "ask_harness":
        return grant
    if grant in {"write_test", "write_source"}:
        path = str((args or {}).get("path") or "")
        target = (context or {}).get("test_file") if grant == "write_test" else (context or {}).get("source_file")
        if name in {"read_file", "create_file", "str_replace_editor"} and _same_path(path, target):
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


def _red_case_guidance_block(context: dict) -> str:
    notes = _red_case_guidance(context)
    if not notes:
        return ""
    return "\nRegression red-case guidance:\n" + "\n".join(f"- {note}" for note in notes)


def _red_case_guidance(context: dict) -> list[str]:
    facts = context.get("contract_facts") or []
    runtime_names = _runtime_arg_names_from_contract_facts(facts)
    fields = _referenced_type_fields(context.get("referenced_type_shapes") or [])
    has_runtime_streak = any("streak" in name.lower() for name in runtime_names)
    streak_fields = [field for field in fields if "streak" in field.lower()]
    cumulative_fields = [field for field in fields if "cumulative" in field.lower()]
    if not (has_runtime_streak and streak_fields and cumulative_fields):
        return []
    return [
        "Use a contrastive callback fixture: keep the runtime streak argument at `0` and include the cumulative field with value `\"6\"`.",
        "Do not make the third callback number `6`; that asserts the happy path and can pass before the source fix.",
        "The red assertion should expect the user-facing output and summary tracking to use the cumulative value `6`.",
    ]


def _referenced_type_fields(shapes: list[dict[str, Any]]) -> list[str]:
    fields: list[str] = []
    for shape in shapes:
        for field in shape.get("fields") or []:
            name = str(field.get("name") or "")
            if name:
                fields.append(name)
    return fields


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
    setter_names = _extract_test_seam_setters(context.get("source_seams") or [])
    setter_setup_lines = _setter_setup_lines(context.get("source_seams") or [])
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
    for setter_name in setter_names:
        lines.append(f'let {setter_name}: ClientModule["{setter_name}"];')
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
    for setter_name in setter_names:
        lines.append(f"  {setter_name} = clientModule.{setter_name};")
    lines.extend([
        "});",
        "",
        'test("covers the reported callback behavior", () => {',
        "  // Arrange issue-grounded doubles and call any generated test-seam setters above.",
    ])
    if fixture_lines:
        lines.extend(f"  {line}" if line else "" for line in fixture_lines)
    else:
        lines.append("  // Build typed callback arguments from the Callback Contract Evidence.")
    if setter_setup_lines:
        lines.extend(f"  {line}" if line else "" for line in setter_setup_lines)
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


def _extract_test_seam_setters(seams: list[str]) -> list[str]:
    setters: list[str] = []
    for seam in seams:
        for match in re.finditer(r"`(__set[A-Za-z0-9_]+)(?:\([^`]*)?`", seam):
            setters.append(match.group(1))
    return sorted(set(setters))


def _setter_setup_lines(seams: list[str]) -> list[str]:
    lines: list[str] = []
    seam_text = "\n".join(seams)
    if "__setClientForTests" in seam_text and "{ say" in seam_text:
        lines.extend([
            'const sayResult: [string] = [""];',
            "const say_spy = mock((_channel: string, _message: string) => Promise.resolve(sayResult));",
            "__setClientForTests({ say: say_spy });",
        ])
    if "__setMemoryManagerForTests" in seam_text and "getEmote" in seam_text:
        lines.append('__setMemoryManagerForTests({ getEmote: () => "teseLove" });')
    if lines:
        lines.append("")
    return lines


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
            or _field_semantic_name_in_facts(name, fact_text)
        ):
            selected.append(field)
    selected = _add_contrastive_value_fields(selected, fields)
    if selected:
        return selected
    return [
        field for field in fields
        if _is_simple_fixture_field(str(field.get("type") or ""))
    ][:3]


def _add_contrastive_value_fields(
    selected: list[dict[str, Any]],
    fields: list[dict[str, Any]],
) -> list[dict[str, Any]]:
    selected_names = {str(field.get("name") or "") for field in selected}
    all_names = [str(field.get("name") or "") for field in fields]
    should_pair_streak = any(_is_streak_month_field(name) for name in selected_names) and any(
        _is_cumulative_month_field(name) for name in all_names
    )
    should_pair_cumulative = any(_is_cumulative_month_field(name) for name in selected_names) and any(
        _is_streak_month_field(name) for name in all_names
    )
    if not (should_pair_streak or should_pair_cumulative):
        return selected

    paired = list(selected)
    for field in fields:
        name = str(field.get("name") or "")
        lowered = name.lower()
        if name in selected_names:
            continue
        if (should_pair_streak and _is_cumulative_month_field(lowered)) or (
            should_pair_cumulative and _is_streak_month_field(lowered)
        ):
            paired.append(field)
            selected_names.add(name)
    return paired


def _field_semantic_name_in_facts(field_name: str, facts_text: str) -> bool:
    lowered_name = field_name.lower()
    lowered_facts = facts_text.lower()
    return (
        (_is_streak_month_field(lowered_name) and "streak" in lowered_facts)
        or (_is_cumulative_month_field(lowered_name) and "cumulative" in lowered_facts)
    )


def _is_streak_month_field(field_name: str) -> bool:
    lowered = field_name.lower()
    return "streak" in lowered and "month" in lowered


def _is_cumulative_month_field(field_name: str) -> bool:
    lowered = field_name.lower()
    return "cumulative" in lowered and "month" in lowered


def _is_simple_fixture_field(type_text: str) -> bool:
    return any(token in type_text for token in ["string", "boolean", "number"]) or re.match(r"^[A-Za-z_$][\w$]*Plan\b", type_text)


def _sample_primitive_value(param_name: str, runtime_name: str, type_name: str, facts: list[str]) -> str:
    name = f"{param_name} {runtime_name}".lower()
    facts_text = "\n".join(facts)
    if type_name == "string":
        if "channel" in name:
            return '"#channel"'
        if "user" in name:
            return '"username"'
        if "message" in name or "msg" in name:
            return '""'
        return '""'
    if type_name == "number":
        if "streak" in name or "msg-param-cumulative-months" in facts_text:
            return "0"
        return "1"
    if type_name == "boolean":
        return "false"
    return "{}"


def _sample_field_value(field_name: str, type_text: str, *, event_name: str) -> str:
    if field_name == "message-type" and event_name:
        return f'"{event_name}"'
    if _is_cumulative_month_field(field_name):
        return '"6"'
    if _is_streak_month_field(field_name):
        return '"0"'
    if field_name == "prime" or "boolean" in type_text:
        return "false"
    if field_name == "planName":
        return '"Tier 1"'
    if field_name == "plan" or type_text.endswith("Plan"):
        return '"1000"'
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
    return (left or "").strip("./") == (right or "").strip("./")


def _looks_like_focused_test_command(command: str, *, context: dict) -> bool:
    test_file = str(context.get("test_file") or "")
    test_command = str(context.get("test_command") or "")
    if test_file and test_file in command:
        return True
    return bool(test_command and command.strip() == test_command)
