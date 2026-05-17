"""Tests for deterministic target environment preparation."""

import json
import os
from pathlib import Path
import subprocess
from types import SimpleNamespace

import pytest

from agentic_tdd_runner import agent
from agentic_tdd_runner import environment
from agentic_tdd_runner.environment import (
    EnvironmentPrepError,
    EnvironmentReport,
    WorktreePrepError,
    WorktreeReport,
    prepare_run_worktree,
    prepare_environment,
    recommended_tools_from_config,
)


def _completed(command, returncode=0, stdout="", stderr=""):
    return subprocess.CompletedProcess(command, returncode, stdout=stdout, stderr=stderr)


def _write_js_project(root, *, package_manager="bun@1.2.0", typecheck=True):
    scripts = {"typecheck": "tsc --noEmit"} if typecheck else {}
    (root / "package.json").write_text(json.dumps({
        "packageManager": package_manager,
        "scripts": scripts,
        "devDependencies": {"typescript": "^5.0.0"},
    }))
    (root / "bun.lock").write_text("")


class TestPrepareEnvironment:
    def test_javascript_project_installs_dependencies_and_runs_typecheck(self, tmp_path, monkeypatch):
        _write_js_project(tmp_path)
        calls = []

        def fake_run(root, command, timeout):
            calls.append(command)
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout="true\n")
            if command[:2] == ["git", "status"]:
                return _completed(command)
            return _completed(command, stdout="ok")

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)

        report = prepare_environment(str(tmp_path), {
            "environment": {"install": "auto", "run_typecheck": True},
            "timeouts": {"tool_execution": 60},
        })

        assert report.ready is True
        assert report.project_type == "javascript"
        assert report.package_manager == "bun"
        assert ["bun", "install", "--frozen-lockfile"] in calls
        assert ["bun", "run", "typecheck"] in calls

    def test_preflights_recommended_tools_with_command_env(self, tmp_path, monkeypatch):
        _write_js_project(tmp_path)
        calls = []

        def fake_run(root, command, timeout):
            calls.append(command)
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout="true\n")
            if command[:2] == ["git", "status"]:
                return _completed(command)
            return _completed(command, stdout="ok")

        def fake_which(tool, path):
            path_dirs = path.split(os.pathsep)
            assert "/opt/homebrew/bin" in path_dirs
            if tool == "rg":
                return "/opt/homebrew/bin/rg"
            return None

        monkeypatch.setenv("PATH", "/usr/bin")
        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)
        monkeypatch.setattr("agentic_tdd_runner.environment.shutil.which", fake_which)

        report = prepare_environment(str(tmp_path), {
            "environment": {"install": "never", "run_typecheck": False},
            "timeouts": {"tool_execution": 60},
            "tools": {"recommended": ["rg"]},
        })

        assert report.ready is True
        assert report.recommended_tools == ["rg"]
        assert report.resolved_tools == {"rg": "/opt/homebrew/bin/rg"}
        assert report.missing_tools == []
        assert "/opt/homebrew/bin" in report.tool_path_dirs
        tool_step = next(step for step in report.steps if step.name == "recommended_tools")
        assert tool_step.returncode == 0
        assert ["bun", "install", "--frozen-lockfile"] not in calls

    def test_missing_recommended_tool_fails_before_install(self, tmp_path, monkeypatch):
        _write_js_project(tmp_path)
        calls = []

        def fake_run(root, command, timeout):
            calls.append(command)
            return _completed(command, stdout="ok")

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)
        monkeypatch.setattr("agentic_tdd_runner.environment.shutil.which", lambda tool, path: None)

        with pytest.raises(EnvironmentPrepError, match="recommended tools unavailable: rg") as exc:
            prepare_environment(str(tmp_path), {
                "environment": {"install": "auto", "run_typecheck": True},
                "timeouts": {"tool_execution": 60},
                "tools": {"recommended": ["rg"]},
            })

        assert exc.value.report.ready is False
        assert exc.value.report.missing_tools == ["rg"]
        assert calls == []

    def test_recommended_tools_from_config_normalizes_and_dedupes(self):
        assert recommended_tools_from_config({
            "tooling": {"recommended": ["rg", "/opt/homebrew/bin/rg", "", 123]},
        }) == ["rg"]

    def test_javascript_project_skips_install_when_node_modules_exists(self, tmp_path, monkeypatch):
        _write_js_project(tmp_path)
        (tmp_path / "node_modules").mkdir()
        calls = []

        def fake_run(root, command, timeout):
            calls.append(command)
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout="true\n")
            if command[:2] == ["git", "status"]:
                return _completed(command)
            return _completed(command, stdout="ok")

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)

        report = prepare_environment(str(tmp_path), {
            "environment": {"install": "auto", "run_typecheck": True},
            "timeouts": {"tool_execution": 60},
        })

        assert report.ready is True
        assert ["bun", "install", "--frozen-lockfile"] not in calls
        install_step = next(step for step in report.steps if step.name == "install_dependencies")
        assert install_step.skipped is True

    def test_prepare_run_worktree_creates_detached_worktree_from_repo(self, tmp_path, monkeypatch):
        repo = tmp_path / "repo"
        destination = tmp_path / "run"
        repo.mkdir()
        calls = []

        def fake_run(root, command, timeout):
            calls.append((root, command))
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout=str(repo) + "\n")
            if command[:3] == ["git", "worktree", "add"]:
                return _completed(command)
            return _completed(command, returncode=1, stderr="unexpected")

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)

        report = prepare_run_worktree(
            str(repo),
            workdir=str(destination),
            base_ref="origin/main",
            run_root=str(tmp_path),
        )

        assert report.workdir == str(destination.resolve())
        assert report.base_ref == "origin/main"
        assert ["git", "worktree", "add", "--detach", str(destination.resolve()), "origin/main"] in [
            command for _root, command in calls
        ]

    def test_prepare_run_worktree_defaults_to_repo_local_worktree_dir(self, tmp_path, monkeypatch):
        repo = tmp_path / "repo"
        repo.mkdir()
        calls = []

        def fake_run(root, command, timeout):
            calls.append((root, command))
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout=str(repo) + "\n")
            if command[:3] == ["git", "worktree", "add"]:
                return _completed(command)
            return _completed(command, returncode=1, stderr="unexpected")

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)

        report = prepare_run_worktree(str(repo), base_ref="origin/main")

        assert report.workdir.startswith(str((repo / ".worktree").resolve()))
        assert report.workdir.endswith(Path(report.workdir).name)
        assert Path(report.workdir).name.startswith("atm-run-")
        assert ["git", "worktree", "add", "--detach", report.workdir, "origin/main"] in [
            command for _root, command in calls
        ]

    def test_prepare_run_worktree_defaults_to_main_ref(self, tmp_path, monkeypatch):
        repo = tmp_path / "repo"
        repo.mkdir()
        calls = []

        def fake_run(root, command, timeout):
            calls.append((root, command))
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout=str(repo) + "\n")
            if command[:3] == ["git", "worktree", "add"]:
                return _completed(command)
            return _completed(command, returncode=1, stderr="unexpected")

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)

        report = prepare_run_worktree(str(repo))

        assert report.base_ref == "main"
        assert ["git", "worktree", "add", "--detach", report.workdir, "main"] in [
            command for _root, command in calls
        ]

    def test_prepare_run_worktree_rejects_file_destination(self, tmp_path, monkeypatch):
        repo = tmp_path / "repo"
        destination = tmp_path / "run"
        repo.mkdir()
        destination.write_text("not a directory")

        def fake_run(root, command, timeout):
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout=str(repo) + "\n")
            raise AssertionError(f"Unexpected command: {command!r}")

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)

        with pytest.raises(WorktreePrepError, match="not a directory"):
            prepare_run_worktree(str(repo), workdir=str(destination))

    def test_prepare_run_worktree_rejects_file_repo(self, tmp_path):
        repo = tmp_path / "repo-file"
        repo.write_text("not a directory")

        with pytest.raises(WorktreePrepError, match="repo is not a directory"):
            prepare_run_worktree(str(repo))

    def test_dirty_worktree_fails_before_install(self, tmp_path, monkeypatch):
        _write_js_project(tmp_path)
        calls = []

        def fake_run(root, command, timeout):
            calls.append(command)
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout="true\n")
            if command[:2] == ["git", "status"]:
                return _completed(command, stdout=" M src/file.ts\n")
            return _completed(command, stdout="ok")

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)

        with pytest.raises(EnvironmentPrepError, match="uncommitted changes"):
            prepare_environment(str(tmp_path), {
                "environment": {"install": "auto", "run_typecheck": True},
                "timeouts": {"tool_execution": 60},
            })

        assert ["bun", "install", "--frozen-lockfile"] not in calls

    def test_git_probe_missing_binary_reports_context(self, tmp_path, monkeypatch):
        _write_js_project(tmp_path)

        def fake_run(root, command, timeout):
            if command[:2] == ["git", "rev-parse"]:
                raise FileNotFoundError("git")
            return _completed(command)

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)

        with pytest.raises(EnvironmentPrepError) as exc:
            prepare_environment(str(tmp_path), {
                "environment": {"install": "auto", "run_typecheck": True},
                "timeouts": {"tool_execution": 60},
            })

        assert exc.value.report.ready is False
        assert exc.value.report.reason == "git is unavailable"
        assert exc.value.report.steps[-1].name == "git_worktree"
        assert exc.value.report.steps[-1].returncode is None

    def test_git_status_timeout_reports_context(self, tmp_path, monkeypatch):
        _write_js_project(tmp_path)

        def fake_run(root, command, timeout):
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout="true\n")
            if command[:2] == ["git", "status"]:
                raise subprocess.TimeoutExpired(command, timeout)
            return _completed(command)

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)

        with pytest.raises(EnvironmentPrepError) as exc:
            prepare_environment(str(tmp_path), {
                "environment": {"install": "auto", "run_typecheck": True},
                "timeouts": {"tool_execution": 60},
            })

        assert exc.value.report.ready is False
        assert exc.value.report.reason == "git status check timed out"
        assert exc.value.report.steps[-1].name == "clean_worktree"
        assert exc.value.report.steps[-1].returncode is None

    def test_run_uses_shared_command_env(self, tmp_path, monkeypatch):
        def fake_subprocess_run(command, **kwargs):
            path_dirs = kwargs["env"]["PATH"].split(os.pathsep)
            assert "/opt/homebrew/bin" in path_dirs
            assert kwargs["env"]["CI"] == "1"
            return _completed(command, stdout="rg 1.0\n")

        monkeypatch.setenv("PATH", "/usr/bin")
        monkeypatch.delenv("CI", raising=False)
        monkeypatch.setattr("agentic_tdd_runner.environment.subprocess.run", fake_subprocess_run)

        result = environment._run(tmp_path, ["rg", "--version"], timeout=3)

        assert result.returncode == 0


class TestMainEnvironmentPrep:
    def _make_config(self):
        return {
            "agent": {"max_steps": 1, "max_tool_output": 8000},
            "environment": {"enabled": True},
            "llm": {"model": "test-model", "url": "http://localhost:9999"},
            "timeouts": {"tool_execution": 10, "llm_request": 10, "test_run": 10},
            "runner": {"command": "bun test", "test_file_patterns": ["*.test.ts"], "exclude_dirs": []},
            "prompt": {"system": "system", "no_test_found": "no test", "quality_failed": "{details}"},
            "verification": {"max_rejections": 1},
            "quality": {"enabled": False},
            "tools": [],
        }

    def test_main_exits_before_discovery_and_chat_when_environment_is_not_ready(self, tmp_path, monkeypatch):
        args = SimpleNamespace(
            issue="bug text",
            source=None,
            symbol=None,
            workdir=str(tmp_path),
            config="unused.toml",
            log_dir=str(tmp_path),
        )
        report = EnvironmentReport(workdir=str(tmp_path), ready=False, reason="missing deps")

        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: self._make_config())
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr(
            "agentic_tdd_runner.environment.prepare_environment",
            lambda workdir, config: (_ for _ in ()).throw(EnvironmentPrepError("missing deps", report)),
        )
        monkeypatch.setattr(
            "agentic_tdd_runner.discovery.load_or_build_semantic_index",
            lambda *a, **kw: (_ for _ in ()).throw(AssertionError("discovery should not run")),
        )
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.chat",
            lambda *a, **kw: (_ for _ in ()).throw(AssertionError("chat should not run")),
        )

        with pytest.raises(SystemExit, match="Target environment is not ready"):
            agent.main()

    def test_main_materializes_worktree_before_environment_prep(self, tmp_path, monkeypatch):
        run_worktree = tmp_path / "run"
        args = SimpleNamespace(
            issue="bug text",
            repo=str(tmp_path / "repo"),
            base_ref="origin/main",
            run_root=str(tmp_path),
            source="src/file.ts",
            symbol="target",
            workdir=None,
            config="unused.toml",
            log_dir=str(tmp_path),
        )
        worktree_report = WorktreeReport(
            repo=str(tmp_path / "repo"),
            workdir=str(run_worktree),
            base_ref="origin/main",
            command=["git", "worktree", "add", "--detach", str(run_worktree), "origin/main"],
        )
        failed_report = EnvironmentReport(workdir=str(run_worktree), ready=False, reason="stop after assert")

        def fake_prepare_environment(workdir, config):
            assert workdir == str(run_worktree)
            raise EnvironmentPrepError("stop after assert", failed_report)

        monkeypatch.setattr("agentic_tdd_runner.agent.parse_args", lambda: args)
        monkeypatch.setattr("agentic_tdd_runner.config.load_config", lambda path: self._make_config())
        monkeypatch.setattr("agentic_tdd_runner.agent.init_log", lambda: str(tmp_path / "agent.jsonl"))
        monkeypatch.setattr("agentic_tdd_runner.agent.emit", lambda msg: None)
        monkeypatch.setattr("agentic_tdd_runner.agent.log", lambda *a, **kw: None)
        monkeypatch.setattr(
            "agentic_tdd_runner.environment.prepare_run_worktree",
            lambda *a, **kw: worktree_report,
        )
        monkeypatch.setattr(
            "agentic_tdd_runner.environment.prepare_environment",
            fake_prepare_environment,
        )
        monkeypatch.setattr(
            "agentic_tdd_runner.agent.chat",
            lambda *a, **kw: (_ for _ in ()).throw(AssertionError("chat should not run")),
        )

        with pytest.raises(SystemExit, match="Target environment is not ready"):
            agent.main()
