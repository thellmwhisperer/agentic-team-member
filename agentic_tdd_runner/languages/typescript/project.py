from __future__ import annotations

import json
import shlex
from pathlib import Path
from typing import Any

from agentic_tdd_runner.runner_bootstrap import (
    ensure_generated_test_config,
    inspect_runner_bootstrap,
)


PROJECT_COMMANDS = {"bun", "node", "npm", "npx", "pnpm", "yarn", "deno"}


def detect_project_type(root: Path) -> str | None:
    return "javascript" if (root / "package.json").is_file() else None


def inspect_project_runner_bootstrap(root: Path) -> Any | None:
    return inspect_runner_bootstrap(root)


def project_manifest(root: Path) -> dict:
    try:
        return json.loads((root / "package.json").read_text())
    except (OSError, json.JSONDecodeError):
        return {}


def project_package_manager(
    root: Path,
    manifest: dict,
    bootstrap: Any | None,
) -> str | None:
    return _report_value(bootstrap, "package_manager") or detect_package_manager(root, manifest)


def project_should_install(root: Path, install_mode: str) -> bool:
    if install_mode == "always":
        return True
    return not (root / "node_modules").is_dir()


def project_install_command(
    root: Path,
    package_manager: str | None,
    bootstrap: Any | None,
) -> list[str] | None:
    if not package_manager:
        return None
    lockfile = _report_value(bootstrap, "lockfile")
    return install_command(root, package_manager, lockfile=lockfile)


def project_preflight_commands(
    root: Path,
    manifest: dict,
    env_cfg: dict,
    package_manager: str | None,
    bootstrap: Any | None,
) -> list[list[str]]:
    commands = javascript_preflight_commands(
        root,
        manifest,
        env_cfg,
        package_manager=package_manager,
    )
    from agentic_tdd_runner.runner_command import runner_version_command

    version_command = runner_version_command(bootstrap)
    if version_command:
        commands.insert(0, version_command)
    return commands


def ensure_project_test_config(root: Path, bootstrap: Any | None) -> Path | None:
    if bootstrap is None:
        return None
    return ensure_generated_test_config(bootstrap)


def shell_project_commands() -> set[str]:
    return set(PROJECT_COMMANDS)


def detect_package_manager(root: Path, manifest: dict | None = None) -> str:
    if manifest:
        pm_field = str(manifest.get("packageManager", ""))
        if pm_field:
            name = pm_field.split("@", 1)[0]
            if name in {"pnpm", "yarn", "bun", "npm"}:
                return name

    lockfiles = {
        "pnpm-lock.yaml": "pnpm",
        "yarn.lock": "yarn",
        "bun.lock": "bun",
        "bun.lockb": "bun",
        "package-lock.json": "npm",
        "npm-shrinkwrap.json": "npm",
    }
    for filename, package_manager in lockfiles.items():
        if (root / filename).is_file():
            return package_manager
    return "npm"


def install_command(
    root: Path,
    package_manager: str,
    *,
    lockfile: str | None = None,
) -> list[str]:
    if package_manager == "bun":
        command = ["bun", "install"]
        if _has_lockfile(root, lockfile, "bun.lock", "bun.lockb"):
            command.append("--frozen-lockfile")
        return command
    if package_manager == "pnpm":
        command = ["pnpm", "install"]
        if _has_lockfile(root, lockfile, "pnpm-lock.yaml"):
            command.append("--frozen-lockfile")
        return command
    if package_manager == "yarn":
        command = ["yarn", "install"]
        if _has_lockfile(root, lockfile, "yarn.lock"):
            command.append("--frozen-lockfile")
        return command
    if _has_lockfile(root, lockfile, "package-lock.json", "npm-shrinkwrap.json"):
        return ["npm", "ci"]
    return ["npm", "install"]


def javascript_preflight_commands(
    root: Path,
    manifest: dict,
    env_cfg: dict,
    *,
    package_manager: str | None = None,
) -> list[list[str]]:
    commands: list[list[str]] = []
    package_manager = package_manager or detect_package_manager(root, manifest)
    scripts = manifest.get("scripts", {}) if isinstance(manifest.get("scripts", {}), dict) else {}
    deps = all_javascript_dependencies(manifest)

    if bool(env_cfg.get("run_typecheck", True)):
        if "typecheck" in scripts:
            commands.append([package_manager, "run", "typecheck"])
        elif "typescript" in deps:
            commands.append(["npx", "tsc", "--noEmit"])

    test_command = env_cfg.get("preflight_test_command")
    if test_command:
        if isinstance(test_command, str):
            commands.append(shlex.split(test_command))
        elif isinstance(test_command, list):
            commands.append([str(part) for part in test_command])

    return commands


def all_javascript_dependencies(manifest: dict) -> set[str]:
    deps: set[str] = set()
    for key in ("dependencies", "devDependencies", "peerDependencies", "optionalDependencies"):
        value = manifest.get(key, {})
        if isinstance(value, dict):
            deps.update(str(name) for name in value)
    return deps


def _has_lockfile(root: Path, lockfile: str | None, *filenames: str) -> bool:
    if any((root / filename).is_file() for filename in filenames):
        return True
    if not lockfile:
        return False
    return Path(lockfile).name in filenames


def _report_value(report: Any | None, key: str) -> str | None:
    if report is None:
        return None
    value = report.get(key) if isinstance(report, dict) else getattr(report, key, None)
    return value if isinstance(value, str) and value else None
