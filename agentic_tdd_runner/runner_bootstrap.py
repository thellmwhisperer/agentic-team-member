"""Repo-aware test runner bootstrap discovery."""

from __future__ import annotations

import json
import os
import re
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
JEST_CONFIG_FILENAMES = (
    "jest.config.ts",
    "jest.config.mts",
    "jest.config.cts",
    "jest.config.js",
    "jest.config.mjs",
    "jest.config.cjs",
    "jest.config.json",
)
JEST_SHIM_FILENAME = ".atm-jest.config.cjs"
NODE_BUILTIN_MODULES = {
    "assert",
    "async_hooks",
    "buffer",
    "child_process",
    "cluster",
    "console",
    "crypto",
    "dgram",
    "dns",
    "events",
    "fs",
    "http",
    "https",
    "inspector",
    "module",
    "net",
    "os",
    "perf_hooks",
    "path",
    "process",
    "querystring",
    "readline",
    "repl",
    "stream",
    "string_decoder",
    "timers",
    "tls",
    "tty",
    "url",
    "util",
    "vm",
    "worker_threads",
    "zlib",
}


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
    test_config_path: str | None = None
    test_config_source: str = ""
    original_test_config_path: str | None = None

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
    test_runner, test_runner_source, test_command = _detect_test_runner(pkg, package_manager)
    test_config_path, test_config_source, original_test_config_path = _detect_test_config(
        package_dir,
        pkg,
        test_runner,
    )

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
        test_config_path=str(test_config_path) if test_config_path else None,
        test_config_source=test_config_source,
        original_test_config_path=str(original_test_config_path) if original_test_config_path else None,
    )


def ensure_generated_test_config(report: RunnerBootstrapReport) -> Path | None:
    """Write a generated runner config shim when bootstrap selected one."""
    if report.test_runner != "jest" or report.test_config_source != "generated:next/jest":
        return None
    if not report.package_dir or not report.test_config_path:
        return None

    package_dir = Path(report.package_dir)
    shim_path = Path(report.test_config_path)
    if not shim_path.is_absolute():
        shim_path = package_dir / shim_path
    original_path = Path(report.original_test_config_path) if report.original_test_config_path else None
    content = _render_next_jest_shim(package_dir, original_path)
    existing = shim_path.read_text() if shim_path.is_file() else None
    if existing != content:
        shim_path.write_text(content)
    return shim_path


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


def _detect_test_runner(pkg: dict, package_manager: str | None) -> tuple[str | None, str, str | None]:
    scripts = pkg.get("scripts", {})
    test_command = scripts.get("test") if isinstance(scripts, dict) else None
    if isinstance(test_command, str) and test_command.strip():
        runner = _classify_test_command(test_command, scripts=scripts)
        return runner or "custom", "package.json:scripts.test", test_command

    deps = _runner_dependencies(pkg)
    detected = []
    if "vitest" in deps:
        detected.append("vitest")
    if "jest" in deps or "@jest/globals" in deps:
        detected.append("jest")
    if len(detected) == 1:
        return detected[0], "package.json:dependencies", None
    if len(detected) > 1:
        return None, f"ambiguous:{','.join(detected)}", None
    if "bun-types" in deps or "@types/bun" in deps:
        return "bun:test", "package.json:dependencies", None
    if package_manager == "bun":
        return "bun:test", "package_manager:bun", None
    return None, "", None


def _classify_test_command(
    command: str,
    *,
    scripts: dict | None = None,
    seen_scripts: set[str] | None = None,
) -> str | None:
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
        return _classify_package_manager_command(
            binary,
            tokens[1:],
            scripts=scripts,
            seen_scripts=seen_scripts,
        )
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


def _classify_package_manager_command(
    binary: str,
    args: list[str],
    *,
    scripts: dict | None = None,
    seen_scripts: set[str] | None = None,
) -> str | None:
    if binary == "bun" and args and args[0] == "test":
        return "bun:test"
    if args and args[0] == "run":
        return _classify_script_reference(_run_script_name(args[1:]), scripts, seen_scripts)
    if binary == "npm" and args and args[0] == "test":
        return _classify_script_reference("test", scripts, seen_scripts)
    if args and args[0] == "exec" and len(args) > 1:
        return _classify_runner_binary(args[1])
    if args and args[0] == "x" and len(args) > 1:
        return _classify_runner_binary(args[1])
    if binary in {"pnpm", "yarn"} and args:
        return _classify_runner_binary(args[0]) or _classify_script_reference(args[0], scripts, seen_scripts)
    return None


def _run_script_name(args: list[str]) -> str | None:
    for arg in args:
        if not arg.startswith("-"):
            return arg
    return None


def _classify_script_reference(
    script_name: str | None,
    scripts: dict | None,
    seen_scripts: set[str] | None,
) -> str | None:
    if not script_name or not isinstance(scripts, dict):
        return None
    if seen_scripts and script_name in seen_scripts:
        return None
    command = scripts.get(script_name)
    if not isinstance(command, str) or not command.strip():
        return None
    seen = set(seen_scripts or set())
    seen.add(script_name)
    return _classify_test_command(command, scripts=scripts, seen_scripts=seen)


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


def _detect_test_config(
    package_dir: Path | None,
    pkg: dict,
    test_runner: str | None,
) -> tuple[Path | None, str, Path | None]:
    if test_runner != "jest" or not package_dir:
        return None, "", None

    config_path = _find_jest_config(package_dir, pkg)
    if not config_path:
        return None, "", None

    if _needs_next_jest_shim(config_path, pkg):
        return package_dir / JEST_SHIM_FILENAME, "generated:next/jest", config_path
    return config_path, f"file:{config_path.name}", None


def _find_jest_config(package_dir: Path, pkg: dict) -> Path | None:
    jest_cfg = pkg.get("jest")
    if isinstance(jest_cfg, str) and jest_cfg.strip():
        path = package_dir / jest_cfg.strip()
        if path.is_file():
            return path

    for filename in JEST_CONFIG_FILENAMES:
        path = package_dir / filename
        if path.is_file():
            return path
    return None


def _needs_next_jest_shim(config_path: Path, pkg: dict) -> bool:
    imports = _config_imports(config_path)
    if not _package_depends_on(pkg, "next") and "next/jest" not in imports:
        return False
    if any(_is_unresolved_config_import(spec, pkg) for spec in imports):
        return True
    return config_path.suffix in {".ts", ".mts", ".cts"} and "next/jest" in imports


def _config_imports(config_path: Path) -> set[str]:
    try:
        text = _strip_javascript_comments(config_path.read_text())
    except OSError:
        return set()
    imports = set()
    patterns = (
        r"\bimport\s+(?:[^'\"]+\s+from\s+)?['\"]([^'\"]+)['\"]",
        r"\bexport\s+[^'\"]+\s+from\s+['\"]([^'\"]+)['\"]",
        r"\brequire\(\s*['\"]([^'\"]+)['\"]\s*\)",
    )
    for pattern in patterns:
        imports.update(re.findall(pattern, text))
    return imports


def _strip_javascript_comments(text: str) -> str:
    text = re.sub(r"/\*.*?\*/", "", text, flags=re.DOTALL)
    return "\n".join(line for line in text.splitlines() if not line.lstrip().startswith("//"))


def _is_unresolved_config_import(specifier: str, pkg: dict) -> bool:
    if specifier.startswith(".") or specifier.startswith("/"):
        return False
    if specifier.startswith("node:"):
        return False
    package_name = _package_name_from_specifier(specifier)
    if package_name in NODE_BUILTIN_MODULES or package_name == "next":
        return False
    version = _dependency_version(pkg, package_name)
    return version is None or version.startswith("workspace:")


def _package_name_from_specifier(specifier: str) -> str:
    if specifier.startswith("@"):
        parts = specifier.split("/", 2)
        return "/".join(parts[:2])
    return specifier.split("/", 1)[0]


def _package_depends_on(pkg: dict, package_name: str) -> bool:
    return _dependency_version(pkg, package_name) is not None


def _dependency_version(pkg: dict, package_name: str) -> str | None:
    for key in ("dependencies", "devDependencies", "peerDependencies", "optionalDependencies"):
        value = pkg.get(key, {})
        if isinstance(value, dict) and package_name in value:
            version = value[package_name]
            return str(version) if isinstance(version, str) else ""
    return None


def _render_next_jest_shim(package_dir: Path, original_config_path: Path | None) -> str:
    test_environment = _detect_jest_test_environment(original_config_path) or _infer_next_jest_test_environment(
        package_dir,
    )
    setup_files = [
        f"./{name}"
        for name in ("jest.setup.js", "jest.setup.ts", "setupTests.js", "setupTests.ts")
        if (package_dir / name).is_file()
    ]
    setup_line = ""
    if setup_files:
        setup_values = ", ".join(json.dumps(path) for path in setup_files)
        setup_line = f"  setupFilesAfterEnv: [{setup_values}],\n"
    return (
        "const nextJest = require('next/jest');\n"
        "\n"
        "const createJestConfig = nextJest({ dir: './' });\n"
        "\n"
        "const customJestConfig = {\n"
        f"  testEnvironment: {json.dumps(test_environment)},\n"
        f"{setup_line}"
        "};\n"
        "\n"
        "module.exports = createJestConfig(customJestConfig);\n"
    )


def _detect_jest_test_environment(config_path: Path | None) -> str | None:
    if not config_path:
        return None
    try:
        text = _strip_javascript_comments(config_path.read_text())
    except OSError:
        return None
    match = re.search(r"\btestEnvironment\s*:\s*['\"]([^'\"]+)['\"]", text)
    if match:
        return match.group(1)
    match = re.search(r"['\"]testEnvironment['\"]\s*:\s*['\"]([^'\"]+)['\"]", text)
    if match:
        return match.group(1)
    return None


def _infer_next_jest_test_environment(package_dir: Path) -> str:
    server_globs = (
        "pages/api/**/*.[jt]s",
        "pages/api/**/*.[jt]sx",
        "app/**/route.[jt]s",
        "app/**/route.[jt]sx",
        "app/**/actions.[jt]s",
        "app/**/actions.[jt]sx",
    )
    for pattern in server_globs:
        if any(package_dir.glob(pattern)):
            return "node"
    return "jest-environment-jsdom"


def _runner_dependencies(pkg: dict) -> set[str]:
    deps: set[str] = set()
    for key in ("dependencies", "devDependencies"):
        value = pkg.get(key, {})
        if isinstance(value, dict):
            deps.update(str(name) for name in value)
    return deps
