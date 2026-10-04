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

