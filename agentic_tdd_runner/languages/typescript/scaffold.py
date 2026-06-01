from __future__ import annotations

import re

from agentic_tdd_runner.compiler.analyzer import _merge_module_mock_dependencies
from agentic_tdd_runner.compiler.scaffold_common import (
    _empty_scaffold,
    _indent_block,
    _safe_identifier,
)
from agentic_tdd_runner.languages.typescript.syntax import parse_signature_params


def build_scaffold(contract: dict) -> dict:
    runner = contract["test_file"].get("runner")
    if runner != "bun:test":
        return _empty_scaffold(f"unsupported_runner:{runner or 'unknown'}")
    return _build_bun_scaffold(contract)


def render_module_mocks(contract: dict, *, declare_spies: bool = False) -> str:
    if contract["test_file"].get("runner") != "bun:test":
        return ""
    return _render_module_mocks(
        contract.get("module_load_dependencies", []),
        declare_spies=declare_spies,
    )


def render_assertion(
    assertion_surface: dict,
    *,
    call_params: list[str] | None = None,
) -> str:
    kind = assertion_surface["kind"]
    binding = assertion_surface["binding"]
    member = assertion_surface["member"]
    call_params = call_params or []
    if kind == "outbound_call_arguments":
        spy_name = f"{binding}_{member}_spy"
        first_arg = call_params[0] if call_params else "__todoValue('assertion_arg_1')"
        return (
            f"expect({spy_name}).toHaveBeenCalledWith("
            f"{first_arg}, expected_value);"
        )
    if kind == "outbound_call":
        spy_name = f"{binding}_spy"
        return f"expect({spy_name}).toHaveBeenCalledWith(expected_value);"
    if kind == "return_value":
        return "expect(result).toBe(expected_value);"
    return f"// TODO: assert {kind} on {binding}.{member}"


def is_mock_setup_line(line: str) -> bool:
    return bool(
        re.search(
            r"(?:\b(?:vi|jest)\.(?:fn|mock)|\bmock\.module)\s*\(",
            line.strip(),
        )
    )


def reusable_test_shapes(file_text: str) -> list[str]:
    shapes = []
    if "mock.module(" in file_text:
        shapes.append("mock.module(...)")
    if "mock(" in file_text:
        shapes.append("mock(...)")
    if "toHaveBeenCalledWith(" in file_text:
        shapes.append("toHaveBeenCalledWith(...)")
    return shapes


def cookbook_guidance(contract: dict) -> list[str]:
    if contract["test_file"].get("runner") != "bun:test":
        return []
    return [
        "### Bun Specifics",
        "- For Bun spies/mocks, use `mock(() => undefined)` for void placeholders; do not silence mock typing with cast-only returns.",
    ]


def _build_bun_scaffold(contract: dict) -> dict:
    target = contract["target"]
    target_name = target["symbol"]
    source_import_path = contract["test_file"]["source_import_path"]
    module_load_dependencies = contract["module_load_dependencies"]
    execution_dependencies = contract["execution_dependencies"]
    injection_plan = {entry["binding"]: entry for entry in contract["injection_plan"]}
    assertion_surface = contract["assertion_surface"]
    params = _signature_params_for_target(target)

    module_mocks_block = _render_module_mocks(module_load_dependencies, declare_spies=True)
    imports_block = _render_bun_imports(
        needs_mock=_bun_scaffold_needs_mock(
            module_mocks_block,
            execution_dependencies,
            injection_plan,
        ),
    )

    import_names = [target_name]
    for plan in injection_plan.values():
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

    arrange_lines = [_render_ts_todo_helper()]
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
                    arrange_lines.append(f"const {spy_name} = {_render_ts_mock()};")
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
                arrange_lines.append(f"const {spy_name} = {_render_ts_mock()};")
                arrange_lines.append(
                    f"// TODO: exercise {binding} through a public caller/registration, module mock, or smallest pure helper using {spy_name}."
                )
                todo_slots.append(f"inject_{binding}_seam")

    for param in params:
        arrange_lines.append(f"const {param} = __todoValue('value_for_{param}');")
        todo_slots.append(f"value_for_{param}")

    expected_name = "expected_value"
    arrange_lines.append(
        f"const {expected_name} = __todoValue('expected_assertion_value');"
    )
    todo_slots.append("expected_assertion_value")

    act_args = ", ".join(params)
    if assertion_surface.get("kind") == "return_value":
        act_block = f"const result = {target_name}({act_args});"
    else:
        act_block = f"{target_name}({act_args});"
    assert_block = render_assertion(assertion_surface, call_params=params)
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
        ),
    }


def _signature_params_for_target(target: dict) -> list[str]:
    return parse_signature_params(target.get("signature", ""))


def _render_bun_imports(*, needs_mock: bool) -> str:
    imports = ["describe", "expect", "test"]
    if needs_mock:
        imports.insert(2, "mock")
    return f"import {{ {', '.join(imports)} }} from 'bun:test';"


def _bun_scaffold_needs_mock(
    module_mocks_block: str,
    execution_dependencies: list[dict],
    injection_plan: dict,
) -> bool:
    if module_mocks_block:
        return True
    for dep in execution_dependencies:
        if dep.get("strategy") != "set_test_seam":
            continue
        binding = dep["binding"]
        observed = dep.get("observed_members") or []
        plan = injection_plan.get(binding, {})
        if observed or not plan.get("setter_name"):
            return True
    return False


def _render_ts_todo_helper() -> str:
    return (
        "const __todoValue = (slot: string) => {\n"
        "  throw new Error(`TODO: ${slot}`);\n"
        "};"
    )


def _render_ts_mock() -> str:
    return "mock(() => undefined)"


def _module_spy_extractions(dependencies: list[dict]) -> tuple[list[str], dict]:
    spy_extractions = {}
    spy_decl_lines = []
    seen = set()
    for dep in _merge_module_mock_dependencies(dependencies):
        if dep.get("strategy") != "mock_module":
            continue
        binding = _safe_identifier(dep.get("binding", "module"))
        dep_scope = dep.get("source_module") or binding
        required_shape = dep.get("required_shape", {})
        for export_name, members in required_shape.items():
            if not isinstance(members, list):
                continue
            for member in members:
                key = (dep_scope, export_name, member)
                if key in spy_extractions:
                    continue
                spy_name = f"{binding}_{_safe_identifier(member)}_spy"
                if spy_name not in seen:
                    spy_decl_lines.append(f"const {spy_name} = {_render_ts_mock()};")
                    seen.add(spy_name)
                spy_extractions[key] = spy_name
    return spy_decl_lines, spy_extractions


def _render_module_mocks(
    dependencies: list[dict],
    *,
    spy_extractions: dict | None = None,
    declare_spies: bool = False,
) -> str:
    spy_decl_lines = []
    if declare_spies:
        spy_decl_lines, discovered = _module_spy_extractions(dependencies)
        merged_spies = dict(discovered)
        if spy_extractions:
            merged_spies.update(spy_extractions)
        spy_extractions = merged_spies

    blocks = []
    for dep in _merge_module_mock_dependencies(dependencies):
        if dep.get("strategy") != "mock_module":
            continue
        module_path = dep["source_module"]
        required_shape = dep.get("required_shape", {})
        render_hint = dep.get("render_hint")
        body = _render_module_shape(
            required_shape,
            render_hint=render_hint,
            depth=2,
            spy_extractions=spy_extractions,
            dep_scope=module_path,
        )
        blocks.append(
            f"mock.module('{module_path}', () => (\n"
            f"{{\n{body}\n}}\n"
            f"));"
        )
    rendered = "\n\n".join(blocks)
    if spy_decl_lines and rendered:
        return "\n".join(spy_decl_lines) + "\n\n" + rendered
    return rendered


def _render_module_shape(
    shape: dict,
    *,
    render_hint: str | None = None,
    depth: int = 0,
    spy_extractions: dict | None = None,
    dep_scope: str | None = None,
) -> str:
    lines = []
    indent = " " * depth
    for key, value in shape.items():
        rendered = _render_binding_value(
            key,
            value,
            render_hint=render_hint,
            depth=depth,
            spy_extractions=spy_extractions,
            parent_key=key,
            dep_scope=dep_scope,
        )
        lines.append(f"{indent}{key}: {rendered},")
    return "\n".join(lines)


def _render_binding_value(
    key: str,
    value: object,
    *,
    render_hint: str | None = None,
    depth: int = 0,
    spy_extractions: dict | None = None,
    parent_key: str | None = None,
    dep_scope: str | None = None,
) -> str:
    indent = " " * depth
    inner_indent = " " * (depth + 2)
    if isinstance(value, dict):
        inner = _render_module_shape(
            value,
            render_hint=render_hint,
            depth=depth + 2,
            spy_extractions=spy_extractions,
            dep_scope=dep_scope,
        )
        return f"{{\n{inner}\n{indent}}}"
    if isinstance(value, list):
        def _member_value(member: str) -> str:
            lookup_key = parent_key or key
            scoped_key = (dep_scope, lookup_key, member)
            if spy_extractions and scoped_key in spy_extractions:
                return spy_extractions[scoped_key]
            return _render_ts_mock()

        if render_hint == "module_object":
            members = "\n".join(
                f"{inner_indent}{member}: {_member_value(member)}," for member in value
            )
            if not members:
                return "{}"
            return f"{{\n{members}\n{indent}}}"
        if key.startswith(("get", "create", "build", "make")):
            members = "\n".join(
                f"{inner_indent}{member}: {_member_value(member)}," for member in value
            )
            if not members:
                return "() => ({})"
            return "() => ({\n" + members + f"\n{indent}}})"
        if not value:
            return "{}"
        members = "\n".join(
            f"{inner_indent}{member}: {_member_value(member)}," for member in value
        )
        return f"{{\n{members}\n{indent}}}"
    if value == "function":
        return _render_ts_mock()
    if value == "value":
        return "undefined"
    return repr(value)


def _render_full_test(
    *,
    imports_block: str,
    module_mocks_block: str,
    target_name: str,
    arrange_block: str,
    act_block: str,
    assert_block: str,
) -> str:
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
