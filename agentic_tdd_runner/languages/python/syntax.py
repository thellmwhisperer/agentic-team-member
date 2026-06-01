"""Python syntax parsing helpers."""
from __future__ import annotations

import re

from agentic_tdd_runner.languages.signature import parse_signature_params as _parse_signature_params

_PY_FROM_IMPORT_PAREN_RE = re.compile(
    r"^from\s+([.\w]+)\s+import\s+\(([^)]+)\)", re.MULTILINE | re.DOTALL,
)
_PY_FROM_IMPORT_RE = re.compile(r"^from\s+([.\w]+)\s+import\s+(.+)$", re.MULTILINE)
_PY_IMPORT_RE = re.compile(r"^import\s+(.+)$", re.MULTILINE)
_TOP_LEVEL_PY_ASSIGN_RE = re.compile(r"^([A-Za-z_]\w*)(?:\s*:\s*[^=]+)?\s*=\s*(.+)\s*$")


def parse_imports(source_text: str) -> dict:
    imports = {}
    for match in _PY_FROM_IMPORT_PAREN_RE.finditer(source_text):
        _add_from_imports(imports, match.group(1), match.group(2))
    for match in _PY_FROM_IMPORT_RE.finditer(source_text):
        if "(" in match.group(0):
            continue
        _add_from_imports(imports, match.group(1), match.group(2))
    for match in _PY_IMPORT_RE.finditer(source_text):
        for piece in match.group(1).split(","):
            item = piece.strip()
            if not item:
                continue
            if " as " in item:
                module_name, local_name = [p.strip() for p in item.split(" as ", 1)]
            else:
                module_name = local_name = item
            imports[local_name] = {
                "source_module": module_name,
                "export_name": module_name,
                "import_kind": "module",
            }
    return imports


def _add_from_imports(imports: dict, module_name: str, names_str: str) -> None:
    for piece in names_str.split(","):
        item = piece.strip()
        if not item:
            continue
        if " as " in item:
            export_name, local_name = [p.strip() for p in item.split(" as ", 1)]
        else:
            export_name = local_name = item
        imports[local_name] = {
            "source_module": module_name,
            "export_name": export_name,
            "import_kind": "named",
        }


def parse_assignments(source_text: str) -> dict:
    assignments = {}
    for line in source_text.splitlines():
        if line.startswith((" ", "\t")):
            continue
        match = _TOP_LEVEL_PY_ASSIGN_RE.match(line)
        if match and not line.startswith(("def ", "class ", "import ", "from ")):
            rhs = match.group(2).strip()
            called_symbol = None
            call_match = re.match(r"([A-Za-z_]\w*)\s*\(", rhs)
            if call_match:
                called_symbol = call_match.group(1)
            assignments[match.group(1)] = {
                "kind": "assign",
                "rhs": rhs,
                "called_symbol": called_symbol,
                "line": line,
            }
    return assignments


def parse_signature_params(signature: str) -> list[str]:
    return _parse_signature_params(
        signature,
        skip_markers=True,
        skip_names={"self", "cls"},
    )
