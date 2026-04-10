#!/usr/bin/env python3
"""Agentic TDD runner — local LLM fixes bugs with tests."""

import argparse
import json
import os
import re
import shlex
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

import requests

# --- Load .env if present ---
_env_path = Path.cwd() / ".env"
if _env_path.exists():
    for line in _env_path.read_text().splitlines():
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            key, _, value = line.partition("=")
            os.environ.setdefault(key.strip(), value.strip())

# --- Force unbuffered stdout ---
sys.stdout.reconfigure(line_buffering=True)
sys.stderr.reconfigure(line_buffering=True)

# --- Config (loaded from TOML, overridden by env vars / CLI args) ---
_CONFIG = None  # populated by main()
WORKDIR = os.environ.get("AGENT_WORKDIR", os.getcwd())
LOG_DIR = os.environ.get("AGENT_LOG_DIR", os.getcwd())

# --- Logging ---
_log_file = None

def init_log():
    global _log_file
    ts = datetime.now().strftime("%Y%m%d-%H%M%S")
    path = os.path.join(LOG_DIR, f"agent-{ts}.jsonl")
    os.makedirs(LOG_DIR, exist_ok=True)
    _log_file = open(path, "w")
    log("init", {"workdir": WORKDIR, "max_steps": _CONFIG["agent"]["max_steps"], "model": _CONFIG["llm"]["model"], "log": path})
    emit(f"LOG: {path}")
    return path

def log(event: str, data: dict):
    entry = {
        "ts": datetime.now(timezone.utc).isoformat(),
        "event": event,
        **data,
    }
    if _log_file:
        _log_file.write(json.dumps(entry, ensure_ascii=False) + "\n")
        _log_file.flush()

def emit(msg: str):
    print(msg, flush=True)


# Tools and prompts loaded from config/agent.toml + config/tools.json


def _resolve_repo_path(path: str, workdir: str = None) -> Path:
    """Resolve a relative path within the workdir. Raises if it escapes."""
    repo_root = Path(workdir or WORKDIR).resolve()
    candidate = (repo_root / path).resolve()
    try:
        candidate.relative_to(repo_root)
    except ValueError as exc:
        raise ValueError(f"path escapes workdir: {path}") from exc
    return candidate


_ALLOWED_COMMANDS = frozenset({
    "git", "grep", "rg", "find", "ls", "cat", "head", "tail", "wc",
    "bun", "node", "npm", "npx", "pnpm", "yarn", "deno",
    "python", "python3", "pip", "pip3", "pytest",
    "echo", "sort", "uniq", "diff", "tr", "cut", "tee",
    "sed", "awk", "xargs", "dirname", "basename",
    "tree", "file", "which", "true", "false", "test",
})

# Flags that allow arbitrary code execution on otherwise safe binaries
_BLOCKED_FLAGS = {
    "python": {"-c"},
    "python3": {"-c"},
    "node": {"-e", "--eval"},
    "deno": {"eval"},
}


def _validate_command(command: str) -> None:
    """Validate that all commands in a pipeline/chain use allowed binaries."""
    import shlex
    if not command or not command.strip():
        raise ValueError("empty command")
    # Reject newlines — they bypass shell operator splitting
    if "\n" in command:
        raise ValueError("newlines not allowed in commands")
    # Split on shell operators to validate each sub-command
    parts = re.split(r"\s*(?:\|\||&&|[|;])\s*", command)
    for part in parts:
        part = part.strip()
        if not part:
            continue
        try:
            tokens = shlex.split(part)
        except ValueError:
            tokens = part.split()
        if not tokens:
            continue
        binary = os.path.basename(tokens[0])
        if binary not in _ALLOWED_COMMANDS:
            raise ValueError(
                f"command '{binary}' is not allowed. "
                f"Allowed: {', '.join(sorted(_ALLOWED_COMMANDS))}"
            )
        # Block dangerous flag combinations
        blocked = _BLOCKED_FLAGS.get(binary, set())
        if blocked:
            for token in tokens[1:]:
                if token in blocked:
                    raise ValueError(
                        f"flag '{token}' not allowed with '{binary}'"
                    )
        # Block env as a wrapper to run arbitrary binaries
        if binary == "env":
            # env VAR=val cmd or env cmd — validate the actual command too
            for token in tokens[1:]:
                if "=" in token:
                    continue  # env var assignment
                # This is the actual binary being wrapped
                wrapped = os.path.basename(token)
                if wrapped not in _ALLOWED_COMMANDS:
                    raise ValueError(
                        f"command '{wrapped}' (via env) is not allowed"
                    )
                # Check blocked flags for the wrapped binary too
                wrapped_blocked = _BLOCKED_FLAGS.get(wrapped, set())
                remaining = tokens[tokens.index(token) + 1:]
                for flag in remaining:
                    if flag in wrapped_blocked:
                        raise ValueError(
                            f"flag '{flag}' not allowed with '{wrapped}' (via env)"
                        )
                break


def execute_tool(name: str, args: dict) -> str:
    try:
        if name == "read_file":
            full_path = _resolve_repo_path(args["path"])
            if os.path.isdir(full_path):
                entries = os.listdir(full_path)
                return "\n".join(sorted(entries))
            with open(full_path, "r") as f:
                return f.read()

        elif name == "run_command":
            _validate_command(args["command"])
            result = subprocess.run(
                args["command"],
                shell=True,
                cwd=WORKDIR,
                capture_output=True,
                text=True,
                timeout=_CONFIG["timeouts"]["tool_execution"],
            )
            output = result.stdout + result.stderr
            return output if output.strip() else "(no output)"

        elif name == "str_replace_editor":
            full_path = _resolve_repo_path(args["path"])
            with open(full_path, "r") as f:
                content = f.read()
            old_str = args["old_str"]
            if old_str not in content:
                return f"ERROR: old_str not found in {args['path']}. Read the file first to get the exact text."
            if content.count(old_str) > 1:
                return f"ERROR: old_str appears {content.count(old_str)} times. Make it more specific."
            new_content = content.replace(old_str, args["new_str"], 1)
            with open(full_path, "w") as f:
                f.write(new_content)
            return f"OK: replaced in {args['path']}"

        elif name == "create_file":
            full_path = _resolve_repo_path(args["path"])
            if os.path.exists(full_path):
                return f"ERROR: {args['path']} already exists. Use str_replace_editor to modify it."
            os.makedirs(os.path.dirname(full_path), exist_ok=True)
            with open(full_path, "w") as f:
                f.write(args["content"])
            return f"OK: created {args['path']}"

        else:
            return f"ERROR: unknown tool {name}"

    except Exception as e:
        return f"ERROR: {type(e).__name__}: {e}"


def truncate(text: str, max_chars: int = 0) -> str:
    if max_chars == 0:
        max_chars = _CONFIG["agent"]["max_tool_output"]
    if len(text) <= max_chars:
        return text
    half = max_chars // 2
    return text[:half] + f"\n\n... ({len(text) - max_chars} chars truncated) ...\n\n" + text[-half:]


def chat(messages: list) -> dict:
    llm = _CONFIG["llm"]
    payload = {
        "model": llm["model"],
        "messages": messages,
        "tools": _CONFIG["tools"],
        "temperature": llm.get("temperature", 0.6),
        "top_p": llm.get("top_p", 0.95),
        "top_k": llm.get("top_k", 20),
        "cache_prompt": True,
    }
    resp = requests.post(llm["url"], json=payload, timeout=_CONFIG["timeouts"]["llm_request"])
    resp.raise_for_status()
    return resp.json()


def find_test_file(hint: str | None = None) -> str | None:
    """Find the test file the agent created. Uses hint from cookbook if available."""
    import fnmatch
    if hint:
        hinted = os.path.join(WORKDIR, hint)
        if os.path.isfile(hinted):
            return hint
    patterns = _CONFIG["runner"]["test_file_patterns"]
    exclude = _CONFIG["runner"].get("exclude_dirs", [])
    for root, _dirs, files in os.walk(WORKDIR):
        if any(ex in root for ex in exclude):
            continue
        for f in files:
            if any(fnmatch.fnmatch(f, p) for p in patterns):
                full = os.path.join(root, f)
                rel = os.path.relpath(full, WORKDIR)
                result = subprocess.run(
                    ["git", "ls-files", "--", rel],
                    cwd=WORKDIR, capture_output=True, text=True
                )
                if not result.stdout.strip():
                    return rel
    return None


def verify_red_green(test_file: str) -> tuple[bool, str]:
    """Verify red-green: test fails without fix, passes with fix.

    1. Stash current changes (with fix)
    2. Run test — should FAIL (red)
    3. Restore fix from stash
    4. Run test — should PASS (green)
    """
    from agentic_tdd_runner.languages import get_language
    lang = get_language(test_file)
    if lang and lang.runner == "pytest":
        run_cmd = "python3 -m pytest"
    else:
        run_cmd = _CONFIG["runner"]["command"]
    test_timeout = _CONFIG["timeouts"]["test_run"]

    emit("\n=== RED-GREEN VERIFICATION ===")

    # Preserve the test file (may be untracked) before stashing
    import shutil
    import tempfile
    test_full = os.path.join(WORKDIR, test_file)
    test_backup = None
    if os.path.isfile(test_full):
        test_backup = tempfile.mktemp(suffix=os.path.basename(test_file))
        shutil.copy2(test_full, test_backup)

    # Stash all changes including untracked (reverts source fix + new files)
    subprocess.run(["git", "stash", "--include-untracked"], cwd=WORKDIR, capture_output=True)

    run_argv = shlex.split(run_cmd)

    red_passed = False
    red_output = ""
    green_passed = False
    green_output = ""

    try:
        # Restore the test file so the red phase can run it
        if test_backup:
            os.makedirs(os.path.dirname(test_full), exist_ok=True)
            shutil.copy2(test_backup, test_full)

        emit("  [RED] Running test WITHOUT fix...")
        red_result = subprocess.run(
            [*run_argv, test_file],
            cwd=WORKDIR, capture_output=True, text=True, timeout=test_timeout,
        )
        red_passed = red_result.returncode == 0
        red_output = (red_result.stdout + red_result.stderr)[:500]
        emit(f"  [RED] exit={red_result.returncode} {'PASS (BAD!)' if red_passed else 'FAIL (good)'}")
        for line in red_output.split("\n")[:10]:
            emit(f"    {line}")
    finally:
        # Always clean up temp files and pop stash, even after exceptions
        if test_backup:
            if os.path.isfile(test_full):
                os.remove(test_full)
            if os.path.isfile(test_backup):
                os.remove(test_backup)
        subprocess.run(["git", "stash", "pop"], cwd=WORKDIR, capture_output=True)

    emit("  [GREEN] Running test WITH fix...")
    green_result = subprocess.run(
        [*run_argv, test_file],
        cwd=WORKDIR, capture_output=True, text=True, timeout=test_timeout,
    )
    green_passed = green_result.returncode == 0
    green_output = (green_result.stdout + green_result.stderr)[:500]
    emit(f"  [GREEN] exit={green_result.returncode} {'PASS (good)' if green_passed else 'FAIL (BAD!)'}")
    for line in green_output.split("\n")[:10]:
        emit(f"    {line}")

    log("verify", {
        "test_file": test_file,
        "red_passed": red_passed,
        "green_passed": green_passed,
        "red_output": red_output,
        "green_output": green_output,
    })

    if red_passed:
        return False, (
            f"REJECTED: Your test ({test_file}) passes even WITHOUT your source fix. "
            f"This means it doesn't test the real code — it probably uses local stub functions "
            f"instead of importing from the source. Rewrite the test to import the real "
            f"function and mock its dependencies properly."
        )

    if not green_passed:
        return False, (
            f"REJECTED: Your test ({test_file}) fails even WITH your source fix. "
            f"The test has errors. Fix them. Error: {green_output[:300]}"
        )

    return True, "VERIFIED: Test fails without fix, passes with fix. Real red-green."


def _detect_package_manager(pkg: dict | None = None) -> str:
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
        if os.path.isfile(os.path.join(WORKDIR, filename)):
            return pm
    return "npm"


def detect_quality_tools(lang_name: str) -> list[dict]:
    """Detect quality tools from the target project's config files.

    Reads package.json (TypeScript) or pyproject.toml (Python) to discover
    which lint/format/typecheck tools are actually installed.
    """
    checks = []

    if lang_name == "typescript":
        pkg_path = os.path.join(WORKDIR, "package.json")
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

            # Typecheck: use scripts.typecheck if defined, else tsc
            if "typecheck" in scripts:
                pm = _detect_package_manager(pkg)
                checks.append({"name": "typecheck", "command": f"{pm} run typecheck"})
            elif "typescript" in all_deps:
                checks.append({"name": "typecheck", "command": "npx tsc --noEmit"})

            # Lint: biome vs eslint
            if any(k.startswith("@biomejs/biome") for k in all_deps):
                checks.append({
                    "name": "lint",
                    "command": "npx biome check {changed_files}",
                    "fix": "npx biome check {changed_files} --fix",
                })
            elif "eslint" in all_deps:
                checks.append({
                    "name": "lint",
                    "command": "npx eslint {changed_files}",
                    "fix": "npx eslint {changed_files} --fix",
                })

            # Format: biome already covers format, else prettier
            has_biome = any(k.startswith("@biomejs/biome") for k in all_deps)
            if not has_biome and "prettier" in all_deps:
                checks.append({
                    "name": "format",
                    "command": "npx prettier --check {changed_files}",
                    "fix": "npx prettier --write {changed_files}",
                })

    elif lang_name == "python":
        pyproject_path = os.path.join(WORKDIR, "pyproject.toml")
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


def _get_changed_files() -> list[str]:
    """Get modified + untracked files relative to WORKDIR."""
    diff = subprocess.run(
        ["git", "diff", "--name-only"], cwd=WORKDIR, capture_output=True, text=True,
    )
    untracked = subprocess.run(
        ["git", "ls-files", "--others", "--exclude-standard"],
        cwd=WORKDIR, capture_output=True, text=True,
    )
    files = set()
    for line in (diff.stdout + untracked.stdout).splitlines():
        line = line.strip()
        if line and os.path.exists(os.path.join(WORKDIR, line)):
            files.add(line)
    return sorted(files)


def run_quality_checks(test_file: str) -> tuple[bool, str]:
    """Run quality checks on changed files. Returns (passed, message)."""
    from agentic_tdd_runner.languages import get_language

    quality_cfg = _CONFIG.get("quality", {})
    if not quality_cfg.get("enabled", False):
        return True, "Quality checks disabled"

    lang = get_language(test_file)
    lang_name = lang.name if lang else "typescript"
    lang_cfg = quality_cfg.get(lang_name, {})
    # Auto-detect tools from the project, fall back to config
    checks = detect_quality_tools(lang_name) or lang_cfg.get("checks", [])
    forbidden = lang_cfg.get("forbidden", [])

    all_changed = _get_changed_files()
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
        fix_cmd = check.get("fix", "").replace("{changed_files}", changed_str)
        if fix_cmd:
            subprocess.run(
                fix_cmd, shell=True, cwd=WORKDIR, capture_output=True,
                timeout=_CONFIG["timeouts"]["tool_execution"],
            )
        cmd = check["command"].replace("{changed_files}", changed_str)
        result = subprocess.run(
            cmd, shell=True, cwd=WORKDIR, capture_output=True, text=True,
            timeout=_CONFIG["timeouts"]["tool_execution"],
        )
        if result.returncode != 0:
            output = (result.stdout + result.stderr)[:500]
            failures.append(f"[{check['name']}] FAILED:\n{output}")

    # Grep forbidden patterns in changed files
    for f in changed:
        full = os.path.join(WORKDIR, f)
        if not os.path.isfile(full):
            continue
        try:
            with open(full) as fh:
                content = fh.read()
        except OSError:
            continue
        for pattern in forbidden:
            for i, line in enumerate(content.splitlines(), 1):
                if pattern in line:
                    failures.append(
                        f"[Forbidden] '{pattern}' in {f}:{i}: {line.strip()}"
                    )

    # Detect duplicated setup lines in test files
    test_files = [f for f in changed if "test" in f]
    for f in test_files:
        full = os.path.join(WORKDIR, f)
        if not os.path.isfile(full):
            continue
        try:
            with open(full) as fh:
                lines = [l.strip() for l in fh if l.strip() and len(l.strip()) > 20]
        except OSError:
            continue
        from collections import Counter
        counts = Counter(lines)
        dupes = [line for line, n in counts.items() if n >= 3]
        if dupes:
            sample = "\n".join(f"  {n}x: {line[:80]}" for line, n in counts.items() if n >= 3)
            failures.append(
                f"[Duplicated setup] {f} has {len(dupes)} lines repeated 3+ times. "
                f"Move shared setup to beforeEach (TS) or a fixture (Python):\n{sample}"
            )

    if failures:
        details = "\n\n".join(failures)
        template = _CONFIG.get("prompt", {}).get(
            "quality_failed", "QUALITY CHECK FAILED:\n\n{details}",
        )
        return False, template.replace("{details}", details)

    return True, "All quality checks passed"


def _parse_pr_content(content: str) -> tuple[str | None, str | None]:
    """Parse PR_TITLE and PR_BODY from LLM response."""
    title = None
    body = None
    title_match = re.search(r"PR_TITLE:\s*(.+?)(?:\n|$)", content)
    if title_match:
        title = title_match.group(1).strip()[:70]
    body_match = re.search(r"PR_BODY:\s*(.+)", content, re.DOTALL)
    if body_match:
        body = body_match.group(1).strip()
    return title, body


def create_pr(messages: list, last_msg: dict, test_file: str, step: int) -> str | None:
    """Ask LLM for PR content, then create branch/commit/push/PR."""
    pr_cfg = _CONFIG.get("pr", {})

    # Ask LLM for title and description
    pr_messages = messages.copy()
    pr_messages.append(last_msg)
    pr_messages.append({
        "role": "user",
        "content": _CONFIG["prompt"]["pr_prompt"],
    })

    try:
        response = chat(pr_messages)
        content = response["choices"][0]["message"].get("content", "")
    except Exception as e:
        emit(f"  [PR] LLM failed to generate PR content: {e}")
        return None

    title, body = _parse_pr_content(content)
    if not title:
        title = f"fix: agent fix at step {step}"
    if not body:
        body = "Automated fix by ATM agent."

    base = pr_cfg.get("base_branch", "main")
    prefix = pr_cfg.get("branch_prefix", "atm/fix-")
    timestamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    branch_name = f"{prefix}{timestamp}"

    try:
        subprocess.run(
            ["git", "checkout", "-b", branch_name],
            cwd=WORKDIR, capture_output=True, check=True,
        )
        subprocess.run(
            ["git", "add", "-A"],
            cwd=WORKDIR, capture_output=True, check=True,
        )
        subprocess.run(
            ["git", "commit", "-m", title, "-m", body],
            cwd=WORKDIR, capture_output=True, check=True,
        )
        subprocess.run(
            ["git", "push", "-u", "origin", branch_name],
            cwd=WORKDIR, capture_output=True, check=True,
        )
        result = subprocess.run(
            ["gh", "pr", "create", "--title", title, "--body", body, "--base", base],
            cwd=WORKDIR, capture_output=True, text=True, check=True,
        )
        pr_url = result.stdout.strip()
        log("pr", {"url": pr_url, "branch": branch_name, "title": title})
        return pr_url

    except subprocess.CalledProcessError as e:
        emit(f"  [PR] Command failed: {e.stderr}")
        log("pr_error", {"error": str(e)})
        return None


def _default_config_path():
    """Find config/agent.toml relative to the package."""
    pkg = Path(__file__).parent.parent / "config" / "agent.toml"
    if pkg.exists():
        return str(pkg)
    return None


def parse_args():
    parser = argparse.ArgumentParser(description="Agentic TDD runner — local LLM fixes bugs with tests")
    parser.add_argument("issue", help="Issue text, or path to a file containing the issue description")
    parser.add_argument("--source", type=str, help="Source file path relative to workdir (e.g. src/twitch/client.ts)")
    parser.add_argument("--symbol", type=str, help="Target function/method name (e.g. handleResub)")
    parser.add_argument("--workdir", type=str, default=WORKDIR, help="Project root directory (default: cwd)")
    parser.add_argument("--config", type=str, default=_default_config_path(), help="Path to agent.toml config file")
    parser.add_argument("--log-dir", type=str, default=LOG_DIR, help="Directory for JSONL logs (default: cwd)")
    return parser.parse_args()


def main():
    global _CONFIG, WORKDIR, LOG_DIR
    args = parse_args()

    from agentic_tdd_runner.config import load_config
    _CONFIG = load_config(args.config)

    WORKDIR = args.workdir
    LOG_DIR = args.log_dir

    log_path = init_log()

    # Issue can be inline text or a path to a file
    issue_text = args.issue
    if os.path.isfile(issue_text):
        with open(issue_text) as f:
            issue_text = f.read()

    # Build system prompt — inject cookbook if source/symbol provided
    system_prompt = _CONFIG["prompt"]["system"].strip()
    if args.source and args.symbol:
        from agentic_tdd_runner.cookbook import build_system_prompt
        system_prompt = build_system_prompt(
            base_prompt=system_prompt,
            issue_text=issue_text,
            source_path=args.source,
            symbol=args.symbol,
            project_root=WORKDIR,
        )
        emit(f"[COOKBOOK] Injected mock cookbook for {args.symbol} in {args.source}")
        log("cookbook", {"source": args.source, "symbol": args.symbol, "prompt_len": len(system_prompt)})

    messages = [
        {"role": "system", "content": system_prompt},
        {"role": "user", "content": f"Fix this bug:\n\n{issue_text}"},
    ]

    emit(f"{'='*60}")
    max_steps = _CONFIG["agent"]["max_steps"]
    emit(f"AGENT START — max {max_steps} steps")
    emit(f"Workdir: {WORKDIR}")
    emit(f"Log: {log_path}")
    emit(f"{'='*60}")

    log("start", {"issue": issue_text[:200]})

    done_rejected = 0  # how many times we rejected DONE
    quality_rejected = 0

    max_rejections = _CONFIG["verification"]["max_rejections"]
    max_quality_rounds = _CONFIG.get("quality", {}).get("max_fix_rounds", 3)
    for step in range(max_steps):
        emit(f"\n>>> Step {step} — requesting LLM...")
        t0 = time.time()

        try:
            response = chat(messages)
        except Exception as e:
            emit(f"  ERROR: {e}")
            log("error", {"step": step, "error": str(e)})
            break

        elapsed = time.time() - t0

        choice = response["choices"][0]
        msg = choice["message"]
        finish = choice["finish_reason"]
        usage = response.get("usage", {})
        timings = response.get("timings", {})
        thinking = msg.get("reasoning_content", "")

        log("step", {
            "step": step,
            "elapsed_s": round(elapsed, 1),
            "finish_reason": finish,
            "thinking": thinking,
            "content": msg.get("content", ""),
            "tool_calls": [
                {"name": tc["function"]["name"], "args": tc["function"]["arguments"]}
                for tc in msg.get("tool_calls", [])
            ],
            "usage": usage,
            "timings": {
                "prompt_ms": timings.get("prompt_ms"),
                "predicted_ms": timings.get("predicted_ms"),
                "prompt_per_second": timings.get("prompt_per_second"),
                "predicted_per_second": timings.get("predicted_per_second"),
            },
        })

        prompt_tok = usage.get("prompt_tokens", "?")
        comp_tok = usage.get("completion_tokens", "?")
        tok_s = timings.get("predicted_per_second", 0)
        emit(f"--- Step {step} | {elapsed:.1f}s | {prompt_tok}→{comp_tok} tok | {tok_s:.1f} tok/s | finish={finish} ---")

        if thinking:
            emit(f"  [THINK] ({len(thinking)} chars)")
            for line in thinking.strip().split("\n"):
                emit(f"    {line}")

        if msg.get("content"):
            emit(f"  [SAY] {msg['content']}")
            if "DONE" in msg["content"].upper():
                # --- RED-GREEN VERIFICATION ---
                test_file = find_test_file()
                if test_file:
                    verified, verify_msg = verify_red_green(test_file)
                    emit(f"\n  [VERIFY] {verify_msg}")
                    log("verify_result", {"verified": verified, "message": verify_msg, "test_file": test_file})

                    if verified:
                        # --- QUALITY ENFORCEMENT ---
                        if _CONFIG.get("quality", {}).get("enabled", False):
                            emit("\n=== QUALITY CHECKS ===")
                            quality_ok, quality_msg = run_quality_checks(test_file)
                            emit(f"  [QUALITY] {quality_msg[:200]}")
                            log("quality", {"passed": quality_ok, "message": quality_msg[:500]})

                            if not quality_ok:
                                quality_rejected += 1
                                if quality_rejected >= max_quality_rounds:
                                    emit(f"\n  [GIVE UP] Quality failed {quality_rejected} times. Stopping.")
                                    log("quality_give_up", {"step": step, "quality_rejected": quality_rejected})
                                    return 1
                                messages.append(msg)
                                messages.append({"role": "user", "content": quality_msg})
                                continue

                        if _CONFIG.get("quality", {}).get("enabled", False):
                            emit("\n=== POST-QUALITY VERIFICATION ===")
                            verified, verify_msg = verify_red_green(test_file)
                            emit(f"  [RE-VERIFY] {verify_msg}")
                            log("post_quality_verify_result", {
                                "verified": verified,
                                "message": verify_msg,
                                "test_file": test_file,
                            })
                            if not verified:
                                done_rejected += 1
                                if done_rejected >= max_rejections:
                                    emit(f"\n  [GIVE UP] Rejected DONE {done_rejected} times. Stopping.")
                                    log("give_up", {"step": step, "done_rejected": done_rejected})
                                    return 1
                                messages.append(msg)
                                messages.append({
                                    "role": "user",
                                    "content": verify_msg,
                                })
                                continue

                        emit(f"\n{'='*60}")
                        emit(f"AGENT DONE at step {step} — VERIFIED")
                        emit(f"{'='*60}")
                        diff = subprocess.run(["git", "diff"], cwd=WORKDIR, capture_output=True, text=True)
                        emit(f"\n--- GIT DIFF ---\n{diff.stdout}")
                        untracked = subprocess.run(["git", "ls-files", "--others", "--exclude-standard"], cwd=WORKDIR, capture_output=True, text=True)
                        if untracked.stdout.strip():
                            emit("\n--- NEW FILES ---")
                            for f in untracked.stdout.strip().split("\n"):
                                emit(f"  {f}")
                                full = os.path.join(WORKDIR, f)
                                try:
                                    with open(full) as fh:
                                        emit(fh.read())
                                except OSError:
                                    pass
                        # --- PR CREATION ---
                        if _CONFIG.get("pr", {}).get("enabled", False):
                            emit("\n=== PR CREATION ===")
                            pr_url = create_pr(messages, msg, test_file, step)
                            if pr_url:
                                emit(f"  [PR] {pr_url}")
                            else:
                                emit("  [PR] Failed — diff printed above, create PR manually")

                        log("done", {"step": step, "verified": True})
                        return 0
                    else:
                        # Reject and nudge
                        done_rejected += 1
                        if done_rejected >= max_rejections:
                            emit(f"\n  [GIVE UP] Rejected DONE {done_rejected} times. Stopping.")
                            log("give_up", {"step": step, "done_rejected": done_rejected})
                            return 1
                        messages.append(msg)
                        messages.append({
                            "role": "user",
                            "content": verify_msg,
                        })
                        continue
                else:
                    emit("  [WARN] No test file found — cannot verify")
                    messages.append(msg)
                    messages.append({
                        "role": "user",
                        "content": _CONFIG["prompt"]["no_test_found"],
                    })
                    continue

        # Append assistant message to history
        messages.append(msg)

        if finish == "tool_calls" and msg.get("tool_calls"):
            for tc in msg["tool_calls"]:
                fn = tc["function"]
                name = fn["name"]
                try:
                    args = json.loads(fn["arguments"])
                except json.JSONDecodeError:
                    args = {}
                    emit(f"  WARNING: failed to parse args: {fn['arguments'][:200]}")

                args_preview = json.dumps(args, ensure_ascii=False)
                if len(args_preview) > 300:
                    args_preview = args_preview[:300] + "..."
                emit(f"  [TOOL] {name}({args_preview})")

                t1 = time.time()
                result = execute_tool(name, args)
                tool_elapsed = time.time() - t1
                result_truncated = truncate(result)

                log("tool", {
                    "step": step,
                    "name": name,
                    "args": args,
                    "result_chars": len(result),
                    "result_truncated": len(result) != len(result_truncated),
                    "elapsed_s": round(tool_elapsed, 3),
                    "result": result_truncated,
                })

                display = result_truncated
                if len(display) > 500:
                    display = display[:250] + f"\n  ... ({len(result)} chars total) ...\n" + display[-250:]
                emit(f"  [RESULT] ({len(result)} chars, {tool_elapsed:.2f}s)")
                for line in display.split("\n")[:20]:
                    emit(f"    {line}")
                if display.count("\n") > 20:
                    emit(f"    ... ({display.count(chr(10))} lines total)")

                messages.append({
                    "role": "tool",
                    "tool_call_id": tc["id"],
                    "content": result_truncated,
                })

        elif finish == "stop":
            if step > 3:
                messages.append({
                    "role": "user",
                    "content": _CONFIG["prompt"]["nudge"]
                })
                emit("  [NUDGE] Continue prompt injected")
        else:
            emit(f"  [FINISH] {finish}")

    emit(f"\n{'='*60}")
    emit(f"AGENT EXHAUSTED — {max_steps} steps without DONE")
    emit(f"{'='*60}")
    log("exhausted", {"steps": max_steps})
    return 1


if __name__ == "__main__":
    sys.exit(main())
