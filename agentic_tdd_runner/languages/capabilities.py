"""Typed capability contract for language plugins."""

from __future__ import annotations

from pathlib import Path
from typing import Any, Protocol, runtime_checkable


@runtime_checkable
class LanguageCapabilities(Protocol):
    name: str
    runner: str
    extensions: list[str]

    def test_file_patterns(self) -> list[str]: ...
    def code_fence(self) -> str: ...
    def default_exclude_dirs(self) -> list[str]: ...
    def supports_test_runner(self, test_runner: str | None) -> bool: ...
    def effective_test_command_template(
        self,
        report: Any,
        configured_command: str | None = None,
    ) -> str: ...
    def test_command_template(self, config: dict | None = None) -> str: ...
    def runner_version_command(self, report: Any) -> list[str] | None: ...
    def is_test_run_command(self, command: str, parts: list[str]) -> bool: ...
    def is_dependency_contract_lookup(self, command: str, parts: list[str]) -> bool: ...
    def test_api_import(self, test_runner: str) -> str | None: ...
    def test_api_facts(self, test_runner: str) -> list[str]: ...
    def typecheck_command(self, root: Path, config: dict, test_runner: str) -> str | None: ...
    def detect_quality_tools(self, workdir: str) -> list[dict]: ...
    def parse_imports(self, source_text: str) -> dict: ...
    def parse_assignments(self, source_text: str) -> dict: ...
    def parse_signature_params(self, signature: str) -> list[str]: ...
    def test_path(self, source_path: str, symbol: str) -> str: ...
    def setter_name(self, binding: str) -> str: ...
    def is_exported(self, source_text: str, symbol: str) -> bool: ...
    def import_path(self, test_path: str, source_path: str) -> str: ...
    def render_seam_setter(self, binding: str, assignment: dict) -> str: ...
    def prepend_export(self, line: str) -> str: ...
    def referenced_type_shapes(
        self,
        *,
        workdir: str | None,
        contract_facts: list[str],
    ) -> list[dict[str, Any]]: ...
    def test_setup_dependency_paths(
        self,
        *,
        workdir: str | None,
        test_file: Any,
    ) -> list[str]: ...
    def extract_mock_modules(self, cookbook_text: str) -> list[str]: ...
    def regression_test_skeleton(self, context: dict) -> str: ...
    def intent_router_observe_tool_result(
        self,
        name: str,
        args: dict,
        result: str,
        runner_facts: Any,
    ) -> dict[str, Any]: ...
    def intent_router_review_tool_call(
        self,
        state: dict[str, Any],
        name: str,
        args: dict,
        runner_facts: Any,
    ) -> dict[str, Any] | None: ...
    def intent_router_is_edit_to_path(
        self,
        name: str,
        args: dict,
        target_path: str | None,
    ) -> bool: ...
    def intent_router_is_framework_lookup_or_premature_run(self, name: str, args: dict) -> bool: ...
    def profile_detectors(self) -> list[Any]: ...


class NeutralLanguageCapabilityDefaults:
    def is_dependency_contract_lookup(self, command: str, parts: list[str]) -> bool:
        return False

    def typecheck_command(self, root: Path, config: dict, test_runner: str) -> str | None:
        return None

    def referenced_type_shapes(
        self,
        *,
        workdir: str | None,
        contract_facts: list[str],
    ) -> list[dict[str, Any]]:
        return []

    def test_setup_dependency_paths(
        self,
        *,
        workdir: str | None,
        test_file: Any,
    ) -> list[str]:
        return []

    def extract_mock_modules(self, cookbook_text: str) -> list[str]:
        return []

    def regression_test_skeleton(self, context: dict) -> str:
        return ""

    def intent_router_observe_tool_result(
        self,
        name: str,
        args: dict,
        result: str,
        runner_facts: Any,
    ) -> dict[str, Any]:
        return {}

    def intent_router_review_tool_call(
        self,
        state: dict[str, Any],
        name: str,
        args: dict,
        runner_facts: Any,
    ) -> dict[str, Any] | None:
        return None

    def intent_router_is_edit_to_path(
        self,
        name: str,
        args: dict,
        target_path: str | None,
    ) -> bool:
        return False

    def intent_router_is_framework_lookup_or_premature_run(self, name: str, args: dict) -> bool:
        return False

    def profile_detectors(self) -> list[Any]:
        return []
