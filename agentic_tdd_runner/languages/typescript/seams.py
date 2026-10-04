"""TypeScript export and seam helpers."""
from __future__ import annotations

import re


def is_exported(source_text: str, symbol: str) -> bool:
    patterns = [
        rf"^\s*export\s+(?:async\s+)?function\s+{re.escape(symbol)}\b",
        rf"^\s*export\s+(?:const|let|var)\s+{re.escape(symbol)}\b",
        rf"export\s*{{[^}}]*\b{re.escape(symbol)}\b[^}}]*}}",
    ]
    return any(re.search(pattern, source_text, re.MULTILINE) for pattern in patterns)


def prepend_export(line: str) -> str:
    stripped = line.lstrip()
    indent = line[: len(line) - len(stripped)]
    return f"{indent}export {stripped}"
