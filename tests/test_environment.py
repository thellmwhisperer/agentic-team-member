"""Tests for deterministic target environment preparation."""

import json
import os
import subprocess

import pytest

from agentic_tdd_runner import environment
from agentic_tdd_runner.environment import (
    EnvironmentPrepError,
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

        def fake_run(root, command, timeout, config=None):
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
        assert ["bun", "--version"] in calls
        assert ["bun", "run", "typecheck"] in calls

    def test_records_runner_bootstrap_and_uses_detected_package_manager(self, tmp_path, monkeypatch):
        (tmp_path / "pnpm-lock.yaml").write_text("")
        package_dir = tmp_path / "apps" / "web"
        package_dir.mkdir(parents=True)
        (package_dir / "package.json").write_text(json.dumps({
            "scripts": {"test": "vitest run", "typecheck": "tsc --noEmit"},
            "devDependencies": {"vitest": "^4.0.0"},
        }))
        calls = []

        def fake_run(root, command, timeout, config=None):
            calls.append(command)
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout="true\n")
            if command[:2] == ["git", "status"]:
                return _completed(command)
            return _completed(command, stdout="ok")

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)

        report = prepare_environment(str(package_dir), {
            "environment": {"install": "auto", "run_typecheck": True},
            "timeouts": {"tool_execution": 60},
        })

        assert report.runner_bootstrap is not None
        assert report.runner_bootstrap.package_dir == str(package_dir)
        assert report.runner_bootstrap.monorepo_root == str(tmp_path)
        assert report.runner_bootstrap.package_manager == "pnpm"
        assert report.runner_bootstrap.test_runner == "vitest"
        assert report.package_manager == "pnpm"
        assert ["pnpm", "install", "--frozen-lockfile"] in calls
        assert ["pnpm", "exec", "vitest", "--version"] in calls
        assert ["pnpm", "run", "typecheck"] in calls
        assert calls.index(["pnpm", "exec", "vitest", "--version"]) < calls.index(["pnpm", "run", "typecheck"])
        assert "runner_bootstrap" not in report.to_log_dict()

    def test_runner_version_preflight_failure_stops_environment_prep(self, tmp_path, monkeypatch):
        _write_js_project(tmp_path)
        calls = []

        def fake_run(root, command, timeout, config=None):
            calls.append(command)
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout="true\n")
            if command[:2] == ["git", "status"]:
                return _completed(command)
            if command == ["bun", "--version"]:
                return _completed(command, returncode=1, stderr="bun unavailable\n")
            return _completed(command, stdout="ok")

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)

        with pytest.raises(EnvironmentPrepError, match="environment step failed: bun --version") as exc:
            prepare_environment(str(tmp_path), {
                "environment": {"install": "never", "run_typecheck": True},
                "timeouts": {"tool_execution": 60},
            })

        assert exc.value.report.steps[-1].name == "preflight_1"
        assert ["bun", "run", "typecheck"] not in calls

    def test_generates_next_jest_config_shim_before_preflight(self, tmp_path, monkeypatch):
        (tmp_path / "package-lock.json").write_text("")
        (tmp_path / "jest.config.ts").write_text(
            "import workspacePreset from '@repo/jest-preset';\n"
            "export default workspacePreset;\n"
        )
        (tmp_path / "jest.setup.ts").write_text("import '@testing-library/jest-dom';\n")
        (tmp_path / "package.json").write_text(json.dumps({
            "packageManager": "npm@10.0.0",
            "scripts": {"test": "jest"},
            "devDependencies": {
                "jest": "^30.0.0",
                "next": "^16.0.0",
            },
        }))
        calls = []

        def fake_run(root, command, timeout, config=None):
            calls.append(command)
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout="true\n")
            if command[:2] == ["git", "status"]:
                assert not (tmp_path / ".atm-jest.config.cjs").exists()
                return _completed(command)
            return _completed(command, stdout="ok")

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)

        report = prepare_environment(str(tmp_path), {
            "environment": {"install": "never", "run_typecheck": False},
            "timeouts": {"tool_execution": 60},
        })

        shim = tmp_path / ".atm-jest.config.cjs"
        assert report.ready is True
        assert report.runner_bootstrap.test_config_path == str(shim)
        assert report.runner_bootstrap.original_test_config_path == str(tmp_path / "jest.config.ts")
        assert "require('next/jest')" in shim.read_text()
        assert 'testEnvironment: "jest-environment-jsdom"' in shim.read_text()
        assert "setupFilesAfterEnv: [\"./jest.setup.ts\"]" in shim.read_text()
        assert ["npm", "exec", "--", "jest", "--version"] in calls
        shim_step = next(step for step in report.steps if step.name == "test_config_shim")
        assert shim_step.stdout == ".atm-jest.config.cjs"

    def test_generated_next_jest_config_preserves_static_test_environment(self, tmp_path, monkeypatch):
        (tmp_path / "package-lock.json").write_text("")
        (tmp_path / "jest.config.ts").write_text(
            "import nextJest from 'next/jest';\n"
            "const customJestConfig = { testEnvironment: 'node' };\n"
            "export default nextJest({ dir: './' })(customJestConfig);\n"
        )
        (tmp_path / "package.json").write_text(json.dumps({
            "packageManager": "npm@10.0.0",
            "scripts": {"test": "jest"},
            "devDependencies": {
                "jest": "^30.0.0",
                "next": "^16.0.0",
            },
        }))

        def fake_run(root, command, timeout, config=None):
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout="true\n")
            if command[:2] == ["git", "status"]:
                return _completed(command)
            return _completed(command, stdout="ok")

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)

        prepare_environment(str(tmp_path), {
            "environment": {"install": "never", "run_typecheck": False},
            "timeouts": {"tool_execution": 60},
        })

        assert 'testEnvironment: "node"' in (tmp_path / ".atm-jest.config.cjs").read_text()

    def test_generated_next_jest_config_uses_node_environment_for_route_handlers(self, tmp_path, monkeypatch):
        route_dir = tmp_path / "app" / "api" / "ping"
        route_dir.mkdir(parents=True)
        (route_dir / "route.ts").write_text("export function GET() {}\n")
        (tmp_path / "package-lock.json").write_text("")
        (tmp_path / "jest.config.ts").write_text(
            "import nextJest from 'next/jest';\n"
            "export default nextJest({ dir: './' })({});\n"
        )
        (tmp_path / "package.json").write_text(json.dumps({
            "packageManager": "npm@10.0.0",
            "scripts": {"test": "jest"},
            "devDependencies": {
                "jest": "^30.0.0",
                "next": "^16.0.0",
            },
        }))

        def fake_run(root, command, timeout, config=None):
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout="true\n")
            if command[:2] == ["git", "status"]:
                return _completed(command)
            return _completed(command, stdout="ok")

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)

        prepare_environment(str(tmp_path), {
            "environment": {"install": "never", "run_typecheck": False},
            "timeouts": {"tool_execution": 60},
        })

        assert 'testEnvironment: "node"' in (tmp_path / ".atm-jest.config.cjs").read_text()

    def test_generated_config_write_failure_stops_environment_prep(self, tmp_path, monkeypatch):
        _write_js_project(tmp_path)

        def fake_run(root, command, timeout, config=None):
            if command[:2] == ["git", "rev-parse"]:
                return _completed(command, stdout="true\n")
            if command[:2] == ["git", "status"]:
                return _completed(command)
            return _completed(command, stdout="ok")

        def fail_config_write(report):
            raise OSError("disk full")

        monkeypatch.setattr("agentic_tdd_runner.environment._run", fake_run)
        monkeypatch.setattr("agentic_tdd_runner.environment.ensure_generated_test_config", fail_config_write)

        with pytest.raises(EnvironmentPrepError, match="could not generate test config shim: disk full") as exc:
            prepare_environment(str(tmp_path), {
                "environment": {"install": "never", "run_typecheck": False},
                "timeouts": {"tool_execution": 60},
            })

        assert exc.value.report.steps[-1].name == "test_config_shim"

    def test_preflights_recommended_tools_with_command_env(self, tmp_path, monkeypatch):
        _write_js_project(tmp_path)
        calls = []
        seen_run_configs = []

        def fake_run(root, command, timeout, config=None):
            calls.append(command)
            seen_run_configs.append(config)
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

        run_config = {
            "environment": {"install": "never", "run_typecheck": False},
            "timeouts": {"tool_execution": 60},
            "tools": {"recommended": ["rg"], "path_dirs": ["/opt/homebrew/bin"]},
        }

        report = prepare_environment(str(tmp_path), run_config)

        assert report.ready is True
        assert report.recommended_tools == ["rg"]
        assert report.resolved_tools == {"rg": "/opt/homebrew/bin/rg"}
        assert report.missing_tools == []
        assert "/opt/homebrew/bin" in report.tool_path_dirs
        assert seen_run_configs
        assert all(config is run_config for config in seen_run_configs)
        tool_step = next(step for step in report.steps if step.name == "recommended_tools")
        assert tool_step.returncode == 0
        assert ["bun", "install", "--frozen-lockfile"] not in calls

    def test_missing_recommended_tool_fails_before_install(self, tmp_path, monkeypatch):
        _write_js_project(tmp_path)
        calls = []

        def fake_run(root, command, timeout, config=None):
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
            "tools": {"recommended": ["rg", "/opt/homebrew/bin/rg", "", 123]},
        }) == ["rg"]

    def test_javascript_project_skips_install_when_node_modules_exists(self, tmp_path, monkeypatch):
        _write_js_project(tmp_path)
        (tmp_path / "node_modules").mkdir()
        calls = []

        def fake_run(root, command, timeout, config=None):
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

    def test_dirty_worktree_fails_before_install(self, tmp_path, monkeypatch):
        _write_js_project(tmp_path)
        calls = []

        def fake_run(root, command, timeout, config=None):
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

        def fake_run(root, command, timeout, config=None):
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

        def fake_run(root, command, timeout, config=None):
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

        result = environment._run(
            tmp_path,
            ["rg", "--version"],
            timeout=3,
            config={"tools": {"path_dirs": ["/opt/homebrew/bin"]}},
        )

        assert result.returncode == 0
