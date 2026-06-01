"""Tests for path and test-file discovery helpers."""

import subprocess

from agentic_tdd_runner.paths import find_test_file
from agentic_tdd_runner.paths import test_runner_command_for_file as command_for_file


def _config(exclude_dirs=None):
    return {
        "runner": {
            "test_file_patterns": ["*.test.ts"],
            "exclude_dirs": exclude_dirs or [],
        },
    }


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


def _pretend_git(monkeypatch):
    monkeypatch.setattr(
        "agentic_tdd_runner.paths.shutil.which",
        lambda binary: "git" if binary == "git" else None,
    )


def test_find_test_file_uses_git_status_candidates(tmp_path, monkeypatch):
    test_file = tmp_path / "src" / "new.test.ts"
    test_file.parent.mkdir()
    test_file.write_text("test('x', () => {});\n")

    def fake_run(cmd, **kwargs):
        if cmd == ["git", "status", "--porcelain", "-z"]:
            return subprocess.CompletedProcess(cmd, 0, stdout=b"?? src/new.test.ts\0", stderr=b"")
        raise AssertionError(f"unexpected command: {cmd}")

    _pretend_git(monkeypatch)
    monkeypatch.setattr("agentic_tdd_runner.paths.subprocess.run", fake_run)

    assert find_test_file(None, str(tmp_path), _config()) == "src/new.test.ts"


def test_find_test_file_uses_existing_hint_before_git(tmp_path, monkeypatch):
    test_file = tmp_path / "src" / "hinted.test.ts"
    test_file.parent.mkdir()
    test_file.write_text("test('x', () => {});\n")

    def fake_run(cmd, **kwargs):
        raise AssertionError(f"unexpected command: {cmd}")

    monkeypatch.setattr("agentic_tdd_runner.paths.subprocess.run", fake_run)

    assert find_test_file("src/hinted.test.ts", str(tmp_path), _config()) == "src/hinted.test.ts"


def test_find_test_file_skips_rename_old_path_entry(tmp_path, monkeypatch):
    old_file = tmp_path / "src" / "old.test.ts"
    old_file.parent.mkdir()
    old_file.write_text("test('old', () => {});\n")

    def fake_run(cmd, **kwargs):
        if cmd == ["git", "status", "--porcelain", "-z"]:
            return subprocess.CompletedProcess(
                cmd,
                0,
                stdout=b"R  src/not-a-test.ts\0src/old.test.ts\0",
                stderr=b"",
            )
        if cmd[:3] == ["git", "ls-files", "--"]:
            return subprocess.CompletedProcess(cmd, 0, stdout="src/old.test.ts\n", stderr="")
        raise AssertionError(f"unexpected command: {cmd}")

    _pretend_git(monkeypatch)
    monkeypatch.setattr("agentic_tdd_runner.paths.subprocess.run", fake_run)

    assert find_test_file(None, str(tmp_path), _config()) is None


def test_find_test_file_filters_excluded_status_paths(tmp_path, monkeypatch):
    test_file = tmp_path / "generated" / "tests" / "skip.test.ts"
    test_file.parent.mkdir(parents=True)
    test_file.write_text("test('skip', () => {});\n")

    def fake_run(cmd, **kwargs):
        if cmd == ["git", "status", "--porcelain", "-z"]:
            return subprocess.CompletedProcess(
                cmd,
                0,
                stdout=b"?? generated/tests/skip.test.ts\0",
                stderr=b"",
            )
        if cmd[:3] == ["git", "ls-files", "--"]:
            return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
        raise AssertionError(f"unexpected command: {cmd}")

    _pretend_git(monkeypatch)
    monkeypatch.setattr("agentic_tdd_runner.paths.subprocess.run", fake_run)

    assert find_test_file(None, str(tmp_path), _config(["generated/tests"])) is None


def test_find_test_file_falls_back_to_untracked_walk_candidate(tmp_path, monkeypatch):
    test_file = tmp_path / "src" / "walk.test.ts"
    test_file.parent.mkdir()
    test_file.write_text("test('walk', () => {});\n")

    def fake_run(cmd, **kwargs):
        if cmd == ["git", "status", "--porcelain", "-z"]:
            return subprocess.CompletedProcess(cmd, 0, stdout=b"", stderr=b"")
        if cmd == ["git", "ls-files", "--", "src/walk.test.ts"]:
            return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
        raise AssertionError(f"unexpected command: {cmd}")

    _pretend_git(monkeypatch)
    monkeypatch.setattr("agentic_tdd_runner.paths.subprocess.run", fake_run)

    assert find_test_file(None, str(tmp_path), _config()) == "src/walk.test.ts"


def test_find_test_file_falls_back_when_git_status_times_out(tmp_path, monkeypatch):
    test_file = tmp_path / "src" / "walk.test.ts"
    test_file.parent.mkdir()
    test_file.write_text("test('walk', () => {});\n")

    def fake_run(cmd, **kwargs):
        if cmd == ["git", "status", "--porcelain", "-z"]:
            raise subprocess.TimeoutExpired(cmd, timeout=kwargs["timeout"])
        if cmd == ["git", "ls-files", "--", "src/walk.test.ts"]:
            return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
        raise AssertionError(f"unexpected command: {cmd}")

    _pretend_git(monkeypatch)
    monkeypatch.setattr("agentic_tdd_runner.paths.subprocess.run", fake_run)

    assert find_test_file(None, str(tmp_path), _config()) == "src/walk.test.ts"


def test_find_test_file_skips_walk_candidate_when_git_ls_files_times_out(tmp_path, monkeypatch):
    test_file = tmp_path / "src" / "walk.test.ts"
    test_file.parent.mkdir()
    test_file.write_text("test('walk', () => {});\n")

    def fake_run(cmd, **kwargs):
        if cmd == ["git", "status", "--porcelain", "-z"]:
            return subprocess.CompletedProcess(cmd, 0, stdout=b"", stderr=b"")
        if cmd == ["git", "ls-files", "--", "src/walk.test.ts"]:
            raise subprocess.TimeoutExpired(cmd, timeout=kwargs["timeout"])
        raise AssertionError(f"unexpected command: {cmd}")

    _pretend_git(monkeypatch)
    monkeypatch.setattr("agentic_tdd_runner.paths.subprocess.run", fake_run)

    assert find_test_file(None, str(tmp_path), _config()) is None


def test_find_test_file_skips_walk_candidate_when_git_ls_files_fails(tmp_path, monkeypatch):
    test_file = tmp_path / "src" / "walk.test.ts"
    test_file.parent.mkdir()
    test_file.write_text("test('walk', () => {});\n")

    def fake_run(cmd, **kwargs):
        if cmd == ["git", "status", "--porcelain", "-z"]:
            return subprocess.CompletedProcess(cmd, 0, stdout=b"", stderr=b"")
        if cmd == ["git", "ls-files", "--", "src/walk.test.ts"]:
            return subprocess.CompletedProcess(cmd, 128, stdout="", stderr="fatal: not a git repository")
        raise AssertionError(f"unexpected command: {cmd}")

    _pretend_git(monkeypatch)
    monkeypatch.setattr("agentic_tdd_runner.paths.subprocess.run", fake_run)

    assert find_test_file(None, str(tmp_path), _config()) is None


def test_find_test_file_excludes_walk_paths_by_path_component(tmp_path, monkeypatch):
    test_file = tmp_path / "distribution" / "walk.test.ts"
    test_file.parent.mkdir()
    test_file.write_text("test('walk', () => {});\n")

    def fake_run(cmd, **kwargs):
        if cmd == ["git", "status", "--porcelain", "-z"]:
            return subprocess.CompletedProcess(cmd, 0, stdout=b"", stderr=b"")
        if cmd == ["git", "ls-files", "--", "distribution/walk.test.ts"]:
            return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
        raise AssertionError(f"unexpected command: {cmd}")

    _pretend_git(monkeypatch)
    monkeypatch.setattr("agentic_tdd_runner.paths.subprocess.run", fake_run)

    assert find_test_file(None, str(tmp_path), _config(["dist"])) == "distribution/walk.test.ts"


def test_find_test_file_prunes_excluded_walk_subtrees(tmp_path, monkeypatch):
    visited_after_prune = []

    def fake_walk(workdir):
        dirs = ["node_modules", "src"]
        yield str(tmp_path), dirs, []
        visited_after_prune.extend(dirs)
        if "node_modules" in dirs:
            yield str(tmp_path / "node_modules"), [], ["ignored.test.ts"]
        if "src" in dirs:
            yield str(tmp_path / "src"), [], ["walk.test.ts"]

    def fake_run(cmd, **kwargs):
        if cmd == ["git", "status", "--porcelain", "-z"]:
            return subprocess.CompletedProcess(cmd, 0, stdout=b"", stderr=b"")
        if cmd == ["git", "ls-files", "--", "src/walk.test.ts"]:
            return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
        raise AssertionError(f"unexpected command: {cmd}")

    _pretend_git(monkeypatch)
    monkeypatch.setattr("agentic_tdd_runner.paths.os.walk", fake_walk)
    monkeypatch.setattr("agentic_tdd_runner.paths.subprocess.run", fake_run)

    assert find_test_file(None, str(tmp_path), _config(["node_modules"])) == "src/walk.test.ts"
    assert visited_after_prune == ["src"]


def test_find_test_file_uses_default_patterns_without_runner_config(tmp_path, monkeypatch):
    test_file = tmp_path / "src" / "walk.test.ts"
    test_file.parent.mkdir()
    test_file.write_text("test('walk', () => {});\n")

    def fake_run(cmd, **kwargs):
        if cmd == ["git", "status", "--porcelain", "-z"]:
            return subprocess.CompletedProcess(cmd, 0, stdout=b"", stderr=b"")
        if cmd == ["git", "ls-files", "--", "src/walk.test.ts"]:
            return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")
        raise AssertionError(f"unexpected command: {cmd}")

    _pretend_git(monkeypatch)
    monkeypatch.setattr("agentic_tdd_runner.paths.subprocess.run", fake_run)

    assert find_test_file(None, str(tmp_path), {}) == "src/walk.test.ts"
