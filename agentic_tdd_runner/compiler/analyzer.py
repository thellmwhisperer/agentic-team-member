from __future__ import annotations

import re
from copy import deepcopy
from pathlib import PurePosixPath

_CALL_RE = re.compile(r"\b([A-Za-z_]\w*)\.([A-Za-z_]\w*)\s*\(")

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
            # Always normalize shape under the export name so the patch targets
            # the right symbol (e.g. patch('pkg.logger', Mock(info=...)))
            # instead of individual members (e.g. patch('pkg.info', ...))
            if export_name not in required_shape:
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
    blocks = dep.get("blocks_if_missing")
    if blocks is None:
        blocks = exec_strategy == "set_test_seam"

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
            "blocks_if_missing": blocks,
            "seam_available": seam_available,
        },
    }



def _build_assertion_surface(assertion_surface, snippet, symbol):
    if assertion_surface and {"kind", "binding", "member"} <= assertion_surface.keys():
        assertion_surface = deepcopy(assertion_surface)
        assertion_surface.setdefault("assertion_shape", "toHaveBeenCalledWith(...)")
        return assertion_surface, []

    calls = list(_CALL_RE.finditer(snippet))

    # If the function has a return statement, check if outbound calls are on
    # actual dependencies (not parameters). Prefer return_value when all calls
    # are on parameters or locals.
    has_return = "return " in snippet
    if calls:
        # Extract parameter names from the first line (crude but effective)
        first_line = snippet.strip().splitlines()[0] if snippet.strip() else ""
        param_match = re.search(r"\((.*?)\)", first_line)
        param_names = set()
        if param_match:
            for p in param_match.group(1).split(","):
                name = p.strip().split(":")[0].split("=")[0].strip().lstrip("*")
                if name:
                    param_names.add(name)

        ranked = []
        for index, match in enumerate(calls):
            binding = match.group(1)
            member = match.group(2)
            # Skip calls on parameters — they're not mockable dependencies
            if binding in param_names:
                continue
            ranked.append((_ASSERTION_MEMBER_SCORES.get(member, 40), index, match))

        if ranked:
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

    if has_return:
        return (
            {
                "kind": "return_value",
                "binding": symbol,
                "member": "return",
                "assertion_shape": "return == expected_value",
            },
            [],
        )

    # Direct callable side effects: foo(args) without binding.member() pattern
    direct_call_re = re.compile(r"(?<![.\w])([A-Za-z_]\w*)\s*\(")
    first_line = snippet.strip().splitlines()[0] if snippet.strip() else ""
    param_match = re.search(r"\((.*?)\)", first_line)
    param_names = {symbol}
    if param_match:
        for p in param_match.group(1).split(","):
            name = p.strip().split(":")[0].split("=")[0].strip().lstrip("*")
            if name:
                param_names.add(name)
    # Also exclude common keywords
    param_names.update({"if", "for", "while", "return", "print", "len", "range", "str", "int", "float", "bool", "list", "dict", "set", "type"})

    direct_calls = []
    for match in direct_call_re.finditer(snippet):
        name = match.group(1)
        if name not in param_names:
            direct_calls.append(name)

    if direct_calls:
        best = direct_calls[0]
        return (
            {
                "kind": "outbound_call",
                "binding": best,
                "member": best,
                "assertion_shape": f"toHaveBeenCalled(...{best}...)",
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
        return deepcopy(existing) if existing else incoming
    if isinstance(incoming, list):
        return deepcopy(incoming) if incoming else existing
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



def _build_pattern_files(pattern_files, tdd):
    built = []
    for entry in pattern_files:
        path = entry["path"] if isinstance(entry, dict) else entry
        why = entry.get("why_selected") if isinstance(entry, dict) else None
        reusable_shapes = []
        try:
            content = tdd.read_file(path)["content"]
        except (FileNotFoundError, PermissionError, OSError, KeyError):
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



def _gaps_from_injection_plan(injection_plan):
    gaps = []
    for entry in injection_plan:
        if entry.get("blocks_if_missing") and not entry.get("seam_available", False):
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


