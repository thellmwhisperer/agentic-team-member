"""Tests for path and test-file discovery helpers."""

import pytest

from agentic_tdd_runner.paths import resolve_repo_path
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


# Recovered from tests/test_agent.py before the prune (0e9a197).
class TestResolveRepoPath:
    """Path resolution must confine access to the workdir."""

    def test_normal_relative_path(self, tmp_path):
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "file.ts").write_text("hello")
        result = resolve_repo_path("src/file.ts", str(tmp_path))
        assert result == tmp_path / "src" / "file.ts"

    def test_rejects_parent_traversal(self, tmp_path):
        with pytest.raises(ValueError, match="escapes workdir"):
            resolve_repo_path("../../../etc/passwd", str(tmp_path))

    def test_rejects_absolute_path(self, tmp_path):
        with pytest.raises(ValueError, match="escapes workdir"):
            resolve_repo_path("/etc/passwd", str(tmp_path))

    def test_rejects_sneaky_traversal(self, tmp_path):
        with pytest.raises(ValueError, match="escapes workdir"):
            resolve_repo_path("src/../../outside", str(tmp_path))

    def test_allows_nested_paths(self, tmp_path):
        (tmp_path / "src" / "deep").mkdir(parents=True)
        (tmp_path / "src" / "deep" / "file.ts").write_text("ok")
        result = resolve_repo_path("src/deep/file.ts", str(tmp_path))
        assert result == tmp_path / "src" / "deep" / "file.ts"
