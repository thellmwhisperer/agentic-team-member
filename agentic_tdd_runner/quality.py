"""Quality gate helpers for the agent runner."""

import difflib
import os
import re
import shlex
import subprocess
import textwrap
from collections.abc import Callable
from pathlib import Path, PurePosixPath

import requests


def detect_package_manager(workdir: str, pkg: dict | None = None) -> str:
    """Detect the package manager. Checks packageManager field first, then lockfiles."""
    if pkg:
        pm_field = pkg.get("packageManager", "")
        if pm_field:
            name = pm_field.split("@")[0]
            if name in ("pnpm", "yarn", "bun", "npm"):
                return name
    lockfiles = {
        "pnpm-lock.yaml": "pnpm",
        "yarn.lock": "yarn",
        "bun.lock": "bun",
    }
    for filename, pm in lockfiles.items():
        if os.path.isfile(os.path.join(workdir, filename)):
            return pm
    return "npm"


def package_exec_prefix(package_manager: str) -> str:
    """Return the package-manager command prefix for direct binary execution."""
    return {
        "npm": "npx",
        "pnpm": "pnpm exec",
        "yarn": "yarn",
        "bun": "bunx",
    }.get(package_manager, "npx")


def detect_quality_tools(lang_name: str, workdir: str) -> list[dict]:
    """Detect quality tools from the target project's config files.

    Reads package.json (TypeScript) or pyproject.toml (Python) to discover
    which lint/format/typecheck tools are actually installed.
    """
    checks = []

    if lang_name == "typescript":
        pkg_path = os.path.join(workdir, "package.json")
        if os.path.isfile(pkg_path):
            import json as _json
            try:
                with open(pkg_path) as f:
                    pkg = _json.load(f)
            except (OSError, ValueError):
                return checks

            scripts = pkg.get("scripts", {})
            dev_deps = pkg.get("devDependencies", {})
            deps = pkg.get("dependencies", {})
            all_deps = {**deps, **dev_deps}
            pm = detect_package_manager(workdir, pkg)
            exec_prefix = package_exec_prefix(pm)

            # Typecheck: use scripts.typecheck if defined, else tsc
            if "typecheck" in scripts:
                checks.append({"name": "typecheck", "command": f"{pm} run typecheck"})
            elif "typescript" in all_deps:
                checks.append({"name": "typecheck", "command": f"{exec_prefix} tsc --noEmit"})

            # Lint: biome vs eslint
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

            # Format: biome already covers format, else prettier
            has_biome = any(k.startswith("@biomejs/biome") for k in all_deps)
            if not has_biome and "prettier" in all_deps:
                checks.append({
                    "name": "format",
                    "command": f"{exec_prefix} prettier --check {{changed_files}}",
                    "fix": f"{exec_prefix} prettier --write {{changed_files}}",
                })

    elif lang_name == "python":
        pyproject_path = os.path.join(workdir, "pyproject.toml")
        has_ruff = False
        if os.path.isfile(pyproject_path):
            try:
                with open(pyproject_path, "rb") as f:
                    import tomllib
                    pyproject = tomllib.load(f)
                has_ruff = "ruff" in pyproject.get("tool", {})
            except (OSError, ValueError):
                pass

        if has_ruff:
            checks.append({
                "name": "lint",
                "command": "python3 -m ruff check {changed_files}",
                "fix": "python3 -m ruff check {changed_files} --fix",
            })
            checks.append({
                "name": "format",
                "command": "python3 -m ruff format --check {changed_files}",
                "fix": "python3 -m ruff format {changed_files}",
            })

    return checks


def get_changed_files(workdir: str) -> list[str]:
    """Get modified + staged + untracked files relative to workdir."""
    diff = subprocess.run(
        ["git", "diff", "--name-only"], cwd=workdir, capture_output=True, text=True,
    )
    staged = subprocess.run(
        ["git", "diff", "--cached", "--name-only"], cwd=workdir, capture_output=True, text=True,
    )
    untracked = subprocess.run(
        ["git", "ls-files", "--others", "--exclude-standard"],
        cwd=workdir, capture_output=True, text=True,
    )
    files = set()
    for line in (diff.stdout + staged.stdout + untracked.stdout).splitlines():
        line = line.strip()
        if line and os.path.exists(os.path.join(workdir, line)):
            files.add(line)
    return sorted(files)


def format_duplicated_setup_finding(path: str, report_lines: list[str]) -> str:
    """Format duplicated setup lines without echoing full test setup content."""
    identifiers: list[str] = []
    for line in report_lines:
        identifiers.extend(re.findall(r"\b([a-zA-Z_]\w+)\s*[=(]", line))
    id_list = ", ".join(dict.fromkeys(identifiers)) if identifiers else "shared setup"
    return (
        f"[Duplicated setup] {path}: {len(report_lines)} repeated lines. "
        f"Move to beforeEach (TS) or fixture (Python): {id_list}"
    )


def run_quality_checks(
    test_file: str,
    *,
    workdir: str,
    config: dict,
    log: Callable[[str, dict], None],
    is_test_file_path: Callable[[str], bool],
    detect_quality_tools_fn: Callable[[str], list[dict]] | None = None,
    get_changed_files_fn: Callable[[], list[str]] | None = None,
) -> tuple[bool, str]:
    """Run quality checks on changed files. Returns (passed, message)."""
    from agentic_tdd_runner.languages import get_language

    quality_cfg = config.get("quality", {})
    if not quality_cfg.get("enabled", False):
        return True, "Quality checks disabled"

    lang = get_language(test_file)
    lang_name = lang.name if lang else "typescript"
    lang_cfg = quality_cfg.get(lang_name, {})
    if detect_quality_tools_fn is None:
        detect_quality_tools_fn = lambda name: detect_quality_tools(name, workdir)
    if get_changed_files_fn is None:
        get_changed_files_fn = lambda: get_changed_files(workdir)

    # Auto-detect tools from the project, fall back to config
    checks = detect_quality_tools_fn(lang_name) or lang_cfg.get("checks", [])
    forbidden = lang_cfg.get("forbidden", [])

    all_changed = get_changed_files_fn()
    if not all_changed:
        return True, "No changed files"

    # Filter to files matching the active language's extensions
    extensions = lang.extensions if lang else [".ts", ".tsx", ".js", ".jsx"]
    changed = [f for f in all_changed if os.path.splitext(f)[1] in extensions]
    if not changed:
        return True, "No changed files matching language"

    changed_str = " ".join(shlex.quote(path) for path in changed)
    failures = []

    # Run checks (fix first if available, then verify)
    for check in checks:
        try:
            fix_cmd = check.get("fix", "").replace("{changed_files}", changed_str)
            if fix_cmd:
                subprocess.run(
                    fix_cmd, shell=True, cwd=workdir, capture_output=True,
                    timeout=config["timeouts"]["tool_execution"],
                )
            cmd = check["command"].replace("{changed_files}", changed_str)
            result = subprocess.run(
                cmd, shell=True, cwd=workdir, capture_output=True, text=True,
                timeout=config["timeouts"]["tool_execution"],
            )
            if result.returncode != 0:
                raw = (result.stdout + result.stderr).strip()
                lines = [ln for ln in raw.splitlines() if ln.strip()]
                n_errors = sum(1 for ln in lines if "error" in ln.lower())
                sample = "\n".join(f"  {ln}" for ln in lines[:30])
                if not sample:
                    sample = f"  {raw[:500]}"
                ownership_hint = typecheck_ownership_hint(check["name"], raw, changed)
                if ownership_hint:
                    sample = f"{sample}\n  {ownership_hint}"
                failures.append(f"[{check['name']}] {n_errors} errors:\n{sample}")
        except subprocess.TimeoutExpired:
            failures.append(f"[{check['name']}] TIMEOUT: command timed out")
        except (OSError, UnicodeDecodeError) as e:
            failures.append(f"[{check['name']}] ERROR: {e}")

    # Grep forbidden patterns in changed files - group by file
    forbidden_by_file: dict[str, list[str]] = {}
    for f in changed:
        full = os.path.join(workdir, f)
        if not os.path.isfile(full):
            continue
        try:
            with open(full, errors="replace") as fh:
                content = fh.read()
        except OSError:
            continue
        for pattern in forbidden:
            for i, line in enumerate(content.splitlines(), 1):
                if pattern in line:
                    forbidden_by_file.setdefault(f, []).append(f"  {f}:{i} '{pattern}'")
    for f, hits in forbidden_by_file.items():
        sample = "\n".join(hits[:3])
        n = len(hits)
        failures.append(f"[Forbidden] {f}: {n} forbidden patterns\n{sample}")

    # Detect duplicated setup lines in test files - report identifiers, not full lines
    test_files = [f for f in changed if is_test_file_path(f)]
    for f in test_files:
        full = os.path.join(workdir, f)
        if not os.path.isfile(full):
            continue
        try:
            with open(full, errors="replace") as fh:
                file_text = fh.read()
        except OSError:
            continue
        setup_dupes, ambiguous_dupes = partition_duplicated_test_lines(file_text)
        report_lines = list(setup_dupes)
        if not report_lines and ambiguous_dupes:
            judge_result = judge_duplicated_setup(
                full, file_text, ambiguous_dupes, config=config, log=log,
            )
            if judge_result is True:
                report_lines = list(ambiguous_dupes)
        if report_lines:
            failures.append(format_duplicated_setup_finding(f, report_lines))

    for finding in detect_side_effect_shape_changes(changed, workdir, is_test_file_path):
        failures.append(finding)

    for finding in detect_parsed_metadata_without_original_fallback(
        changed, workdir, is_test_file_path,
    ):
        failures.append(finding)

    for finding in detect_empty_object_type_assertions(changed, workdir):
        failures.append(finding)

    if failures:
        details = "\n\n".join(failures)
        template = config.get("prompt", {}).get(
            "quality_failed", "QUALITY CHECK FAILED:\n\n{details}",
        )
        return False, template.replace("{details}", details)

    return True, "All quality checks passed"


def is_obvious_assert_line(line: str) -> bool:
    stripped = line.strip()
    if re.search(r"\bexpect\s*\(", stripped):
        return True
    if re.match(r"^assert\b", stripped):
        return True
    if re.search(
        r"\b(?:toHaveBeenCalledWith|toHaveBeenCalled|toEqual|toBe|toContain|toMatch|toStrictEqual|toBeTruthy|toBeFalsy)\s*\(",
        stripped,
    ):
        return True
    return False


def is_obvious_setup_line(line: str) -> bool:
    stripped = line.strip()
    if re.search(r"\b(mock|spyOn)\s*\(", stripped):
        return True
    if re.search(r"\b(?:vi|jest)\.(?:fn|mock)\s*\(", stripped):
        return True
    if re.search(r"__set[A-Za-z_]\w*\s*\(", stripped):
        return True
    if re.match(r"^(?:const|let|var)\s+\w*(?:spy|mock|stub|double|fixture)\w*\s*=", stripped, re.IGNORECASE):
        return True
    return False


def is_obvious_act_line(line: str) -> bool:
    stripped = line.strip()
    if is_obvious_assert_line(stripped) or is_obvious_setup_line(stripped):
        return False
    if re.match(r"^(?:await\s+)?[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*\s*\(", stripped):
        return True
    if re.match(
        r"^(?:const|let|var)\s+\w+\s*=\s*(?:await\s+)?[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*\s*\(",
        stripped,
    ):
        return True
    return False


def partition_duplicated_test_lines(file_text: str) -> tuple[list[str], list[str]]:
    from collections import Counter

    lines = [ln.strip() for ln in file_text.splitlines() if ln.strip() and len(ln.strip()) > 20]
    counts = Counter(lines)

    setup_dupes: list[str] = []
    ambiguous_dupes: list[str] = []
    for line, count in counts.items():
        if count < 2:
            continue
        if is_obvious_assert_line(line) or is_obvious_act_line(line):
            continue
        if is_obvious_setup_line(line):
            setup_dupes.append(line)
        elif count >= 3:
            ambiguous_dupes.append(line)
    return setup_dupes, ambiguous_dupes


def typecheck_ownership_hint(check_name: str, raw_output: str, changed_files: list[str]) -> str | None:
    if check_name != "typecheck" or not raw_output:
        return None
    changed_set = {PurePosixPath(path).as_posix() for path in changed_files}
    changed_name_counts: dict[str, int] = {}
    for changed_file in changed_files:
        name = PurePosixPath(changed_file).name
        changed_name_counts[name] = changed_name_counts.get(name, 0) + 1
    for match in re.finditer(r"([^\s:(]+?\.(?:tsx?|jsx?|py))\((\d+),(\d+)\):\s*error\b", raw_output):
        emitted = PurePosixPath(match.group(1))
        path = emitted.as_posix()
        if path in changed_set:
            return (
                "Ownership: this typecheck error is in a changed file, so it belongs "
                "to this fix. Do not classify it as unrelated."
            )
        if emitted.parent == PurePosixPath(".") and changed_name_counts.get(emitted.name) == 1:
            return (
                "Ownership: this typecheck error is in a changed file, so it belongs "
                "to this fix. Do not classify it as unrelated."
            )
    return None


_SIDE_EFFECT_CALLEE_MARKERS = (
    "analytics",
    "dispatch",
    "emit",
    "event",
    "log",
    "logger",
    "metric",
    "notify",
    "publish",
    "record",
    "report",
    "send",
    "telemetry",
    "track",
)


def extract_side_effect_call(line: str) -> tuple[str, tuple[str, ...]] | None:
    callee_matches = list(re.finditer(r"\b([A-Za-z_$]\w*(?:\.[A-Za-z_$]\w*)*)\s*\(", line))
    if not callee_matches:
        return None
    object_match = re.search(r"\{([^{}]+)\}", line)
    if not object_match:
        return None
    keys = extract_object_literal_keys(object_match.group(1))
    if not keys:
        return None

    for match in callee_matches:
        callee = match.group(1)
        lowered = callee.lower()
        parts = re.split(r"[._]", lowered)
        if any(marker in lowered or marker in parts for marker in _SIDE_EFFECT_CALLEE_MARKERS):
            return callee, keys
    return None


def extract_object_literal_keys(body: str) -> tuple[str, ...]:
    keys: list[str] = []
    for raw_part in body.split(","):
        part = raw_part.strip()
        if not part or part.startswith("..."):
            continue
        if ":" in part:
            key = part.split(":", 1)[0].strip()
        else:
            match = re.match(r"([A-Za-z_$]\w*)\b", part)
            if not match:
                continue
            key = match.group(1)
        key = key.strip("'\"")
        if re.match(r"^[A-Za-z_$]\w*$", key):
            keys.append(key)
    return tuple(dict.fromkeys(keys))


def detect_side_effect_shape_changes(
    changed_files: list[str],
    workdir: str,
    is_test_file_path: Callable[[str], bool],
) -> list[str]:
    findings: list[str] = []
    for file_path in changed_files:
        suffix = Path(file_path).suffix.lower()
        if suffix not in {".ts", ".tsx", ".js", ".jsx"}:
            continue
        if is_test_file_path(file_path):
            continue

        before_result = subprocess.run(
            ["git", "show", f"HEAD:{file_path}"],
            cwd=workdir,
            capture_output=True,
            text=True,
        )
        if before_result.returncode != 0:
            continue
        full_path = Path(workdir) / file_path
        try:
            after_text = full_path.read_text(errors="replace")
        except OSError:
            continue

        before_lines = before_result.stdout.splitlines()
        after_lines = after_text.splitlines()
        matcher = difflib.SequenceMatcher(a=before_lines, b=after_lines, autojunk=False)
        for tag, old_start, old_end, new_start, new_end in matcher.get_opcodes():
            if tag == "equal":
                continue
            removed = before_lines[old_start:old_end]
            added = after_lines[new_start:new_end]
            for old_line in removed:
                old_call = extract_side_effect_call(old_line)
                if not old_call:
                    continue
                old_callee, old_keys = old_call
                for offset, new_line in enumerate(added):
                    new_call = extract_side_effect_call(new_line)
                    if not new_call:
                        continue
                    new_callee, new_keys = new_call
                    if old_callee != new_callee or set(old_keys) == set(new_keys):
                        continue
                    old_display = ", ".join(old_keys)
                    new_display = ", ".join(new_keys)
                    line_no = new_start + offset + 1
                    findings.append(
                        f"[Side-effect shape] {file_path}:{line_no} {old_callee} object keys "
                        f"changed from {{{old_display}}} to {{{new_display}}}. Preserve existing "
                        "payload keys for logging/tracking/events unless the issue explicitly "
                        "requires a public contract change; use `oldKey: newValue` when only the "
                        "value changed."
                    )
                    break
    return findings


_FALLBACK_VALUE_NAME_RE = re.compile(
    r"(?:count|id|index|level|limit|month|number|price|qty|quantity|score|size|status|total|value)",
    re.IGNORECASE,
)


def find_matching_brace(text: str, open_index: int) -> int:
    depth = 0
    quote: str | None = None
    escaped = False
    for idx in range(open_index, len(text)):
        ch = text[idx]
        if escaped:
            escaped = False
            continue
        if ch == "\\" and quote:
            escaped = True
            continue
        if quote:
            if ch == quote:
                quote = None
            continue
        if ch in {"'", '"', "`"}:
            quote = ch
            continue
        if ch == "{":
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth == 0:
                return idx
    return -1


def extract_param_names(params_text: str) -> list[str]:
    names: list[str] = []
    for raw_param in params_text.split(","):
        param = raw_param.strip()
        if not param:
            continue
        if param.startswith("..."):
            param = param[3:].strip()
        name = param.split(":", 1)[0].split("=", 1)[0].strip()
        if name and re.match(r"^_?[A-Za-z_$]\w*$", name):
            names.append(name)
    return names


def iter_function_bodies(text: str):
    pattern = re.compile(
        r"(?:export\s+)?function\s+[A-Za-z_$]\w*\s*\((?P<params>.*?)\)"
        r"\s*(?::\s*[^{]+)?\{",
        re.DOTALL,
    )
    for match in pattern.finditer(text):
        open_index = match.end() - 1
        close_index = find_matching_brace(text, open_index)
        if close_index == -1:
            continue
        yield (
            match,
            extract_param_names(match.group("params")),
            text[open_index + 1:close_index],
        )


def body_parses_external_metadata(body: str) -> bool:
    return bool(
        re.search(
            r"\b(?:Number|parseInt)\s*\([\s\S]{0,240}\[[^\]]*['\"][^'\"]+['\"][^\]]*\]",
            body,
        )
    )


def body_has_invalid_metadata_fallback(body: str, original_param: str) -> bool:
    escaped = re.escape(original_param)
    has_nan_or_finite_guard = re.search(
        r"\b(?:Number\.isNaN|Number\.isFinite|isNaN)\s*\(",
        body,
    )
    if has_nan_or_finite_guard and re.search(rf"\b{escaped}\b", body):
        return True

    return False


def detect_parsed_metadata_without_original_fallback(
    changed_files: list[str],
    workdir: str,
    is_test_file_path: Callable[[str], bool],
) -> list[str]:
    """Flag parsed bracket metadata that ignores the original value-like callback arg."""
    findings: list[str] = []
    for file_path in changed_files:
        suffix = Path(file_path).suffix.lower()
        if suffix not in {".ts", ".tsx", ".js", ".jsx"}:
            continue
        if is_test_file_path(file_path):
            continue
        full_path = Path(workdir) / file_path
        try:
            text = full_path.read_text(errors="replace")
        except OSError:
            continue
        for match, params, body in iter_function_bodies(text):
            if not body_parses_external_metadata(body):
                continue
            value_params = [
                name for name in params
                if name.startswith("_") and _FALLBACK_VALUE_NAME_RE.search(name.lstrip("_"))
            ]
            ignored_original = False
            for name in value_params:
                if re.search(rf"\b{re.escape(name)}\b", body):
                    continue
                line_no = text.count("\n", 0, match.start()) + 1
                findings.append(
                    f"[Metadata fallback] {file_path}:{line_no} parses external metadata "
                    f"while ignoring original value-like parameter `{name}`. Preserve the "
                    "original callback/input value as the fallback when parsed metadata is "
                    "missing or invalid, unless the issue explicitly requires a new default."
                )
                ignored_original = True
                break
            if ignored_original:
                continue
            for name in value_params:
                if not re.search(rf"\b{re.escape(name)}\b", body):
                    continue
                if body_has_invalid_metadata_fallback(body, name):
                    continue
                line_no = text.count("\n", 0, match.start()) + 1
                findings.append(
                    f"[Metadata fallback] {file_path}:{line_no} parses external metadata "
                    f"and mentions `{name}`, but does not prove that invalid parsed values "
                    "fall back to the original callback/input value. Apply the fallback "
                    "after parsing, for example with an explicit NaN/finite guard, unless "
                    "the issue explicitly requires a new default."
                )
                break
    return findings


def detect_empty_object_type_assertions(changed_files: list[str], workdir: str) -> list[str]:
    """Flag empty object casts that paper over missing test or callback payload fields."""
    findings: list[str] = []
    assertion_re = re.compile(r"\{\s*\}\s+as\s+([A-Z][A-Za-z0-9_$]*(?:<[^;\n]+>)?)")
    for file_path in changed_files:
        suffix = Path(file_path).suffix.lower()
        if suffix not in {".ts", ".tsx"}:
            continue
        full_path = Path(workdir) / file_path
        try:
            text = full_path.read_text(errors="replace")
        except OSError:
            continue
        for match in assertion_re.finditer(text):
            type_name = match.group(1).strip()
            line_no = text.count("\n", 0, match.start()) + 1
            findings.append(
                f"[Type assertion] {file_path}:{line_no} uses an empty object cast "
                f"`{{}} as {type_name}`. Build a structural test double or declare a "
                f"typed value such as `const value: {type_name} = {{...}}` so TypeScript "
                "checks the payload instead of hiding missing fields."
            )
    return findings


def build_duplicated_setup_judge_prompt(file_path: str, file_text: str, duplicated_lines: list[str]) -> str:
    suffix = Path(file_path).suffix.lower()
    fence = "py" if suffix == ".py" else "ts"
    repeated_lines = "\n".join(
        f"{idx}. {line}" for idx, line in enumerate(duplicated_lines, start=1)
    )
    return textwrap.dedent(
        f"""\
        You are a strict binary classifier for duplicated test setup.

        Task:
        Decide whether the repeated lines below are SHARED SETUP that should move to beforeEach/fixture.

        Answer YES only if the repeated lines are setup code shared across tests, such as:
        - creating spies, mocks, fakes, or test doubles
        - calling seam setters like __setXForTests(...)
        - repeated object construction for fixtures
        - repeated arrange-only initialization with no assertion

        Answer NO if the repeated lines are legitimate per-test ACT or ASSERT, such as:
        - calling the function under test
        - expect(...)
        - assertions on spy calls
        - per-test inputs or expected outputs
        - lines whose meaning depends on the specific test case

        Rules:
        - Repeated ACT is NOT duplicated setup.
        - Repeated ASSERT is NOT duplicated setup.
        - Never answer YES because of expect(...), matcher chains like toHaveBeenCalledWith(...), or the direct call to the function under test.
        - If repeated lines mix setup with ACT/ASSERT, ignore the ACT/ASSERT lines and judge only the remaining setup candidates.
        - If unsure, answer NO.
        - Return exactly one word: YES or NO.

        Example 1
        Repeated lines:
        - const spy = mock(() => {{}});
        - __setClientForTests(client_test_double);
        Answer: YES

        Example 2
        Repeated lines:
        - handleResub(channel, username, streakMonths, message, userstate);
        - expect(client_say_spy).toHaveBeenCalledWith(channel, expected_message);
        Answer: NO

        Test file:
        ```{fence}
        {file_text}
        ```

        Repeated lines to classify:
        {repeated_lines}
        """
    ).strip()


def judge_duplicated_setup(
    file_path: str,
    file_text: str,
    duplicated_lines: list[str],
    *,
    config: dict,
    log: Callable[[str, dict], None],
) -> bool | None:
    """Return True/False from the small judge, or None when unavailable."""
    judge_cfg = config.get("quality", {}).get("duplicated_setup_judge", {})
    if not judge_cfg.get("enabled", False):
        return None

    prompt = build_duplicated_setup_judge_prompt(file_path, file_text, duplicated_lines)
    payload = {
        "model": judge_cfg.get("model", "qwen3.5:0.8b"),
        "messages": [{"role": "user", "content": prompt}],
        "stream": False,
        "think": judge_cfg.get("think", False),
        "options": {
            "temperature": judge_cfg.get("temperature", 0),
            "num_ctx": judge_cfg.get("num_ctx", 4096),
        },
    }
    timeout_s = judge_cfg.get("timeout", 10)
    url = judge_cfg.get("url", "http://127.0.0.1:11434/api/chat")

    try:
        response = requests.post(url, json=payload, timeout=timeout_s)
        response.raise_for_status()
        content = response.json().get("message", {}).get("content", "").strip().upper()
    except Exception as exc:
        log("duplicated_setup_judge_error", {"file": file_path, "error": str(exc)})
        return None

    if content == "YES":
        log("duplicated_setup_judge", {"file": file_path, "decision": "YES"})
        return True
    if content == "NO":
        log("duplicated_setup_judge", {"file": file_path, "decision": "NO"})
        return False

    log("duplicated_setup_judge_error", {
        "file": file_path, "error": f"unexpected response: {content[:50]}",
    })
    return None
