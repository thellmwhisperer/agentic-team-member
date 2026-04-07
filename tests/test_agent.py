"""Tests for agent tool execution and file discovery."""
import os
import subprocess

import pytest

from agentic_tdd_runner.agent import _resolve_repo_path, find_test_file


class TestResolveRepoPath:
    """Path resolution must confine access to the workdir."""

    def test_normal_relative_path(self, tmp_path):
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "file.ts").write_text("hello")
        result = _resolve_repo_path("src/file.ts", str(tmp_path))
        assert result == tmp_path / "src" / "file.ts"

    def test_rejects_parent_traversal(self, tmp_path):
        with pytest.raises(ValueError, match="escapes workdir"):
            _resolve_repo_path("../../../etc/passwd", str(tmp_path))

    def test_rejects_absolute_path(self, tmp_path):
        with pytest.raises(ValueError, match="escapes workdir"):
            _resolve_repo_path("/etc/passwd", str(tmp_path))

    def test_rejects_sneaky_traversal(self, tmp_path):
        with pytest.raises(ValueError, match="escapes workdir"):
            _resolve_repo_path("src/../../outside", str(tmp_path))

    def test_allows_nested_paths(self, tmp_path):
        (tmp_path / "src" / "deep").mkdir(parents=True)
        (tmp_path / "src" / "deep" / "file.ts").write_text("ok")
        result = _resolve_repo_path("src/deep/file.ts", str(tmp_path))
        assert result == tmp_path / "src" / "deep" / "file.ts"


class TestFindTestFile:
    """find_test_file discovers new test files matching configured patterns."""

    def _setup_git_repo(self, tmp_path):
        subprocess.run("git init", shell=True, cwd=tmp_path, capture_output=True)
        subprocess.run("git commit --allow-empty -m init", shell=True, cwd=tmp_path, capture_output=True)

    def test_finds_ts_test_file(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "foo.test.ts").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts", "*.test.tsx", "*.test.js", "test_*.py"], "exclude_dirs": ["node_modules"]},
        })
        assert find_test_file() == "src/foo.test.ts"

    def test_finds_python_test_file(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "test_worker.py").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts", "test_*.py"], "exclude_dirs": ["node_modules"]},
        })
        assert find_test_file() == "src/test_worker.py"

    def test_excludes_node_modules(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "node_modules" / "pkg").mkdir(parents=True)
        (tmp_path / "node_modules" / "pkg" / "foo.test.ts").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts"], "exclude_dirs": ["node_modules"]},
        })
        assert find_test_file() is None

    def test_uses_hint_path_when_exists(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "handleResub.test.ts").write_text("test")
        (tmp_path / "src" / "other.test.ts").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
        })
        # With hint, returns the hinted path directly
        assert find_test_file(hint="src/handleResub.test.ts") == "src/handleResub.test.ts"

    def test_falls_back_to_discovery_when_hint_missing(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "foo.test.ts").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
        })
        # Hint file doesn't exist, falls back to discovery
        assert find_test_file(hint="src/nonexistent.test.ts") == "src/foo.test.ts"

    def test_verify_red_green_preserves_untracked_test(self, tmp_path, monkeypatch):
        """An untracked test file must survive the stash cycle in verify_red_green."""
        import subprocess as sp
        from agentic_tdd_runner.agent import verify_red_green

        # Set up a git repo with committed source
        sp.run("git init", shell=True, cwd=tmp_path, capture_output=True)
        sp.run(["git", "config", "user.email", "test@test.com"], cwd=tmp_path, capture_output=True)
        sp.run(["git", "config", "user.name", "test"], cwd=tmp_path, capture_output=True)
        src = tmp_path / "src"
        src.mkdir()
        (src / "math.ts").write_text("original")
        sp.run("git add -A && git commit -m base", shell=True, cwd=tmp_path, capture_output=True)

        # Agent creates: modified source (fix) + new untracked test file
        (src / "math.ts").write_text("fixed")
        (src / "math.test.ts").write_text("test content")

        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"command": "echo", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "timeouts": {"test_run": 10},
        })

        verify_red_green("src/math.test.ts")

        # Both files must exist after verification
        assert (src / "math.test.ts").exists(), "Untracked test file disappeared"
        assert (src / "math.ts").read_text() == "fixed", "Source fix not restored"

    def test_python_test_uses_pytest_command(self, tmp_path, monkeypatch):
        """When a Python test is found, verify_red_green should use pytest, not bun test."""
        # This tests that the runner command adapts to the file type
        from agentic_tdd_runner.languages import get_language
        lang = get_language("test_worker.py")
        assert lang is not None
        assert lang.runner == "pytest"

    def test_finds_tsx_test_file(self, tmp_path, monkeypatch):
        self._setup_git_repo(tmp_path)
        (tmp_path / "src").mkdir()
        (tmp_path / "src" / "widget.test.tsx").write_text("test")
        monkeypatch.setattr("agentic_tdd_runner.agent.WORKDIR", str(tmp_path))
        monkeypatch.setattr("agentic_tdd_runner.agent._CONFIG", {
            "runner": {"test_file_patterns": ["*.test.ts", "*.test.tsx"], "exclude_dirs": []},
        })
        assert find_test_file() == "src/widget.test.tsx"
