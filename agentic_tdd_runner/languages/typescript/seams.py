"""TypeScript export and seam helpers."""
from __future__ import annotations

import re


def setter_name(binding: str) -> str:
    return f"__set{binding[:1].upper()}{binding[1:]}ForTests"


def is_exported(source_text: str, symbol: str) -> bool:
    patterns = [
        rf"^\s*export\s+(?:async\s+)?function\s+{re.escape(symbol)}\b",
        rf"^\s*export\s+(?:const|let|var)\s+{re.escape(symbol)}\b",
        rf"export\s*{{[^}}]*\b{re.escape(symbol)}\b[^}}]*}}",
    ]
    return any(re.search(pattern, source_text, re.MULTILINE) for pattern in patterns)


def render_seam_setter(binding: str, assignment: dict) -> str:
    name = setter_name(binding)
    type_hint = assignment.get("type_annotation") or f"typeof {binding}"
    observed_members = assignment.get("observed_members") or []
    value_type = type_hint
    if assignment.get("type_annotation") and observed_members:
        members = " | ".join(f"'{member}'" for member in observed_members)
        value_type = f"Pick<{type_hint}, {members}>"
    return (
        f"export function {name}(value: {value_type}): void {{\n"
        f"  {binding} = value as {type_hint};\n"
        f"}}"
    )


def prepend_export(line: str) -> str:
    stripped = line.lstrip()
    indent = line[: len(line) - len(stripped)]
    return f"{indent}export {stripped}"
