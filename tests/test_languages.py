"""Tests for language plugin registry."""
from agentic_tdd_runner.languages import get_language, supported_extensions


class TestRegistry:
    """Language plugins are registered and resolved by file extension."""

    def test_typescript_resolved_by_ts_extension(self):
        lang = get_language("src/twitch/client.ts")
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

    def test_runner_is_bun(self):
        lang = get_language("file.ts")
        assert lang.runner == "bun:test"

    def test_test_path_convention(self):
        lang = get_language("file.ts")
        assert lang.test_path("src/twitch/client.ts", "handleResub") == "src/twitch/handleResub.test.ts"

    def test_setter_name_convention(self):
        lang = get_language("file.ts")
        assert lang.setter_name("client") == "__setClientForTests"

    def test_detects_export(self):
        lang = get_language("file.ts")
        assert lang.is_exported("export function foo() {}", "foo") is True
        assert lang.is_exported("function foo() {}", "foo") is False


class TestTypeScriptImportPath:
    """TypeScript plugin computes relative import paths."""

    def test_sibling_import_path(self):
        lang = get_language("file.ts")
        assert lang.import_path("src/twitch/handleResub.test.ts", "src/twitch/client.ts") == "./client"

    def test_parent_import_path(self):
        lang = get_language("file.ts")
        assert lang.import_path("src/twitch/test.ts", "src/logger.ts") == "../logger"


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

    def test_aliased_module_import_preserves_original_name(self):
        lang = get_language("file.py")
        imports = lang.parse_imports("import numpy as np")
        assert imports["np"]["source_module"] == "numpy"
        assert imports["np"]["export_name"] == "numpy"

    def test_runner_is_pytest(self):
        lang = get_language("file.py")
        assert lang.runner == "pytest"

    def test_test_path_convention(self):
        lang = get_language("file.py")
        assert lang.test_path("src/worker.py", "process_item") == "src/test_process_item.py"

    def test_setter_name_convention(self):
        lang = get_language("file.py")
        assert lang.setter_name("client") == "__set_client_for_tests"
