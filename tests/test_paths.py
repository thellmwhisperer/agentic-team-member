"""Tests for path and test-file discovery helpers."""

import subprocess

from agentic_tdd_runner.paths import find_test_file


def test_find_test_file_uses_git_status_candidates(tmp_path, monkeypatch):
    test_file = tmp_path / "src" / "new.test.ts"
    test_file.parent.mkdir()
    test_file.write_text("test('x', () => {});\n")

    def fake_run(cmd, **kwargs):
        if cmd == ["git", "status", "--porcelain", "-z"]:
            return subprocess.CompletedProcess(cmd, 0, stdout=b"?? src/new.test.ts\0", stderr=b"")
        raise AssertionError(f"unexpected command: {cmd}")

    monkeypatch.setattr("agentic_tdd_runner.paths.subprocess.run", fake_run)

    config = {
        "runner": {
            "test_file_patterns": ["*.test.ts"],
            "exclude_dirs": [],
        },
    }

    assert find_test_file(None, str(tmp_path), config) == "src/new.test.ts"
