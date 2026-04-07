"""Language-agnostic source analysis: signatures, symbols, snippets, import paths."""
from __future__ import annotations

import re

from agentic_tdd_runner.languages import get_language


def _extract_signature(source_text, symbol):
    patterns = [
        rf"^(?:export\s+)?(?:async\s+)?function\s+{re.escape(symbol)}\s*\((.*?)\)(?:\s*:\s*([^\{{]+))?",
        rf"^(?:export\s+)?(?:const|let|var)\s+{re.escape(symbol)}\s*=\s*\((.*?)\)(?:\s*:\s*([^=\{{]+))?\s*=>",
        rf"^(?:export\s+)?(?:const|let|var)\s+{re.escape(symbol)}\s*=\s*([A-Za-z_$][\w$]*)\s*=>",
        rf"^def\s+{re.escape(symbol)}\s*\((.*?)\)(?:\s*->\s*([^:]+))?",
    ]
    for pattern in patterns:
        match = re.search(pattern, source_text, re.MULTILINE)
        if not match:
            continue
        params = match.group(1).strip()
        returns = match.group(2).strip() if match.lastindex >= 2 and match.group(2) else None
        if returns:
            return f"{symbol}({params}): {returns}"
        return f"{symbol}({params})"
    return symbol


def _find_symbol_line(source_text, symbol):
    for idx, line in enumerate(source_text.splitlines(), start=1):
        if re.search(rf"\b{re.escape(symbol)}\b", line):
            return idx
    return None


def _extract_target_snippet(source_text, target):
    lines = source_text.splitlines()
    start = max(1, target.get("line_start") or 1)
    end = min(len(lines), target.get("line_end") or len(lines))
    if start <= end and lines:
        snippet = "\n".join(lines[start - 1:end])
        if source_text.endswith("\n") and end == len(lines):
            snippet += "\n"
        return snippet
    return source_text


def _compute_source_import_path(test_path, source_path):
    lang = get_language(source_path)
    if lang:
        return lang.import_path(test_path, source_path)
    # Fallback for unknown languages: relative path without extension
    from pathlib import PurePosixPath
    test_dir = PurePosixPath(test_path).parent
    source_no_ext = PurePosixPath(source_path).with_suffix("")
    target_parts = source_no_ext.parts
    start_parts = test_dir.parts
    common = 0
    for left, right in zip(target_parts, start_parts):
        if left != right:
            break
        common += 1
    up = [".."] * (len(start_parts) - common)
    down = list(target_parts[common:])
    parts = up + down
    rel = "/".join(parts) if parts else "."
    if not rel.startswith("."):
        rel = f"./{rel}"
    return rel
