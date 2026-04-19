"""Language-agnostic source analysis: signatures, symbols, snippets, import paths."""
from __future__ import annotations

import re

from agentic_tdd_runner.languages import get_language


def _extract_signature(source_text, symbol):
    # First try single-line patterns
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

    # Multiline: find the opening paren and collect until the closing paren
    multiline_openers = [
        rf"^(?:export\s+)?(?:async\s+)?function\s+{re.escape(symbol)}\s*\(",
        rf"^(?:export\s+)?(?:const|let|var)\s+{re.escape(symbol)}\s*=\s*\(",
        rf"^\s*def\s+{re.escape(symbol)}\s*\(",
    ]
    for opener in multiline_openers:
        match = re.search(opener, source_text, re.MULTILINE)
        if not match:
            continue
        # Collect from the opening paren to the closing paren
        start = source_text.index("(", match.start())
        depth = 0
        end = start
        for i in range(start, len(source_text)):
            if source_text[i] == "(":
                depth += 1
            elif source_text[i] == ")":
                depth -= 1
                if depth == 0:
                    end = i + 1
                    break
        params_raw = source_text[start + 1:end - 1]
        # Normalize whitespace
        params = re.sub(r"\s+", " ", params_raw).strip()
        # Check for return type after closing paren
        rest = source_text[end:end + 50].strip()
        ret_match = re.match(r"(?:\s*:\s*([^{\n:]+)|\s*->\s*([^:\n]+))", rest)
        returns = None
        if ret_match:
            returns = (ret_match.group(1) or ret_match.group(2) or "").strip()
        if returns:
            return f"{symbol}({params}): {returns}"
        return f"{symbol}({params})"

    return symbol


_DEFINITION_PATTERNS = (
    r"(?:export\s+)?(?:async\s+)?function\s+{esc}\s*\(",
    r"(?:export\s+)?(?:const|let|var)\s+{esc}\s*=",
    r"^\s*def\s+{esc}\s*\(",
)


def _find_symbol_line(source_text, symbol):
    line, _source = _find_symbol_line_with_source(source_text, symbol)
    return line


def _find_symbol_line_with_source(source_text, symbol):
    """Return (line, source) where source is 'definition', 'fallback', or None.

    'definition' means we matched a real `function`/`const|let|var`/`def` pattern
    and the line points at the authoritative declaration. 'fallback' means we
    only found the symbol as a bare word — likely a comment, call site, or class
    method (the parser doesn't yet recognize those). Callers gate guidance on
    this so they don't misdirect the agent to the wrong region."""
    esc = re.escape(symbol)
    definition_patterns = [p.format(esc=esc) for p in _DEFINITION_PATTERNS]
    for idx, line in enumerate(source_text.splitlines(), start=1):
        for pattern in definition_patterns:
            if re.search(pattern, line):
                return idx, "definition"
    for idx, line in enumerate(source_text.splitlines(), start=1):
        if re.search(rf"\b{esc}\b", line):
            return idx, "fallback"
    return None, None


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
