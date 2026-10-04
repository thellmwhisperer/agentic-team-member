"""Deterministic target-repository environment preparation."""

from __future__ import annotations

import json
import os
import shutil
import shlex
import subprocess
from dataclasses import asdict, dataclass, field
from pathlib import Path

from agentic_tdd_runner.runner_bootstrap import (
    RunnerBootstrapReport,
    ensure_generated_test_config,
    inspect_runner_bootstrap,
)
from agentic_tdd_runner.runner_command import runner_version_command
from agentic_tdd_runner.shell import build_command_env


@dataclass
class PrepStep:
    name: str
    command: list[str] = field(default_factory=list)
    returncode: int | None = None
    stdout: str = ""
    stderr: str = ""
    skipped: bool = False
    reason: str = ""

    def to_log_dict(self) -> dict:
        return asdict(self)


@dataclass
class EnvironmentReport:
    workdir: str
    project_type: str = "unknown"
    package_manager: str | None = None
    runner_bootstrap: RunnerBootstrapReport | None = None
    install_command: list[str] | None = None
    preflight_commands: list[list[str]] = field(default_factory=list)
    tool_path_dirs: list[str] = field(default_factory=list)
    recommended_tools: list[str] = field(default_factory=list)
    resolved_tools: dict[str, str] = field(default_factory=dict)
    missing_tools: list[str] = field(default_factory=list)
    steps: list[PrepStep] = field(default_factory=list)
    ready: bool = True
    reason: str = ""

    def to_log_dict(self) -> dict:
        data = asdict(self)
        data.pop("runner_bootstrap", None)
        data["steps"] = [step.to_log_dict() for step in self.steps]
        return data


@dataclass
class WorktreeReport:
    repo: str
    workdir: str
    base_ref: str
    command: list[str]

    def to_log_dict(self) -> dict:
        return asdict(self)


class EnvironmentPrepError(RuntimeError):
    """Raised when deterministic setup cannot make the repo ready."""

    def __init__(self, message: str, report: EnvironmentReport):
        super().__init__(message)
        self.report = report


class WorktreePrepError(RuntimeError):
    """Raised when an isolated run worktree cannot be created."""


def prepare_run_worktree(
    repo: str,
    *,
    workdir: str | None = None,
    base_ref: str | None = None,
    run_root: str | None = None,
) -> WorktreeReport:
    """Create an isolated detached worktree for a run."""
    repo_root = _resolve_git_repo(repo)
    ref = base_ref or "main"
    destination = Path(workdir).resolve() if workdir else _default_run_worktree_path(repo_root, run_root)

    if destination.exists():
        if not destination.is_dir():
            raise WorktreePrepError(f"worktree destination is not a directory: {destination}")
        if any(destination.iterdir()):
            raise WorktreePrepError(f"worktree destination is not empty: {destination}")

    destination.parent.mkdir(parents=True, exist_ok=True)

    command = ["git", "worktree", "add", "--detach", str(destination), ref]
    try:
        result = _run(repo_root, command, timeout=120)
    except FileNotFoundError as exc:
        raise WorktreePrepError("git is unavailable") from exc
    except subprocess.TimeoutExpired as exc:
        raise WorktreePrepError(f"worktree creation timed out: {destination}") from exc
    if result.returncode != 0:
        detail = (result.stderr or result.stdout or "").strip()
        raise WorktreePrepError(f"worktree creation failed: {detail}")

    return WorktreeReport(
        repo=str(repo_root),
        workdir=str(destination),
        base_ref=ref,
        command=command,
    )


def prepare_environment(workdir: str, config: dict) -> EnvironmentReport:
    """Prepare the target repository before any model call is made."""
    root = Path(workdir).resolve()
    report = EnvironmentReport(workdir=str(root))
    env_cfg = config.get("environment", {}) or {}
    timeout = int(env_cfg.get("timeout", config.get("timeouts", {}).get("tool_execution", 60)))

    if not root.exists():
        _fail(report, f"workdir does not exist: {root}")
    if not root.is_dir():
        _fail(report, f"workdir is not a directory: {root}")

    report.runner_bootstrap = inspect_runner_bootstrap(root)
    _preflight_recommended_tools(report, config)
    _require_git_worktree(root, report, timeout=timeout, config=config)

    if bool(env_cfg.get("require_clean", True)):
        _require_clean_worktree(root, report, timeout=timeout, config=config)

    project_type = detect_project_type(root)
    report.project_type = project_type

    if project_type == "javascript":
        pkg = _read_package_json(root)
        package_manager = report.runner_bootstrap.package_manager or detect_package_manager(root, pkg)
        report.package_manager = package_manager

        install_mode = str(env_cfg.get("install", "auto"))
        if install_mode not in {"auto", "always", "never"}:
            _fail(report, f"invalid environment.install value: {install_mode}")

        if install_mode != "never" and _should_install_javascript(root, install_mode):
            install_cmd = install_command_for_javascript(
                root,
                package_manager,
                lockfile=report.runner_bootstrap.lockfile if report.runner_bootstrap else None,
            )
            report.install_command = install_cmd
            _run_step(root, report, "install_dependencies", install_cmd, timeout=timeout, config=config)
        else:
            report.steps.append(PrepStep(
                name="install_dependencies",
                skipped=True,
                reason="dependencies already present" if install_mode != "never" else "disabled",
            ))

        _ensure_generated_test_config(root, report)

        preflight_commands = javascript_preflight_commands(root, pkg, env_cfg, package_manager=package_manager)
        version_command = runner_version_command(report.runner_bootstrap)
        if version_command:
            preflight_commands.insert(0, version_command)
        report.preflight_commands = preflight_commands
        for index, command in enumerate(preflight_commands, start=1):
            _run_step(root, report, f"preflight_{index}", command, timeout=timeout, config=config)

    elif project_type == "python":
        report.steps.append(PrepStep(
            name="install_dependencies",
            skipped=True,
            reason="python environment preparation is not automated yet",
        ))
    else:
        report.steps.append(PrepStep(
            name="detect_project",
            skipped=True,
            reason="no supported project manifest found",
        ))

    report.ready = True
    return report


def detect_project_type(root: Path) -> str:
    if (root / "package.json").is_file():
        return "javascript"
    if (root / "pyproject.toml").is_file() or (root / "requirements.txt").is_file():
        return "python"
    return "unknown"


def detect_package_manager(root: Path, pkg: dict | None = None) -> str:
    if pkg:
        pm_field = str(pkg.get("packageManager", ""))
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


def install_command_for_javascript(
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


def _has_lockfile(root: Path, lockfile: str | None, *filenames: str) -> bool:
    if any((root / filename).is_file() for filename in filenames):
        return True
    if not lockfile:
        return False
    return Path(lockfile).name in filenames


def javascript_preflight_commands(
    root: Path,
    pkg: dict,
    env_cfg: dict,
    *,
    package_manager: str | None = None,
) -> list[list[str]]:
    commands: list[list[str]] = []
    package_manager = package_manager or detect_package_manager(root, pkg)
    scripts = pkg.get("scripts", {}) if isinstance(pkg.get("scripts", {}), dict) else {}
    deps = _all_javascript_dependencies(pkg)

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


def recommended_tools_from_config(config: dict) -> list[str]:
    tools_cfg = config.get("tooling")
    if not isinstance(tools_cfg, dict):
        tools_cfg = config.get("tools", {}) or {}
    if not isinstance(tools_cfg, dict):
        return []
    configured = tools_cfg.get("recommended", [])
    if isinstance(configured, str):
        configured = [configured]
    if not isinstance(configured, list):
        return []

    tools = []
    seen = set()
    for tool in configured:
        if not isinstance(tool, str):
            continue
        name = os.path.basename(tool.strip())
        if name and name not in seen:
            tools.append(name)
            seen.add(name)
    return tools


def _read_package_json(root: Path) -> dict:
    try:
        return json.loads((root / "package.json").read_text())
    except (OSError, json.JSONDecodeError):
        return {}


def _all_javascript_dependencies(pkg: dict) -> set[str]:
    deps: set[str] = set()
    for key in ("dependencies", "devDependencies", "peerDependencies", "optionalDependencies"):
        value = pkg.get(key, {})
        if isinstance(value, dict):
            deps.update(str(name) for name in value)
    return deps


def _should_install_javascript(root: Path, install_mode: str) -> bool:
    if install_mode == "always":
        return True
    return not (root / "node_modules").is_dir()


def _preflight_recommended_tools(report: EnvironmentReport, config: dict) -> None:
    env = build_command_env(config)
    report.tool_path_dirs = [path for path in env.get("PATH", "").split(os.pathsep) if path]
    tools = recommended_tools_from_config(config)
    report.recommended_tools = tools
    if not tools:
        return

    resolved = {}
    missing = []
    for tool in tools:
        path = shutil.which(tool, path=env["PATH"])
        if path:
            resolved[tool] = path
        else:
            missing.append(tool)
    report.resolved_tools = resolved
    report.missing_tools = missing
    report.steps.append(PrepStep(
        name="recommended_tools",
        command=["which", *tools],
        returncode=1 if missing else 0,
        stdout=json.dumps(resolved, sort_keys=True),
        stderr=f"missing: {', '.join(missing)}" if missing else "",
    ))
    if missing:
        _fail(report, f"recommended tools unavailable: {', '.join(missing)}")


def _resolve_git_repo(repo: str) -> Path:
    root = Path(repo).expanduser().resolve()
    if not root.exists():
        raise WorktreePrepError(f"repo does not exist: {root}")
    if not root.is_dir():
        raise WorktreePrepError(f"repo is not a directory: {root}")
    try:
        result = _run(root, ["git", "rev-parse", "--show-toplevel"], timeout=30)
    except FileNotFoundError as exc:
        raise WorktreePrepError("git is unavailable") from exc
    except subprocess.TimeoutExpired as exc:
        raise WorktreePrepError(f"repo inspection timed out: {root}") from exc
    if result.returncode != 0:
        detail = (result.stderr or result.stdout or "").strip()
        raise WorktreePrepError(f"not a git repo: {detail}")
    return Path(result.stdout.strip()).resolve()


def _default_run_worktree_path(repo_root: Path, run_root: str | None) -> Path:
    import time

    stamp = time.strftime("%Y%m%d-%H%M%S")
    if run_root:
        candidate = Path(run_root).expanduser()
        # A relative --run-root belongs to the target repo, not to the caller's cwd.
        root = candidate if candidate.is_absolute() else repo_root / candidate
    else:
        root = repo_root / ".worktree"
    return root.resolve() / f"atm-run-{stamp}"


def _require_git_worktree(
    root: Path,
    report: EnvironmentReport,
    *,
    timeout: int,
    config: dict | None = None,
) -> None:
    command = ["git", "rev-parse", "--is-inside-work-tree"]
    try:
        result = _run(root, command, timeout=timeout, config=config)
    except FileNotFoundError as exc:
        report.steps.append(PrepStep(name="git_worktree", command=command, returncode=None, stderr=str(exc)))
        _fail(report, "git is unavailable")
    except subprocess.TimeoutExpired as exc:
        report.steps.append(PrepStep(name="git_worktree", command=command, returncode=None, stderr=f"timeout: {exc}"))
        _fail(report, "git worktree check timed out")
    step = PrepStep(
        name="git_worktree",
        command=command,
        returncode=result.returncode,
        stdout=result.stdout,
        stderr=result.stderr,
    )
    report.steps.append(step)
    if result.returncode != 0 or result.stdout.strip() != "true":
        _fail(report, "workdir is not a git worktree")


def _require_clean_worktree(
    root: Path,
    report: EnvironmentReport,
    *,
    timeout: int,
    config: dict | None = None,
) -> None:
    command = ["git", "status", "--porcelain"]
    try:
        result = _run(root, command, timeout=timeout, config=config)
    except FileNotFoundError as exc:
        report.steps.append(PrepStep(name="clean_worktree", command=command, returncode=None, stderr=str(exc)))
        _fail(report, "git is unavailable")
    except subprocess.TimeoutExpired as exc:
        report.steps.append(PrepStep(name="clean_worktree", command=command, returncode=None, stderr=f"timeout: {exc}"))
        _fail(report, "git status check timed out")
    step = PrepStep(
        name="clean_worktree",
        command=command,
        returncode=result.returncode,
        stdout=result.stdout,
        stderr=result.stderr,
    )
    report.steps.append(step)
    if result.returncode != 0:
        _fail(report, "could not inspect git status")
    if result.stdout.strip():
        _fail(report, "workdir has uncommitted changes before the run")


def _run_step(
    root: Path,
    report: EnvironmentReport,
    name: str,
    command: list[str],
    *,
    timeout: int,
    config: dict | None = None,
) -> None:
    try:
        result = _run(root, command, timeout=timeout, config=config)
    except FileNotFoundError as exc:
        step = PrepStep(name=name, command=command, returncode=None, stderr=str(exc))
        report.steps.append(step)
        _fail(report, f"required command is unavailable: {command[0]}")
    except subprocess.TimeoutExpired as exc:
        step = PrepStep(name=name, command=command, returncode=None, stderr=f"timeout: {exc}")
        report.steps.append(step)
        _fail(report, f"environment step timed out: {' '.join(command)}")

    step = PrepStep(
        name=name,
        command=command,
        returncode=result.returncode,
        stdout=result.stdout,
        stderr=result.stderr,
    )
    report.steps.append(step)
    if result.returncode != 0:
        _fail(report, f"environment step failed: {' '.join(command)}")


def _ensure_generated_test_config(root: Path, report: EnvironmentReport) -> None:
    if not report.runner_bootstrap:
        return
    try:
        shim_path = ensure_generated_test_config(report.runner_bootstrap)
    except OSError as exc:
        report.steps.append(PrepStep(name="test_config_shim", returncode=None, stderr=str(exc)))
        _fail(report, f"could not generate test config shim: {exc}")
    if shim_path:
        report.steps.append(PrepStep(
            name="test_config_shim",
            command=["write", str(shim_path)],
            returncode=0,
            stdout=os.path.relpath(shim_path, root),
        ))


def _run(
    root: Path,
    command: list[str],
    *,
    timeout: int,
    config: dict | None = None,
) -> subprocess.CompletedProcess:
    env = build_command_env(config)
    env.setdefault("CI", "1")
    return subprocess.run(
        command,
        cwd=root,
        capture_output=True,
        text=True,
        timeout=timeout,
        env=env,
    )


def _fail(report: EnvironmentReport, reason: str) -> None:
    report.ready = False
    report.reason = reason
    raise EnvironmentPrepError(reason, report)
