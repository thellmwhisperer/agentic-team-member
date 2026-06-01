"""Deterministic reconnaissance cookbook generation."""

from __future__ import annotations

import json
import re
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path, PurePosixPath

from agentic_tdd_runner.languages import supported_extensions

TEST_NAME_RE = re.compile(r"(^test[_\.-]|[_\.-](?:test|spec)\.)", re.IGNORECASE)
ISSUE_PATH_RE = re.compile(r"`([^`]+\.[A-Za-z0-9]+)`|['\"]([^'\"]+\.[A-Za-z0-9]+)['\"]")
BACKTICK_SYMBOL_RE = re.compile(r"`([A-Za-z_$][\w$]*)\s*(?:\(\))?`")
BARE_IDENTIFIER_RE = re.compile(r"\b([A-Za-z_$][\w$]{2,})\b")
GENERIC_ANCHOR_PARTS = {"common", "components", "shared", "ui", "utils"}
EXCLUDE_DIRS = {
    ".atm",
    ".git",
    ".next",
    ".turbo",
    ".venv",
    "__pycache__",
    "build",
    "coverage",
    "dist",
    "node_modules",
}
SYMBOL_STOPWORDS = {
    "Bug",
    "Where",
}


@dataclass(frozen=True)
class CandidateConsumer:
    symbol: str
    path: str

    def to_log_dict(self) -> dict:
        return {"symbol": self.symbol, "path": self.path}


@dataclass(frozen=True)
class ReconCookbook:
    markdown: str
    frameworks: list[str]
    test_stack: list[str]
    entry_points: list[str]
    anti_anchor_paths: list[str]
    candidate_consumers: list[CandidateConsumer]
    example_tests: list[str]
    hypotheses: list[str]

    def to_log_dict(self) -> dict:
        sections = []
        if self.frameworks:
            sections.append("framework")
        if self.test_stack:
            sections.append("test_stack")
        if self.entry_points:
            sections.append("entry_points")
        if self.anti_anchor_paths:
            sections.append("anti_anchor")
        if self.candidate_consumers:
            sections.append("candidate_consumers")
        if self.example_tests:
            sections.append("example_tests")
        if self.hypotheses:
            sections.append("hypothesis_space")
        return {
            "phase": "recon",
            "sections": sections,
            "frameworks": list(self.frameworks),
            "test_stack": list(self.test_stack),
            "entry_points": list(self.entry_points),
            "anti_anchor_paths": list(self.anti_anchor_paths),
            "candidate_consumers": [
                consumer.to_log_dict() for consumer in self.candidate_consumers
            ],
            "example_tests": list(self.example_tests),
            "hypotheses": list(self.hypotheses),
            "markdown": self.markdown,
        }


def build_recon_cookbook(
    *,
    issue_text: str,
    project_root: str,
    config: dict | None = None,
) -> ReconCookbook:
    """Build deterministic pre-episode framing from repo facts."""
    root = Path(project_root)
    package = _read_package(root)
    deps = _all_package_deps(package)
    source_files = _source_files(root)
    frameworks = _detect_frameworks(root, deps)
    test_stack = _detect_test_stack(root, package, deps, config=config)
    entry_points = _detect_entry_points(root, source_files, frameworks)
    issue_paths = _extract_issue_paths(issue_text)
    anti_anchor_paths = [
        path for path in issue_paths if _is_generic_anchor_path(path)
    ]
    symbols = _extract_issue_symbols(issue_text)
    candidate_consumers = _candidate_consumers(
        root,
        source_files,
        symbols,
        exclude_paths=set(issue_paths),
    )
    example_tests = _example_tests_for_consumers(root, source_files, candidate_consumers)
    hypotheses = _hypotheses(issue_text, frameworks)
    markdown = _render_markdown(
        frameworks=frameworks,
        test_stack=test_stack,
        entry_points=entry_points,
        anti_anchor_paths=anti_anchor_paths,
        candidate_consumers=candidate_consumers,
        example_tests=example_tests,
        hypotheses=hypotheses,
    )
    return ReconCookbook(
        markdown=markdown,
        frameworks=frameworks,
        test_stack=test_stack,
        entry_points=entry_points,
        anti_anchor_paths=anti_anchor_paths,
        candidate_consumers=candidate_consumers,
        example_tests=example_tests,
        hypotheses=hypotheses,
    )


def _read_package(root: Path) -> dict:
    try:
        data = json.loads((root / "package.json").read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError):
        return {}
    return data if isinstance(data, dict) else {}


def _all_package_deps(package: dict) -> set[str]:
    deps: set[str] = set()
    for key in ("dependencies", "devDependencies", "peerDependencies", "optionalDependencies"):
        value = package.get(key, {})
        if isinstance(value, dict):
            deps.update(str(name) for name in value)
    return deps


def _detect_frameworks(root: Path, deps: set[str]) -> list[str]:
    frameworks = []
    if "next" in deps or any(root.glob("next.config.*")):
        frameworks.append("Next.js")
    if "@remix-run/react" in deps or (root / "remix.config.js").is_file():
        frameworks.append("Remix")
    if "@sveltejs/kit" in deps or (root / "svelte.config.js").is_file():
        frameworks.append("SvelteKit")
    return frameworks


def _detect_test_stack(
    root: Path,
    package: dict,
    deps: set[str],
    *,
    config: dict | None,
) -> list[str]:
    stack = []
    bootstrap = ((config or {}).get("runner", {}) or {}).get("bootstrap")
    runner = _text_value(bootstrap, "test_runner")
    if runner:
        stack.append(runner)

    scripts = package.get("scripts", {}) if isinstance(package, dict) else {}
    test_script = scripts.get("test") if isinstance(scripts, dict) else None
    for name in ("jest", "vitest"):
        if name in deps or (isinstance(test_script, str) and name in test_script):
            stack.append(name)
    if "bun-types" in deps or "@types/bun" in deps:
        stack.append("bun:test")
    if "@testing-library/react" in deps:
        stack.append("@testing-library/react")
    if "pytest" in deps or (root / "pytest.ini").is_file():
        stack.append("pytest")
    return _dedupe(stack)


def _detect_entry_points(
    root: Path,
    source_files: list[Path],
    frameworks: list[str],
) -> list[str]:
    entry_points = []
    if "Next.js" in frameworks:
        if (root / "app").is_dir():
            entry_points.append("Next.js App Router (`app/`)")
        if (root / "pages").is_dir():
            entry_points.append("Next.js Pages Router (`pages/`)")
        client_files = _files_containing(root, source_files, '"use client"', limit=3)
        if client_files:
            entry_points.append("Client components: " + ", ".join(f"`{path}`" for path in client_files))
    return entry_points


def _extract_issue_paths(issue_text: str) -> list[str]:
    paths = []
    for match in ISSUE_PATH_RE.finditer(issue_text):
        raw = match.group(1) or match.group(2)
        if "/" not in raw and "\\" not in raw:
            continue
        paths.append(_normalize_path(raw))
    return _dedupe(paths)


def _extract_issue_symbols(issue_text: str) -> list[str]:
    symbols = []
    for match in BACKTICK_SYMBOL_RE.finditer(issue_text):
        symbol = match.group(1)
        if symbol in SYMBOL_STOPWORDS:
            continue
        symbols.append(symbol)
    for match in BARE_IDENTIFIER_RE.finditer(issue_text):
        symbol = match.group(1)
        if symbol in SYMBOL_STOPWORDS or not _looks_like_bare_symbol(symbol):
            continue
        symbols.append(symbol)
    return _dedupe(symbols)


def _looks_like_bare_symbol(symbol: str) -> bool:
    has_lower = any(ch.islower() for ch in symbol)
    has_upper = any(ch.isupper() for ch in symbol)
    has_internal_upper = any(ch.isupper() for ch in symbol[1:])
    return has_lower and has_upper and (symbol[0].islower() or has_internal_upper)


def _is_generic_anchor_path(path: str) -> bool:
    parts = {part.lower() for part in PurePosixPath(path).parts}
    return bool(parts & GENERIC_ANCHOR_PARTS)


def _candidate_consumers(
    root: Path,
    source_files: list[Path],
    symbols: list[str],
    *,
    exclude_paths: set[str],
) -> list[CandidateConsumer]:
    consumers: list[CandidateConsumer] = []
    seen: set[tuple[str, str]] = set()
    for path in source_files:
        rel = _normalize_path(str(path.relative_to(root)))
        if rel in exclude_paths or _is_test_path(rel):
            continue
        text = _read_source_text(path)
        if text is None:
            continue
        for symbol in symbols:
            if not re.search(rf"\b{re.escape(symbol)}\b", text):
                continue
            key = (symbol, rel)
            if key in seen:
                continue
            consumers.append(CandidateConsumer(symbol=symbol, path=rel))
            seen.add(key)
            if len(consumers) >= 8:
                return consumers
    return consumers


def _example_tests_for_consumers(
    root: Path,
    source_files: list[Path],
    consumers: list[CandidateConsumer],
) -> list[str]:
    tests = []
    all_tests = [
        path for path in source_files
        if _is_test_path(str(path.relative_to(root)))
    ]
    for consumer in consumers:
        consumer_dir = PurePosixPath(consumer.path).parent
        for path in all_tests:
            rel = _normalize_path(str(path.relative_to(root)))
            test_dir = PurePosixPath(rel).parent
            if test_dir == consumer_dir or str(test_dir).startswith(str(consumer_dir) + "/"):
                tests.append(rel)
                break
        if len(tests) >= 2:
            break
    return _dedupe(tests)


def _hypotheses(issue_text: str, frameworks: list[str]) -> list[str]:
    text = issue_text.lower()
    hypotheses = []
    if "Next.js" in frameworks and any(token in text for token in ("flash", "hydration", "flicker")):
        hypotheses.extend([
            "Hydration or SSR/client markup mismatch.",
            "Client effect timing or stale closure.",
            "Server/client data ordering before render.",
        ])
    if (
        any(token in text for token in ("retry", "rate limit"))
        or re.search(r"\bapi\b", text)
    ):
        hypotheses.extend([
            "API wrapper behavior at the external boundary.",
            "Backend/service layer translating the wrapper result.",
        ])
    return hypotheses[:5]


def _render_markdown(
    *,
    frameworks: list[str],
    test_stack: list[str],
    entry_points: list[str],
    anti_anchor_paths: list[str],
    candidate_consumers: list[CandidateConsumer],
    example_tests: list[str],
    hypotheses: list[str],
) -> str:
    if not any((frameworks, test_stack, entry_points, anti_anchor_paths, candidate_consumers, example_tests, hypotheses)):
        return ""

    lines = [
        "## Recon Cookbook",
        "",
        "### Sequencing Rail",
        "- Locate -> trace -> distinguish symptom from cause -> test + fix.",
        "- Treat the selected target as a hypothesis until code evidence confirms the bug boundary.",
        "",
    ]
    _append_section(lines, "Framework", frameworks)
    _append_section(lines, "Test Stack", test_stack)
    _append_section(lines, "Entry Points", entry_points)
    if anti_anchor_paths:
        lines.append("### Anti-anchor")
        for path in anti_anchor_paths:
            lines.append(f"- Issue mentions generic/shared path `{path}`; do not assume it is the cause without tracing consumers.")
        lines.append("")
    if candidate_consumers:
        lines.append("### Candidate Consumers")
        for consumer in candidate_consumers:
            lines.append(f"- `{consumer.symbol}` is referenced by `{consumer.path}`.")
        lines.append("")
    _append_section(lines, "Example Tests To Read First", [f"`{path}`" for path in example_tests])
    _append_section(lines, "Hypothesis Space", hypotheses)
    while lines and lines[-1] == "":
        lines.pop()
    return "\n".join(lines)


def _append_section(lines: list[str], title: str, values: list[str]) -> None:
    if not values:
        return
    lines.append(f"### {title}")
    for value in values:
        lines.append(f"- {value}")
    lines.append("")


def _files_containing(
    root: Path,
    source_files: list[Path],
    needle: str,
    *,
    limit: int,
) -> list[str]:
    matches = []
    for path in source_files:
        text = _read_source_text(path)
        if text is None or needle not in text:
            continue
        matches.append(_normalize_path(str(path.relative_to(root))))
        if len(matches) >= limit:
            break
    return matches


def _source_files(root: Path):
    if not root.exists():
        return []
    source_suffixes = set(supported_extensions())
    files = []
    for path in root.rglob("*"):
        if not path.is_file() or path.suffix not in source_suffixes:
            continue
        rel = path.relative_to(root)
        if any(part in EXCLUDE_DIRS for part in rel.parts):
            continue
        files.append(path)
    return sorted(files)


def _read_source_text(path: Path) -> str | None:
    try:
        return path.read_text(encoding="utf-8", errors="ignore")
    except OSError:
        return None


def _is_test_path(path: str) -> bool:
    return bool(TEST_NAME_RE.search(PurePosixPath(path).name) or "/__tests__/" in path)


def _normalize_path(path: str) -> str:
    return PurePosixPath(path.replace("\\", "/")).as_posix().removeprefix("./")


def _text_value(container: object, key: str) -> str | None:
    value = container.get(key) if isinstance(container, Mapping) else getattr(container, key, None)
    return value.strip() if isinstance(value, str) and value.strip() else None


def _dedupe(values: list[str]) -> list[str]:
    seen = set()
    deduped = []
    for value in values:
        if value in seen:
            continue
        seen.add(value)
        deduped.append(value)
    return deduped
