from __future__ import annotations

import re
from copy import deepcopy
from pathlib import PurePosixPath

from agentic_tdd_runner.compiler.parser import _is_exported, _prepend_export


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


