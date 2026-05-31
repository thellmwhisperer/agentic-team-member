"""Tests for deterministic runner facts."""

import json
import textwrap

from agentic_tdd_runner.runner_facts import build_runner_facts


def _write_file(tmp_path, relative_path, content):
    path = tmp_path / relative_path
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(textwrap.dedent(content))
    return path


def _config():
    return {
        "runner": {
            "command": "bun test",
            "framework": "bun:test",
            "test_file_patterns": [
                "*.test.ts",
                "*.test.tsx",
                "*.test.js",
                "*.test.jsx",
                "tests/**/*.spec.ts",
            ],
        },
        "environment": {"run_typecheck": True},
    }


def _episode():
    return {
        "source_file": "src/events/client.ts",
        "target_symbol": "processRenewal",
        "test_file": "src/events/processRenewal.test.ts",
        "runner": "bun:test",
        "function_line_range": {"start": 10, "end": 20, "source": "definition"},
    }


def test_build_runner_facts_reports_bun_test_import_and_commands(tmp_path):
    _write_file(tmp_path, "package.json", json.dumps({"scripts": {"typecheck": "tsc --noEmit"}}))
    _write_file(tmp_path, "src/events/client.ts", "export function processRenewal() {}\n")

    facts = build_runner_facts(str(tmp_path), _config(), episode=_episode())

    assert facts.test_runner == "bun:test"
    assert facts.test_command == "bun test"
    assert facts.typecheck_command == "bun run typecheck"
    assert facts.test_api_import == 'import { beforeEach, describe, expect, mock, test } from "bun:test";'
    assert facts.recommended_test_file == "src/events/processRenewal.test.ts"
    assert facts.source_line_range == {"start": 10, "end": 20, "source": "definition"}
    assert "mockFn.mockClear()" in facts.to_prompt_section()
    assert "mock.module(...)" in facts.to_prompt_section()
    assert "src/events/client.ts:10-20" in facts.to_prompt_section()
    assert facts.nearby_tests == []
    assert facts.symbol_tests == []


def test_build_runner_facts_discovers_existing_nearby_and_symbol_tests(tmp_path):
    _write_file(tmp_path, "package.json", json.dumps({"scripts": {"typecheck": "tsc --noEmit"}}))
    _write_file(tmp_path, "src/events/client.ts", "export function processRenewal() {}\n")
    _write_file(
        tmp_path,
        "src/events/client.test.ts",
        """
        import { test } from "bun:test";
        import { processRenewal } from "./client";

        test("processRenewal formats months", () => {
          processRenewal();
        });
        """,
    )

    facts = build_runner_facts(str(tmp_path), _config(), episode=_episode())

    assert facts.nearby_tests == ["src/events/client.test.ts"]
    assert facts.symbol_tests == ["src/events/client.test.ts"]
    assert "nearby tests in src/events: src/events/client.test.ts" in facts.to_prompt_section()


def test_build_runner_facts_honors_directory_aware_patterns(tmp_path):
    _write_file(tmp_path, "package.json", json.dumps({"scripts": {"typecheck": "tsc --noEmit"}}))
    _write_file(tmp_path, "src/events/client.ts", "export function processRenewal() {}\n")
    _write_file(
        tmp_path,
        "tests/events/processRenewal.spec.ts",
        """
        import { processRenewal } from "../../src/events/client";
        processRenewal();
        """,
    )

    facts = build_runner_facts(str(tmp_path), _config(), episode=_episode())

    assert facts.symbol_tests == ["tests/events/processRenewal.spec.ts"]


def test_build_runner_facts_excludes_build_artifact_directories_by_default(tmp_path):
    _write_file(tmp_path, "package.json", json.dumps({"scripts": {"typecheck": "tsc --noEmit"}}))
    _write_file(tmp_path, "src/events/client.ts", "export function processRenewal() {}\n")
    _write_file(tmp_path, "src/events/client.test.ts", "processRenewal();\n")
    _write_file(tmp_path, "dist/client.test.ts", "processRenewal();\n")
    _write_file(tmp_path, ".git/objects/noise.test.ts", "processRenewal();\n")

    facts = build_runner_facts(str(tmp_path), _config(), episode=_episode())

    assert facts.symbol_tests == ["src/events/client.test.ts"]
