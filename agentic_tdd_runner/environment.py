"""Deterministic target-repository environment preparation."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
from dataclasses import asdict, dataclass, field
from pathlib import Path

from agentic_tdd_runner.languages import plugins
from agentic_tdd_runner.runner_bootstrap import RunnerBootstrapReport
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

    project_language, project_type = detect_project(root)
    report.project_type = project_type
    inspect_bootstrap = getattr(project_language, "inspect_runner_bootstrap", None)
    if callable(inspect_bootstrap):
        report.runner_bootstrap = inspect_bootstrap(root)
    _preflight_recommended_tools(report, config)
    _require_git_worktree(root, report, timeout=timeout, config=config)

    if bool(env_cfg.get("require_clean", True)):
        _require_clean_worktree(root, report, timeout=timeout, config=config)

    if project_type == "javascript":
        pkg = _project_manifest(project_language, root)
        package_manager = _project_package_manager(
            project_language,
            root,
            pkg,
            report.runner_bootstrap,
        )
        report.package_manager = package_manager

        install_mode = str(env_cfg.get("install", "auto"))
        if install_mode not in {"auto", "always", "never"}:
            _fail(report, f"invalid environment.install value: {install_mode}")

        if install_mode != "never" and _project_should_install(
            project_language,
            root,
            install_mode,
        ):
            install_cmd = _project_install_command(
                project_language,
                root,
                package_manager,
                report.runner_bootstrap,
            )
            if install_cmd:
                report.install_command = install_cmd
                _run_step(root, report, "install_dependencies", install_cmd, timeout=timeout, config=config)
        else:
            report.steps.append(PrepStep(
                name="install_dependencies",
                skipped=True,
                reason="dependencies already present" if install_mode != "never" else "disabled",
            ))

        _ensure_project_test_config(root, report, project_language)

        preflight_commands = _project_preflight_commands(
            project_language,
            root,
            pkg,
            env_cfg,
            package_manager,
            report.runner_bootstrap,
        )
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


def detect_project(root: Path) -> tuple[object | None, str]:
    for language in plugins():
        detect = getattr(language, "detect_project_type", None)
        project_type = detect(root) if callable(detect) else None
        if isinstance(project_type, str) and project_type:
            return language, project_type
    return None, "unknown"


def detect_project_type(root: Path) -> str:
    return detect_project(root)[1]


def _project_manifest(language: object | None, root: Path) -> dict:
    manifest = getattr(language, "project_manifest", None)
    if not callable(manifest):
        return {}
    value = manifest(root)
    return value if isinstance(value, dict) else {}


def _project_package_manager(
    language: object | None,
    root: Path,
    manifest: dict,
    bootstrap: RunnerBootstrapReport | None,
) -> str | None:
    package_manager = getattr(language, "project_package_manager", None)
    if not callable(package_manager):
        return None
    value = package_manager(root, manifest, bootstrap)
    return value if isinstance(value, str) and value else None


def _project_should_install(
    language: object | None,
    root: Path,
    install_mode: str,
) -> bool:
    should_install = getattr(language, "project_should_install", None)
    return bool(should_install(root, install_mode)) if callable(should_install) else False


def _project_install_command(
    language: object | None,
    root: Path,
    package_manager: str | None,
    bootstrap: RunnerBootstrapReport | None,
) -> list[str] | None:
    install_command = getattr(language, "project_install_command", None)
    if not callable(install_command):
        return None
    command = install_command(root, package_manager, bootstrap)
    return command if isinstance(command, list) and command else None


def _project_preflight_commands(
    language: object | None,
    root: Path,
    manifest: dict,
    env_cfg: dict,
    package_manager: str | None,
    bootstrap: RunnerBootstrapReport | None,
) -> list[list[str]]:
    preflight = getattr(language, "project_preflight_commands", None)
    if not callable(preflight):
        return []
    commands = preflight(root, manifest, env_cfg, package_manager, bootstrap)
    return commands if isinstance(commands, list) else []


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
    root = Path(run_root).expanduser().resolve() if run_root else repo_root / ".worktree"
    return root / f"atm-run-{stamp}"


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


def _ensure_project_test_config(
    root: Path,
    report: EnvironmentReport,
    language: object | None,
) -> None:
    if not report.runner_bootstrap:
        return
    ensure_config = getattr(language, "ensure_project_test_config", None)
    if not callable(ensure_config):
        return
    try:
        shim_path = ensure_config(root, report.runner_bootstrap)
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
