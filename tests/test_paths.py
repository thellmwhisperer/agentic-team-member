"""Tests for path and test-file discovery helpers."""

from agentic_tdd_runner.paths import test_runner_command_for_file as command_for_file


def test_runner_command_for_file_uses_bootstrap_for_javascript_tests():
    config = {
        "runner": {
            "command": "bun test",
            "bootstrap": {
                "package_manager": "pnpm",
                "test_runner": "vitest",
            },
        },
    }

    assert command_for_file("src/math.test.ts", config) == "pnpm exec vitest run"


def test_runner_command_for_file_can_override_detected_runner():
    config = {
        "runner": {
            "command": "bun test",
            "framework": "bun:test",
            "override_detected": True,
            "bootstrap": {
                "package_manager": "pnpm",
                "test_runner": "vitest",
            },
        },
    }

    assert command_for_file("src/math.test.ts", config) == "bun test"


def test_runner_command_for_file_preserves_configured_fallback_for_custom_runner():
    config = {
        "runner": {
            "command": "turbo run test --filter web",
            "bootstrap": {
                "package_manager": "npm",
                "test_runner": "custom",
            },
        },
    }

    assert command_for_file("src/math.test.ts", config) == "turbo run test --filter web"


def test_runner_command_for_file_uses_custom_bootstrap_before_stale_bun():
    config = {
        "runner": {
            "command": "bun test",
            "bootstrap": {
                "package_manager": "npm",
                "test_runner": "custom",
                "test_command": "tsx --test src/**/*.test.ts",
            },
        },
    }

    assert command_for_file("src/math.test.ts", config) == "tsx --test"


def test_runner_command_for_file_keeps_pytest_for_python_tests():
    config = {
        "runner": {
            "command": "bun test",
            "bootstrap": {
                "package_manager": "pnpm",
                "test_runner": "vitest",
            },
        },
    }

    assert command_for_file("tests/test_math.py", config) == "python3 -m pytest"


def test_runner_command_for_file_unknown_language_does_not_default_to_bun():
    assert command_for_file("spec/worker_spec.rb", {"runner": {}}) == ""
