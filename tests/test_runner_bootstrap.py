"""Tests for repo-aware test runner bootstrap discovery."""

import json

from agentic_tdd_runner.runner_bootstrap import inspect_runner_bootstrap


def _write_package(path, payload):
    path.mkdir(parents=True, exist_ok=True)
    (path / "package.json").write_text(json.dumps(payload))


def test_detects_pnpm_monorepo_and_leaf_jest_runner(tmp_path):
    (tmp_path / "pnpm-lock.yaml").write_text("")
    package_dir = tmp_path / "apps" / "web"
    _write_package(package_dir, {
        "scripts": {"test": "jest --runInBand"},
        "devDependencies": {"jest": "^30.0.0"},
    })

    report = inspect_runner_bootstrap(package_dir / "src")

    assert report.package_dir == str(package_dir)
    assert report.package_json == str(package_dir / "package.json")
    assert report.monorepo_root == str(tmp_path)
    assert report.lockfile == str(tmp_path / "pnpm-lock.yaml")
    assert report.package_manager == "pnpm"
    assert report.package_manager_source == "lockfile:pnpm-lock.yaml"
    assert report.test_runner == "jest"
    assert report.test_runner_source == "package.json:scripts.test"
    assert report.test_command == "jest --runInBand"


def test_detects_bun_test_from_script_and_lockfile(tmp_path):
    (tmp_path / "bun.lockb").write_text("")
    _write_package(tmp_path, {
        "packageManager": "bun@1.2.0",
        "scripts": {"test": "bun test src/foo.test.ts"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert report.package_manager == "bun"
    assert report.package_manager_source == "lockfile:bun.lockb"
    assert report.test_runner == "bun:test"
    assert report.test_command == "bun test src/foo.test.ts"


def test_detects_vitest_from_dependencies_without_test_script(tmp_path):
    _write_package(tmp_path, {
        "devDependencies": {"vitest": "^4.0.0"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert report.package_manager == "npm"
    assert report.package_manager_source == "default"
    assert report.test_runner == "vitest"
    assert report.test_runner_source == "package.json:dependencies"
    assert report.test_command is None


def test_ignores_peer_runner_dependencies_without_test_script(tmp_path):
    _write_package(tmp_path, {
        "peerDependencies": {"vitest": "^4.0.0"},
        "optionalDependencies": {"jest": "^30.0.0"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert report.test_runner is None
    assert report.test_runner_source == ""


def test_does_not_guess_when_dependency_fallback_is_ambiguous(tmp_path):
    _write_package(tmp_path, {
        "devDependencies": {"jest": "^30.0.0", "vitest": "^4.0.0"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert report.test_runner is None
    assert report.test_runner_source == "ambiguous:vitest,jest"


def test_detects_bun_test_from_package_manager_without_bun_types(tmp_path):
    (tmp_path / "bun.lock").write_text("")
    _write_package(tmp_path, {
        "packageManager": "bun@1.2.0",
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert report.test_runner == "bun:test"
    assert report.test_runner_source == "package_manager:bun"


def test_detects_node_test_from_script(tmp_path):
    _write_package(tmp_path, {
        "scripts": {"test": "node --test test/*.test.js"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert report.test_runner == "node:test"
    assert report.test_command == "node --test test/*.test.js"


def test_keeps_custom_test_script_when_runner_is_unknown(tmp_path):
    _write_package(tmp_path, {
        "scripts": {"test": "turbo run test --filter web"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert report.test_runner == "custom"
    assert report.test_runner_source == "package.json:scripts.test"
    assert report.test_command == "turbo run test --filter web"


def test_detects_wrapped_runner_commands(tmp_path):
    _write_package(tmp_path, {
        "scripts": {"test": "cross-env CI=1 pnpm exec vitest run"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert report.test_runner == "vitest"
    assert report.test_command == "cross-env CI=1 pnpm exec vitest run"


def test_detects_cross_env_shell_quoted_runner_payload(tmp_path):
    _write_package(tmp_path, {
        "scripts": {"test": 'cross-env-shell NODE_ENV=test "vitest run"'},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert report.test_runner == "vitest"
    assert report.test_command == 'cross-env-shell NODE_ENV=test "vitest run"'


def test_detects_npx_runner_with_flags(tmp_path):
    _write_package(tmp_path, {
        "scripts": {"test": "npx --yes jest --runInBand"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert report.test_runner == "jest"
    assert report.test_command == "npx --yes jest --runInBand"


def test_jest_config_detection_ignores_comments_and_node_builtins(tmp_path):
    config = tmp_path / "jest.config.js"
    config.write_text(
        "const http = require('http');\n"
        "// import workspacePreset from '@repo/jest-preset';\n"
        "/*\n"
        "import hiddenPreset from '@repo/hidden';\n"
        "*/\n"
        "module.exports = { testEnvironment: 'node' };\n"
    )
    _write_package(tmp_path, {
        "scripts": {"test": "jest"},
        "dependencies": {"next": "^16.0.0"},
        "devDependencies": {"jest": "^30.0.0"},
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert report.test_config_path == str(config)
    assert report.test_config_source == "file:jest.config.js"
    assert report.original_test_config_path is None


def test_detects_runner_behind_npm_run_script_reference(tmp_path):
    _write_package(tmp_path, {
        "scripts": {
            "test": "npm run test:unit",
            "test:unit": "vitest run",
        },
    })

    report = inspect_runner_bootstrap(tmp_path)

    assert report.test_runner == "vitest"
    assert report.test_command == "npm run test:unit"


def test_returns_empty_report_for_non_javascript_project(tmp_path):
    (tmp_path / "pyproject.toml").write_text("[project]\nname = 'demo'\n")

    report = inspect_runner_bootstrap(tmp_path)

    assert report.package_dir is None
    assert report.package_manager is None
    assert report.test_runner is None
    assert report.to_log_dict()["workdir"] == str(tmp_path)
