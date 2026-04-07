"""Deterministic P1 -> P2 contract builder for the TDD runner."""
from __future__ import annotations

from copy import deepcopy
import json
from pathlib import PurePosixPath
import re


SCHEMA_VERSION = "p1p2.v1"

_CALL_RE = re.compile(r"\b([A-Za-z_]\w*)\.([A-Za-z_]\w*)\s*\(")
_TS_NAMED_IMPORT_RE = re.compile(
    r"^\s*import\s*{([^}]+)}\s*from\s*['\"]([^'\"]+)['\"]\s*;?",
    re.MULTILINE,
)
_TS_DEFAULT_IMPORT_RE = re.compile(
    r"^\s*import\s+([A-Za-z_]\w*)\s+from\s+['\"]([^'\"]+)['\"]\s*;?",
    re.MULTILINE,
)
_TS_NAMESPACE_IMPORT_RE = re.compile(
    r"^\s*import\s+\*\s+as\s+([A-Za-z_]\w*)\s+from\s+['\"]([^'\"]+)['\"]\s*;?",
    re.MULTILINE,
)
_PY_FROM_IMPORT_RE = re.compile(r"^\s*from\s+([.\w]+)\s+import\s+(.+)$", re.MULTILINE)
_PY_IMPORT_RE = re.compile(r"^\s*import\s+(.+)$", re.MULTILINE)
_TOP_LEVEL_TS_ASSIGN_RE = re.compile(
    r"^(?:export\s+)?(const|let|var)\s+([A-Za-z_]\w*)"
    r"(?:\s*:\s*([^=;]+))?(?:\s*=\s*(.+?))?;?\s*$"
)
_TOP_LEVEL_PY_ASSIGN_RE = re.compile(r"^([A-Za-z_]\w*)\s*=\s*(.+)\s*$")
_SECTION_HEADERS = {
    "bug summary": "bug_summary",
    "source file": "source_file",
    "source path": "source_file",
    "source lines": "source_lines",
    "target symbol": "target_symbol",
    "function signature": "function_signature",
    "source snippet": "source_snippet",
    "dependencies to mock": "dependencies",
    "dependencies": "dependencies",
    "module load dependencies": "module_load_dependencies",
    "execution dependencies": "execution_dependencies",
    "test file reference pattern": "pattern_files",
    "pattern files": "pattern_files",
    "suggested test file": "test_file",
    "test file": "test_file",
    "test runner": "runner",
    "exported": "exported",
    "assertion surface": "assertion_surface",
    "expected behavior": "expected_behavior",
    "gaps": "gaps",
}
_ASSERTION_MEMBER_SCORES = {
    "say": 120,
    "send": 110,
    "reply": 105,
    "emit": 100,
    "publish": 95,
    "write": 90,
    "respond": 85,
    "dispatch": 80,
    "save": 75,
    "insert": 72,
    "update": 70,
    "delete": 68,
    "track": 30,
    "report": 25,
    "event": 20,
    "info": 10,
    "warn": 8,
    "error": 8,
    "get": -10,
    "find": -12,
    "load": -12,
}


def build_p1p2_contract(facts):
    """Build a deterministic P1 -> P2 contract from normalized discovery facts."""
    target = deepcopy(facts["target"])
    test_file = deepcopy(facts["test_file"])
    pre_test_source_edits = deepcopy(facts.get("pre_test_source_edits", []))
    module_load_dependencies = deepcopy(facts.get("module_load_dependencies", []))
    execution_dependencies = deepcopy(facts.get("execution_dependencies", []))
    injection_plan = deepcopy(facts.get("injection_plan", []))
    assertion_surface = deepcopy(facts["assertion_surface"])
    pattern_files = deepcopy(facts.get("pattern_files", []))
    gaps = deepcopy(facts.get("gaps", []))

    test_file.setdefault(
        "source_import_path",
        _compute_source_import_path(test_file["path"], target["source_path"]),
    )

    gaps.extend(_gaps_from_injection_plan(injection_plan))
    gaps = _dedupe_gaps(gaps)

    contract = {
        "schema_version": SCHEMA_VERSION,
        "target": target,
        "test_file": test_file,
        "pre_test_source_edits": pre_test_source_edits,
        "module_load_dependencies": module_load_dependencies,
        "execution_dependencies": execution_dependencies,
        "injection_plan": injection_plan,
        "assertion_surface": assertion_surface,
        "pattern_files": pattern_files,
        "gaps": gaps,
    }
    contract["ready_for_p2"] = not _has_blocking_gaps(gaps)
    contract["scaffold"] = _build_scaffold(contract)
    return contract


def compile_p1_handoff(handoff, tdd):
    """Compile a P1 handoff summary into a structured P1 -> P2 contract."""
    parsed = _parse_p1_handoff(handoff)
    target = deepcopy(parsed["target"])
    source_result = tdd.read_file(target["source_path"])
    source_text = source_result["content"]

    target.setdefault("kind", "function")
    target.setdefault("line_start", parsed["target"].get("line_start") or 1)
    target.setdefault("line_end", parsed["target"].get("line_end") or len(source_text.splitlines()))
    if not target.get("signature") or "\n" in target.get("signature", "") or "{" in target.get("signature", ""):
        target["signature"] = _extract_signature(source_text, target["symbol"])

    if target["line_start"] == 1 and target["line_end"] == len(source_text.splitlines()):
        symbol_line = _find_symbol_line(source_text, target["symbol"])
        if symbol_line:
            target["line_start"] = symbol_line
            target["line_end"] = max(symbol_line, target["line_end"])

    snippet = _extract_target_snippet(source_text, target)
    imports = _parse_import_bindings(source_text)
    assignments = _parse_top_level_assignments(source_text)

    module_load_dependencies = []
    execution_dependencies = []
    injection_plan = []
    seen_module = set()
    seen_execution = set()
    seen_injection = set()
    for dep in parsed["dependencies"]:
        compiled = _compile_dependency(dep, snippet, imports, assignments)
        module_entry = compiled.get("module_load")
        if module_entry and module_entry["binding"] not in seen_module:
            module_load_dependencies.append(module_entry)
            seen_module.add(module_entry["binding"])
        execution_entry = compiled.get("execution")
        if execution_entry and execution_entry["binding"] not in seen_execution:
            execution_dependencies.append(execution_entry)
            seen_execution.add(execution_entry["binding"])
        injection_entry = compiled.get("injection")
        if injection_entry and injection_entry["binding"] not in seen_injection:
            injection_plan.append(injection_entry)
            seen_injection.add(injection_entry["binding"])

    module_load_dependencies = _enrich_module_load_dependencies(
        module_load_dependencies,
        imports,
        source_text,
    )

    assertion_surface, assertion_gaps = _build_assertion_surface(
        parsed.get("assertion_surface"),
        snippet,
        target["symbol"],
    )
    inferred_binding = assertion_surface.get("binding")
    if (
        inferred_binding
        and inferred_binding != target["symbol"]
        and inferred_binding not in seen_module
        and inferred_binding not in seen_execution
    ):
        compiled = _compile_dependency(
            {"binding": inferred_binding},
            snippet,
            imports,
            assignments,
        )
        execution_entry = compiled.get("execution")
        if execution_entry and execution_entry["binding"] not in seen_execution:
            execution_dependencies.append(execution_entry)
            seen_execution.add(execution_entry["binding"])
        injection_entry = compiled.get("injection")
        if injection_entry and injection_entry["binding"] not in seen_injection:
            injection_plan.append(injection_entry)
            seen_injection.add(injection_entry["binding"])
    seam_edits = _realize_generated_test_seams(
        target["source_path"],
        source_text,
        assignments,
        execution_dependencies,
        injection_plan,
    )
    pre_test_source_edits = _build_pre_test_source_edits(
        target,
        source_text,
        exported_hint=parsed.get("exported"),
        seam_edits=seam_edits,
    )
    pattern_files = _build_pattern_files(parsed.get("pattern_files", []), tdd)
    gaps = deepcopy(parsed.get("gaps", []))
    gaps.extend(assertion_gaps)

    test_path = parsed["test_file"].get("path") or _default_test_path(
        target["source_path"], target["symbol"]
    )
    runner = parsed["test_file"].get("runner") or _infer_runner(target["source_path"])

    facts = {
        "target": target,
        "test_file": {
            "path": test_path,
            "runner": runner,
        },
        "pre_test_source_edits": pre_test_source_edits,
        "module_load_dependencies": module_load_dependencies,
        "execution_dependencies": execution_dependencies,
        "injection_plan": injection_plan,
        "assertion_surface": assertion_surface,
        "pattern_files": pattern_files,
        "gaps": gaps,
    }
    return build_p1p2_contract(facts)


def render_p2_user_prompt(user_prompt, contract):
    """Render the P2 user prompt from the structured contract."""
    prompt_contract = deepcopy(contract)
    rendered_test = prompt_contract["scaffold"].pop("rendered_test", "")
    contract_json = json.dumps(prompt_contract, indent=2, sort_keys=True)
    language = "python" if contract["test_file"]["runner"] == "pytest" else "ts"
    return (
        f"{user_prompt}\n\n"
        f"P1 contract:\n```json\n{contract_json}\n```\n\n"
        f"P2 scaffold for {contract['test_file']['path']}:\n"
        f"```{language}\n{rendered_test}\n```\n\n"
        f"Use exactly this test path: {contract['test_file']['path']}.\n"
        "YOUR TASK: fill the TODO slots, keep the scaffold structure, "
        "write the test file at that path, and RUN it. Do not rediscover import paths, "
        "mock shapes, or assertion surfaces unless the contract is wrong."
    )


def _compute_source_import_path(test_path, source_path):
    if source_path.endswith(".py"):
        return _python_import_path(source_path)

    test_dir = PurePosixPath(test_path).parent
    source_no_ext = _strip_suffix(PurePosixPath(source_path))
    relative = PurePosixPath(
        _relpath_posix(source_no_ext.as_posix(), test_dir.as_posix())
    ).as_posix()
    if not relative.startswith("."):
        relative = f"./{relative}"
    return relative


def _python_import_path(source_path):
    source = PurePosixPath(source_path)
    parts = list(source.parts)
    if parts and parts[-1].endswith(".py"):
        parts[-1] = parts[-1][:-3]
    if parts and parts[-1] == "__init__":
        parts = parts[:-1]
    return ".".join(part for part in parts if part)


def _strip_suffix(path):
    return path.with_suffix("")


def _relpath_posix(target, start):
    target_parts = PurePosixPath(target).parts
    start_parts = PurePosixPath(start).parts

    common = 0
    for left, right in zip(target_parts, start_parts):
        if left != right:
            break
        common += 1

    up = [".."] * (len(start_parts) - common)
    down = list(target_parts[common:])
    parts = up + down
    return "." if not parts else "/".join(parts)


def _gaps_from_injection_plan(injection_plan):
    gaps = []
    for entry in injection_plan:
        if entry.get("blocks_p2_if_missing") and not entry.get("seam_available", False):
            gaps.append(
                {
                    "kind": "missing_test_seam",
                    "message": (
                        f"{entry['binding']} requires injection strategy "
                        f"'{entry['strategy']}' but no framework seam is available"
                    ),
                    "owner": "framework",
                }
            )
    return gaps


def _dedupe_gaps(gaps):
    seen = set()
    ordered = []
    for gap in gaps:
        key = (gap.get("kind"), gap.get("message"), gap.get("owner"))
        if key in seen:
            continue
        seen.add(key)
        ordered.append(gap)
    return ordered


def _has_blocking_gaps(gaps):
    blocking_owners = {"framework", "compiler_pass"}
    return any(gap.get("owner") in blocking_owners for gap in gaps)


def _build_scaffold(contract):
    runner = contract["test_file"]["runner"]
    if runner == "bun:test":
        return _build_bun_scaffold(contract)
    if runner == "pytest":
        return _build_pytest_scaffold(contract)
    raise ValueError(f"unsupported runner: {runner}")


def _build_bun_scaffold(contract):
    target = contract["target"]
    target_name = target["symbol"]
    source_import_path = contract["test_file"]["source_import_path"]
    module_load_dependencies = contract["module_load_dependencies"]
    execution_dependencies = contract["execution_dependencies"]
    injection_plan = {entry["binding"]: entry for entry in contract["injection_plan"]}
    assertion_surface = contract["assertion_surface"]
    params = _parse_signature_params(target.get("signature", ""))

    imports_block = "import { describe, expect, mock, test } from 'bun:test';"
    module_mocks_block = _render_module_mocks(module_load_dependencies)
    import_names = [target_name]
    for binding, plan in injection_plan.items():
        setter_name = plan.get("setter_name")
        if setter_name and setter_name not in import_names:
            import_names.append(setter_name)
    if module_mocks_block:
        module_mocks_block = (
            f"{module_mocks_block}\n\n"
            f"const {{ {', '.join(import_names)} }} = await import('{source_import_path}');"
        )
    else:
        module_mocks_block = f"import {{ {', '.join(import_names)} }} from '{source_import_path}';"

    arrange_lines = []
    todo_slots = []
    for dep in execution_dependencies:
        binding = dep["binding"]
        strategy = dep["strategy"]
        observed = dep.get("observed_members") or []
        plan = injection_plan.get(binding, {})
        if strategy == "set_test_seam":
            setter_name = plan.get("setter_name")
            if setter_name:
                double_name = f"{binding}_test_double"
                shape_lines = []
                for member in observed:
                    spy_name = f"{binding}_{member}_spy"
                    arrange_lines.append(f"const {spy_name} = mock(() => undefined);")
                    shape_lines.append(f"  {member}: {spy_name},")
                if shape_lines:
                    arrange_lines.append(
                        "const "
                        f"{double_name} = {{\n" + "\n".join(shape_lines) + "\n};"
                    )
                else:
                    arrange_lines.append(f"const {double_name} = {{}};")
                arrange_lines.append(f"{setter_name}({double_name});")
            else:
                member = observed[0] if observed else "value"
                spy_name = f"{binding}_{member}_spy"
                arrange_lines.append(f"const {spy_name} = mock(() => undefined);")
                arrange_lines.append(
                    f"// TODO: inject {spy_name} through the framework seam for {binding}"
                )
                todo_slots.append(f"inject_{binding}_seam")

    for param in params:
        arrange_lines.append(f"const {param} = /* TODO */;")
        todo_slots.append(f"value_for_{param}")

    expected_name = "expected_message"
    if assertion_surface["kind"] != "outbound_call_arguments":
        expected_name = "expected_value"
    arrange_lines.append(f"const {expected_name} = /* TODO */;")
    todo_slots.append("expected_assertion_value")

    act_args = ", ".join(params)
    act_block = f"{target_name}({act_args});"
    assert_block = _render_assertion(assertion_surface, runner="bun:test")
    if "TODO" in assert_block:
        todo_slots.append("assertion_surface_binding")

    return {
        "imports_block": imports_block,
        "module_mocks_block": module_mocks_block,
        "arrange_block": "\n".join(arrange_lines),
        "act_block": act_block,
        "assert_block": assert_block,
        "todo_slots": todo_slots,
        "rendered_test": _render_full_test(
            imports_block=imports_block,
            module_mocks_block=module_mocks_block,
            target_name=target_name,
            arrange_block="\n".join(arrange_lines),
            act_block=act_block,
            assert_block=assert_block,
            runner="bun:test",
        ),
    }


def _build_pytest_scaffold(contract):
    target = contract["target"]
    target_name = target["symbol"]
    source_import_path = contract["test_file"]["source_import_path"]
    execution_dependencies = contract["execution_dependencies"]
    injection_plan = {entry["binding"]: entry for entry in contract["injection_plan"]}
    assertion_surface = contract["assertion_surface"]
    params = _parse_signature_params(target.get("signature", ""))

    import_names = [target_name]
    for binding, plan in injection_plan.items():
        setter_name = plan.get("setter_name")
        if setter_name and setter_name not in import_names:
            import_names.append(setter_name)
    imports_block = (
        "from unittest.mock import Mock\n"
        f"from {source_import_path} import {', '.join(import_names)}"
    )
    arrange_lines = []
    todo_slots = []

    for dep in execution_dependencies:
        binding = dep["binding"]
        strategy = dep["strategy"]
        observed = dep.get("observed_members") or []
        plan = injection_plan.get(binding, {})
        if strategy == "set_test_seam":
            setter_name = plan.get("setter_name")
            if setter_name:
                double_name = f"{binding}_test_double"
                shape_lines = []
                for member in observed:
                    spy_name = f"{binding}_{member}_spy"
                    arrange_lines.append(f"{spy_name} = Mock()")
                    shape_lines.append(f"    '{member}': {spy_name},")
                if shape_lines:
                    arrange_lines.append(
                        f"{double_name} = {{\n" + "\n".join(shape_lines) + "\n}"
                    )
                else:
                    arrange_lines.append(f"{double_name} = {{}}")
                arrange_lines.append(f"{setter_name}({double_name})")
            else:
                member = observed[0] if observed else "value"
                spy_name = f"{binding}_{member}_spy"
                arrange_lines.append(f"{spy_name} = Mock()")
                arrange_lines.append(
                    f"# TODO: inject {spy_name} through the framework seam for {binding}"
                )
                todo_slots.append(f"inject_{binding}_seam")

    for param in params:
        arrange_lines.append(f"{param} = ...")
        todo_slots.append(f"value_for_{param}")

    expected_name = "expected_value"
    arrange_lines.append(f"{expected_name} = ...")
    todo_slots.append("expected_assertion_value")

    act_args = ", ".join(params)
    act_block = f"{target_name}({act_args})"
    assert_block = _render_assertion(assertion_surface, runner="pytest")
    if "TODO" in assert_block:
        todo_slots.append("assertion_surface_binding")

    return {
        "imports_block": imports_block,
        "module_mocks_block": "",
        "arrange_block": "\n".join(arrange_lines),
        "act_block": act_block,
        "assert_block": assert_block,
        "todo_slots": todo_slots,
        "rendered_test": _render_full_test(
            imports_block=imports_block,
            module_mocks_block="",
            target_name=target_name,
            arrange_block="\n".join(arrange_lines),
            act_block=act_block,
            assert_block=assert_block,
            runner="pytest",
        ),
    }


def _parse_signature_params(signature):
    match = re.search(r"\((.*)\)", signature)
    if not match:
        return []
    raw = match.group(1).strip()
    if not raw:
        return []
    params = []
    for piece in raw.split(","):
        name = piece.strip().split(":", 1)[0].strip()
        name = name.split("=", 1)[0].strip()
        if name:
            params.append(name.lstrip("*"))
    return params


def _render_module_mocks(dependencies):
    blocks = []
    for dep in _merge_module_mock_dependencies(dependencies):
        if dep.get("strategy") != "mock_module":
            continue
        module_path = dep["source_module"]
        required_shape = dep.get("required_shape", {})
        render_hint = dep.get("render_hint")
        body = _render_module_shape(required_shape, render_hint=render_hint, depth=2)
        blocks.append(
            f"mock.module('{module_path}', () => (\n"
            f"{{\n{body}\n}}\n"
            f"));"
        )
    return "\n\n".join(blocks)


def _render_module_shape(shape, *, render_hint=None, depth=0):
    lines = []
    indent = " " * depth
    for key, value in shape.items():
        rendered = _render_binding_value(key, value, render_hint=render_hint, depth=depth)
        lines.append(f"{indent}{key}: {rendered},")
    return "\n".join(lines)


def _render_binding_value(key, value, *, render_hint=None, depth=0):
    indent = " " * depth
    inner_indent = " " * (depth + 2)
    if isinstance(value, dict):
        inner = _render_module_shape(value, render_hint=render_hint, depth=depth + 2)
        return f"{{\n{inner}\n{indent}}}"
    if isinstance(value, list):
        if render_hint == "module_object":
            members = "\n".join(
                f"{inner_indent}{member}: mock(() => {{}})," for member in value
            )
            if not members:
                return "{}"
            return f"{{\n{members}\n{indent}}}"
        if key.startswith(("get", "create", "build", "make")):
            members = "\n".join(
                f"{inner_indent}{member}: mock(() => {{}})," for member in value
            )
            if not members:
                return "() => ({})"
            return "() => ({\n" + members + f"\n{indent}}})"
        if not value:
            return "{}"
        members = "\n".join(
            f"{inner_indent}{member}: mock(() => {{}})," for member in value
        )
        return f"{{\n{members}\n{indent}}}"
    if value == "function":
        return "mock(() => {})"
    if value == "value":
        return "undefined"
    return repr(value)


def _render_assertion(assertion_surface, *, runner):
    kind = assertion_surface["kind"]
    binding = assertion_surface["binding"]
    member = assertion_surface["member"]
    if kind == "outbound_call_arguments":
        spy_name = f"{binding}_{member}_spy"
        if runner == "pytest":
            return (
                f"{spy_name}.assert_called_with("
                "..., expected_value)"
            )
        return (
            f"expect({spy_name}).toHaveBeenCalledWith("
            "/* TODO: channel */, expected_message);"
        )
    if kind == "return_value":
        if runner == "pytest":
            return "assert result == expected_value"
        return "expect(result).toBe(expected_value);"
    return f"// TODO: assert {kind} on {binding}.{member}"


def _render_full_test(
    *,
    imports_block,
    module_mocks_block,
    target_name,
    arrange_block,
    act_block,
    assert_block,
    runner,
):
    if runner == "pytest":
        parts = [imports_block, ""]
        if module_mocks_block:
            parts.extend([module_mocks_block, ""])
        parts.extend(
            [
                f"def test_{target_name}():",
                _indent_block(arrange_block, 4),
                "",
                _indent_block(f"result = {act_block}", 4),
                "",
                _indent_block(assert_block, 4),
                "",
            ]
        )
        return "\n".join(parts).rstrip() + "\n"

    return (
        f"{imports_block}\n\n"
        f"{module_mocks_block}\n\n"
        f"describe('{target_name}', () => {{\n"
        f"  test('TODO behavior', () => {{\n"
        f"{_indent_block(arrange_block, 4)}\n\n"
        f"{_indent_block(act_block, 4)}\n\n"
        f"{_indent_block(assert_block, 4)}\n"
        f"  }});\n"
        f"}});\n"
    )


def _indent_block(text, spaces):
    indent = " " * spaces
    return "\n".join(f"{indent}{line}" if line else "" for line in text.splitlines())


def _parse_p1_handoff(text):
    payload = _extract_json_payload(text)
    if payload is not None:
        return _normalize_json_payload(payload)
    return _parse_narrative_handoff(text)


def _extract_json_payload(text):
    marker_match = re.search(r"P1_FACTS:\s*", text)
    start = marker_match.end() if marker_match else 0
    decoder = json.JSONDecoder()
    idx = text.find("{", start)
    while idx != -1:
        try:
            obj, _ = decoder.raw_decode(text, idx)
        except json.JSONDecodeError:
            idx = text.find("{", idx + 1)
            continue
        if isinstance(obj, dict) and (
            "target" in obj or obj.get("schema_version") == SCHEMA_VERSION
        ):
            return obj
        idx = text.find("{", idx + 1)
    return None


def _normalize_json_payload(payload):
    if payload.get("schema_version") == SCHEMA_VERSION:
        return {
            "target": deepcopy(payload["target"]),
            "test_file": deepcopy(payload["test_file"]),
            "dependencies": _deps_from_contract(payload),
            "pattern_files": deepcopy(payload.get("pattern_files", [])),
            "assertion_surface": deepcopy(payload["assertion_surface"]),
            "gaps": deepcopy(payload.get("gaps", [])),
            "exported": True,
        }

    target = deepcopy(payload["target"])
    test_file = deepcopy(payload.get("test_file", {}))
    dependencies = deepcopy(payload.get("dependencies", []))
    pattern_files = deepcopy(payload.get("pattern_files", []))
    assertion_surface = deepcopy(payload.get("assertion_surface"))
    gaps = deepcopy(payload.get("gaps", []))
    exported = payload.get("exported")
    return {
        "target": target,
        "test_file": test_file,
        "dependencies": dependencies,
        "pattern_files": pattern_files,
        "assertion_surface": assertion_surface,
        "gaps": gaps,
        "exported": exported,
    }


def _deps_from_contract(contract):
    deps = []
    for entry in contract.get("module_load_dependencies", []):
        deps.append(
            {
                "binding": entry["binding"],
                "source_module": entry.get("source_module"),
                "phase": "module_load",
                "required_shape": deepcopy(entry.get("required_shape", {})),
                "strategy": entry.get("strategy"),
                "origin_kind": entry.get("origin_kind"),
                "render_hint": entry.get("render_hint"),
                "observed_members": _required_shape_members(entry.get("required_shape", {})),
            }
        )
    injection_by_binding = {
        entry["binding"]: entry for entry in contract.get("injection_plan", [])
    }
    for entry in contract.get("execution_dependencies", []):
        injection = injection_by_binding.get(entry["binding"], {})
        deps.append(
            {
                "binding": entry["binding"],
                "source_module": entry.get("source_module"),
                "phase": "execution",
                "required_shape": deepcopy(entry.get("required_shape", {})),
                "strategy": injection.get("strategy", entry.get("strategy")),
                "origin_kind": entry.get("origin_kind"),
                "observed_members": deepcopy(entry.get("observed_members", [])),
                "seam_available": injection.get("seam_available"),
                "blocks_p2_if_missing": injection.get("blocks_p2_if_missing"),
            }
        )
    return deps


def _parse_narrative_handoff(text):
    sections = _parse_sections(text)
    source_path, line_start, line_end = _parse_source_file_section(sections)
    if not source_path:
        source_path = _parse_backticked_path(text)
    if line_start is None or line_end is None:
        line_match = re.search(r"lines?\s+(\d+)\s*[-:]\s*(\d+)", text)
        if line_match:
            line_start = int(line_match.group(1))
            line_end = int(line_match.group(2))

    signature = _clean_code_block("\n".join(sections.get("function_signature", [])))
    if not signature:
        sig_match = re.search(
            r"Function Signature:\s*```[a-zA-Z0-9_-]*\n(.*?)```",
            text,
            re.DOTALL,
        )
        if sig_match:
            signature = _clean_code_block(sig_match.group(1))
    symbol = (
        _parse_symbol_from_signature(signature)
        or _clean_inline_value("\n".join(sections.get("target_symbol", [])))
        or _guess_symbol_from_text(text)
    )

    deps = []
    dependency_lines = sections.get("dependencies", [])
    if not dependency_lines:
        dependency_lines = [line for line in text.splitlines() if " - from " in line]
    deps.extend(_parse_dependency_lines(dependency_lines, phase_hint=None))
    deps.extend(
        _parse_dependency_lines(
            sections.get("module_load_dependencies", []), phase_hint="module_load"
        )
    )
    deps.extend(
        _parse_dependency_lines(
            sections.get("execution_dependencies", []), phase_hint="execution"
        )
    )

    pattern_lines = sections.get("pattern_files", [])
    if not pattern_lines:
        pattern_lines = [line for line in text.splitlines() if ".test." in line or "_test.py" in line]
    pattern_files = _parse_pattern_file_lines(pattern_lines)
    test_file_path = _parse_backticked_path("\n".join(sections.get("test_file", [])))
    runner = _clean_inline_value("\n".join(sections.get("runner", []))) or None
    exported = _parse_boolish("\n".join(sections.get("exported", [])))
    assertion_surface = _parse_assertion_surface_section(sections.get("assertion_surface", []))
    gaps = _parse_gap_lines(sections.get("gaps", []))

    return {
        "target": {
            "symbol": symbol,
            "kind": "function",
            "source_path": source_path,
            "line_start": line_start,
            "line_end": line_end,
            "signature": signature,
        },
        "test_file": {
            "path": test_file_path,
            "runner": runner,
        },
        "dependencies": deps,
        "pattern_files": pattern_files,
        "assertion_surface": assertion_surface,
        "gaps": gaps,
        "exported": exported,
    }


def _parse_sections(text):
    sections = {}
    current = None
    for raw_line in text.splitlines():
        line = raw_line.rstrip()
        match = re.match(r"^\*\*(.+?)\*\*:\s*(.*)$", line)
        if not match:
            match = re.match(r"^([A-Za-z][A-Za-z /_-]+):\s*(.*)$", line)
        if match:
            label = match.group(1).strip().lower()
            if label in _SECTION_HEADERS:
                current = _SECTION_HEADERS[label]
                sections.setdefault(current, [])
                remainder = match.group(2).strip()
                if remainder:
                    sections[current].append(remainder)
                continue
        if current:
            sections.setdefault(current, []).append(line)
    return sections


def _parse_source_file_section(sections):
    combined = "\n".join(sections.get("source_file", []) + sections.get("source_lines", []))
    path = _parse_backticked_path(combined)
    line_match = re.search(r"lines?\s+(\d+)\s*[-:]\s*(\d+)", combined)
    line_start = int(line_match.group(1)) if line_match else None
    line_end = int(line_match.group(2)) if line_match else None
    return path, line_start, line_end


def _parse_dependency_lines(lines, *, phase_hint):
    deps = []
    for line in lines:
        match = re.search(r"`([^`]+)`\s*-\s*from\s*`([^`]+)`", line)
        if not match:
            continue
        binding = match.group(1).strip()
        source_module = match.group(2).strip()
        strategy = _parse_keyed_token(line, "strategy")
        origin_kind = _parse_keyed_token(line, "origin_kind")
        seam_available = _parse_boolish(_parse_keyed_token(line, "seam_available"))
        blocks_p2 = _parse_boolish(_parse_keyed_token(line, "blocks_p2_if_missing"))
        deps.append(
            {
                "binding": binding,
                "source_module": source_module,
                "phase": phase_hint,
                "strategy": strategy,
                "origin_kind": origin_kind,
                "seam_available": seam_available,
                "blocks_p2_if_missing": blocks_p2,
            }
        )
    return deps


def _parse_pattern_file_lines(lines):
    entries = []
    for line in lines:
        path = _parse_backticked_path(line)
        if not path:
            continue
        why = line.split(" - ", 1)[1].strip() if " - " in line else "pattern from P1 handoff"
        entries.append({"path": path, "why_selected": why})
    return entries


def _parse_gap_lines(lines):
    gaps = []
    for line in lines:
        text = line.lstrip("-* ").strip()
        if not text:
            continue
        gaps.append(
            {
                "kind": "handoff_gap",
                "message": text,
                "owner": "compiler_pass",
            }
        )
    return gaps


def _parse_assertion_surface_section(lines):
    text = _clean_code_block("\n".join(lines)).strip()
    if not text:
        return None
    payload = _extract_json_payload(text)
    if isinstance(payload, dict) and {"kind", "binding", "member"} <= payload.keys():
        return payload
    kind = _parse_keyed_token(text, "kind")
    binding = _parse_keyed_token(text, "binding")
    member = _parse_keyed_token(text, "member")
    assertion_shape = _parse_keyed_token(text, "assertion_shape")
    if kind and binding and member:
        return {
            "kind": kind,
            "binding": binding,
            "member": member,
            "assertion_shape": assertion_shape or "",
        }
    call_match = re.search(r"([A-Za-z_]\w*)\.([A-Za-z_]\w*)", text)
    if call_match:
        return {
            "kind": "outbound_call_arguments",
            "binding": call_match.group(1),
            "member": call_match.group(2),
            "assertion_shape": "toHaveBeenCalledWith(...)",
        }
    return None


def _compile_dependency(dep, snippet, imports, assignments):
    binding = dep["binding"]
    observed_members = dep.get("observed_members") or _observed_members(snippet, binding)
    required_shape = dep.get("required_shape") or _required_shape_from_members(observed_members)
    source_module = dep.get("source_module")
    phase = dep.get("phase")
    strategy = dep.get("strategy")
    origin_kind = dep.get("origin_kind")
    render_hint = dep.get("render_hint")

    assignment = assignments.get(binding)
    direct_import = imports.get(binding)
    factory_import = None
    if assignment and assignment.get("called_symbol"):
        factory_import = imports.get(assignment["called_symbol"])

    if phase == "module_load" or (
        phase is None and (factory_import or direct_import)
    ):
        if factory_import:
            module_name = source_module or factory_import["source_module"]
            export_name = assignment["called_symbol"]
            required_shape = _normalize_factory_required_shape(
                required_shape,
                export_name=export_name,
                observed_members=observed_members,
            )
            return {
                "module_load": {
                    "binding": binding,
                    "origin_kind": origin_kind or "factory_result",
                    "source_module": module_name,
                    "required_shape": required_shape,
                    "strategy": strategy or "mock_module",
                    "render_hint": render_hint,
                }
            }
        if direct_import:
            module_name = source_module or direct_import["source_module"]
            export_name = direct_import["export_name"]
            if not required_shape:
                required_shape = {export_name: observed_members}
            render_hint = render_hint or (
                "module_object" if direct_import["import_kind"] in {"default", "namespace"} else None
            )
            return {
                "module_load": {
                    "binding": binding,
                    "origin_kind": origin_kind or "module_singleton",
                    "source_module": module_name,
                    "required_shape": required_shape,
                    "strategy": strategy or "mock_module",
                    "render_hint": render_hint,
                }
            }

    exec_strategy = strategy or "set_test_seam"
    seam_available = dep.get("seam_available")
    if seam_available is None:
        seam_available = exec_strategy != "set_test_seam"
    blocks_p2 = dep.get("blocks_p2_if_missing")
    if blocks_p2 is None:
        blocks_p2 = exec_strategy == "set_test_seam"

    return {
        "execution": {
            "binding": binding,
            "origin_kind": origin_kind or (
                "module_local_mutable" if assignment else "unknown"
            ),
            "required_shape": required_shape,
            "strategy": exec_strategy,
            "observed_members": observed_members,
            "source_module": source_module,
        },
        "injection": {
            "binding": binding,
            "strategy": exec_strategy,
            "steps": [
                f"provide controllable test double for {binding}"
            ],
            "blocks_p2_if_missing": blocks_p2,
            "seam_available": seam_available,
        },
    }


def _build_pre_test_source_edits(target, source_text, *, exported_hint, seam_edits):
    source_path = target["source_path"]
    edits = list(seam_edits)
    if source_path.endswith(".py"):
        return edits

    symbol = target["symbol"]
    if exported_hint is True or _is_exported(source_text, symbol):
        return edits

    for line in source_text.splitlines():
        stripped = line.strip()
        if stripped.startswith(f"function {symbol}(") or stripped.startswith(f"async function {symbol}("):
            edits.insert(
                0,
                {
                    "kind": "mechanical_export",
                    "path": source_path,
                    "old": line,
                    "new": _prepend_export(line),
                },
            )
            return edits
        if stripped.startswith(f"const {symbol} ") or stripped.startswith(f"let {symbol} "):
            edits.insert(
                0,
                {
                    "kind": "mechanical_export",
                    "path": source_path,
                    "old": line,
                    "new": _prepend_export(line),
                },
            )
            return edits
    return edits


def _realize_generated_test_seams(
    source_path,
    source_text,
    assignments,
    execution_dependencies,
    injection_plan,
):
    edits = []
    exec_by_binding = {entry["binding"]: entry for entry in execution_dependencies}
    for plan in injection_plan:
        if plan.get("strategy") != "set_test_seam":
            continue
        binding = plan["binding"]
        assignment = assignments.get(binding)
        if not _can_generate_test_seam(source_path, assignment):
            continue
        setter_name = _setter_name(binding, source_path)
        plan["seam_available"] = True
        plan["setter_name"] = setter_name
        plan["steps"] = [f"call {setter_name}(testDouble) before invoking target"]
        execution_entry = exec_by_binding.get(binding)
        if execution_entry is not None:
            execution_entry["setter_name"] = setter_name
        if _has_test_seam(source_text, setter_name):
            continue
        edits.append(
            {
                "kind": "mechanical_test_seam",
                "path": source_path,
                "binding": binding,
                "setter_name": setter_name,
                "old": assignment["line"],
                "new": assignment["line"]
                + ("\n\n" if not assignment["line"].endswith("\n") else "\n")
                + _render_test_seam_setter(binding, assignment, source_path),
            }
        )
    return edits


def _can_generate_test_seam(source_path, assignment):
    if not assignment:
        return False
    if source_path.endswith(".py"):
        return assignment.get("kind") == "assign"
    return assignment.get("kind") in {"let", "var"}


def _setter_name(binding, source_path):
    if source_path.endswith(".py"):
        return f"__set_{binding}_for_tests"
    return f"__set{binding[:1].upper()}{binding[1:]}ForTests"


def _has_test_seam(source_text, setter_name):
    return setter_name in source_text


def _render_test_seam_setter(binding, assignment, source_path):
    setter_name = _setter_name(binding, source_path)
    if source_path.endswith(".py"):
        return (
            f"def {setter_name}(value):\n"
            f"    global {binding}\n"
            f"    {binding} = value\n"
        )
    return (
        f"export function {setter_name}(value: any): void {{\n"
        f"  {binding} = value;\n"
        f"}}"
    )


def _build_assertion_surface(assertion_surface, snippet, symbol):
    if assertion_surface and {"kind", "binding", "member"} <= assertion_surface.keys():
        assertion_surface = deepcopy(assertion_surface)
        assertion_surface.setdefault("assertion_shape", "toHaveBeenCalledWith(...)")
        return assertion_surface, []

    calls = list(_CALL_RE.finditer(snippet))
    if calls:
        ranked = []
        for index, match in enumerate(calls):
            member = match.group(2)
            ranked.append((_ASSERTION_MEMBER_SCORES.get(member, 40), index, match))
        _, _, best = max(ranked, key=lambda item: (item[0], item[1]))
        binding = best.group(1)
        member = best.group(2)
        return (
            {
                "kind": "outbound_call_arguments",
                "binding": binding,
                "member": member,
                "assertion_shape": f"toHaveBeenCalledWith(...{binding}.{member}...)",
            },
            [],
        )

    if "return " in snippet:
        return (
            {
                "kind": "return_value",
                "binding": symbol,
                "member": "return",
                "assertion_shape": "return == expected_value",
            },
            [],
        )

    return (
        {
            "kind": "unknown",
            "binding": symbol,
            "member": "unknown",
            "assertion_shape": "",
        },
        [
            {
                "kind": "missing_assertion_surface",
                "message": "compiler pass could not infer assertion surface from P1 handoff",
                "owner": "compiler_pass",
            }
        ],
    )


def _build_pattern_files(pattern_files, tdd):
    built = []
    for entry in pattern_files:
        path = entry["path"] if isinstance(entry, dict) else entry
        why = entry.get("why_selected") if isinstance(entry, dict) else None
        reusable_shapes = []
        try:
            content = tdd.read_file(path)["content"]
        except Exception:
            content = ""
        if "mock.module(" in content:
            reusable_shapes.append("mock.module(...)")
        if "mock(" in content:
            reusable_shapes.append("mock(...)")
        if ".assert_called_with(" in content or "assert_called_with(" in content:
            reusable_shapes.append("assert_called_with(...)")
        if "toHaveBeenCalledWith(" in content:
            reusable_shapes.append("toHaveBeenCalledWith(...)")
        built.append(
            {
                "path": path,
                "why_selected": why or "pattern from P1 handoff",
                "reusable_shapes": reusable_shapes,
                "non_reusable_noise": [],
            }
        )
    return built


def _parse_import_bindings(source_text):
    imports = {}

    for match in _TS_NAMED_IMPORT_RE.finditer(source_text):
        module_name = match.group(2)
        for piece in match.group(1).split(","):
            item = piece.strip()
            if not item:
                continue
            if " as " in item:
                export_name, local_name = [part.strip() for part in item.split(" as ", 1)]
            else:
                export_name = local_name = item
            imports[local_name] = {
                "source_module": module_name,
                "export_name": export_name,
                "import_kind": "named",
            }

    for match in _TS_DEFAULT_IMPORT_RE.finditer(source_text):
        imports[match.group(1)] = {
            "source_module": match.group(2),
            "export_name": "default",
            "import_kind": "default",
        }

    for match in _TS_NAMESPACE_IMPORT_RE.finditer(source_text):
        imports[match.group(1)] = {
            "source_module": match.group(2),
            "export_name": match.group(1),
            "import_kind": "namespace",
        }

    for match in _PY_FROM_IMPORT_RE.finditer(source_text):
        module_name = match.group(1)
        for piece in match.group(2).split(","):
            item = piece.strip()
            if not item:
                continue
            if " as " in item:
                export_name, local_name = [part.strip() for part in item.split(" as ", 1)]
            else:
                export_name = local_name = item
            imports[local_name] = {
                "source_module": module_name,
                "export_name": export_name,
                "import_kind": "named",
            }

    for match in _PY_IMPORT_RE.finditer(source_text):
        for piece in match.group(1).split(","):
            item = piece.strip()
            if not item:
                continue
            if " as " in item:
                module_name, local_name = [part.strip() for part in item.split(" as ", 1)]
            else:
                module_name = local_name = item
            imports[local_name] = {
                "source_module": module_name,
                "export_name": local_name,
                "import_kind": "module",
            }

    return imports


def _parse_top_level_assignments(source_text):
    assignments = {}
    for line in source_text.splitlines():
        if line.startswith((" ", "\t")):
            continue
        ts_match = _TOP_LEVEL_TS_ASSIGN_RE.match(line)
        if ts_match:
            rhs = (ts_match.group(4) or "").strip()
            called_symbol = None
            call_match = re.match(r"([A-Za-z_]\w*)\s*\(", rhs)
            if call_match:
                called_symbol = call_match.group(1)
            assignments[ts_match.group(2)] = {
                "kind": ts_match.group(1),
                "type_annotation": (ts_match.group(3) or "").strip() or None,
                "rhs": rhs,
                "called_symbol": called_symbol,
                "line": line,
            }
            continue
        py_match = _TOP_LEVEL_PY_ASSIGN_RE.match(line)
        if py_match and not line.startswith(("def ", "class ", "import ", "from ")):
            rhs = py_match.group(2).strip()
            called_symbol = None
            call_match = re.match(r"([A-Za-z_]\w*)\s*\(", rhs)
            if call_match:
                called_symbol = call_match.group(1)
            assignments[py_match.group(1)] = {
                "kind": "assign",
                "rhs": rhs,
                "called_symbol": called_symbol,
                "line": line,
            }
    return assignments


def _enrich_module_load_dependencies(dependencies, imports, source_text):
    imports_by_module = {}
    for local_name, import_meta in imports.items():
        imports_by_module.setdefault(import_meta["source_module"], {})[local_name] = import_meta

    enriched = []
    for dep in dependencies:
        module_name = dep.get("source_module")
        if not module_name:
            enriched.append(dep)
            continue
        required_shape = deepcopy(dep.get("required_shape", {}))
        for local_name, import_meta in imports_by_module.get(module_name, {}).items():
            sibling_shape = _required_shape_for_import_binding(
                local_name,
                import_meta,
                source_text,
            )
            required_shape = _merge_required_shapes(required_shape, sibling_shape)
        updated = deepcopy(dep)
        updated["required_shape"] = required_shape
        enriched.append(updated)
    return enriched


def _required_shape_for_import_binding(local_name, import_meta, source_text):
    observed_members = _observed_members(source_text, local_name)
    export_name = import_meta["export_name"]
    if observed_members:
        return {export_name: observed_members}
    if _binding_is_called(source_text, local_name):
        return {export_name: "function"}
    return {export_name: "value"}


def _binding_is_called(source_text, binding):
    pattern = rf"(?<![\w.]){re.escape(binding)}\s*\("
    return re.search(pattern, source_text) is not None


def _observed_members(snippet, binding):
    members = []
    for match in _CALL_RE.finditer(snippet):
        if match.group(1) != binding:
            continue
        member = match.group(2)
        if member not in members:
            members.append(member)
    return members


def _required_shape_from_members(members):
    return {member: [] for member in members} if members else {}


def _merge_module_mock_dependencies(dependencies):
    merged = []
    by_module = {}
    for dep in dependencies:
        if dep.get("strategy") != "mock_module":
            merged.append(dep)
            continue
        module_name = dep.get("source_module")
        if not module_name:
            merged.append(dep)
            continue
        idx = by_module.get(module_name)
        if idx is None:
            copy = deepcopy(dep)
            by_module[module_name] = len(merged)
            merged.append(copy)
            continue
        existing = merged[idx]
        existing["required_shape"] = _merge_required_shapes(
            existing.get("required_shape", {}),
            dep.get("required_shape", {}),
        )
        existing["render_hint"] = None
    return merged


def _merge_required_shapes(left, right):
    merged = deepcopy(left)
    for key, value in right.items():
        if key not in merged:
            merged[key] = deepcopy(value)
            continue
        merged[key] = _merge_required_shape_value(merged[key], value)
    return merged


def _merge_required_shape_value(existing, incoming):
    if isinstance(existing, dict) and isinstance(incoming, dict):
        return _merge_required_shapes(existing, incoming)
    if isinstance(existing, list) and isinstance(incoming, list):
        return _merge_unique(existing, incoming)
    if isinstance(existing, list):
        return deepcopy(existing)
    if isinstance(incoming, list):
        return deepcopy(incoming)
    if isinstance(existing, dict):
        return deepcopy(existing)
    if isinstance(incoming, dict):
        return deepcopy(incoming)
    if existing == "function" or incoming == "function":
        return "function"
    return existing


def _merge_unique(left, right):
    merged = list(left)
    for item in right:
        if item not in merged:
            merged.append(item)
    return merged


def _normalize_factory_required_shape(required_shape, *, export_name, observed_members):
    if not required_shape:
        return {export_name: observed_members}
    if export_name in required_shape:
        return required_shape
    if all(isinstance(value, list) for value in required_shape.values()):
        return {export_name: sorted(set(_required_shape_members(required_shape)))}
    return required_shape


def _required_shape_members(required_shape):
    members = []
    for key, value in required_shape.items():
        if isinstance(value, list):
            if value:
                members.extend(value)
            else:
                members.append(key)
        elif isinstance(value, dict):
            members.extend(_required_shape_members(value))
    return sorted(set(members))


def _extract_signature(source_text, symbol):
    patterns = [
        rf"^(?:export\s+)?(?:async\s+)?function\s+{re.escape(symbol)}\s*\((.*?)\)(?:\s*:\s*([^\{{]+))?",
        rf"^def\s+{re.escape(symbol)}\s*\((.*?)\)(?:\s*->\s*([^:]+))?",
    ]
    for pattern in patterns:
        match = re.search(pattern, source_text, re.MULTILINE)
        if not match:
            continue
        params = match.group(1).strip()
        returns = match.group(2).strip() if match.lastindex and match.group(2) else None
        if returns:
            return f"{symbol}({params}): {returns}"
        return f"{symbol}({params})"
    return symbol


def _find_symbol_line(source_text, symbol):
    for idx, line in enumerate(source_text.splitlines(), start=1):
        if re.search(rf"\b{re.escape(symbol)}\b", line):
            return idx
    return None


def _extract_target_snippet(source_text, target):
    lines = source_text.splitlines()
    start = max(1, target.get("line_start") or 1)
    end = min(len(lines), target.get("line_end") or len(lines))
    if start <= end and lines:
        snippet = "\n".join(lines[start - 1:end])
        if source_text.endswith("\n") and end == len(lines):
            snippet += "\n"
        return snippet
    return source_text


def _default_test_path(source_path, symbol):
    source = PurePosixPath(source_path)
    suffix = source.suffix
    if suffix == ".py":
        return (source.parent / f"test_{symbol}.py").as_posix()
    return (source.parent / f"{symbol}.test{suffix}").as_posix()


def _infer_runner(source_path):
    return "pytest" if source_path.endswith(".py") else "bun:test"


def _parse_symbol_from_signature(signature):
    match = re.match(r"([A-Za-z_]\w*)\s*\(", signature.strip())
    return match.group(1) if match else None


def _guess_symbol_from_text(text):
    match = re.search(r"\b([A-Za-z_]\w*)\s*\(", text)
    if match:
        return match.group(1)
    match = re.search(r"`([A-Za-z_]\w*)`", text)
    return match.group(1) if match else None


def _clean_inline_value(text):
    stripped = text.strip()
    if stripped.startswith("`") and stripped.endswith("`"):
        return stripped[1:-1]
    return stripped or None


def _clean_code_block(text):
    stripped = text.strip()
    if stripped.startswith("```"):
        stripped = re.sub(r"^```[a-zA-Z0-9_-]*\n?", "", stripped)
        stripped = re.sub(r"\n?```$", "", stripped)
    return stripped.strip()


def _parse_backticked_path(text):
    match = re.search(r"`([^`]+\.[A-Za-z0-9]+)`", text)
    return match.group(1) if match else None


def _parse_keyed_token(text, key):
    match = re.search(rf"{re.escape(key)}\s*=\s*([A-Za-z0-9_.:-]+)", text)
    return match.group(1) if match else None


def _parse_boolish(text):
    if text is None:
        return None
    value = text.strip().lower()
    if value in {"true", "yes", "y"}:
        return True
    if value in {"false", "no", "n"}:
        return False
    return None


def _is_exported(source_text, symbol):
    patterns = [
        rf"^\s*export\s+(?:async\s+)?function\s+{re.escape(symbol)}\b",
        rf"^\s*export\s+(?:const|let|var)\s+{re.escape(symbol)}\b",
        rf"export\s*{{[^}}]*\b{re.escape(symbol)}\b[^}}]*}}",
    ]
    return any(re.search(pattern, source_text, re.MULTILINE) for pattern in patterns)


def _prepend_export(line):
    stripped = line.lstrip()
    indent = line[: len(line) - len(stripped)]
    return f"{indent}export {stripped}"
