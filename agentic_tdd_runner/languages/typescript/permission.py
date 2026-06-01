"""TypeScript/JavaScript permission-context helpers."""
from __future__ import annotations

import re


_CONTROL_FLOW_SYMBOLS = {
    "if",
    "else",
    "for",
    "while",
    "switch",
    "catch",
    "do",
    "try",
    "finally",
    "return",
    "await",
}


def definition_symbol_from_line(line: str) -> str | None:
    patterns = (
        r"^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)\s*\(",
        r"^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=.*=>",
        r"^\s*(?:public|private|protected|static|async|\s)*([A-Za-z_$][\w$]*)\s*\([^)]*\)\s*(?::[^{]+)?\{",
    )
    for pattern in patterns:
        match = re.search(pattern, line)
        if not match:
            continue
        symbol = match.group(1)
        if symbol not in _CONTROL_FLOW_SYMBOLS:
            return symbol
    return None


def looks_like_method_definition(line: str, symbol: str) -> bool:
    esc = re.escape(symbol)
    return bool(
        re.search(
            rf"^\s*(?:public|private|protected|static|async|\s)*{esc}\s*\(",
            line,
        )
    )


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
    runner = str(context.get("runner") or "").strip()
    if not runner or runner == "bun:test":
        return None

    payload = "\n".join(str(args.get(field) or "") for field in ("content", "new_str", "patch"))
    if "bun:test" not in payload and "mock.module(" not in payload:
        return None
    return (
        "PERMISSION DENIED: detected runner is "
        f"`{runner}`, so do not create Bun-specific test scaffolding. "
        "Use Runner Facts and the project test runner instead."
    )


def _extract_import_declarations(source_text: str) -> list[str]:
    imports: list[str] = []
    lines = source_text.splitlines()
    index = 0
    while index < len(lines):
        line = lines[index]
        if not line.lstrip().startswith("import "):
            index += 1
            continue

        declaration = [line]
        while ";" not in lines[index] and index + 1 < len(lines):
            index += 1
            declaration.append(lines[index])
        imports.append("\n".join(declaration))
        index += 1
    return imports
