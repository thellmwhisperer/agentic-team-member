"""Deterministic facts about the target project's test runner."""

from __future__ import annotations

import fnmatch
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path, PurePosixPath

from agentic_tdd_runner.languages import get_language, plugins
from agentic_tdd_runner.runner_authority import override_detected_runner
from agentic_tdd_runner.runner_command import effective_test_command_template


GENERIC_TEST_FILE_PATTERNS = [
    "test_*.py",
    "*_test.py",
    "*.test.*",
    "*.spec.*",
]
BASE_EXCLUDE_DIRS = {
    ".git",
    ".hg",
    ".mypy_cache",
    ".pytest_cache",
    ".ruff_cache",
    ".tox",
    ".venv",
    "build",
    "coverage",
    "dist",
    "node_modules",
    "vendor",
    "venv",
    "__pycache__",
}


@dataclass(frozen=True)
class RunnerFacts:
    """Small fact report the harness can answer without spending model tokens."""

    test_runner: str
    test_command: str
    typecheck_command: str | None
    test_api_import: str | None
    recommended_test_file: str | None
    source_file: str | None
    target_symbol: str | None
    nearby_tests: list[str]
    symbol_tests: list[str]
    source_line_range: dict | None = None
    test_api_facts: list[str] | None = None

    def to_prompt_section(self) -> str:
        source_dir = (
            PurePosixPath(self.source_file).parent.as_posix()
            if self.source_file
            else "."
        )
        lines = [
            "## Runner Facts",
            (
                "These facts were computed by the harness before the model ran. "
                "Use them before inspecting project config."
            ),
            f"- test runner: {self.test_runner}",
            f"- test command: {self.test_command or 'not configured'}",
        ]
        if self.typecheck_command:
            lines.append(f"- typecheck command: {self.typecheck_command}")
        if self.test_api_import:
            lines.append(f"- test API import: `{self.test_api_import}`")
        if self.recommended_test_file:
            lines.append(f"- recommended regression test file: {self.recommended_test_file}")
        if self.source_line_range:
            start = self.source_line_range.get("start")
            end = self.source_line_range.get("end")
            source = self.source_line_range.get("source")
            if start and end and source == "definition":
                lines.append(f"- target source range: {self.source_file}:{start}-{end}")
        for fact in self.test_api_facts or []:
            lines.append(f"- test API fact: {fact}")
        nearby = ", ".join(self.nearby_tests) if self.nearby_tests else "none"
        lines.append(f"- nearby tests in {source_dir}: {nearby}")
        symbol = ", ".join(self.symbol_tests) if self.symbol_tests else "none"
        if self.target_symbol:
            lines.append(f"- existing tests mentioning {self.target_symbol}: {symbol}")
        return "\n".join(lines)

    def to_log_dict(self) -> dict:
        return {
            "test_runner": self.test_runner,
            "test_command": self.test_command,
            "typecheck_command": self.typecheck_command,
            "test_api_import": self.test_api_import,
            "recommended_test_file": self.recommended_test_file,
            "source_file": self.source_file,
            "target_symbol": self.target_symbol,
            "nearby_tests": list(self.nearby_tests),
            "symbol_tests": list(self.symbol_tests),
            "source_line_range": dict(self.source_line_range) if self.source_line_range else None,
            "test_api_facts": list(self.test_api_facts or []),
        }


def build_runner_facts(
    project_root: str,
    config: dict,
    *,
    episode: dict | None = None,
) -> RunnerFacts:
    """Build a reusable deterministic fact report for the current run."""
    root = Path(project_root)
    runner_cfg = config.get("runner", {}) if isinstance(config, dict) else {}
    runner_cfg = runner_cfg if isinstance(runner_cfg, dict) else {}
    bootstrap = runner_cfg.get("bootstrap") if isinstance(runner_cfg, dict) else None
    language = _language_for_episode(episode)
    override_detected = override_detected_runner(runner_cfg)
    test_runner = _test_runner(runner_cfg, bootstrap, language, episode, override_detected)
    test_command = _test_command(runner_cfg, bootstrap, language, test_runner, override_detected)
    source_file = _episode_value(episode, "source_file")
    target_symbol = _episode_value(episode, "target_symbol")
    recommended_test_file = _episode_value(episode, "test_file")
    patterns = _test_file_patterns(runner_cfg, language)
    default_exclude_dirs = BASE_EXCLUDE_DIRS | set(
        _language_default_exclude_dirs(language)
    )
    exclude_dirs = default_exclude_dirs | set(runner_cfg.get("exclude_dirs") or [])

    test_files = _discover_test_files(root, patterns, exclude_dirs)
    nearby_tests = _nearby_tests(test_files, source_file)
    symbol_tests = _symbol_tests(root, test_files, target_symbol)

    return RunnerFacts(
        test_runner=test_runner,
        test_command=test_command,
        typecheck_command=_typecheck_command(root, config, language, test_runner),
        test_api_import=_test_api_import(language, test_runner),
        recommended_test_file=recommended_test_file,
        source_file=source_file,
        target_symbol=target_symbol,
        nearby_tests=nearby_tests,
        symbol_tests=symbol_tests,
        source_line_range=_episode_line_range(episode),
        test_api_facts=_test_api_facts(language, test_runner),
    )


def _episode_value(episode: Mapping[str, object] | None, key: str) -> str | None:
    if not episode:
        return None
    value = episode.get(key)
    return value if isinstance(value, str) and value else None


def _configured_text(value: object) -> str | None:
    return value.strip() if isinstance(value, str) and value.strip() else None


def _bootstrap_value(bootstrap: object, key: str) -> str | None:
    value = (
        bootstrap.get(key)
        if isinstance(bootstrap, Mapping)
        else getattr(bootstrap, key, None)
    )
    return _configured_text(value)


def _language_for_episode(episode: Mapping[str, object] | None) -> object | None:
    for key in ("test_file", "source_file"):
        value = _episode_value(episode, key)
        if not value:
            continue
        language = get_language(value)
        if language:
            return language
    return None


def _language_runner(language: object | None) -> str | None:
    runner = getattr(language, "runner", None)
    return runner if isinstance(runner, str) and runner else None


def _test_runner(
    runner_cfg: Mapping[str, object],
    bootstrap: object,
    language: object | None,
    episode: Mapping[str, object] | None,
    override_detected: bool,
) -> str:
    configured = _configured_text(runner_cfg.get("framework"))
    episode_runner = _episode_value(episode, "runner")
    detected = _bootstrap_value(bootstrap, "test_runner")
    if override_detected:
        return configured or episode_runner or detected or _language_runner(language) or "unknown"
    return detected or episode_runner or configured or _language_runner(language) or "unknown"


def _language_for_runner(test_runner: str) -> object | None:
    for plugin in plugins():
        supports_fn = getattr(plugin, "supports_test_runner", None)
        if callable(supports_fn) and supports_fn(test_runner):
            return plugin
    return None


def _test_command(
    runner_cfg: Mapping[str, object],
    bootstrap: object,
    language: object | None,
    test_runner: str,
    override_detected: bool,
) -> str:
    configured = _configured_text(runner_cfg.get("command"))
    if override_detected and configured:
        return configured
    if override_detected:
        command = _language_command_template(language, runner_cfg)
        if command:
            return command
        runner_language = _language_for_runner(test_runner)
        command = _language_effective_command(runner_language, test_runner)
        if command:
            return command
        return ""

    detected = effective_test_command_template(bootstrap, None)
    if detected:
        return detected

    if configured:
        return configured

    command = _language_command_template(language, runner_cfg)
    if command:
        return command

    runner_language = _language_for_runner(test_runner)
    command = _language_effective_command(runner_language, test_runner)
    if command:
        return command
    return ""


def _language_command_template(
    language: object | None,
    runner_cfg: Mapping[str, object],
) -> str:
    command_fn = getattr(language, "test_command_template", None)
    if callable(command_fn):
        command = command_fn({"runner": runner_cfg})
        if command:
            return command
    return ""


def _language_effective_command(language: object | None, test_runner: str) -> str:
    command_fn = getattr(language, "effective_test_command_template", None)
    if callable(command_fn):
        return command_fn({"test_runner": test_runner}, None)
    return ""


def _test_file_patterns(
    runner_cfg: Mapping[str, object],
    language: object | None,
) -> list[str]:
    configured = runner_cfg.get("test_file_patterns")
    if isinstance(configured, list) and configured:
        return [str(pattern) for pattern in configured if str(pattern)]
    patterns_fn = getattr(language, "test_file_patterns", None)
    if callable(patterns_fn):
        patterns = patterns_fn()
        if patterns:
            return [str(pattern) for pattern in patterns if str(pattern)]
    return list(GENERIC_TEST_FILE_PATTERNS)


def _language_default_exclude_dirs(language: object | None) -> list[str]:
    exclude_fn = getattr(language, "default_exclude_dirs", None)
    if not callable(exclude_fn):
        return []
    return [str(path) for path in exclude_fn()]


def _episode_line_range(episode: Mapping[str, object] | None) -> dict | None:
    if not episode:
        return None
    value = episode.get("function_line_range")
    if not isinstance(value, dict):
        return None
    start = value.get("start")
    end = value.get("end")
    source = value.get("source")
    if not isinstance(start, int) or not isinstance(end, int):
        return None
    return {"start": start, "end": end, "source": source}


def _test_api_facts(language: object | None, test_runner: str) -> list[str]:
    facts_fn = getattr(language, "test_api_facts", None)
    if not callable(facts_fn):
        runner_language = _language_for_runner(test_runner)
        facts_fn = getattr(runner_language, "test_api_facts", None)
    if callable(facts_fn):
        return list(facts_fn(test_runner))
    return []


def _discover_test_files(root: Path, patterns: list[str], exclude_dirs: set[str]) -> list[str]:
    files: list[str] = []
    if not root.exists():
        return files
    for path in root.rglob("*"):
        if not path.is_file():
            continue
        try:
            rel = path.relative_to(root)
        except ValueError:
            continue
        if any(part in exclude_dirs for part in rel.parts):
            continue
        rel_posix = rel.as_posix()
        if any(
            fnmatch.fnmatch(rel_posix, pattern) or fnmatch.fnmatch(path.name, pattern)
            for pattern in patterns
        ):
            files.append(rel_posix)
    return sorted(files)


def _nearby_tests(test_files: list[str], source_file: str | None) -> list[str]:
    if not source_file:
        return []
    source_dir = PurePosixPath(source_file).parent
    return [
        test_file
        for test_file in test_files
        if PurePosixPath(test_file).parent == source_dir
    ]


def _symbol_tests(root: Path, test_files: list[str], target_symbol: str | None) -> list[str]:
    if not target_symbol:
        return []
    matches: list[str] = []
    for test_file in test_files:
        rel = PurePosixPath(test_file)
        if target_symbol in rel.name:
            matches.append(test_file)
            continue
        try:
            text = (root / test_file).read_text(errors="ignore")
        except OSError:
            continue
        if target_symbol in text:
            matches.append(test_file)
    return sorted(dict.fromkeys(matches))


def _typecheck_command(
    root: Path,
    config: dict,
    language: object | None,
    test_runner: str,
) -> str | None:
    typecheck_fn = getattr(language, "typecheck_command", None)
    if not callable(typecheck_fn):
        runner_language = _language_for_runner(test_runner)
        typecheck_fn = getattr(runner_language, "typecheck_command", None)
    if callable(typecheck_fn):
        return typecheck_fn(root, config, test_runner)
    return None


def _test_api_import(language: object | None, test_runner: str) -> str | None:
    import_fn = getattr(language, "test_api_import", None)
    if not callable(import_fn):
        runner_language = _language_for_runner(test_runner)
        import_fn = getattr(runner_language, "test_api_import", None)
    if callable(import_fn):
        return import_fn(test_runner)
    return None
