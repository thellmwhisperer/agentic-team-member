"""Python / pytest language plugin."""
from __future__ import annotations

import re
from pathlib import PurePosixPath
from typing import Any

from agentic_tdd_runner.languages import register
from agentic_tdd_runner.languages.capabilities import OptionalLanguageCapabilityDefaults
from agentic_tdd_runner.languages.signature import parse_signature_params

_PY_FROM_IMPORT_PAREN_RE = re.compile(
    r"^from\s+([.\w]+)\s+import\s+\(([^)]+)\)", re.MULTILINE | re.DOTALL,
)
_PY_FROM_IMPORT_RE = re.compile(r"^from\s+([.\w]+)\s+import\s+(.+)$", re.MULTILINE)
_PY_IMPORT_RE = re.compile(r"^import\s+(.+)$", re.MULTILINE)
_TOP_LEVEL_PY_ASSIGN_RE = re.compile(r"^([A-Za-z_]\w*)(?:\s*:\s*[^=]+)?\s*=\s*(.+)\s*$")


class PythonLanguage(OptionalLanguageCapabilityDefaults):
    name = "python"
    runner = "pytest"
    extensions = [".py"]

    def test_file_patterns(self) -> list[str]:
        return ["test_*.py", "*_test.py"]

    def code_fence(self) -> str:
        return "python"

    def default_exclude_dirs(self) -> list[str]:
        return ["__pycache__", ".pytest_cache"]

    def supports_test_runner(self, test_runner: str | None) -> bool:
        return test_runner == "pytest"

    def effective_test_command_template(
        self,
        report: Any,
        configured_command: str | None = None,
    ) -> str:
        return "python3 -m pytest"

    def test_command_template(self, config: dict | None = None) -> str:
        return "python3 -m pytest"

    def runner_version_command(self, report: Any) -> list[str] | None:
        if isinstance(report, dict):
            test_runner = report.get("test_runner")
        else:
            test_runner = getattr(report, "test_runner", None)
        if test_runner == "pytest":
            return ["python3", "-m", "pytest", "--version"]
        return None

    def is_test_run_command(self, command: str, parts: list[str]) -> bool:
        from pathlib import Path

        if not parts:
            return False
        executable = Path(parts[0]).name
        if executable == "pytest":
            return True
        if executable.startswith("python") and "-m" in parts and "pytest" in parts:
            return True
        return any(re.search(r"(^|/)test_.*\.py$|_test\.py$", part) for part in parts[1:])

    def test_api_import(self, test_runner: str) -> str | None:
        return None

    def test_api_facts(self, test_runner: str) -> list[str]:
        return []

    def detect_quality_tools(self, workdir: str) -> list[dict]:
        from pathlib import Path

        pyproject_path = Path(workdir) / "pyproject.toml"
        has_ruff = False
        if pyproject_path.is_file():
            try:
                with pyproject_path.open("rb") as f:
                    import tomllib
                    pyproject = tomllib.load(f)
                has_ruff = "ruff" in pyproject.get("tool", {})
            except (OSError, ValueError):
                pass

        if not has_ruff:
            return []
        return [
            {
                "name": "lint",
                "command": "python3 -m ruff check {changed_files}",
                "fix": "python3 -m ruff check {changed_files} --fix",
            },
            {
                "name": "format",
                "command": "python3 -m ruff format --check {changed_files}",
                "fix": "python3 -m ruff format {changed_files}",
            },
        ]

    def parse_imports(self, source_text: str) -> dict:
        imports = {}
        # First pass: parenthesized imports (from pkg import (\n    a,\n    b\n))
        for match in _PY_FROM_IMPORT_PAREN_RE.finditer(source_text):
            self._add_from_imports(imports, match.group(1), match.group(2))
        # Second pass: single-line imports (from pkg import a, b)
        for match in _PY_FROM_IMPORT_RE.finditer(source_text):
            # Skip if this line is part of a parenthesized import (has opening paren)
            if "(" in match.group(0):
                continue
            self._add_from_imports(imports, match.group(1), match.group(2))
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

    @staticmethod
    def _add_from_imports(imports, module_name, names_str):
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

    def parse_assignments(self, source_text: str) -> dict:
        assignments = {}
        for line in source_text.splitlines():
            if line.startswith((" ", "\t")):
                continue
            m = _TOP_LEVEL_PY_ASSIGN_RE.match(line)
            if m and not line.startswith(("def ", "class ", "import ", "from ")):
                rhs = m.group(2).strip()
                called_symbol = None
                call_match = re.match(r"([A-Za-z_]\w*)\s*\(", rhs)
                if call_match:
                    called_symbol = call_match.group(1)
                assignments[m.group(1)] = {
                    "kind": "assign",
                    "rhs": rhs,
                    "called_symbol": called_symbol,
                    "line": line,
                }
        return assignments

    def parse_signature_params(self, signature: str) -> list[str]:
        return parse_signature_params(
            signature,
            skip_markers=True,
            skip_names={"self", "cls"},
        )

    def test_path(self, source_path: str, symbol: str) -> str:
        source = PurePosixPath(source_path)
        return (source.parent / f"test_{symbol}.py").as_posix()

    def setter_name(self, binding: str) -> str:
        return f"__set_{binding}_for_tests"

    def is_exported(self, source_text: str, symbol: str) -> bool:
        return True  # Python functions are always importable

    def import_path(self, test_path: str, source_path: str) -> str:
        parts = list(PurePosixPath(source_path).parts)
        if parts and parts[-1].endswith(".py"):
            parts[-1] = parts[-1][:-3]
        if parts and parts[-1] == "__init__":
            parts = parts[:-1]
        return ".".join(part for part in parts if part)

    def render_seam_setter(self, binding: str, assignment: dict) -> str:
        name = self.setter_name(binding)
        return (
            f"def {name}(value):\n"
            f"    global {binding}\n"
            f"    {binding} = value\n"
        )

    def prepend_export(self, line: str) -> str:
        return line  # Python doesn't need export keyword


_plugin = PythonLanguage()
register(_plugin.extensions, _plugin)
