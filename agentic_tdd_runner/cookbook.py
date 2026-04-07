"""Cookbook generator: deterministic mock/scaffold recipes for the TDD agent.

Reads source files directly (no model involvement) and produces a text section
suitable for injection into the agent's system prompt.
"""
from __future__ import annotations

import re
from copy import deepcopy
from pathlib import Path

from agentic_tdd_runner.compiler import (
    _build_assertion_surface,
    _build_pre_test_source_edits,
    _compile_dependency,
    _compute_source_import_path,
    _enrich_module_load_dependencies,
    _extract_signature,
    _extract_target_snippet,
    _find_symbol_line,
    _observed_members,
    _realize_generated_test_seams,
    _render_module_mocks,
    build_contract,
)
from agentic_tdd_runner.languages import get_language


def generate_cookbook(
    source_path: str,
    symbol: str,
    project_root: str,
    *,
    test_path: str | None = None,
    line_start: int | None = None,
    line_end: int | None = None,
) -> str:
    """Generate a text cookbook section for injection into the TDD agent's system prompt."""
    full_path = Path(project_root) / source_path
    source_text = full_path.read_text()

    lang = get_language(source_path)
    if lang is None:
        raise ValueError(f"Unsupported file type: {source_path}")

    imports = lang.parse_imports(source_text)
    assignments = lang.parse_assignments(source_text)
    signature = _extract_signature(source_text, symbol)

    sym_line = line_start or _find_symbol_line(source_text, symbol)
    fn_end = line_end or _find_function_end(source_text, sym_line)

    # Detect class methods (indented def in Python)
    owner_class = None
    if sym_line and lang.name == "python":
        lines = source_text.splitlines()
        sym_line_text = lines[sym_line - 1] if sym_line <= len(lines) else ""
        if sym_line_text.startswith((" ", "\t")):
            # Find the class that owns this method
            for i in range(sym_line - 2, -1, -1):
                m = re.match(r"^class\s+(\w+)", lines[i])
                if m:
                    owner_class = m.group(1)
                    break

    target = {
        "symbol": symbol,
        "kind": "method" if owner_class else "function",
        "owner_class": owner_class,
        "source_path": source_path,
        "line_start": sym_line or 1,
        "line_end": fn_end or len(source_text.splitlines()),
        "signature": signature,
    }
    snippet = _extract_target_snippet(source_text, target)

    deps = _discover_dependencies(snippet, imports, assignments, exclude_symbol=symbol)

    module_load_dependencies = []
    execution_dependencies = []
    injection_plan = []
    seen_module = set()
    seen_execution = set()
    seen_injection = set()

    for dep in deps:
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
        module_load_dependencies, imports, source_text,
    )

    assertion_surface, assertion_gaps = _build_assertion_surface(None, snippet, symbol)

    # Add assertion surface binding as execution dep if not already tracked
    inferred_binding = assertion_surface.get("binding")
    if (
        inferred_binding
        and inferred_binding != symbol
        and inferred_binding not in seen_module
        and inferred_binding not in seen_execution
    ):
        compiled = _compile_dependency(
            {"binding": inferred_binding}, snippet, imports, assignments,
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
        source_path, source_text, assignments,
        execution_dependencies, injection_plan,
    )
    pre_test_source_edits = _build_pre_test_source_edits(
        target, source_text,
        exported_hint=lang.is_exported(source_text, symbol),
        seam_edits=seam_edits,
    )

    resolved_test_path = test_path or lang.test_path(source_path, symbol)
    runner = lang.runner

    facts = {
        "target": target,
        "test_file": {
            "path": resolved_test_path,
            "runner": runner,
            "source_import_path": _compute_source_import_path(
                resolved_test_path, source_path,
            ),
        },
        "pre_test_source_edits": pre_test_source_edits,
        "module_load_dependencies": module_load_dependencies,
        "execution_dependencies": execution_dependencies,
        "injection_plan": injection_plan,
        "assertion_surface": assertion_surface,
        "pattern_files": [],
        "gaps": assertion_gaps,
    }

    contract = build_contract(facts)
    return _render_cookbook_text(contract, lang)


def build_system_prompt(
    base_prompt: str,
    issue_text: str,
    *,
    source_path: str | None = None,
    symbol: str | None = None,
    project_root: str | None = None,
) -> str:
    """Build a system prompt with an optional cookbook section injected."""
    if not source_path or not symbol or not project_root:
        return base_prompt

    cookbook = generate_cookbook(source_path, symbol, project_root)
    return f"{base_prompt}\n\n{cookbook}"


def _find_function_end(source_text: str, start_line: int | None) -> int | None:
    """Heuristic: find the closing brace/dedent of a function starting at start_line."""
    if not start_line:
        return None
    lines = source_text.splitlines()
    if start_line > len(lines):
        return None

    is_python = not any(line.rstrip().endswith("{") for line in lines[start_line - 1: start_line + 2])

    if is_python:
        header = lines[start_line - 1]
        base_indent = len(header) - len(header.lstrip(" \t"))
        for i in range(start_line, len(lines)):
            line = lines[i]
            if not line.strip():
                continue
            indent = len(line) - len(line.lstrip(" \t"))
            if i > start_line and indent <= base_indent:
                return i
        return len(lines)

    # TS/JS: brace counting
    depth = 0
    started = False
    for i in range(start_line - 1, len(lines)):
        for ch in lines[i]:
            if ch == "{":
                depth += 1
                started = True
            elif ch == "}":
                depth -= 1
                if started and depth == 0:
                    return i + 1
    return len(lines)


def _discover_dependencies(snippet, imports, assignments, *, exclude_symbol=None):
    """Scan the snippet for all bindings (from imports and assignments) that are used."""
    deps = []
    seen = set()
    all_bindings = set(imports.keys()) | set(assignments.keys())
    if exclude_symbol:
        all_bindings.discard(exclude_symbol)
    for binding in sorted(all_bindings):
        if binding in seen:
            continue
        pattern = rf"\b{re.escape(binding)}\b"
        if re.search(pattern, snippet):
            dep = {"binding": binding}
            import_meta = imports.get(binding)
            if import_meta:
                dep["source_module"] = import_meta["source_module"]
            deps.append(dep)
            seen.add(binding)
    return deps


def _render_cookbook_text(contract: dict, lang) -> str:
    """Render a contract as human-readable text for the agent's system prompt."""
    parts = []
    target = contract["target"]
    owner_class = target.get("owner_class")
    if owner_class:
        parts.append(f"## Mock Cookbook for {owner_class}.{target['symbol']}")
        parts.append("")
        parts.append(f"**Note:** `{target['symbol']}` is a method of `{owner_class}`. "
                      f"Import `{owner_class}` and test via `{owner_class}().{target['symbol']}(...)`.")
        parts.append("")
    else:
        parts.append(f"## Mock Cookbook for {target['symbol']}")
        parts.append("")

    # Source edits
    edits = contract.get("pre_test_source_edits", [])
    if edits:
        parts.append("### Source Edits (apply before testing)")
        for edit in edits:
            parts.append(f"EDIT: {edit['path']}")
            parts.append("OLD:")
            parts.append(edit["old"])
            parts.append("NEW:")
            parts.append(edit["new"])
            parts.append("")

    # Module mocks (bun:test only — pytest uses unittest.mock in the scaffold)
    runner = contract["test_file"]["runner"]
    mock_text = (
        _render_module_mocks(contract.get("module_load_dependencies", []))
        if runner == "bun:test"
        else ""
    )
    if mock_text:
        parts.append("### Module Mocks (paste before source import)")
        parts.append("```ts")
        parts.append(mock_text)
        parts.append("```")
        parts.append("")

    # Test seams
    seam_deps = [
        dep for dep in contract.get("execution_dependencies", [])
        if dep.get("strategy") == "set_test_seam"
    ]
    if seam_deps:
        parts.append("### Test Seams")
        for dep in seam_deps:
            binding = dep["binding"]
            setter = dep.get("setter_name") or lang.setter_name(binding)
            members = dep.get("observed_members", [])
            members_str = ", ".join(members) if members else "..."
            parts.append(
                f"- `{binding}` → call `{setter}({{ {members_str} }})` before invoking target"
            )
        parts.append("")

    # Assertion
    assertion = contract.get("assertion_surface", {})
    if assertion.get("kind") == "outbound_call_arguments":
        parts.append("### Assertion")
        parts.append(
            f"Assert on `{assertion['binding']}.{assertion['member']}` "
            f"with `toHaveBeenCalledWith(...)`"
        )
        parts.append("")

    # Test scaffold
    scaffold = contract.get("scaffold", {})
    rendered = scaffold.get("rendered_test", "")
    if owner_class and runner == "pytest":
        # Override scaffold for class methods
        import_path = contract["test_file"].get("source_import_path", "")
        sig = target.get("signature", f"{target['symbol']}(self)")
        # Extract params excluding self
        param_match = re.search(r"\((.*?)\)", sig)
        params = []
        if param_match:
            for p in param_match.group(1).split(","):
                name = p.strip().split(":")[0].split("=")[0].strip()
                if name and name != "self":
                    params.append(name)
        param_setup = "\n".join(f"    {p} = ..." for p in params)
        param_args = ", ".join(params)
        rendered = (
            f"from {import_path} import {owner_class}\n\n\n"
            f"def test_{target['symbol']}():\n"
            f"{param_setup}\n\n"
            f"    obj = {owner_class}()\n"
            f"    result = obj.{target['symbol']}({param_args})\n\n"
            f"    assert result == ...\n"
        )
    if rendered:
        code_lang = "python" if runner == "pytest" else "ts"
        parts.append(f"### Test Scaffold ({contract['test_file']['path']})")
        parts.append(f"```{code_lang}")
        parts.append(rendered.rstrip())
        parts.append("```")
        parts.append("")

    return "\n".join(parts)
