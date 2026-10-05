"""Tests for shell command helpers."""

import os

from agentic_tdd_runner.config import load_config
from agentic_tdd_runner.shell import build_command_env


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


def test_build_command_env_reads_path_dirs_from_a_loaded_config(monkeypatch, tmp_path):
    monkeypatch.setenv("PATH", "/usr/bin")
    toml = tmp_path / "agent.toml"
    toml.write_text('[tools]\npath_dirs = ["/configured/bin"]\n')

    env = build_command_env(load_config(toml))

    assert "/configured/bin" in env["PATH"].split(os.pathsep)
