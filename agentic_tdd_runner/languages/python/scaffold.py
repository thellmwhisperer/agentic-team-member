from __future__ import annotations

from agentic_tdd_runner.compiler.analyzer import _merge_module_mock_dependencies
from agentic_tdd_runner.compiler.scaffold_common import _empty_scaffold, _indent_block
from agentic_tdd_runner.languages.python.syntax import parse_signature_params


def build_scaffold(contract: dict) -> dict:
    runner = contract["test_file"].get("runner")
    if runner != "pytest":
        return _empty_scaffold(f"unsupported_runner:{runner or 'unknown'}")
    return _build_pytest_scaffold(contract)


def render_module_mocks(contract: dict, *, declare_spies: bool = False) -> str:
    return ""


def reusable_test_shapes(file_text: str) -> list[str]:
    if ".assert_called_with(" in file_text or "assert_called_with(" in file_text:
        return ["assert_called_with(...)"]
    return []


def render_assertion(assertion_surface: dict, *, call_params: list[str] | None = None) -> str:
    kind = assertion_surface["kind"]
    binding = assertion_surface["binding"]
    member = assertion_surface["member"]
    if kind == "outbound_call_arguments":
        spy_name = f"{binding}_{member}_spy"
        return (
            f"{spy_name}.assert_called_with("
            "..., expected_value)"
        )
    if kind == "outbound_call":
        spy_name = f"{binding}_spy"
        return f"{spy_name}.assert_called_with(expected_value)"
    if kind == "return_value":
        return "assert result == expected_value"
    return f"# TODO: assert {kind} on {binding}.{member}"


def _build_pytest_scaffold(contract: dict) -> dict:
    target = contract["target"]
    target_name = target["symbol"]
    source_import_path = contract["test_file"]["source_import_path"]
    module_load_dependencies = contract["module_load_dependencies"]
    execution_dependencies = contract["execution_dependencies"]
    injection_plan = {entry["binding"]: entry for entry in contract["injection_plan"]}
    assertion_surface = contract["assertion_surface"]
    params = _signature_params_for_target(target)

    import_names = [target_name]
    for plan in injection_plan.values():
        setter_name = plan.get("setter_name")
        if setter_name and setter_name not in import_names:
            import_names.append(setter_name)
    needs_patch = bool(module_load_dependencies)

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
                for member in members:
                    if member == a_member and dep["binding"] == a_binding:
                        spy_name = f"{a_binding}_{a_member}_spy"
                        spy_preamble_lines.append(f"{spy_name} = Mock()")
                        member_mocks.append(f"{member}={spy_name}")
                    else:
                        member_mocks.append(f"{member}=Mock()")
                mock_kwargs = ", ".join(member_mocks)
                inner_mock = f"Mock({mock_kwargs})"
                if origin_kind == "factory_result":
                    factory_mock = f"Mock(return_value={inner_mock})"
                else:
                    factory_mock = inner_mock
            elif isinstance(members, list):
                if export_name == a_binding and a_kind == "outbound_call":
                    spy_name = f"{a_binding}_spy"
                    spy_preamble_lines.append(f"{spy_name} = Mock()")
                    factory_mock = spy_name
                else:
                    factory_mock = "Mock()"
            else:
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
                    f"# TODO: exercise {binding} through a public caller/registration, module mock, or smallest pure helper using {spy_name}."
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
    assert_block = render_assertion(assertion_surface)
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
            patch_lines=patch_lines,
            source_import_path=source_import_path,
            import_names=import_names,
            spy_preamble_lines=spy_preamble_lines,
        ),
    }


def _signature_params_for_target(target: dict) -> list[str]:
    return parse_signature_params(target.get("signature", ""))


def _render_full_test(
    *,
    imports_block: str,
    module_mocks_block: str,
    target_name: str,
    arrange_block: str,
    act_block: str,
    assert_block: str,
    patch_lines: list[str] | None = None,
    source_import_path: str | None = None,
    import_names: list[str] | None = None,
    spy_preamble_lines: list[str] | None = None,
) -> str:
    parts = [imports_block, ""]
    if patch_lines:
        parts.extend([
            "",
            f"def test_{target_name}():",
        ])
        if spy_preamble_lines:
            for spy_line in spy_preamble_lines:
                parts.append(f"    {spy_line}")
            parts.append("")
        indent = 4
        for patch_line in patch_lines:
            parts.append(f"{' ' * indent}with {patch_line}:")
            indent += 4
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
                _indent_block(
                    act_block if act_block.startswith("result") else f"result = {act_block}",
                    4,
                ),
                "",
                _indent_block(assert_block, 4),
                "",
            ]
        )
    return "\n".join(parts).rstrip() + "\n"
