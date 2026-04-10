from __future__ import annotations

from agentic_tdd_runner.languages import get_language


def _build_pre_test_source_edits(target, source_text, *, exported_hint, seam_edits):
    source_path = target["source_path"]
    lang = get_language(source_path)
    edits = list(seam_edits)

    if not lang or lang.name == "python":
        return edits

    symbol = target["symbol"]
    if exported_hint is True or lang.is_exported(source_text, symbol):
        return edits

    for line in source_text.splitlines():
        stripped = line.strip()
        if stripped.startswith(f"function {symbol}(") or stripped.startswith(f"async function {symbol}("):
            edits.insert(0, {
                "kind": "mechanical_export",
                "path": source_path,
                "old": line,
                "new": lang.prepend_export(line),
            })
            return edits
        if stripped.startswith(f"const {symbol} ") or stripped.startswith(f"let {symbol} ") or stripped.startswith(f"var {symbol} "):
            edits.insert(0, {
                "kind": "mechanical_export",
                "path": source_path,
                "old": line,
                "new": lang.prepend_export(line),
            })
            return edits
    return edits


def _realize_generated_test_seams(
    source_path,
    source_text,
    assignments,
    execution_dependencies,
    injection_plan,
):
    lang = get_language(source_path)
    if not lang:
        return []
    edits = []
    exec_by_binding = {entry["binding"]: entry for entry in execution_dependencies}
    for plan in injection_plan:
        if plan.get("strategy") != "set_test_seam":
            continue
        binding = plan["binding"]
        assignment = assignments.get(binding)
        if not _can_generate_test_seam(source_path, assignment):
            continue
        name = lang.setter_name(binding)
        plan["seam_available"] = True
        plan["setter_name"] = name
        plan["steps"] = [f"call {name}(testDouble) before invoking target"]
        execution_entry = exec_by_binding.get(binding)
        observed_members = execution_entry.get("observed_members", []) if execution_entry else []
        if execution_entry is not None:
            execution_entry["setter_name"] = name
        if name in source_text:
            continue
        edits.append({
            "kind": "mechanical_test_seam",
            "path": source_path,
            "binding": binding,
            "setter_name": name,
            "old": assignment["line"],
            "new": assignment["line"]
            + ("\n\n" if not assignment["line"].endswith("\n") else "\n")
            + lang.render_seam_setter(binding, assignment, observed_members=observed_members),
        })
    return edits


def _can_generate_test_seam(source_path, assignment):
    if not assignment:
        return False
    lang = get_language(source_path)
    if lang and lang.name == "python":
        return assignment.get("kind") == "assign"
    return assignment.get("kind") in {"let", "var"}


def _setter_name(binding, source_path):
    """Backward-compatible wrapper — delegates to language plugin."""
    lang = get_language(source_path)
    if lang:
        return lang.setter_name(binding)
    return f"__set{binding[:1].upper()}{binding[1:]}ForTests"
