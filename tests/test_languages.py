"""Tests for language plugin registry."""
from agentic_tdd_runner.languages import get_language, plugins, supported_extensions
from agentic_tdd_runner.languages.capabilities import (
    LanguageCapabilities,
    NeutralLanguageCapabilityDefaults,
    validate_language_capability,
)


class TestRegistry:
    """Language plugins are registered and resolved by file extension."""

    def test_typescript_resolved_by_ts_extension(self):
        lang = get_language("src/events/client.ts")
        assert lang.name == "typescript"

    def test_python_resolved_by_py_extension(self):
        lang = get_language("src/worker.py")
        assert lang.name == "python"

    def test_unknown_extension_returns_none(self):
        lang = get_language("main.go")
        assert lang is None

    def test_supported_extensions_lists_both(self):
        exts = supported_extensions()
        assert ".ts" in exts
        assert ".py" in exts

    def test_registered_plugins_satisfy_language_capability_contract(self):
        assert plugins()
        for lang in plugins():
            assert isinstance(lang, LanguageCapabilities)
            assert validate_language_capability(lang) == []

    def test_language_contract_validator_catches_signature_mismatch(self):
        class BadPython(type(get_language("file.py"))):
            def test_path(self, source_path: str) -> str:
                return source_path

        errors = validate_language_capability(BadPython())

        assert any("signature mismatch for test_path" in error for error in errors)

    def test_unknown_extension_has_no_language_capability(self):
        assert get_language("README.md") is None


class TestTypeScriptPlugin:
    """TypeScript plugin parses imports and generates bun:test scaffolds."""

    def test_parses_named_import(self):
        lang = get_language("file.ts")
        imports = lang.parse_imports("import { getLogger } from '../logger';")
        assert "getLogger" in imports
        assert imports["getLogger"]["source_module"] == "../logger"

    def test_parses_top_level_const(self):
        lang = get_language("file.ts")
        assigns = lang.parse_assignments("const logger = getLogger();")
        assert "logger" in assigns
        assert assigns["logger"]["called_symbol"] == "getLogger"

    def test_parses_optional_signature_param_name(self):
        lang = get_language("file.ts")
        assert lang.parse_signature_params("update(name?: string, count = 1)") == [
            "name",
            "count",
        ]

    def test_parses_rest_signature_param_name(self):
        lang = get_language("file.ts")
        assert lang.parse_signature_params("collect(...items: string[])") == ["items"]

    def test_runner_is_bun(self):
        lang = get_language("file.ts")
        assert lang.runner == "bun:test"

    def test_test_path_convention(self):
        lang = get_language("file.ts")
        assert lang.test_path("src/events/client.ts", "processRenewal") == "src/events/processRenewal.test.ts"

    def test_setter_name_convention(self):
        lang = get_language("file.ts")
        assert lang.setter_name("client") == "__setClientForTests"

    def test_seam_setter_uses_type_annotation(self):
        lang = get_language("file.ts")
        assignment = {"kind": "let", "type_annotation": "EventBusClient", "rhs": "undefined", "line": "let client: EventBusClient;"}
        result = lang.render_seam_setter("client", assignment)
        assert "any" not in result
        assert "EventBusClient" in result

    def test_seam_setter_uses_pick_for_observed_members(self):
        lang = get_language("file.ts")
        assignment = {
            "kind": "let",
            "type_annotation": "EventBusClient",
            "observed_members": ["say"],
            "rhs": "undefined",
            "line": "let client: EventBusClient;",
        }
        result = lang.render_seam_setter("client", assignment)
        assert "any" not in result
        assert "value: Pick<EventBusClient, 'say'>" in result
        assert "client = value as EventBusClient;" in result

    def test_seam_setter_without_annotation_uses_typeof(self):
        lang = get_language("file.ts")
        assignment = {"kind": "let", "type_annotation": None, "rhs": "undefined", "line": "let client;"}
        result = lang.render_seam_setter("client", assignment)
        assert "any" not in result
        assert "typeof client" in result

    def test_detects_export(self):
        lang = get_language("file.ts")
        assert lang.is_exported("export function foo() {}", "foo") is True
        assert lang.is_exported("function foo() {}", "foo") is False

    def test_typescript_required_capabilities_are_not_neutral_defaults(self):
        lang = get_language("file.ts")
        required = (
            "is_dependency_contract_lookup",
            "typecheck_command",
            "referenced_type_shapes",
            "test_setup_dependency_paths",
            "extract_mock_modules",
            "regression_test_skeleton",
            "intent_router_observe_tool_result",
            "intent_router_review_tool_call",
            "intent_router_is_edit_to_path",
            "intent_router_is_framework_lookup_or_premature_run",
        )

        for capability in required:
            assert getattr(type(lang), capability) is not getattr(
                NeutralLanguageCapabilityDefaults,
                capability,
            )


class TestTypeScriptImportPath:
    """TypeScript plugin computes relative import paths."""

    def test_sibling_import_path(self):
        lang = get_language("file.ts")
        assert lang.import_path("src/events/processRenewal.test.ts", "src/events/client.ts") == "./client"

    def test_parent_import_path(self):
        lang = get_language("file.ts")
        assert lang.import_path("src/events/test.ts", "src/logger.ts") == "../logger"


class TestPythonImportPath:
    """Python plugin computes dotted import paths."""

    def test_module_import_path(self):
        lang = get_language("file.py")
        assert lang.import_path("src/test_worker.py", "src/worker.py") == "src.worker"

    def test_init_import_path(self):
        lang = get_language("file.py")
        assert lang.import_path("tests/test_foo.py", "src/pkg/__init__.py") == "src.pkg"


class TestPythonPlugin:
    """Python plugin parses imports and generates pytest scaffolds."""

    def test_parses_from_import(self):
        lang = get_language("file.py")
        imports = lang.parse_imports("from src.logger import get_logger")
        assert "get_logger" in imports
        assert imports["get_logger"]["source_module"] == "src.logger"

    def test_parses_parenthesized_from_import(self):
        lang = get_language("file.py")
        source = "from pkg import (\n    client,\n    logger,\n)"
        imports = lang.parse_imports(source)
        assert "client" in imports
        assert "logger" in imports
        assert imports["client"]["source_module"] == "pkg"
        assert "(" not in imports  # must not parse the paren as a name

    def test_parses_top_level_assign(self):
        lang = get_language("file.py")
        assigns = lang.parse_assignments("logger = get_logger()")
        assert "logger" in assigns
        assert assigns["logger"]["called_symbol"] == "get_logger"

    def test_parses_typed_assignment(self):
        lang = get_language("file.py")
        assigns = lang.parse_assignments("logger: Logger = get_logger()")
        assert "logger" in assigns
        assert assigns["logger"]["called_symbol"] == "get_logger"

    def test_parses_typed_assignment_without_call(self):
        lang = get_language("file.py")
        assigns = lang.parse_assignments("name: str = 'hello'")
        assert "name" in assigns
        assert assigns["name"]["called_symbol"] is None

    def test_parses_python_signature_params(self):
        lang = get_language("file.py")
        assert lang.parse_signature_params("process(self, item, /, *, verbose=False)") == [
            "item",
            "verbose",
        ]

    def test_aliased_module_import_preserves_original_name(self):
        lang = get_language("file.py")
        imports = lang.parse_imports("import numpy as np")
        assert imports["np"]["source_module"] == "numpy"
        assert imports["np"]["export_name"] == "numpy"

    def test_ignores_indented_imports_inside_function_body(self):
        lang = get_language("file.py")
        source = """import top_level

def work():
    import local_only
    from hidden.module import helper
"""
        imports = lang.parse_imports(source)

        assert "top_level" in imports
        assert "local_only" not in imports
        assert "helper" not in imports

    def test_ignores_indented_parenthesized_from_import(self):
        lang = get_language("file.py")
        source = """from visible import (
    first,
)

class Worker:
    from hidden import (
        second,
    )
"""
        imports = lang.parse_imports(source)

        assert "first" in imports
        assert "second" not in imports

    def test_runner_is_pytest(self):
        lang = get_language("file.py")
        assert lang.runner == "pytest"

    def test_test_path_convention(self):
        lang = get_language("file.py")
        assert lang.test_path("src/worker.py", "process_item") == "src/test_process_item.py"

    def test_setter_name_convention(self):
        lang = get_language("file.py")
        assert lang.setter_name("client") == "__set_client_for_tests"

    def test_python_capability_defaults_do_not_inherit_javascript_behavior(self, tmp_path):
        lang = get_language("file.py")

        assert lang.typecheck_command(tmp_path, {}, "pytest") is None
        assert lang.is_dependency_contract_lookup(
            "cat node_modules/@types/example/index.d.ts",
            ["cat", "node_modules/@types/example/index.d.ts"],
        ) is False
        assert lang.referenced_type_shapes(
            workdir=str(tmp_path),
            contract_facts=["pkg type declarations expose `ready(payload: Payload)`"],
        ) == []
        assert lang.test_setup_dependency_paths(workdir=str(tmp_path), test_file="test_worker.py") == []
        assert lang.extract_mock_modules("mock.module('./service', () => ({}));") == []
        assert lang.regression_test_skeleton({"target_symbol": "process_item"}) == ""
        assert lang.intent_router_observe_tool_result(
            "run_command",
            {"command": "python3 -m pytest"},
            "ok",
            {"test_runner": "pytest"},
        ) == {}
        assert lang.intent_router_review_tool_call(
            {},
            "read_file",
            {"path": "package.json"},
            {"test_runner": "pytest"},
        ) is None
        assert lang.intent_router_is_edit_to_path(
            "str_replace_editor",
            {"path": "test_worker.py"},
            "test_worker.py",
        ) is False
        assert lang.intent_router_is_framework_lookup_or_premature_run(
            "read_file",
            {"path": "package.json"},
        ) is False
        assert lang.profile_detectors() == []
