"""TypeScript / bun:test language plugin."""
from __future__ import annotations

import re
from pathlib import PurePosixPath

from agentic_tdd_runner.languages import register

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


class TypeScriptLanguage:
    name = "typescript"
    runner = "bun:test"
    extensions = [".ts", ".tsx", ".js", ".jsx"]

    def parse_imports(self, source_text: str) -> dict:
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

    def parse_assignments(self, source_text: str) -> dict:
        assignments = {}
        for line in source_text.splitlines():
            if line.startswith((" ", "\t")):
                continue
            m = _TOP_LEVEL_TS_ASSIGN_RE.match(line)
            if m:
                rhs = (m.group(4) or "").strip()
                called_symbol = None
                call_match = re.match(r"([A-Za-z_]\w*)\s*\(", rhs)
                if call_match:
                    called_symbol = call_match.group(1)
                assignments[m.group(2)] = {
                    "kind": m.group(1),
                    "type_annotation": (m.group(3) or "").strip() or None,
                    "rhs": rhs,
                    "called_symbol": called_symbol,
                    "line": line,
                }
        return assignments

    def test_path(self, source_path: str, symbol: str) -> str:
        source = PurePosixPath(source_path)
        return (source.parent / f"{symbol}.test{source.suffix}").as_posix()

    def setter_name(self, binding: str) -> str:
        return f"__set{binding[:1].upper()}{binding[1:]}ForTests"

    def is_exported(self, source_text: str, symbol: str) -> bool:
        patterns = [
            rf"^\s*export\s+(?:async\s+)?function\s+{re.escape(symbol)}\b",
            rf"^\s*export\s+(?:const|let|var)\s+{re.escape(symbol)}\b",
            rf"export\s*{{[^}}]*\b{re.escape(symbol)}\b[^}}]*}}",
        ]
        return any(re.search(p, source_text, re.MULTILINE) for p in patterns)

    def render_seam_setter(self, binding: str, assignment: dict) -> str:
        name = self.setter_name(binding)
        return (
            f"export function {name}(value: any): void {{\n"
            f"  {binding} = value;\n"
            f"}}"
        )

    def import_path(self, test_path: str, source_path: str) -> str:
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

    def prepend_export(self, line: str) -> str:
        stripped = line.lstrip()
        indent = line[: len(line) - len(stripped)]
        return f"{indent}export {stripped}"


_plugin = TypeScriptLanguage()
register(_plugin.extensions, _plugin)
