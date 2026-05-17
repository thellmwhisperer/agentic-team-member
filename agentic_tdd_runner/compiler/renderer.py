from __future__ import annotations

import re
from copy import deepcopy
from pathlib import PurePosixPath

from agentic_tdd_runner.compiler.analyzer import _merge_module_mock_dependencies
from agentic_tdd_runner.languages import get_language
from agentic_tdd_runner.languages.signature import parse_signature_params


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
    params = _signature_params_for_target(target)

    imports_block = "import { describe, expect, mock, test } from 'bun:test';"

    module_mocks_block = _render_module_mocks(module_load_dependencies, declare_spies=True)

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
    module_load_dependencies = contract["module_load_dependencies"]
    execution_dependencies = contract["execution_dependencies"]
    injection_plan = {entry["binding"]: entry for entry in contract["injection_plan"]}
    assertion_surface = contract["assertion_surface"]
    params = _signature_params_for_target(target)

    import_names = [target_name]
    for binding, plan in injection_plan.items():
        setter_name = plan.get("setter_name")
        if setter_name and setter_name not in import_names:
            import_names.append(setter_name)
    needs_patch = bool(module_load_dependencies)

    # Build patch context managers for module-load deps
    # If the assertion surface targets a module_load member, extract its spy as named var
    a_kind = assertion_surface.get("kind")
    a_binding = assertion_surface.get("binding")
    a_member = assertion_surface.get("member")
    spy_preamble_lines = []

    patch_lines = []
    for dep in _merge_module_mock_dependencies(module_load_dependencies):
        if dep.get("strategy") != "mock_module":
            continue
        module_path = dep["source_module"]
        origin_kind = dep.get("origin_kind", "")
        required_shape = dep.get("required_shape", {})
        for export_name, members in required_shape.items():
            if isinstance(members, list) and members:
                member_mocks = []
                for m in members:
                    if m == a_member and dep["binding"] == a_binding:
                        spy_name = f"{a_binding}_{a_member}_spy"
                        spy_preamble_lines.append(f"{spy_name} = Mock()")
                        member_mocks.append(f"{m}={spy_name}")
                    else:
                        member_mocks.append(f"{m}=Mock()")
                mock_kwargs = ", ".join(member_mocks)
                inner_mock = f"Mock({mock_kwargs})"
                if origin_kind == "factory_result":
                    factory_mock = f"Mock(return_value={inner_mock})"
                else:
                    factory_mock = inner_mock
            elif isinstance(members, list):
                # Empty members — direct import (e.g. from alerts import send)
                # If this is the assertion binding, extract as named spy
                if export_name == a_binding and a_kind == "outbound_call":
                    spy_name = f"{a_binding}_spy"
                    spy_preamble_lines.append(f"{spy_name} = Mock()")
                    factory_mock = spy_name
                else:
                    factory_mock = "Mock()"
            else:
                # Scalar value like "function" or "value"
                if export_name == a_binding and a_kind == "outbound_call":
                    spy_name = f"{a_binding}_spy"
                    spy_preamble_lines.append(f"{spy_name} = Mock()")
                    factory_mock = spy_name
                else:
                    factory_mock = "Mock()"
            patch_lines.append(
                f"patch('{module_path}.{export_name}', {factory_mock})"
            )

    if needs_patch:
        # Only import Mock/patch at top level; target imported inside test after patches
        imports_block = "from unittest.mock import Mock, patch"
    else:
        imports_block = (
            f"from unittest.mock import Mock\n"
            f"from {source_import_path} import {', '.join(import_names)}"
        )
    module_mocks_block = ""
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
                spy_defs = []
                mock_kwargs = []
                for member in observed:
                    spy_name = f"{binding}_{member}_spy"
                    spy_defs.append(f"{spy_name} = Mock()")
                    mock_kwargs.append(f"{member}={spy_name}")
                arrange_lines.extend(spy_defs)
                if mock_kwargs:
                    arrange_lines.append(
                        f"{double_name} = Mock({', '.join(mock_kwargs)})"
                    )
                else:
                    arrange_lines.append(f"{double_name} = Mock()")
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
            runner="pytest",
            patch_lines=patch_lines,
            source_import_path=source_import_path,
            import_names=import_names,
            spy_preamble_lines=spy_preamble_lines,
        ),
    }



def _signature_params_for_target(target):
    lang = get_language(target.get("source_path", ""))
    parser = getattr(lang, "parse_signature_params", None) if lang else None
    if callable(parser):
        return parser(target.get("signature", ""))
    return parse_signature_params(target.get("signature", ""))



def _safe_identifier(value):
    return re.sub(r"\W+", "_", str(value)).strip("_") or "value"


def _render_ts_mock():
    return "mock(() => undefined as never)"


def _module_spy_extractions(dependencies):
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


def _render_module_mocks(dependencies, *, spy_extractions=None, declare_spies=False):
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
            required_shape, render_hint=render_hint, depth=2,
            spy_extractions=spy_extractions, dep_scope=module_path,
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



def _render_module_shape(shape, *, render_hint=None, depth=0, spy_extractions=None, dep_scope=None):
    lines = []
    indent = " " * depth
    for key, value in shape.items():
        rendered = _render_binding_value(
            key, value, render_hint=render_hint, depth=depth,
            spy_extractions=spy_extractions, parent_key=key, dep_scope=dep_scope,
        )
        lines.append(f"{indent}{key}: {rendered},")
    return "\n".join(lines)



def _render_binding_value(
    key, value, *, render_hint=None, depth=0, spy_extractions=None, parent_key=None, dep_scope=None,
):
    indent = " " * depth
    inner_indent = " " * (depth + 2)
    if isinstance(value, dict):
        inner = _render_module_shape(
            value, render_hint=render_hint, depth=depth + 2,
            spy_extractions=spy_extractions, dep_scope=dep_scope,
        )
        return f"{{\n{inner}\n{indent}}}"
    if isinstance(value, list):
        def _member_value(member):
            # Check if this member has a named spy extraction
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
    if kind == "outbound_call":
        spy_name = f"{binding}_spy"
        if runner == "pytest":
            return f"{spy_name}.assert_called_with(expected_value)"
        return f"expect({spy_name}).toHaveBeenCalledWith(expected_message);"
    if kind == "return_value":
        if runner == "pytest":
            return "assert result == expected_value"
        return "expect(result).toBe(expected_value);"
    if runner == "pytest":
        return f"# TODO: assert {kind} on {binding}.{member}"
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
    patch_lines=None,
    source_import_path=None,
    import_names=None,
    spy_preamble_lines=None,
):
    if runner == "pytest":
        parts = [imports_block, ""]
        if patch_lines:
            # Module-load deps need patching before import.
            # Use `with patch(...)` inside the test so the target module
            # is imported after the patches are active.
            parts.extend([
                "",
                f"def test_{target_name}():",
            ])
            # Spy preamble: named spy variables must be defined before the patch
            # so they can be referenced inside the patch Mock() call
            if spy_preamble_lines:
                for spl in spy_preamble_lines:
                    parts.append(f"    {spl}")
                parts.append("")
            indent = 4
            for pl in patch_lines:
                parts.append(f"{' ' * indent}with {pl}:")
                indent += 4
            # Lazy import inside the with block
            names = ", ".join(import_names or [target_name])
            parts.append(f"{' ' * indent}from {source_import_path} import {names}")
            parts.append("")
            parts.append(_indent_block(arrange_block, indent))
            parts.append("")
            parts.append(_indent_block(
                act_block if act_block.startswith("result") else f"result = {act_block}",
                indent,
            ))
            parts.append("")
            parts.append(_indent_block(assert_block, indent))
            parts.append("")
        else:
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
