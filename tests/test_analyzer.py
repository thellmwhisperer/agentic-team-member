"""Tests for compiler analyzer functions."""
from agentic_tdd_runner.compiler.analyzer import (
    _build_assertion_surface,
    _merge_required_shape_value,
)
from agentic_tdd_runner.compiler.renderer import _render_assertion


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


class TestRenderAssertionDirectCall:
    """outbound_call kind must render a usable assertion, not a TODO."""

    def test_bun_direct_call_assertion(self):
        surface = {"kind": "outbound_call", "binding": "send", "member": "send",
                    "assertion_shape": "toHaveBeenCalled(...)"}
        result = _render_assertion(surface, runner="bun:test")
        assert "TODO" not in result
        assert "send" in result

    def test_pytest_direct_call_assertion(self):
        surface = {"kind": "outbound_call", "binding": "send", "member": "send",
                    "assertion_shape": "toHaveBeenCalled(...)"}
        result = _render_assertion(surface, runner="pytest")
        assert "TODO" not in result
        assert "send" in result
