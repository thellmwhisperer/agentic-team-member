"""TypeScript syntax parsing helpers."""
from __future__ import annotations

import re

from agentic_tdd_runner.languages.signature import parse_signature_params as _parse_signature_params

_TS_NAMED_IMPORT_RE = re.compile(
    r"^\s*import\s*{([^}]+)}\s*from\s*['\"]([^'\"]+)['\"]\s*;?",
    re.MULTILINE,
)
_TS_DEFAULT_IMPORT_RE = re.compile(
    r"^\s*import\s+([A-Za-z_]\w*)\s+from\s+['\"]([^'\"]+)['\"]\s*;?",
    re.MULTILINE,
)
_TS_NAMESPACE_IMPORT_RE = re.compile(
    r"^\s*import\s+\*\s+as\s+([A-Za-z_]\w*)\s+from\s+['\"]([^'\"]+)['\"]\s*;?",
    re.MULTILINE,
)
_TOP_LEVEL_TS_ASSIGN_RE = re.compile(
    r"^(?:export\s+)?(const|let|var)\s+([A-Za-z_]\w*)"
    r"(?:\s*:\s*([^=;]+))?(?:\s*=\s*(.+?))?;?\s*$"
)


def parse_imports(source_text: str) -> dict:
    imports = {}
    for match in _TS_NAMED_IMPORT_RE.finditer(source_text):
        module_name = match.group(2)
        for piece in match.group(1).split(","):
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
    for match in _TS_DEFAULT_IMPORT_RE.finditer(source_text):
        imports[match.group(1)] = {
            "source_module": match.group(2),
            "export_name": "default",
            "import_kind": "default",
        }
    for match in _TS_NAMESPACE_IMPORT_RE.finditer(source_text):
        imports[match.group(1)] = {
            "source_module": match.group(2),
            "export_name": match.group(1),
            "import_kind": "namespace",
        }
    return imports


def parse_assignments(source_text: str) -> dict:
    assignments = {}
    for line in source_text.splitlines():
        if line.startswith((" ", "\t")):
            continue
        match = _TOP_LEVEL_TS_ASSIGN_RE.match(line)
        if match:
            rhs = (match.group(4) or "").strip()
            called_symbol = None
            call_match = re.match(r"([A-Za-z_]\w*)\s*\(", rhs)
            if call_match:
                called_symbol = call_match.group(1)
            assignments[match.group(2)] = {
                "kind": match.group(1),
                "type_annotation": (match.group(3) or "").strip() or None,
                "rhs": rhs,
                "called_symbol": called_symbol,
                "line": line,
            }
    return assignments


def parse_signature_params(signature: str) -> list[str]:
    return _parse_signature_params(signature, strip_optional_marker=True)
