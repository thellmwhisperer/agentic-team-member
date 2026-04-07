from __future__ import annotations

import re
from copy import deepcopy
from pathlib import PurePosixPath

from agentic_tdd_runner.compiler.analyzer import _merge_module_mock_dependencies


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
    if assertion_surface.get("kind") == "return_value":
        act_block = f"const result = {target_name}({act_args});"
    else:
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
    if assertion_surface.get("kind") == "return_value":
        act_block = f"result = {target_name}({act_args})"
    else:
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
                _indent_block(act_block if act_block.startswith("result") else f"result = {act_block}", 4),
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


