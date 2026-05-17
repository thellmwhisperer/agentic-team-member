"""Repo-aware test runner bootstrap discovery."""

from __future__ import annotations

import json
import os
import shlex
from dataclasses import asdict, dataclass
from pathlib import Path


LOCKFILES = (
    ("pnpm-lock.yaml", "pnpm"),
    ("yarn.lock", "yarn"),
    ("bun.lockb", "bun"),
    ("bun.lock", "bun"),
    ("package-lock.json", "npm"),
    ("npm-shrinkwrap.json", "npm"),
)

PACKAGE_MANAGERS = {"bun", "npm", "pnpm", "yarn"}
RUNNER_BINS = {"jest", "vitest"}


@dataclass(frozen=True)
class RunnerBootstrapReport:
    """Deterministic repo facts needed before building test commands."""

    workdir: str
    package_dir: str | None = None
    package_json: str | None = None
    monorepo_root: str | None = None
    lockfile: str | None = None
    package_manager: str | None = None
    package_manager_source: str = ""
    test_runner: str | None = None
    test_runner_source: str = ""
    test_command: str | None = None

    def to_log_dict(self) -> dict:
        return asdict(self)


def inspect_runner_bootstrap(workdir: str | Path) -> RunnerBootstrapReport:
    """Inspect local repo metadata without mutating the target tree."""
    root = Path(workdir).expanduser().resolve()
    package_dir = _nearest_package_dir(root)
    package_json = package_dir / "package.json" if package_dir else None
    pkg = _read_package_json(package_json) if package_json else {}
    lockfile, lockfile_package_manager = _nearest_lockfile(root)
    package_manager, package_manager_source = _detect_package_manager(
        pkg,
        lockfile,
        lockfile_package_manager,
    )
    test_runner, test_runner_source, test_command = _detect_test_runner(pkg)

    return RunnerBootstrapReport(
        workdir=str(root),
        package_dir=str(package_dir) if package_dir else None,
        package_json=str(package_json) if package_json else None,
        monorepo_root=str(lockfile.parent if lockfile else package_dir) if (lockfile or package_dir) else None,
        lockfile=str(lockfile) if lockfile else None,
        package_manager=package_manager,
        package_manager_source=package_manager_source,
        test_runner=test_runner,
        test_runner_source=test_runner_source,
        test_command=test_command,
    )


def _nearest_package_dir(root: Path) -> Path | None:
    for directory in _walk_up(root):
        if (directory / "package.json").is_file():
            return directory
    return None


def _nearest_lockfile(root: Path) -> tuple[Path | None, str | None]:
    for directory in _walk_up(root):
        for filename, package_manager in LOCKFILES:
            path = directory / filename
            if path.is_file():
                return path, package_manager
    return None, None


def _walk_up(root: Path):
    current = root if root.is_dir() else root.parent
    while True:
        yield current
        if current.parent == current:
            return
        current = current.parent


def _read_package_json(path: Path) -> dict:
    try:
        data = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError):
        return {}
    return data if isinstance(data, dict) else {}


def _detect_package_manager(
    pkg: dict,
    lockfile: Path | None,
    lockfile_package_manager: str | None,
) -> tuple[str | None, str]:
    if lockfile and lockfile_package_manager:
        return lockfile_package_manager, f"lockfile:{lockfile.name}"

    package_manager = str(pkg.get("packageManager", ""))
    if package_manager:
        name = package_manager.split("@", 1)[0]
        if name in PACKAGE_MANAGERS:
            return name, "package.json:packageManager"

    if pkg:
        return "npm", "default"
    return None, ""


def _detect_test_runner(pkg: dict) -> tuple[str | None, str, str | None]:
    scripts = pkg.get("scripts", {})
    test_command = scripts.get("test") if isinstance(scripts, dict) else None
    if isinstance(test_command, str) and test_command.strip():
        runner = _classify_test_command(test_command)
        return runner or "custom", "package.json:scripts.test", test_command

    deps = _all_dependencies(pkg)
    if "vitest" in deps:
        return "vitest", "package.json:dependencies", None
    if "jest" in deps or "@jest/globals" in deps:
        return "jest", "package.json:dependencies", None
    if "bun-types" in deps or "@types/bun" in deps:
        return "bun:test", "package.json:dependencies", None
    return None, "", None


def _classify_test_command(command: str) -> str | None:
    try:
        tokens = shlex.split(command)
    except ValueError:
        return None
    tokens = _skip_command_wrappers(tokens)
    tokens = _split_nested_shell_payload(tokens)
    if not tokens:
        return None

    binary = os.path.basename(tokens[0])
    if binary == "bun" and len(tokens) > 1 and tokens[1] == "test":
        return "bun:test"
    if binary == "node" and "--test" in tokens[1:]:
        return "node:test"
    if binary in RUNNER_BINS:
        return binary
    if binary == "npx" and len(tokens) > 1:
        return _classify_npx_command(tokens[1:])
    if binary in PACKAGE_MANAGERS and len(tokens) > 1:
        return _classify_package_manager_command(binary, tokens[1:])
    return None


def _split_nested_shell_payload(tokens: list[str]) -> list[str]:
    if len(tokens) != 1 or " " not in tokens[0]:
        return tokens
    try:
        nested = shlex.split(tokens[0])
    except ValueError:
        return tokens
    return nested or tokens


def _skip_command_wrappers(tokens: list[str]) -> list[str]:
    remaining = list(tokens)
    while remaining:
        binary = os.path.basename(remaining[0])
        if _is_env_assignment(binary):
            remaining = remaining[1:]
            continue
        if binary == "env":
            remaining = remaining[1:]
            continue
        if binary in {"cross-env", "cross-env-shell"}:
            remaining = remaining[1:]
            while remaining and _is_env_assignment(remaining[0]):
                remaining = remaining[1:]
            continue
        break
    return remaining


def _classify_npx_command(args: list[str]) -> str | None:
    for arg in args:
        if arg.startswith("-"):
            continue
        return _classify_runner_binary(arg)
    return None


def _classify_package_manager_command(binary: str, args: list[str]) -> str | None:
    if binary == "bun" and args and args[0] == "test":
        return "bun:test"
    if args and args[0] == "exec" and len(args) > 1:
        return _classify_runner_binary(args[1])
    if args and args[0] == "x" and len(args) > 1:
        return _classify_runner_binary(args[1])
    if binary in {"pnpm", "yarn"} and args:
        return _classify_runner_binary(args[0])
    return None


def _classify_runner_binary(binary: str) -> str | None:
    name = os.path.basename(binary)
    if name in RUNNER_BINS:
        return name
    return None


def _is_env_assignment(token: str) -> bool:
    if "=" not in token or token.startswith("-"):
        return False
    name, _value = token.split("=", 1)
    return bool(name) and all(ch.isalnum() or ch == "_" for ch in name)


def _all_dependencies(pkg: dict) -> set[str]:
    deps: set[str] = set()
    for key in ("dependencies", "devDependencies", "peerDependencies", "optionalDependencies"):
        value = pkg.get(key, {})
        if isinstance(value, dict):
            deps.update(str(name) for name in value)
    return deps
