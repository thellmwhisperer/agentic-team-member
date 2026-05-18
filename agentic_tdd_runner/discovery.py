"""Deterministic target discovery and semantic indexing for issue-only runs."""
from __future__ import annotations

import json
import re
import unicodedata
from pathlib import Path, PurePosixPath

from agentic_tdd_runner.cookbook import _find_function_end
from agentic_tdd_runner.languages import get_language

_WORD_RE = re.compile(r"[@!\w-]+", re.UNICODE)
_STRING_RE = re.compile(r"['\"`]([^'\"`\n]{1,200})['\"`]")
_CALL_NAME_RE = re.compile(r"\b([A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*)\s*\(")
_GUARD_METHOD_RE = re.compile(r"\.\s*(startsWith|includes|endsWith)\(\s*(['\"`])([^'\"`\n]{1,200})\2")
_GUARD_EQUALS_RE = re.compile(r"(?:===|==)\s*(['\"`])([^'\"`\n]{1,200})\1")
_ROUTE_COMMENT_RE = re.compile(r"^\s*//\s*---\s*(.*?)\s*---\s*$")
_ALIAS_ASSIGN_RE = re.compile(r"^\s*(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(.+?)\s*;\s*$")
_IF_CONDITION_RE = re.compile(r"^\s*if\s*\((.+)\)\s*\{?\s*$")
_IDENTIFIER_RE = re.compile(r"\b[A-Za-z_$][\w$]*\b")
_TS_FUNCTION_RE = re.compile(
    r"^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][\w$]*)\s*\(",
    re.MULTILINE,
)
_TS_ARROW_RE = re.compile(
    r"^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?(?:\([^)]*\)|[A-Za-z_$][\w$]*)\s*=>",
    re.MULTILINE,
)
_PY_DEF_RE = re.compile(r"^\s*def\s+([A-Za-z_]\w*)\s*\(", re.MULTILINE)
_PY_CLASS_RE = re.compile(r"^\s*class\s+([A-Za-z_]\w*)\b", re.MULTILINE)
_TS_CLASS_RE = re.compile(r"^\s*(?:export\s+)?class\s+([A-Za-z_$][\w$]*)\b", re.MULTILINE)
_TS_METHOD_RE = re.compile(
    r"^\s*(?:(?:public|private|protected|static|readonly)\s+)*(?:async\s+)?([A-Za-z_$][\w$]*)\s*\(",
)
_DEFAULT_SEMANTIC_INDEX_RELATIVE_PATH = Path(".atm/semantic-index.generated.json")
_SEMANTIC_INDEX_VERSION = 5
_EXCLUDED_DIRS = {
    ".atm",
    ".cache",
    ".git",
    ".mypy_cache",
    ".next",
    ".pytest_cache",
    ".ruff_cache",
    ".turbo",
    ".venv",
    ".worktree",
    ".worktrees",
    "__pycache__",
    "build",
    "coverage",
    "dist",
    "env",
    "node_modules",
    "out",
    "target",
    "venv",
}
_PATH_DOMAIN_EXCLUDES = {
    "app",
    "apps",
    "lib",
    "libs",
    "packages",
    "py",
    "python",
    "src",
    "test",
    "tests",
    "ts",
}
_DOMAIN_FEATURE_EXCLUDES = {
    "ask",
    "clear",
    "client",
    "create",
    "emit",
    "error",
    "event",
    "execute",
    "fetch",
    "get",
    "handle",
    "handler",
    "info",
    "logger",
    "manager",
    "notify",
    "publish",
    "record",
    "response",
    "save",
    "say",
    "searches",
    "send",
    "service",
    "set",
    "start",
    "stop",
    "summary",
    "timer",
    "timers",
    "track",
    "update",
    "warn",
    "with",
    "write",
}
_STOPWORDS = {
    "a",
    "an",
    "and",
    "appears",
    "as",
    "at",
    "bug",
    "como",
    "cuando",
    "de",
    "del",
    "do",
    "does",
    "el",
    "en",
    "es",
    "for",
    "funciona",
    "function",
    "if",
    "la",
    "lo",
    "los",
    "no",
    "only",
    "or",
    "para",
    "por",
    "que",
    "respuesta",
    "solo",
    "the",
    "to",
    "un",
    "una",
    "when",
    "word",
    "y",
}
_NEARBY_TEST_COMMON_TOKENS = {
    "all",
    "any",
    "async",
    "await",
    "be",
    "boolean",
    "can",
    "case",
    "client",
    "const",
    "create",
    "defined",
    "describe",
    "error",
    "expect",
    "false",
    "from",
    "get",
    "has",
    "in",
    "info",
    "is",
    "js",
    "jsx",
    "length",
    "let",
    "log",
    "logger",
    "message",
    "messages",
    "new",
    "not",
    "null",
    "number",
    "of",
    "ok",
    "old",
    "on",
    "out",
    "process",
    "promise",
    "py",
    "resolve",
    "return",
    "set",
    "should",
    "some",
    "src",
    "string",
    "test",
    "then",
    "to",
    "true",
    "ts",
    "tsx",
    "undefined",
    "user",
    "users",
    "username",
    "value",
    "var",
    "with",
    "without",
}
_LOW_VALUE_LOGGING_CALLS = {
    "log.error",
    "log.info",
    "log.warn",
    "logger.error",
    "logger.event",
    "logger.info",
    "logger.response",
    "logger.warn",
}
_SEMANTIC_CALL_MEMBER_PREFIXES = (
    "ask",
    "create",
    "emit",
    "execute",
    "fetch",
    "notify",
    "publish",
    "record",
    "save",
    "search",
    "send",
    "track",
    "update",
    "write",
)
_SEMANTIC_CALL_MEMBERS = {
    "clear",
    "error",
    "event",
    "info",
    "response",
    "say",
    "set",
    "values",
    "warn",
}
_LOW_VALUE_NEARBY_TEST_SEAMS = {
    "client",
    "log",
    "logger",
}
_LOW_VALUE_SEAM_NAME_HINT_TOKENS = {
    "client",
    "log",
    "logger",
    "manager",
    "service",
    "this",
}
_LOW_VALUE_SEAM_MEMBER_HINT_TOKENS = {
    "ask",
    "create",
    "event",
    "get",
    "has",
    "info",
    "on",
    "response",
    "say",
    "send",
    "set",
    "track",
    "values",
    "with",
}
_API_RETRY_WORD_TOKENS = {
    "backoff",
    "retry",
    "retried",
    "retries",
    "retrying",
    "transient",
}
_API_RETRY_STATUS_TOKENS = {"429", "500", "503"}
_API_RETRY_CONTEXT_TOKENS = {
    "api",
    "apis",
    "client",
    "clients",
    "endpoint",
    "endpoints",
    "external",
    "http",
    "https",
    "provider",
    "providers",
    "sdk",
    "service",
    "services",
}
_API_RETRY_STATUS_CONTEXT_TOKENS = {
    "error",
    "errors",
    "fail",
    "failed",
    "failing",
    "fails",
    "failure",
    "failures",
    "http",
    "status",
    "statuses",
    "transient",
}
_API_RATE_LIMIT_TOKENS = {
    "limit",
    "limited",
    "limits",
    "quota",
    "rate",
    "throttle",
    "throttled",
}
_API_BOUNDARY_TOKENS = {
    "api",
    "apis",
    "client",
    "clients",
    "endpoint",
    "endpoints",
    "fetch",
    "http",
    "https",
    "provider",
    "providers",
    "request",
    "requests",
    "response",
    "responses",
    "sdk",
    "service",
    "services",
}
_LOCAL_FILE_READER_IO_TOKENS = {
    "exists",
    "file",
    "files",
    "fs",
    "open",
    "path",
    "read",
    "readdir",
}
_LOCAL_FILE_READER_DATA_TOKENS = {
    "json",
    "latest",
    "load",
    "read",
    "reader",
    "report",
}
_API_BOUNDARY_BONUS_BASE = 8
_API_BOUNDARY_BONUS_PER_TOKEN = 3
_API_BOUNDARY_BONUS_CAP = 24
_API_NON_BOUNDARY_PENALTY = 8
_LOCAL_FILE_READER_PENALTY = 8


def build_semantic_index(project_root: str) -> dict:
    """Build a deterministic semantic layer for supported source files."""
    root = Path(project_root)
    source_files: list[tuple[str, str, object]] = []
    test_files: list[dict] = []
    for path in sorted(root.rglob("*")):
        if not path.is_file():
            continue
        rel_path = path.relative_to(root).as_posix()
        if _is_excluded_path(rel_path):
            continue
        lang = get_language(path.as_posix())
        if lang is None:
            continue
        try:
            source_text = path.read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError):
            continue
        if _is_test_file(rel_path):
            test_files.append({"path": rel_path, "text": source_text})
            continue
        source_files.append((rel_path, source_text, lang))

    symbols: list[dict] = []
    files: list[dict] = []
    for rel_path, source_text, lang in source_files:
        file_symbols = _candidates_for_file(rel_path, source_text, lang, test_files)
        symbols.extend(file_symbols)
        files.append(_build_file_fact(rel_path, lang.name, file_symbols, test_files))

    symbols.sort(key=lambda item: (item["source_path"], item["line_start"], item["symbol"]))
    files.sort(key=lambda item: item["path"])
    return {
        "version": _SEMANTIC_INDEX_VERSION,
        "project_root": project_root,
        "files": files,
        "symbols": symbols,
        "candidates": symbols,
    }


def _is_excluded_path(path: str) -> bool:
    return any(part in _EXCLUDED_DIRS for part in PurePosixPath(path).parts)


def write_semantic_index(project_root: str, *, output_path: str | Path) -> Path:
    """Materialize the generated semantic layer to a deterministic JSON file."""
    destination = Path(output_path)
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(
        json.dumps(build_semantic_index(project_root), indent=2, sort_keys=True) + "\n"
    )
    return destination


def load_semantic_index(index_path: str | Path) -> dict:
    """Load a previously generated semantic index JSON file."""
    return json.loads(Path(index_path).read_text())


def load_or_build_semantic_index(project_root: str, *, output_path: str | Path | None = None) -> dict:
    """Reuse the generated semantic layer when present, otherwise build it."""
    destination = Path(output_path) if output_path is not None else Path(project_root) / _DEFAULT_SEMANTIC_INDEX_RELATIVE_PATH
    if destination.exists():
        payload = load_semantic_index(destination)
        if payload.get("version") == _SEMANTIC_INDEX_VERSION:
            return payload
    write_semantic_index(project_root, output_path=destination)
    return load_semantic_index(destination)


def rank_targets(
    issue_text: str,
    project_root: str,
    *,
    index: dict | None = None,
    limit: int = 5,
) -> list[dict]:
    """Return ranked target candidates for an issue."""
    semantic_index = index or build_semantic_index(project_root)
    issue_tokens = set(_normalize_tokens(issue_text))
    if not issue_tokens:
        return []
    issue_shape = _classify_issue_shape(issue_tokens)

    scored = []
    for candidate in semantic_index["candidates"]:
        score = _score_candidate(candidate, issue_tokens, issue_shape=issue_shape)
        if score <= 0:
            continue
        enriched = dict(candidate)
        enriched["score"] = score
        if issue_shape:
            enriched["issue_shape"] = issue_shape
        scored.append(enriched)

    scored.sort(key=lambda item: (-item["score"], item["source_path"], item["line_start"], item["symbol"]))
    return scored[: max(0, limit)]


def discover_target(issue_text: str, project_root: str, *, index: dict | None = None) -> dict | None:
    """Return the best candidate target for an issue, or None if no signal exists."""
    ranked = rank_targets(issue_text=issue_text, project_root=project_root, index=index, limit=1)
    return ranked[0] if ranked else None


def _candidates_for_file(source_path: str, source_text: str, lang, test_files: list[dict]) -> list[dict]:
    if lang.name == "typescript":
        return _typescript_candidates(source_path, source_text, test_files)
    if lang.name == "python":
        return _python_candidates(source_path, source_text, test_files)
    return []


def _typescript_candidates(source_path: str, source_text: str, test_files: list[dict]) -> list[dict]:
    candidates = []
    for match in _TS_FUNCTION_RE.finditer(source_text):
        line_start = source_text[: match.start()].count("\n") + 1
        line_end = _find_function_end(source_text, line_start, lang=get_language(source_path)) or line_start
        snippet = _slice_lines(source_text, line_start, line_end)
        candidates.append(
            _build_candidate(
                source_path,
                match.group(1),
                "function",
                line_start,
                line_end,
                snippet,
                source_text=source_text,
                test_files=test_files,
            )
        )

    for match in _TS_ARROW_RE.finditer(source_text):
        line_start = source_text[: match.start()].count("\n") + 1
        line_end = _find_function_end(source_text, line_start, lang=get_language(source_path)) or line_start
        snippet = _slice_lines(source_text, line_start, line_end)
        candidates.append(
            _build_candidate(
                source_path,
                match.group(1),
                "function",
                line_start,
                line_end,
                snippet,
                source_text=source_text,
                test_files=test_files,
            )
        )

    lines = source_text.splitlines()
    current_class = None
    class_indent = 0
    class_depth = 0
    for idx, line in enumerate(lines, start=1):
        class_match = _TS_CLASS_RE.match(line)
        if class_match:
            current_class = class_match.group(1)
            class_indent = len(line) - len(line.lstrip())
            class_depth = line.count("{") - line.count("}")
            continue

        if current_class:
            class_depth += line.count("{") - line.count("}")
            if class_depth < 0 or (class_depth == 0 and (len(line) - len(line.lstrip())) <= class_indent and line.strip() == "}"):
                current_class = None
                class_depth = 0
                continue

            method_match = _TS_METHOD_RE.match(line)
            if not method_match:
                continue
            symbol = method_match.group(1)
            if symbol in {"if", "for", "while", "switch", "catch", "constructor"}:
                continue
            if line.lstrip().startswith(("if ", "for ", "while ", "switch ", "catch ")):
                continue
            line_end = _find_function_end(source_text, idx, lang=get_language(source_path)) or idx
            snippet = _slice_lines(source_text, idx, line_end)
            candidates.append(
                _build_candidate(
                    source_path,
                    symbol,
                    "method",
                    idx,
                    line_end,
                    snippet,
                    source_text=source_text,
                    test_files=test_files,
                    owner_class=current_class,
                )
            )
    return candidates


def _python_candidates(source_path: str, source_text: str, test_files: list[dict]) -> list[dict]:
    candidates = []
    lines = source_text.splitlines()
    current_class = None
    class_indent = None
    for idx, line in enumerate(lines, start=1):
        class_match = _PY_CLASS_RE.match(line)
        if class_match:
            current_class = class_match.group(1)
            class_indent = len(line) - len(line.lstrip())
            continue

        if current_class and line.strip():
            indent = len(line) - len(line.lstrip())
            if indent <= (class_indent or 0):
                current_class = None
                class_indent = None

        def_match = _PY_DEF_RE.match(line)
        if not def_match:
            continue
        symbol = def_match.group(1)
        owner_class = current_class if current_class and (len(line) - len(line.lstrip())) > (class_indent or 0) else None
        line_end = _find_function_end(source_text, idx, lang=get_language(source_path)) or idx
        snippet = _slice_lines(source_text, idx, line_end)
        candidates.append(
            _build_candidate(
                source_path,
                symbol,
                "method" if owner_class else "function",
                idx,
                line_end,
                snippet,
                source_text=source_text,
                test_files=test_files,
                owner_class=owner_class,
            )
        )
    return candidates


def _build_candidate(
    source_path: str,
    symbol: str,
    kind: str,
    line_start: int,
    line_end: int,
    snippet: str,
    *,
    source_text: str,
    test_files: list[dict],
    owner_class: str | None = None,
) -> dict:
    strings = sorted({match.group(1) for match in _STRING_RE.finditer(snippet)})
    entrypoints = _extract_entrypoints(source_text, symbol)
    routes = _extract_routes(snippet)
    guard_patterns = sorted({pattern for route in routes for pattern in route["guard_patterns"]})
    triggers = sorted({trigger for route in routes for trigger in route["triggers"]})
    calls = _extract_semantic_calls(snippet)
    telemetry = _extract_telemetry(calls)
    observables = _extract_observables(calls)
    test_seams = _extract_test_seams(calls, observables)
    domains = _infer_domains(
        source_path=source_path,
        symbol=symbol,
        entrypoints=entrypoints,
        routes=routes,
        triggers=triggers,
        observables=observables,
    )
    path_tokens = _normalize_tokens(source_path)
    symbol_tokens = _normalize_tokens(symbol)
    owner_tokens = _normalize_tokens(owner_class or "")
    string_tokens = _normalize_tokens(" ".join(strings))
    semantic_tokens = _normalize_tokens(
        " ".join(
            domains
            + triggers
            + guard_patterns
            + calls
            + telemetry
            + observables
            + [item["name"] for item in entrypoints]
        )
    )
    terms = sorted(set(path_tokens + symbol_tokens + owner_tokens + string_tokens + semantic_tokens))
    rank_tokens = sorted(set(terms + _normalize_tokens(" ".join(calls + observables))))
    nearby_test_signal_tokens = sorted(set(terms + _normalize_tokens(snippet)))
    nearby_tests = _nearby_tests_for_source(
        source_path,
        symbol,
        test_files,
        signal_tokens=nearby_test_signal_tokens,
        seam_names=[seam["name"] for seam in test_seams],
        seam_members=[member for seam in test_seams for member in seam["members"]],
    )
    qualified_name = f"{owner_class}.{symbol}" if owner_class else symbol
    return {
        "source_path": source_path,
        "symbol": symbol,
        "qualified_name": qualified_name,
        "kind": kind,
        "owner_class": owner_class,
        "line_start": line_start,
        "line_end": line_end,
        "domains": domains,
        "entrypoints": entrypoints,
        "triggers": triggers,
        "guard_patterns": guard_patterns,
        "routes": routes,
        "calls": calls,
        "telemetry": telemetry,
        "observables": observables,
        "test_seams": test_seams,
        "nearby_tests": nearby_tests,
        "path_tokens": path_tokens,
        "symbol_tokens": symbol_tokens,
        "string_tokens": string_tokens,
        "strings": strings,
        "terms": terms,
        "rank_tokens": rank_tokens,
    }


def _slice_lines(source_text: str, line_start: int, line_end: int) -> str:
    lines = source_text.splitlines()
    return "\n".join(lines[line_start - 1:line_end])


def _build_file_fact(source_path: str, language: str, symbols: list[dict], test_files: list[dict]) -> dict:
    domains = sorted({domain for symbol in symbols for domain in symbol["domains"]})
    return {
        "path": source_path,
        "language": language,
        "domains": domains,
        "symbols": sorted(symbol["symbol"] for symbol in symbols),
        "nearby_tests": _nearby_tests_for_source(source_path, None, test_files),
    }


def _is_test_file(path: str) -> bool:
    posix = PurePosixPath(path)
    name = posix.name
    return (
        ".test." in name
        or name.startswith("test_")
        or "tests" in posix.parts
    )


def _nearby_tests_for_source(
    source_path: str,
    symbol: str | None,
    test_files: list[dict],
    *,
    signal_tokens: list[str] | None = None,
    seam_names: list[str] | None = None,
    seam_members: list[str] | None = None,
) -> list[str]:
    source_parent = PurePosixPath(source_path).parent
    source_stem = PurePosixPath(source_path).stem.lower()
    signal_token_set = {
        token for token in (signal_tokens or [])
        if token not in _NEARBY_TEST_COMMON_TOKENS
    }
    normalized_seam_names = {
        seam_name.lower()
        for seam_name in (seam_names or [])
        if seam_name.lower() not in _LOW_VALUE_NEARBY_TEST_SEAMS
    }
    normalized_seam_members = {
        member.lower()
        for member in (seam_members or [])
        if member.lower() not in {"say", "event", "response", "error", "info", "on"}
    }
    tracking_seam_members = {
        member for member in normalized_seam_members
        if member.startswith("track")
    }
    seam_name_hint_tokens = {
        token
        for seam_name in (seam_names or [])
        for token in _normalize_tokens(seam_name)
        if token not in _NEARBY_TEST_COMMON_TOKENS and token not in _LOW_VALUE_SEAM_NAME_HINT_TOKENS
    }
    seam_member_hint_tokens = {
        token
        for seam_member in (seam_members or [])
        for token in _normalize_tokens(seam_member)
        if token not in _NEARBY_TEST_COMMON_TOKENS and token not in _LOW_VALUE_SEAM_MEMBER_HINT_TOKENS
    }
    scored: list[tuple[int, str]] = []
    for test_file in test_files:
        path = test_file["path"]
        posix = PurePosixPath(path)
        same_dir = posix.parent == source_parent
        score = 0
        name_lower = posix.name.lower()
        text_lower = test_file["text"].lower()
        path_tokens = {
            token for token in _normalize_tokens(path)
            if token not in _NEARBY_TEST_COMMON_TOKENS
        }
        test_tokens = {
            token for token in _normalize_tokens(f"{path}\n{test_file['text']}")
            if token not in _NEARBY_TEST_COMMON_TOKENS
        }
        cross_module_allowed = same_dir
        if not same_dir:
            if normalized_seam_names and any(seam_name in text_lower for seam_name in normalized_seam_names):
                cross_module_allowed = True
            elif normalized_seam_members and any(member.lower() in text_lower for member in normalized_seam_members):
                cross_module_allowed = True
        if not cross_module_allowed:
            continue
        if same_dir:
            score += 10
        if symbol:
            symbol_lower = symbol.lower()
            if symbol_lower in name_lower:
                score += 100
            if re.search(rf"\b{re.escape(symbol_lower)}\b", text_lower):
                score += 25
        if source_stem and source_stem in name_lower:
            score += 50
        if signal_token_set:
            score += len(signal_token_set & test_tokens) * 2
        score += len(seam_name_hint_tokens & path_tokens) * 18
        score += len(seam_member_hint_tokens & path_tokens) * 30
        for seam_name in normalized_seam_names:
            if seam_name and seam_name in text_lower:
                score += 30
        for seam_member in normalized_seam_members:
            if seam_member and seam_member in text_lower:
                score += 12
        for tracking_member in tracking_seam_members:
            if tracking_member and tracking_member in text_lower:
                score += 100
        score += 10
        scored.append((score, path))
    scored.sort(key=lambda item: (-item[0], item[1]))
    return [path for _, path in scored]


def _extract_entrypoints(source_text: str, symbol: str) -> list[dict]:
    pattern = re.compile(
        rf"([A-Za-z_$][\w$]*)\s*\.\s*(on|once)\(\s*(['\"`])([^'\"`\n]{{1,200}})\3\s*,\s*{re.escape(symbol)}\b"
    )
    entrypoints = []
    for match in pattern.finditer(source_text):
        emitter, method, _, event_name = match.groups()
        line = source_text[: match.start()].count("\n") + 1
        entrypoints.append(
            {
                "kind": "event",
                "emitter": f"{emitter}.{method}",
                "name": event_name,
                "line": line,
            }
        )
    entrypoints.sort(key=lambda item: (item["emitter"], item["name"], item["line"]))
    return entrypoints


def _extract_guard_patterns(snippet: str) -> list[str]:
    patterns = set()
    for method, _, literal in _GUARD_METHOD_RE.findall(snippet):
        patterns.add(f"{method}('{literal}')")
    for _, literal in _GUARD_EQUALS_RE.findall(snippet):
        patterns.add(f"== '{literal}'")
    return sorted(patterns)


def _extract_triggers(guard_patterns: list[str]) -> list[str]:
    triggers = set()
    for pattern in guard_patterns:
        match = re.search(r"'([^']+)'", pattern)
        if not match:
            continue
        literal = match.group(1)
        if literal.startswith(("@", "!")):
            triggers.add(literal)
    return sorted(triggers)


def _extract_routes(snippet: str) -> list[dict]:
    routes = []
    alias_patterns: dict[str, set[str]] = {}
    pending_label: str | None = None
    brace_depth = 0

    for raw_line in snippet.splitlines():
        line = raw_line.rstrip()
        stripped = line.strip()
        current_depth = brace_depth

        if current_depth == 1:
            comment_match = _ROUTE_COMMENT_RE.match(line)
            if comment_match:
                pending_label = comment_match.group(1)

            alias_match = _ALIAS_ASSIGN_RE.match(line)
            if alias_match:
                alias_patterns[alias_match.group(1)] = set(_extract_guard_patterns(alias_match.group(2)))

            if_match = _IF_CONDITION_RE.match(line)
            if if_match:
                condition = if_match.group(1).strip()
                guard_patterns = _resolve_condition_guard_patterns(condition, alias_patterns)
                triggers = _extract_triggers(guard_patterns)
                if guard_patterns or pending_label:
                    routes.append(
                        {
                            "condition": condition,
                            "guard_patterns": sorted(guard_patterns),
                            "label": pending_label,
                            "triggers": triggers,
                        }
                    )
                pending_label = None

        brace_depth += line.count("{") - line.count("}")

    return routes


def _resolve_condition_guard_patterns(condition: str, alias_patterns: dict[str, set[str]]) -> set[str]:
    patterns = set(_extract_guard_patterns(condition))
    for identifier in _IDENTIFIER_RE.findall(condition):
        patterns.update(alias_patterns.get(identifier, set()))
    return patterns


def _extract_semantic_calls(snippet: str) -> list[str]:
    calls = set()
    for line in snippet.splitlines():
        stripped = line.strip()
        if not stripped:
            continue
        if _looks_like_definition_line(stripped):
            continue
        for name in _CALL_NAME_RE.findall(stripped):
            if _is_semantic_call(name):
                calls.add(name)
    return sorted(calls)


def _looks_like_definition_line(stripped_line: str) -> bool:
    if _TS_FUNCTION_RE.match(stripped_line) or _TS_ARROW_RE.match(stripped_line) or _PY_DEF_RE.match(stripped_line):
        return True
    if stripped_line.startswith(("if ", "for ", "while ", "switch ", "catch ")):
        return False
    if _TS_METHOD_RE.match(stripped_line) and ("{" in stripped_line or stripped_line.endswith("(")):
        return True
    return False


def _is_semantic_call(name: str) -> bool:
    if name in {"setInterval", "clearInterval"}:
        return True
    if "." not in name:
        return False
    parts = name.split(".")
    member = parts[-1]
    member_lower = member.lower()
    owner_lower = parts[-2].lower() if len(parts) > 1 else ""
    root_lower = parts[0].lower()
    if name in _LOW_VALUE_LOGGING_CALLS:
        return True
    if member_lower in _SEMANTIC_CALL_MEMBERS:
        return True
    if member.startswith(_SEMANTIC_CALL_MEMBER_PREFIXES):
        return True
    if root_lower.endswith(("service", "manager")) and member.startswith("get"):
        return True
    if owner_lower.endswith(("service", "manager", "timers")) and member.startswith("get"):
        return True
    return False


def _extract_observables(calls: list[str]) -> list[str]:
    return [
        call
        for call in calls
        if not call.endswith(".values") and call not in _LOW_VALUE_LOGGING_CALLS
    ]


def _extract_telemetry(calls: list[str]) -> list[str]:
    return [call for call in calls if call in _LOW_VALUE_LOGGING_CALLS]


def _extract_test_seams(calls: list[str], observables: list[str]) -> list[dict]:
    grouped: dict[tuple[str, str], set[str]] = {}
    standalone: dict[tuple[str, str], None] = {}

    seam_candidates = _filter_test_seam_candidates(calls, observables)
    for name in seam_candidates:
        if "." not in name:
            standalone[("function", name)] = None
            continue

        parts = name.split(".")
        if parts[0] == "this":
            if len(parts) == 2:
                standalone[("instance_method", name)] = None
                continue
            seam_name = ".".join(parts[:2])
            grouped.setdefault(("instance_object", seam_name), set()).add(parts[-1])
            continue

        seam_name = parts[0]
        grouped.setdefault(("module_object", seam_name), set()).add(parts[-1])

    seams = []
    for (kind, name), members in grouped.items():
        seams.append(
            {
                "kind": kind,
                "name": name,
                "members": sorted(members),
            }
        )
    for kind, name in standalone:
        seams.append(
            {
                "kind": kind,
                "name": name,
                "members": [],
            }
        )
    seams.sort(key=lambda item: item["name"])
    return seams


def _filter_test_seam_candidates(calls: list[str], observables: list[str]) -> list[str]:
    unique = sorted(set(calls + observables))
    high_value = [name for name in unique if name not in _LOW_VALUE_LOGGING_CALLS]
    return high_value or unique


def _infer_domains(
    *,
    source_path: str,
    symbol: str,
    entrypoints: list[dict],
    routes: list[dict],
    triggers: list[str],
    observables: list[str],
) -> list[str]:
    domains = set()
    path_parts = {
        part.lower()
        for part in PurePosixPath(source_path).parts[:-1]
        if part and part.lower() not in _PATH_DOMAIN_EXCLUDES
    }
    domains.update(path_parts)

    event_names = {item["name"] for item in entrypoints}
    for event_name in event_names:
        event_domain = _event_domain(event_name)
        if event_domain:
            domains.add(event_domain)

    route_triggers = {trigger for route in routes for trigger in route["triggers"]}
    if route_triggers:
        domains.add("routing")
    if any(trigger.startswith("@") for trigger in route_triggers):
        domains.add("routing.mention")
    if any(trigger.startswith("!") for trigger in route_triggers):
        domains.add("routing.command")

    domains.update(_feature_domains(symbol=symbol, observables=observables))

    timer_tokens = set(_normalize_tokens(" ".join([symbol, *observables])))
    has_timer_signal = (
        "timer" in timer_tokens
        or "timers" in timer_tokens
        or "setInterval" in observables
        or "clearInterval" in observables
    )
    if has_timer_signal:
        domains.add("timer")
    if has_timer_signal and (
        "setInterval" in observables
        or "clearInterval" in observables
        or any(observable.endswith((".clear", ".set")) for observable in observables)
    ):
        domains.add("timer.management")

    return sorted(domains)


def _event_domain(event_name: str) -> str | None:
    tokens = _normalize_tokens(event_name)
    if not tokens:
        return None
    return "event." + ".".join(tokens)


def _feature_domains(*, symbol: str, observables: list[str]) -> set[str]:
    domains = set()
    candidates = [symbol]
    candidates.extend(observable.split(".")[-1] for observable in observables)
    for candidate in candidates:
        for token in _normalize_tokens(candidate):
            if token in _STOPWORDS or token in _DOMAIN_FEATURE_EXCLUDES:
                continue
            if token.isdigit():
                continue
            domains.add(f"feature.{token}")
    return domains


def _classify_issue_shape(issue_tokens: set[str]) -> str | None:
    has_retry_signal = bool(issue_tokens & _API_RETRY_WORD_TOKENS)
    has_api_context = bool(issue_tokens & _API_RETRY_CONTEXT_TOKENS)
    has_rate_limit_signal = bool(issue_tokens & _API_RATE_LIMIT_TOKENS)
    has_status_retry_signal = bool(issue_tokens & _API_RETRY_STATUS_TOKENS) and bool(
        issue_tokens & _API_RETRY_STATUS_CONTEXT_TOKENS
    )
    if has_retry_signal and (has_api_context or has_status_retry_signal):
        return "api_retry"
    if has_api_context and (has_rate_limit_signal or has_status_retry_signal):
        return "api_retry"
    return None


def _has_local_file_reader_signal(candidate_tokens: set[str]) -> bool:
    return bool(candidate_tokens & _LOCAL_FILE_READER_IO_TOKENS) and bool(
        candidate_tokens & _LOCAL_FILE_READER_DATA_TOKENS
    )


def _candidate_token_set(candidate: dict) -> set[str]:
    tokens: set[str] = set()
    for key in (
        "path_tokens",
        "symbol_tokens",
        "string_tokens",
        "terms",
        "domains",
        "calls",
        "observables",
    ):
        for item in candidate.get(key, []):
            tokens.update(_normalize_tokens(str(item)))
    return tokens


def _score_candidate(candidate: dict, issue_tokens: set[str], *, issue_shape: str | None = None) -> int:
    path_overlap = len(issue_tokens & set(candidate["path_tokens"]))
    symbol_overlap = len(issue_tokens & set(candidate["symbol_tokens"]))
    string_overlap = len(issue_tokens & set(candidate["string_tokens"]))
    term_overlap = len(issue_tokens & set(candidate["terms"]))
    score = (path_overlap * 3) + (symbol_overlap * 5) + (string_overlap * 6) + term_overlap

    if issue_shape == "api_retry":
        candidate_tokens = set(candidate.get("rank_tokens") or _candidate_token_set(candidate))
        api_boundary_signal = candidate_tokens & _API_BOUNDARY_TOKENS
        if api_boundary_signal:
            score += min(
                _API_BOUNDARY_BONUS_CAP,
                _API_BOUNDARY_BONUS_BASE + (len(api_boundary_signal) * _API_BOUNDARY_BONUS_PER_TOKEN),
            )
        else:
            score -= _API_NON_BOUNDARY_PENALTY
        if _has_local_file_reader_signal(candidate_tokens) and not api_boundary_signal:
            score -= _LOCAL_FILE_READER_PENALTY

    return score


def _normalize_tokens(text: str) -> list[str]:
    text = _split_camel_case(text)
    text = _canonicalize_text(text)
    raw_tokens = _WORD_RE.findall(text.lower())
    tokens = []
    for token in raw_tokens:
        token = token.strip("@!-_")
        if not token or token in _STOPWORDS:
            continue
        if len(token) == 1 and not token.isdigit():
            continue
        tokens.append(token)
    return tokens


def _split_camel_case(text: str) -> str:
    return re.sub(r"([a-z0-9])([A-Z])", r"\1 \2", text)


def _canonicalize_text(text: str) -> str:
    normalized = unicodedata.normalize("NFKD", text)
    return "".join(ch for ch in normalized if not unicodedata.combining(ch))
