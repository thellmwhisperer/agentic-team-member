from __future__ import annotations

import re
from copy import deepcopy
from pathlib import PurePosixPath

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

_PY_FROM_IMPORT_RE = re.compile(r"^\s*from\s+([.\w]+)\s+import\s+(.+)$", re.MULTILINE)

_PY_IMPORT_RE = re.compile(r"^\s*import\s+(.+)$", re.MULTILINE)

_TOP_LEVEL_TS_ASSIGN_RE = re.compile(
    r"^(?:export\s+)?(const|let|var)\s+([A-Za-z_]\w*)"
    r"(?:\s*:\s*([^=;]+))?(?:\s*=\s*(.+?))?;?\s*$"
)

_TOP_LEVEL_PY_ASSIGN_RE = re.compile(r"^([A-Za-z_]\w*)\s*=\s*(.+)\s*$")


def _parse_import_bindings(source_text):
    imports = {}

    for match in _TS_NAMED_IMPORT_RE.finditer(source_text):
        module_name = match.group(2)
        for piece in match.group(1).split(","):
            item = piece.strip()
            if not item:
                continue
            if " as " in item:
                export_name, local_name = [part.strip() for part in item.split(" as ", 1)]
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

    for match in _PY_FROM_IMPORT_RE.finditer(source_text):
        module_name = match.group(1)
        for piece in match.group(2).split(","):
            item = piece.strip()
            if not item:
                continue
            if " as " in item:
                export_name, local_name = [part.strip() for part in item.split(" as ", 1)]
            else:
                export_name = local_name = item
            imports[local_name] = {
                "source_module": module_name,
                "export_name": export_name,
                "import_kind": "named",
            }

    for match in _PY_IMPORT_RE.finditer(source_text):
        for piece in match.group(1).split(","):
            item = piece.strip()
            if not item:
                continue
            if " as " in item:
                module_name, local_name = [part.strip() for part in item.split(" as ", 1)]
            else:
                module_name = local_name = item
            imports[local_name] = {
                "source_module": module_name,
                "export_name": local_name,
                "import_kind": "module",
            }

    return imports



def _parse_top_level_assignments(source_text):
    assignments = {}
    for line in source_text.splitlines():
        if line.startswith((" ", "\t")):
            continue
        ts_match = _TOP_LEVEL_TS_ASSIGN_RE.match(line)
        if ts_match:
            rhs = (ts_match.group(4) or "").strip()
            called_symbol = None
            call_match = re.match(r"([A-Za-z_]\w*)\s*\(", rhs)
            if call_match:
                called_symbol = call_match.group(1)
            assignments[ts_match.group(2)] = {
                "kind": ts_match.group(1),
                "type_annotation": (ts_match.group(3) or "").strip() or None,
                "rhs": rhs,
                "called_symbol": called_symbol,
                "line": line,
            }
            continue
        py_match = _TOP_LEVEL_PY_ASSIGN_RE.match(line)
        if py_match and not line.startswith(("def ", "class ", "import ", "from ")):
            rhs = py_match.group(2).strip()
            called_symbol = None
            call_match = re.match(r"([A-Za-z_]\w*)\s*\(", rhs)
            if call_match:
                called_symbol = call_match.group(1)
            assignments[py_match.group(1)] = {
                "kind": "assign",
                "rhs": rhs,
                "called_symbol": called_symbol,
                "line": line,
            }
    return assignments



def _extract_signature(source_text, symbol):
    patterns = [
        rf"^(?:export\s+)?(?:async\s+)?function\s+{re.escape(symbol)}\s*\((.*?)\)(?:\s*:\s*([^\{{]+))?",
        rf"^def\s+{re.escape(symbol)}\s*\((.*?)\)(?:\s*->\s*([^:]+))?",
    ]
    for pattern in patterns:
        match = re.search(pattern, source_text, re.MULTILINE)
        if not match:
            continue
        params = match.group(1).strip()
        returns = match.group(2).strip() if match.lastindex and match.group(2) else None
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



def _is_exported(source_text, symbol):
    patterns = [
        rf"^\s*export\s+(?:async\s+)?function\s+{re.escape(symbol)}\b",
        rf"^\s*export\s+(?:const|let|var)\s+{re.escape(symbol)}\b",
        rf"export\s*{{[^}}]*\b{re.escape(symbol)}\b[^}}]*}}",
    ]
    return any(re.search(pattern, source_text, re.MULTILINE) for pattern in patterns)



def _prepend_export(line):
    stripped = line.lstrip()
    indent = line[: len(line) - len(stripped)]
    return f"{indent}export {stripped}"

def _default_test_path(source_path, symbol):
    source = PurePosixPath(source_path)
    suffix = source.suffix
    if suffix == ".py":
        return (source.parent / f"test_{symbol}.py").as_posix()
    return (source.parent / f"{symbol}.test{suffix}").as_posix()



def _infer_runner(source_path):
    return "pytest" if source_path.endswith(".py") else "bun:test"



def _compute_source_import_path(test_path, source_path):
    if source_path.endswith(".py"):
        return _python_import_path(source_path)

    test_dir = PurePosixPath(test_path).parent
    source_no_ext = _strip_suffix(PurePosixPath(source_path))
    relative = PurePosixPath(
        _relpath_posix(source_no_ext.as_posix(), test_dir.as_posix())
    ).as_posix()
    if not relative.startswith("."):
        relative = f"./{relative}"
    return relative



def _python_import_path(source_path):
    source = PurePosixPath(source_path)
    parts = list(source.parts)
    if parts and parts[-1].endswith(".py"):
        parts[-1] = parts[-1][:-3]
    if parts and parts[-1] == "__init__":
        parts = parts[:-1]
    return ".".join(part for part in parts if part)



def _strip_suffix(path):
    return path.with_suffix("")



def _relpath_posix(target, start):
    target_parts = PurePosixPath(target).parts
    start_parts = PurePosixPath(start).parts

    common = 0
    for left, right in zip(target_parts, start_parts):
        if left != right:
            break
        common += 1

    up = [".."] * (len(start_parts) - common)
    down = list(target_parts[common:])
    parts = up + down
    return "." if not parts else "/".join(parts)


