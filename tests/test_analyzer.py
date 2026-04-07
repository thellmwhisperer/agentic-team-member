"""Tests for compiler analyzer functions."""
from agentic_tdd_runner.compiler.analyzer import _merge_required_shape_value


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
