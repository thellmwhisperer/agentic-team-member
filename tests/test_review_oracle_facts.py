"""Tests for agentic_tdd_runner.review_oracle.facts local and repo-scoped detectors."""
from __future__ import annotations

import dataclasses
import textwrap

import pytest

from agentic_tdd_runner.review_oracle.facts import (
    collect_repo_facts,
    collect_exact_facts,
    extract_invocation_surface,
    extract_patch_semantics,
    extract_pytest_surface,
    extract_result_surface,
    extract_schema_surface,
    extract_target_identity,
    extract_upstream_return_surface,
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


def _build_repo_scoped_fact_repo(tmp_path):
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


        from unittest.mock import patch


        def test_patch_examples():
            with patch("pkg.logger"):
                pass
            with patch("pkg.logger"):
                pass
    """)
    _write_file(tmp_path, "test_query.py", """\
        from unittest.mock import patch as mock_patch


        def test_more_patch_examples():
            with mock_patch("pkg.logger"):
                pass
            with mock_patch("pkg.client"):
                pass
    """)
    _write_file(tmp_path, "schema.sql", """\
        CREATE TABLE query_cache (
            id INTEGER PRIMARY KEY,
            sql TEXT NOT NULL,
            path TEXT NOT NULL,
            fallback_reason TEXT
        );

        CREATE TABLE query_runs (
            run_id INTEGER PRIMARY KEY,
            query_cache_id INTEGER NOT NULL,
            status TEXT NOT NULL
        );

        INSERT INTO query_cache (id, sql, path)
        VALUES (1, 'SELECT 1', 'compiler');
    """)
    _write_file(tmp_path, "query.py", """\
        def compiler_query():
            payload = {
                "rows": [],
                "sql": "SELECT 1",
                "path": "compiler",
                "confidence": 0.95,
            }
            return payload


        def fallback_query():
            return {
                "rows": [],
                "sql": "SELECT 2",
                "path": "llm_fallback",
                "fallback_reason": "empty",
                "retried": True,
            }


        def passthrough_query(result):
            return result
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

    def test_invocation_surface_ignores_non_self_dispatch_candidates(self, tmp_path):
        _write_file(tmp_path, "server.py", """\
            import json

            class Client:
                def _handle_query(self, args):
                    return {"rows": ["client"]}

            class ToolHandler:
                def call(self, name, args):
                    result, _meta = self._dispatch(name, args)
                    return {"content": [{"type": "text", "text": json.dumps(result)}]}

                def _dispatch(self, name, args):
                    client = Client()
                    if name == "client_query":
                        return client._handle_query(args), None
                    if name == "roca_query":
                        return self._handle_query(args), None
                    raise ValueError("unknown tool")

                def _handle_query(self, args):
                    return {"rows": ["self"]}
        """)

        fact = extract_invocation_surface(
            str(tmp_path),
            "server.py",
            "ToolHandler",
            "_handle_query",
        )

        assert fact.value["dispatch_key"] == "roca_query"
        assert fact.value["dispatch_branch"] == "server.py:16-17"

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


class TestRepoFacts:
    def test_patch_semantics_collects_local_targets_and_repo_counts(self, tmp_path):
        _build_repo_scoped_fact_repo(tmp_path)

        fact = extract_patch_semantics(str(tmp_path), "test_server.py")

        assert fact.name == "patch_semantics"
        assert fact.value["module_test_file"] == "test_server.py"
        assert fact.value["observed_patch_aliases"] == ["patch"]
        assert fact.value["local_patch_targets"] == [
            {"target": "pkg.logger", "alias": "patch", "range": "test_server.py:28"},
            {"target": "pkg.logger", "alias": "patch", "range": "test_server.py:30"},
        ]
        assert fact.value["total_patch_target_counts"] == [
            {"target": "pkg.client", "count": 1},
            {"target": "pkg.logger", "count": 3},
        ]
        assert fact.confidence_class == "repo_counted"

    def test_patch_semantics_skips_parse_errors_and_third_party_dirs(self, tmp_path):
        _build_repo_scoped_fact_repo(tmp_path)
        _write_file(tmp_path, "legacy_bad.py", """\
            def broken(:
                pass
        """)
        _write_file(tmp_path, ".venv/site-packages/vendor_patch.py", """\
            from unittest.mock import patch

            def test_vendor_patch():
                with patch("pkg.vendor"):
                    pass
        """)

        with pytest.warns(UserWarning, match="legacy_bad.py"):
            fact = extract_patch_semantics(str(tmp_path), "test_server.py")

        assert fact.value["total_patch_target_counts"] == [
            {"target": "pkg.client", "count": 1},
            {"target": "pkg.logger", "count": 3},
        ]

    def test_schema_surface_collects_tables_and_seed_inserts(self, tmp_path):
        _build_repo_scoped_fact_repo(tmp_path)

        fact = extract_schema_surface(str(tmp_path), "schema.sql")

        assert fact.name == "schema_surface"
        assert fact.value["schema_file"] == "schema.sql"
        assert fact.value["tables"] == [
            {
                "name": "query_cache",
                "columns": ["id", "sql", "path", "fallback_reason"],
                "range": "schema.sql:1-6",
            },
            {
                "name": "query_runs",
                "columns": ["run_id", "query_cache_id", "status"],
                "range": "schema.sql:8-12",
            },
        ]
        assert fact.value["seed_inserts"] == [
            {
                "table": "query_cache",
                "columns": ["id", "sql", "path"],
                "range": "schema.sql:14-15",
            },
        ]
        assert fact.confidence_class == "text_exact"

    def test_upstream_return_surface_collects_function_return_shapes(self, tmp_path):
        _build_repo_scoped_fact_repo(tmp_path)

        fact = extract_upstream_return_surface(str(tmp_path), "query.py")

        assert fact.name == "upstream_return_surface"
        assert fact.value["module_source_file"] == "query.py"
        assert fact.value["functions"] == [
            {
                "name": "compiler_query",
                "range": "query.py:1-8",
                "return_key_sets": [["confidence", "path", "rows", "sql"]],
            },
            {
                "name": "fallback_query",
                "range": "query.py:11-18",
                "return_key_sets": [["fallback_reason", "path", "retried", "rows", "sql"]],
            },
        ]
        assert fact.confidence_class == "ast_exact"

    def test_collect_repo_facts_returns_repo_scoped_sections(self, tmp_path):
        _build_repo_scoped_fact_repo(tmp_path)

        facts = collect_repo_facts(
            str(tmp_path),
            test_path="test_server.py",
            schema_path="schema.sql",
            upstream_path="query.py",
        )

        assert [fact.name for fact in facts] == [
            "patch_semantics",
            "schema_surface",
            "upstream_return_surface",
        ]
