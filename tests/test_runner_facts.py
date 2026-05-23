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
        "source_file": "src/twitch/client.ts",
        "target_symbol": "handleResub",
        "test_file": "src/twitch/handleResub.test.ts",
        "runner": "bun:test",
    }


def test_build_runner_facts_reports_bun_test_import_and_commands(tmp_path):
    _write_file(tmp_path, "package.json", json.dumps({"scripts": {"typecheck": "tsc --noEmit"}}))
    _write_file(tmp_path, "src/twitch/client.ts", "export function handleResub() {}\n")

    facts = build_runner_facts(str(tmp_path), _config(), episode=_episode())

    assert facts.test_runner == "bun:test"
    assert facts.test_command == "bun test"
    assert facts.typecheck_command == "bun run typecheck"
    assert facts.test_api_import == 'import { beforeEach, describe, expect, mock, test } from "bun:test";'
    assert facts.recommended_test_file == "src/twitch/handleResub.test.ts"
    assert facts.nearby_tests == []
    assert facts.symbol_tests == []


def test_build_runner_facts_discovers_existing_nearby_and_symbol_tests(tmp_path):
    _write_file(tmp_path, "package.json", json.dumps({"scripts": {"typecheck": "tsc --noEmit"}}))
    _write_file(tmp_path, "src/twitch/client.ts", "export function handleResub() {}\n")
    _write_file(
        tmp_path,
        "src/twitch/client.test.ts",
        """
        import { test } from "bun:test";
        import { handleResub } from "./client";

        test("handleResub formats months", () => {
          handleResub();
        });
        """,
    )

    facts = build_runner_facts(str(tmp_path), _config(), episode=_episode())

    assert facts.nearby_tests == ["src/twitch/client.test.ts"]
    assert facts.symbol_tests == ["src/twitch/client.test.ts"]
    assert "nearby tests in src/twitch: src/twitch/client.test.ts" in facts.to_prompt_section()


def test_build_runner_facts_honors_directory_aware_patterns(tmp_path):
    _write_file(tmp_path, "package.json", json.dumps({"scripts": {"typecheck": "tsc --noEmit"}}))
    _write_file(tmp_path, "src/twitch/client.ts", "export function handleResub() {}\n")
    _write_file(
        tmp_path,
        "tests/twitch/handleResub.spec.ts",
        """
        import { handleResub } from "../../src/twitch/client";
        handleResub();
        """,
    )

    facts = build_runner_facts(str(tmp_path), _config(), episode=_episode())

    assert facts.symbol_tests == ["tests/twitch/handleResub.spec.ts"]
