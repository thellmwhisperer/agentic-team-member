"""Tests for compiler analyzer functions."""
import inspect

from agentic_tdd_runner.compiler.analyzer import (
    _build_assertion_surface,
    _build_pattern_files,
    _merge_required_shape_value,
)
from agentic_tdd_runner.compiler import analyzer


class TestMergeRequiredShapeValue:
    def test_empty_list_loses_to_function_scalar(self):
        assert _merge_required_shape_value([], "function") == "function"

    def test_function_scalar_wins_over_empty_list(self):
        assert _merge_required_shape_value("function", []) == "function"

    def test_nonempty_list_preserved(self):
        assert _merge_required_shape_value(["info", "warn"], "function") == ["info", "warn"]

    def test_both_lists_merged(self):
        result = _merge_required_shape_value(["a"], ["b"])
        assert "a" in result and "b" in result


class TestBuildAssertionSurface:
    """Assertion surface inference from snippet analysis."""

    def test_param_method_prefers_return_value(self):
        snippet = "def process(item):\n    return item.upper()\n"
        surface, _ = _build_assertion_surface(None, snippet, "process")
        assert surface["kind"] == "return_value"

    def test_outbound_member_call(self):
        snippet = "function handle(ch) {\n  logger.send(ch, 'hi');\n}\n"
        surface, _ = _build_assertion_surface(None, snippet, "handle")
        assert surface["kind"] == "outbound_call_arguments"
        assert surface["binding"] == "logger"

    def test_direct_callable_with_return(self):
        snippet = "def process(item):\n    return format_name(item)\n"
        surface, _ = _build_assertion_surface(None, snippet, "process")
        assert surface["kind"] == "return_value"

    def test_direct_callable_side_effect_no_return(self):
        """Direct callables (not binding.member) should be detected as outbound calls."""
        snippet = "def process(item):\n    send_notification(item)\n"
        surface, _ = _build_assertion_surface(None, snippet, "process")
        assert surface["kind"] == "outbound_call"
        assert surface["binding"] == "send_notification"

    def test_preserves_explicit_surface(self):
        explicit = {"kind": "outbound_call_arguments", "binding": "x", "member": "y"}
        surface, _ = _build_assertion_surface(explicit, "anything", "sym")
        assert surface["kind"] == "outbound_call_arguments"
        assert surface["binding"] == "x"


class TestPatternFiles:
    def test_reusable_shapes_are_language_owned(self):
        class FakeTDD:
            def read_file(self, path):
                return {
                    "content": "\n".join([
                        "mock.module('../logger', () => ({}));",
                        "const spy = mock(() => undefined);",
                        "expect(spy).toHaveBeenCalledWith(value);",
                    ])
                }

        built = _build_pattern_files([{"path": "src/service.test.ts"}], FakeTDD())

        assert built[0]["reusable_shapes"] == [
            "mock.module(...)",
            "mock(...)",
            "toHaveBeenCalledWith(...)",
        ]

    def test_unknown_language_pattern_files_do_not_inherit_typescript_shapes(self):
        class FakeTDD:
            def read_file(self, path):
                return {"content": "mock.module('../logger', () => ({}));"}

        built = _build_pattern_files([{"path": "spec/service_spec.rb"}], FakeTDD())

        assert built[0]["reusable_shapes"] == []

    def test_core_analyzer_does_not_contain_runner_specific_mock_shapes(self):
        source = inspect.getsource(analyzer)

        assert "mock.module(" not in source
        assert "mock(...)" not in source
