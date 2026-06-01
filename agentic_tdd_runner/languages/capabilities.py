"""Typed capability contract for language plugins."""

from __future__ import annotations

import inspect
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
    def detect_project_type(self, root: Path) -> str | None: ...
    def inspect_runner_bootstrap(self, root: Path) -> Any | None: ...
    def project_manifest(self, root: Path) -> dict: ...
    def project_package_manager(
        self,
        root: Path,
        manifest: dict,
        bootstrap: Any | None,
    ) -> str | None: ...
    def project_should_install(self, root: Path, install_mode: str) -> bool: ...
    def project_install_command(
        self,
        root: Path,
        package_manager: str | None,
        bootstrap: Any | None,
    ) -> list[str] | None: ...
    def project_preflight_commands(
        self,
        root: Path,
        manifest: dict,
        env_cfg: dict,
        package_manager: str | None,
        bootstrap: Any | None,
    ) -> list[list[str]]: ...
    def ensure_project_test_config(self, root: Path, bootstrap: Any | None) -> Path | None: ...
    def shell_project_commands(self) -> set[str]: ...
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
    def definition_symbol_from_line(self, line: str) -> str | None: ...
    def looks_like_method_definition(self, line: str, symbol: str) -> bool: ...
    def relevant_import_declarations(self, source_text: str, needles: list[str]) -> list[str]: ...
    def permission_write_test_conflict(self, args: dict, context: dict) -> str | None: ...
    def source_imports_spec(self, source_text: str, spec: str) -> bool: ...
    def build_scaffold(self, contract: dict) -> dict: ...
    def render_module_mocks(self, contract: dict, *, declare_spies: bool = False) -> str: ...
    def is_mock_setup_line(self, line: str) -> bool: ...
    def reusable_test_shapes(self, file_text: str) -> list[str]: ...
    def scaffold_cookbook_guidance(self, contract: dict) -> list[str]: ...


class OptionalLanguageCapabilityDefaults:
    """No-op defaults only for optional language capabilities.

    Core capabilities, such as parsing, path generation, and command templates,
    must remain implemented by each language plugin so missing behavior fails the
    contract validator instead of silently inheriting a neutral fallback.
    """

    def is_dependency_contract_lookup(self, command: str, parts: list[str]) -> bool:
        return False

    def detect_project_type(self, root: Path) -> str | None:
        return None

    def inspect_runner_bootstrap(self, root: Path) -> Any | None:
        return None

    def project_manifest(self, root: Path) -> dict:
        return {}

    def project_package_manager(
        self,
        root: Path,
        manifest: dict,
        bootstrap: Any | None,
    ) -> str | None:
        return None

    def project_should_install(self, root: Path, install_mode: str) -> bool:
        return False

    def project_install_command(
        self,
        root: Path,
        package_manager: str | None,
        bootstrap: Any | None,
    ) -> list[str] | None:
        return None

    def project_preflight_commands(
        self,
        root: Path,
        manifest: dict,
        env_cfg: dict,
        package_manager: str | None,
        bootstrap: Any | None,
    ) -> list[list[str]]:
        return []

    def ensure_project_test_config(self, root: Path, bootstrap: Any | None) -> Path | None:
        return None

    def shell_project_commands(self) -> set[str]:
        return set()

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

    def definition_symbol_from_line(self, line: str) -> str | None:
        return None

    def looks_like_method_definition(self, line: str, symbol: str) -> bool:
        return False

    def relevant_import_declarations(self, source_text: str, needles: list[str]) -> list[str]:
        return []

    def permission_write_test_conflict(self, args: dict, context: dict) -> str | None:
        return None

    def source_imports_spec(self, source_text: str, spec: str) -> bool:
        return False

    def build_scaffold(self, contract: dict) -> dict:
        runner = ((contract or {}).get("test_file") or {}).get("runner") or "unknown"
        return {
            "imports_block": "",
            "module_mocks_block": "",
            "arrange_block": "",
            "act_block": "",
            "assert_block": "",
            "todo_slots": [f"unsupported_runner:{runner}"],
            "rendered_test": "",
        }

    def render_module_mocks(self, contract: dict, *, declare_spies: bool = False) -> str:
        return ""

    def is_mock_setup_line(self, line: str) -> bool:
        return False

    def reusable_test_shapes(self, file_text: str) -> list[str]:
        return []

    def scaffold_cookbook_guidance(self, contract: dict) -> list[str]:
        return []


def validate_language_capability(plugin: object) -> list[str]:
    """Return contract mismatches that pytest can enforce without a typechecker."""
    errors: list[str] = []
    for attribute in ("name", "runner", "extensions"):
        if not hasattr(plugin, attribute):
            errors.append(f"missing attribute: {attribute}")
    for method_name in language_capability_method_names():
        expected = getattr(LanguageCapabilities, method_name)
        actual = getattr(plugin, method_name, None)
        if not callable(actual):
            errors.append(f"missing method: {method_name}")
            continue
        expected_shape = _signature_shape(inspect.signature(expected), drop_self=True)
        actual_shape = _signature_shape(inspect.signature(actual), drop_self=False)
        if actual_shape != expected_shape:
            errors.append(
                f"signature mismatch for {method_name}: "
                f"expected {expected_shape}, got {actual_shape}"
            )
    return errors


def language_capability_method_names() -> tuple[str, ...]:
    return tuple(
        name
        for name, value in LanguageCapabilities.__dict__.items()
        if not name.startswith("_") and callable(value)
    )


def _signature_shape(signature: inspect.Signature, *, drop_self: bool) -> tuple:
    params = list(signature.parameters.values())
    if drop_self and params and params[0].name == "self":
        params = params[1:]
    return (
        tuple(
            (
                param.name,
                param.kind,
                param.default is not inspect.Signature.empty,
                _annotation_key(param.annotation),
            )
            for param in params
        ),
        _annotation_key(signature.return_annotation),
    )


def _annotation_key(annotation: Any) -> str:
    if annotation is inspect.Signature.empty:
        return ""
    return str(annotation)
