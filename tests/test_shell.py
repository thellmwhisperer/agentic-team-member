"""Tests for shell command helpers."""

import os

import pytest

from agentic_tdd_runner.shell import build_command_env, validate_command


def test_build_command_env_preserves_path_and_adds_common_tool_dirs(monkeypatch):
    monkeypatch.setenv("PATH", os.pathsep.join(["/custom/bin", "/usr/bin"]))

    env = build_command_env()

    path_dirs = env["PATH"].split(os.pathsep)
    assert path_dirs[:2] == ["/custom/bin", "/usr/bin"]
    assert "/opt/homebrew/bin" in path_dirs
    assert os.path.expanduser("~/.bun/bin") in path_dirs
    assert path_dirs.count("/usr/bin") == 1


def test_build_command_env_accepts_configured_tool_dirs(monkeypatch):
    monkeypatch.setenv("PATH", "/usr/bin")

    env = build_command_env({"tools": {"path_dirs": ["~/tools", "/custom/bin"]}})

    path_dirs = env["PATH"].split(os.pathsep)
    assert os.path.expanduser("~/tools") in path_dirs
    assert "/custom/bin" in path_dirs


def test_build_command_env_accepts_loaded_tooling_config(monkeypatch):
    monkeypatch.setenv("PATH", "/usr/bin")

    env = build_command_env({
        "tools": [{"type": "function", "function": {"name": "run_command"}}],
        "tooling": {"path_dirs": ["/configured/bin"]},
    })

    assert "/configured/bin" in env["PATH"].split(os.pathsep)


def test_validate_command_rejects_cd_chains():
    with pytest.raises(ValueError, match="command 'cd' is not allowed"):
        validate_command("cd apps/web && bun test src/client.test.ts")
