"""Tests for agentic_tdd_runner.review_oracle.facts (§§1,2,3,5 AST-exact detectors)."""
from __future__ import annotations

import dataclasses
import textwrap

import pytest

from agentic_tdd_runner.review_oracle.facts import (
    collect_exact_facts,
    extract_invocation_surface,
    extract_pytest_surface,
    extract_result_surface,
    extract_target_identity,
)
from agentic_tdd_runner.review_oracle.types import Fact


def _write_file(tmp_path, relative_path, content):
    path = tmp_path / relative_path
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(textwrap.dedent(content))
    return relative_path


def _build_python_query_repo(tmp_path):
    _write_file(tmp_path, "server.py", """\
        import json


        class ToolHandler:
            def _with_latency(self, payload):
                payload["latency_ms"] = 7
                payload["version"] = "dev"
                return payload

            def call(self, name, args):
                try:
                    result, _meta = self._dispatch(name, args)
                    return {"content": [{"type": "text", "text": json.dumps(result)}]}
                except ValueError as err:
                    return {"content": [{"type": "text", "text": json.dumps({"error": str(err)})}], "isError": True}

            def _dispatch(self, name, args):
                if name == "roca_query":
                    return self._handle_query(args), None
                if name == "ping":
                    return {"ok": True}, None
                raise ValueError("unknown tool")

            def _handle_query(self, args):
                compiler_result = {
                    "rows": [],
                    "sql": "SELECT 1",
                    "path": "compiler",
                    "queryplan": "QP",
                    "confidence": 0.95,
                }
                if args.get("mode") == "compiler":
                    return self._with_latency(compiler_result)

                fallback_result = {
                    "rows": [],
                    "sql": "SELECT 2",
                    "path": "llm_fallback",
                    "fallback_reason": "empty",
                    "retried": True,
                }
                return self._with_latency(fallback_result)
    """)
    _write_file(tmp_path, "test_server.py", """\
        import pytest


        @pytest.fixture
        def db(tmp_path):
            return object()


        @pytest.fixture
        def handler(db, tmp_path):
            return object()


        class MockLlm:
            def __call__(self, prompt):
                return {"sql": "SELECT 1"}


        class TestRocaQuery:
            def _handler_with_llm(self, db, tmp_path, sql):
                return object()
    """)


class TestFactType:
    def test_frozen_dataclass(self):
        fact = Fact(
            name="target_identity",
            value={"symbol": "_handle_query"},
            derivation_rule="parse AST",
            inputs_used=("server.py:1212-1387",),
            confidence_class="ast_exact",
        )
        with pytest.raises(dataclasses.FrozenInstanceError):
            fact.name = "other"  # type: ignore[misc]

    def test_required_fields(self):
        fact = Fact(
            name="pytest_surface",
            value={"fixtures": ["db", "handler"]},
            derivation_rule="scan @pytest.fixture decorators",
            inputs_used=("test_server.py:13-28",),
            confidence_class="ast_exact",
        )
        assert fact.name == "pytest_surface"
        assert fact.value == {"fixtures": ["db", "handler"]}
        assert fact.derivation_rule == "scan @pytest.fixture decorators"
        assert fact.inputs_used == ("test_server.py:13-28",)
        assert fact.confidence_class == "ast_exact"

    def test_inputs_used_is_tuple(self):
        """inputs_used stays tuple-typed for stable fact metadata."""
        fact = Fact(
            name="x",
            value=None,
            derivation_rule="r",
            inputs_used=("a.py", "b.py"),
            confidence_class="ast_exact",
        )
        assert isinstance(fact.inputs_used, tuple)


class TestExactFacts:
    def test_target_identity_finds_private_method_signature(self, tmp_path):
        _build_python_query_repo(tmp_path)

        fact = extract_target_identity(str(tmp_path), "server.py", "_handle_query")

        assert fact.name == "target_identity"
        assert fact.value["owner_class"] == "ToolHandler"
        assert fact.value["symbol"] == "_handle_query"
        assert fact.value["signature"] == "_handle_query(self, args)"
        assert fact.value["visibility"] == "private method"
        assert fact.value["source_range"] == "server.py:24-42"
        assert fact.confidence_class == "ast_exact"

    def test_invocation_surface_reads_dispatch_and_wrapper_shapes(self, tmp_path):
        _build_python_query_repo(tmp_path)

        fact = extract_invocation_surface(
            str(tmp_path),
            "server.py",
            "ToolHandler",
            "_handle_query",
        )

        assert fact.value["public_entrypoint"] == "ToolHandler.call(name, args)"
        assert fact.value["dispatch_key"] == "roca_query"
        assert fact.value["dispatch_branch"] == "server.py:18-19"
        assert fact.value["wrapper_success_shape"] == '{"content": [{"type": "text", "text": json.dumps(result)}]}'
        assert fact.value["wrapper_error_shape"] == (
            '{"content": [{"type": "text", "text": json.dumps({"error": str(err)})}], "isError": True}'
        )
        assert fact.value["unwrap_pattern"] == 'body = json.loads(result["content"][0]["text"])'

    def test_result_surface_collects_wrapper_keys_and_helper_added_keys(self, tmp_path):
        _build_python_query_repo(tmp_path)

        fact = extract_result_surface(str(tmp_path), "server.py", "_handle_query")

        assert fact.value["wrapper_success_keys"] == ["content"]
        assert fact.value["wrapper_error_keys"] == ["content", "isError"]
        assert fact.value["body_keys_always_added_by_helper"] == {
            "_with_latency": ["latency_ms", "version"],
        }
        assert fact.value["body_return_key_sets"] == [
            ["confidence", "path", "queryplan", "rows", "sql"],
            ["fallback_reason", "path", "retried", "rows", "sql"],
        ]

    def test_pytest_surface_collects_fixtures_and_helpers(self, tmp_path):
        _build_python_query_repo(tmp_path)

        fact = extract_pytest_surface(str(tmp_path), "test_server.py")

        assert fact.value["module_test_file"] == "test_server.py"
        assert fact.value["module_fixtures"] == [
            {"name": "db", "range": "test_server.py:5-6"},
            {"name": "handler", "range": "test_server.py:10-11"},
        ]
        assert fact.value["helper_classes"] == [
            {"name": "MockLlm", "range": "test_server.py:14-16"},
        ]
        assert fact.value["helper_methods"] == [
            {"class": "TestRocaQuery", "name": "_handler_with_llm", "range": "test_server.py:20-21"},
        ]

    def test_invocation_surface_tolerates_missing_call_method(self, tmp_path):
        _write_file(tmp_path, "server.py", """\
            class ToolHandler:
                def _dispatch(self, tool_name, args):
                    if tool_name == "roca_query":
                        return self._handle_query(args), None
                    raise ValueError("unknown tool")

                def _handle_query(self, args):
                    return {"rows": [], "path": "compiler"}
        """)

        fact = extract_invocation_surface(
            str(tmp_path),
            "server.py",
            "ToolHandler",
            "_handle_query",
        )

        assert fact.value["public_entrypoint"] is None
        assert fact.value["dispatch_key"] == "roca_query"
        assert fact.value["dispatch_branch"] == "server.py:3-4"
        assert fact.value["wrapper_success_shape"] is None
        assert fact.value["wrapper_error_shape"] is None
        assert fact.value["unwrap_pattern"] is None

    def test_target_identity_handles_nested_defaults_and_type_annotations(self, tmp_path):
        _write_file(tmp_path, "server.py", """\
            from typing import Callable

            def build_default():
                return 1

            class ToolHandler:
                def _handle_query(self, callback: Callable[[int], int] = build_default()) -> dict:
                    return {"rows": []}
        """)

        fact = extract_target_identity(str(tmp_path), "server.py", "_handle_query")

        assert fact.value["signature"] == "_handle_query(self, callback: Callable[[int], int]=build_default())"

    def test_invocation_surface_ignores_is_error_false_success_wrapper(self, tmp_path):
        _write_file(tmp_path, "server.py", """\
            import json

            class ToolHandler:
                def call(self, name, args) -> dict:
                    result, _meta = self._dispatch(name, args)
                    return {"content": [{"type": "text", "text": json.dumps(result)}], "isError": False}

                def _dispatch(self, tool_name, args):
                    if tool_name == "roca_query":
                        return self._handle_query(args), None
                    raise ValueError("unknown tool")

                def _handle_query(self, args):
                    return {"rows": []}
        """)

        fact = extract_invocation_surface(
            str(tmp_path),
            "server.py",
            "ToolHandler",
            "_handle_query",
        )

        assert fact.value["public_entrypoint"] == "ToolHandler.call(name, args)"
        assert fact.value["dispatch_key"] == "roca_query"
        assert fact.value["wrapper_success_shape"] == (
            '{"content": [{"type": "text", "text": json.dumps(result)}], "isError": False}'
        )
        assert fact.value["wrapper_error_shape"] is None
        assert fact.value["unwrap_pattern"] == 'body = json.loads(result["content"][0]["text"])'

    def test_invocation_surface_leaves_unwrap_pattern_unset_when_not_proven(self, tmp_path):
        _write_file(tmp_path, "server.py", """\
            class ToolHandler:
                def call(self, name, args):
                    result, _meta = self._dispatch(name, args)
                    return {"content": [{"type": "text", "text": result}]}

                def _dispatch(self, name, args):
                    if name == "roca_query":
                        return self._handle_query(args), None
                    raise ValueError("unknown tool")

                def _handle_query(self, args):
                    return {"rows": []}
        """)

        fact = extract_invocation_surface(
            str(tmp_path),
            "server.py",
            "ToolHandler",
            "_handle_query",
        )

        assert fact.value["wrapper_success_shape"] == '{"content": [{"type": "text", "text": result}]}'
        assert fact.value["unwrap_pattern"] is None

    def test_collect_exact_facts_returns_four_sections_for_python_query_target(self, tmp_path):
        _build_python_query_repo(tmp_path)

        facts = collect_exact_facts(
            str(tmp_path),
            "server.py",
            "_handle_query",
            test_path="test_server.py",
        )

        assert [fact.name for fact in facts] == [
            "target_identity",
            "invocation_surface",
            "result_surface",
            "pytest_surface",
        ]
