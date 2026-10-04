"""What the worker knows per language: file extensions and the quality tools a repo declares."""
from __future__ import annotations

import json
import re
import tomllib
from pathlib import Path, PurePosixPath

EXTENSIONS = {
    "python": [".py"],
    "typescript": [".ts", ".tsx", ".js", ".jsx"],
}
JS_MOCK_SETUP_RE = re.compile(r"(?:\b(?:vi|jest)\.(?:fn|mock)|\bmock\.module)\s*\(")
PACKAGE_MANAGERS = {"bun", "npm", "pnpm", "yarn"}


def language_for(path: str) -> str | None:
    suffix = PurePosixPath(path).suffix
    return next((name for name, extensions in EXTENSIONS.items() if suffix in extensions), None)


def detect_quality_tools(language: str | None, workdir: str) -> list[dict]:
    if language == "python":
        return _python_quality_tools(workdir)
    if language == "typescript":
        return _typescript_quality_tools(workdir)
    return []


def _python_quality_tools(workdir: str) -> list[dict]:
    pyproject_path = Path(workdir) / "pyproject.toml"
    has_ruff = False
    if pyproject_path.is_file():
        try:
            with pyproject_path.open("rb") as f:
                pyproject = tomllib.load(f)
            has_ruff = "ruff" in pyproject.get("tool", {})
        except (OSError, ValueError):
            pass

    if not has_ruff:
        return []
    return [
        {
            "name": "lint",
            "command": "python3 -m ruff check {changed_files}",
            "fix": "python3 -m ruff check {changed_files} --fix",
        },
        {
            "name": "format",
            "command": "python3 -m ruff format --check {changed_files}",
            "fix": "python3 -m ruff format {changed_files}",
        },
    ]


def _typescript_quality_tools(workdir: str) -> list[dict]:
    pkg_path = Path(workdir) / "package.json"
    if not pkg_path.is_file():
        return []
    try:
        pkg = json.loads(pkg_path.read_text())
    except (OSError, ValueError):
        return []

    scripts = pkg.get("scripts", {})
    dev_deps = pkg.get("devDependencies", {})
    deps = pkg.get("dependencies", {})
    all_deps = {**deps, **dev_deps}
    package_manager = detect_package_manager(workdir, pkg)
    exec_prefix = package_exec_prefix(package_manager)
    checks = []

    if "typecheck" in scripts:
        checks.append({"name": "typecheck", "command": f"{package_manager} run typecheck"})
    elif "typescript" in all_deps:
        checks.append({"name": "typecheck", "command": f"{exec_prefix} tsc --noEmit"})

    if any(k.startswith("@biomejs/biome") for k in all_deps):
        checks.append({
            "name": "lint",
            "command": f"{exec_prefix} biome check {{changed_files}}",
            "fix": f"{exec_prefix} biome check {{changed_files}} --fix",
        })
    elif "eslint" in all_deps:
        checks.append({
            "name": "lint",
            "command": f"{exec_prefix} eslint {{changed_files}}",
            "fix": f"{exec_prefix} eslint {{changed_files}} --fix",
        })

    has_biome = any(k.startswith("@biomejs/biome") for k in all_deps)
    if not has_biome and "prettier" in all_deps:
        checks.append({
            "name": "format",
            "command": f"{exec_prefix} prettier --check {{changed_files}}",
            "fix": f"{exec_prefix} prettier --write {{changed_files}}",
        })
    return checks


def detect_package_manager(workdir: str, pkg: dict | None = None) -> str:
    if pkg:
        pm_field = pkg.get("packageManager", "")
        if pm_field:
            name = pm_field.split("@")[0]
            if name in PACKAGE_MANAGERS:
                return name
    lockfiles = {
        "pnpm-lock.yaml": "pnpm",
        "yarn.lock": "yarn",
        "bun.lock": "bun",
    }
    for filename, package_manager in lockfiles.items():
        if (Path(workdir) / filename).is_file():
            return package_manager
    return "npm"


def package_exec_prefix(package_manager: str) -> str:
    return {
        "npm": "npx",
        "pnpm": "pnpm exec",
        "yarn": "yarn",
        "bun": "bunx",
    }.get(package_manager, "npx")
