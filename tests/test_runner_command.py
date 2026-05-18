"""Tests for focused test command resolution."""

import json

from agentic_tdd_runner.runner_bootstrap import inspect_runner_bootstrap
from agentic_tdd_runner.runner_command import (
    effective_test_command_template,
    runner_version_command,
)


def _write_package(path, payload):
    path.mkdir(parents=True, exist_ok=True)
    (path / "package.json").write_text(json.dumps(payload))


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


def test_falls_back_for_custom_bootstrap_report(tmp_path):
    _write_package(tmp_path, {
        "scripts": {"test": "turbo run test --filter web"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert effective_test_command_template(report, "bun test") == "bun test"
    assert runner_version_command(report) is None


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
    }

    assert effective_test_command_template(report, "bun test") == "yarn jest"
    assert runner_version_command(report) == ["yarn", "jest", "--version"]
