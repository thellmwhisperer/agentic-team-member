"""Python / pytest language plugin."""
from __future__ import annotations

import re
from typing import Any

from agentic_tdd_runner.languages import register
from agentic_tdd_runner.languages.capabilities import OptionalLanguageCapabilityDefaults
from agentic_tdd_runner.languages.python import paths, permission, profile, seams, syntax


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
        return syntax.parse_imports(source_text)

    def parse_assignments(self, source_text: str) -> dict:
        return syntax.parse_assignments(source_text)

    def parse_signature_params(self, signature: str) -> list[str]:
        return syntax.parse_signature_params(signature)

    def test_path(self, source_path: str, symbol: str) -> str:
        return paths.test_path(source_path, symbol)

    def setter_name(self, binding: str) -> str:
        return seams.setter_name(binding)

    def is_exported(self, source_text: str, symbol: str) -> bool:
        return seams.is_exported(source_text, symbol)

    def import_path(self, test_path: str, source_path: str) -> str:
        return paths.import_path(test_path, source_path)

    def render_seam_setter(self, binding: str, assignment: dict) -> str:
        return seams.render_seam_setter(binding, assignment)

    def prepend_export(self, line: str) -> str:
        return seams.prepend_export(line)

    def definition_symbol_from_line(self, line: str) -> str | None:
        return permission.definition_symbol_from_line(line)

    def looks_like_method_definition(self, line: str, symbol: str) -> bool:
        return permission.looks_like_method_definition(line, symbol)

    def relevant_import_declarations(self, source_text: str, needles: list[str]) -> list[str]:
        return permission.relevant_import_declarations(source_text, needles)

    def permission_write_test_conflict(self, args: dict, context: dict) -> str | None:
        return permission.permission_write_test_conflict(args, context)

    def profile_detectors(self) -> list[Any]:
        return profile.profile_detectors()

    def source_imports_spec(self, source_text: str, spec: str) -> bool:
        return profile.source_imports_spec(source_text, spec)


_plugin = PythonLanguage()
register(_plugin.extensions, _plugin)
