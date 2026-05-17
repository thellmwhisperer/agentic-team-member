"""Tests for path and test-file discovery helpers."""

import subprocess

from agentic_tdd_runner.paths import find_test_file


def _config(exclude_dirs=None):
    return {
        "runner": {
            "test_file_patterns": ["*.test.ts"],
            "exclude_dirs": exclude_dirs or [],
        },
    }


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
