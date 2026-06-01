"""Tests for focused test command resolution."""

import json
import shlex

from agentic_tdd_runner.runner_bootstrap import inspect_runner_bootstrap
from agentic_tdd_runner.runner_command import (
    effective_test_command_template,
    runner_version_command,
)


def _write_package(path, payload):
    path.mkdir(parents=True, exist_ok=True)
    (path / "package.json").write_text(json.dumps(payload))


def test_missing_runner_does_not_default_to_bun():
    assert effective_test_command_template(None, None) == ""
    assert runner_version_command(None) is None


def test_builds_pytest_template_from_python_runner():
    report = {"test_runner": "pytest"}

    assert effective_test_command_template(report, "bun test") == "python3 -m pytest"
    assert runner_version_command(report) == ["python3", "-m", "pytest", "--version"]


def test_builds_bun_test_template_from_bootstrap_report(tmp_path):
    (tmp_path / "bun.lockb").write_text("")
    _write_package(tmp_path, {
        "packageManager": "bun@1.2.0",
        "scripts": {"test": "bun test src/foo.test.ts"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert effective_test_command_template(report, "npm test") == "bun test"
    assert runner_version_command(report) == ["bun", "--version"]


def test_builds_npm_vitest_template_from_dependency_detected_report(tmp_path):
    _write_package(tmp_path, {
        "devDependencies": {"vitest": "^4.0.0"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert effective_test_command_template(report, "bun test") == "npm exec -- vitest run"
    assert runner_version_command(report) == ["npm", "exec", "--", "vitest", "--version"]


def test_falls_back_for_ambiguous_bootstrap_report(tmp_path):
    _write_package(tmp_path, {
        "devDependencies": {"jest": "^30.0.0", "vitest": "^4.0.0"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert effective_test_command_template(report, "bun test") == "bun test"
    assert runner_version_command(report) is None


def test_builds_node_test_template_from_bootstrap_report(tmp_path):
    _write_package(tmp_path, {
        "scripts": {"test": "node --test test/*.test.js"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert effective_test_command_template(report, "bun test") == "node --test"
    assert runner_version_command(report) == ["node", "--version"]


def test_uses_package_script_for_custom_bootstrap_report(tmp_path):
    _write_package(tmp_path, {
        "scripts": {"test": "turbo run test --filter web"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert effective_test_command_template(report, "bun test") == "npm test --"
    assert runner_version_command(report) is None


def test_derives_custom_command_template_from_script_globs(tmp_path):
    _write_package(tmp_path, {
        "scripts": {"test": "tsx --test src/**/*.test.ts"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert report.test_runner == "custom"
    assert effective_test_command_template(report, "bun test") == "tsx --test"


def test_builds_package_manager_specific_runner_templates(tmp_path):
    (tmp_path / "pnpm-lock.yaml").write_text("")
    _write_package(tmp_path, {
        "scripts": {"test": "vitest run"},
        "devDependencies": {"vitest": "^4.0.0"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert effective_test_command_template(report, "bun test") == "pnpm exec vitest run"
    assert runner_version_command(report) == ["pnpm", "exec", "vitest", "--version"]


def test_builds_runner_template_from_serialized_bootstrap_report():
    report = {
        "package_manager": "yarn",
        "test_runner": "jest",
        "test_config_path": ".atm-jest.config.cjs",
    }

    assert shlex.split(effective_test_command_template(report, "bun test")) == [
        "yarn",
        "jest",
        "--runInBand",
        "--watchman=false",
        "--coverage=false",
        "--config",
        ".atm-jest.config.cjs",
    ]
    assert runner_version_command(report) == ["yarn", "jest", "--version"]


def test_builds_jest_template_with_generated_next_shim(tmp_path):
    (tmp_path / "package-lock.json").write_text("")
    (tmp_path / "jest.config.ts").write_text(
        "import baseConfig from '@repo/jest-config';\n"
        "export default baseConfig;\n"
    )
    _write_package(tmp_path, {
        "scripts": {"test": "jest"},
        "devDependencies": {"jest": "^30.0.0", "next": "^16.0.0"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert report.test_config_path == str(tmp_path / ".atm-jest.config.cjs")
    assert report.test_config_source == "generated:next/jest"
    assert report.original_test_config_path == str(tmp_path / "jest.config.ts")
    assert shlex.split(effective_test_command_template(report, "bun test")) == [
        "npm",
        "exec",
        "--",
        "jest",
        "--runInBand",
        "--watchman=false",
        "--coverage=false",
        "--config",
        str(tmp_path / ".atm-jest.config.cjs"),
    ]
