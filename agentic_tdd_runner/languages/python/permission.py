"""Python permission-context helpers."""
from __future__ import annotations

import re


def definition_symbol_from_line(line: str) -> str | None:
    match = re.search(r"^\s*(?:async\s+)?def\s+([A-Za-z_][\w]*)\s*\(", line)
    return match.group(1) if match else None


def looks_like_method_definition(line: str, symbol: str) -> bool:
    esc = re.escape(symbol)
    return bool(re.search(rf"^\s*(?:async\s+)?def\s+{esc}\s*\(", line))


def relevant_import_declarations(source_text: str, needles: list[str]) -> list[str]:
    if not needles:
        return []
    declarations = _extract_import_declarations(source_text)
    return [
        declaration
        for declaration in declarations
        if any(needle in declaration for needle in needles)
    ]


def permission_write_test_conflict(args: dict, context: dict) -> str | None:
    return None


def _extract_import_declarations(source_text: str) -> list[str]:
    imports: list[str] = []
    lines = source_text.splitlines()
    index = 0
    while index < len(lines):
        line = lines[index]
        if line.startswith((" ", "\t")):
            index += 1
            continue
        stripped = line.strip()
        if not (stripped.startswith("import ") or stripped.startswith("from ")):
            index += 1
            continue

        declaration = [line]
        paren_balance = stripped.count("(") - stripped.count(")")
        while paren_balance > 0 and index + 1 < len(lines):
            index += 1
            declaration.append(lines[index])
            paren_balance += lines[index].count("(") - lines[index].count(")")
        imports.append("\n".join(declaration))
        index += 1
    return imports
