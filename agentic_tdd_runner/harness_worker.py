"""Harness worker spike: run a subscription coding agent instead of the model loop.

Keeps the deterministic parts of ATM (isolated worktree, environment prep,
runner bootstrap, red/green oracle, quality checks) and hands the edit work to
`claude -p` or `codex exec` as a subprocess.
"""
from __future__ import annotations

import argparse
import fnmatch
import json
import os
import re
import shlex
import shutil
import signal
import subprocess
import sys
import threading
import time
from datetime import datetime, timezone
from pathlib import Path

from agentic_tdd_runner import quality, verification
from agentic_tdd_runner.config import load_config
from agentic_tdd_runner.environment import (
    EnvironmentPrepError,
    WorktreePrepError,
    detect_project_type,
    javascript_preflight_commands,
    prepare_environment,
    prepare_run_worktree,
)
from agentic_tdd_runner.paths import is_test_file_path, resolve_repo_path
from agentic_tdd_runner.shell import build_command_env

QUALITY_LANG = {"javascript": "typescript", "python": "python"}
REPORT_SCHEMA = {
    "type": "object",
    "properties": {
        "test_file": {"type": "string"},
        "changed_files": {"type": "array", "items": {"type": "string"}},
        "summary": {"type": "string"},
        "commands_run": {"type": "array", "items": {"type": "string"}},
        "follow_ups": {"type": "array", "items": {
            "type": "object",
            "properties": {
                "title": {"type": "string"},
                "paths": {"type": "array", "items": {"type": "string"}},
                "red_test": {"type": "string"},
            },
            "required": ["title", "paths", "red_test"],
            "additionalProperties": False,
        }},
    },
    "required": ["test_file", "changed_files", "summary", "commands_run", "follow_ups"],
    "additionalProperties": False,
}


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run claude/codex on an issue inside an ATM run worktree")
    parser.add_argument("--repo", required=True, help="Existing git repo to fix")
    parser.add_argument("--base-ref", default="main")
    parser.add_argument("--run-root", help="Directory for run worktrees; a relative path is taken from REPO (default: REPO/.worktree)")
    parser.add_argument("--issue-number", type=int)
    parser.add_argument("--github-repo", help="owner/repo for --issue-number")
    parser.add_argument("--issue-file", help="File with the issue text (first line is the title)")
    parser.add_argument("--harness", choices=["claude", "codex"], default="claude")
    parser.add_argument("--model")
    parser.add_argument("--timeout", type=int, default=1800)
    parser.add_argument("--config", default="config/agent.toml")
    parser.add_argument("--log-dir")
    parser.add_argument("--artifact-dir")
    parser.add_argument("--harness-bin", help="Override the harness executable")
    parser.add_argument("--dry-run", action="store_true", help="Prepare and print the brief only")
    parser.add_argument("--scope", action="append", default=[], metavar="GLOB",
                        help="Repo-relative glob the diff may touch (repeatable; default: derived from the issue)")
    args = parser.parse_args(argv)
    if not args.issue_file and not (args.issue_number and args.github_repo):
        parser.error("give --issue-file or both --issue-number and --github-repo")
    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    args.artifact_dir = args.artifact_dir or os.path.join(".tmp", "harness-worker", stamp)
    args.log_dir = args.log_dir or args.artifact_dir
    return args


def load_issue(args: argparse.Namespace) -> tuple[str, str]:
    if args.issue_file:
        text = Path(args.issue_file).read_text()
        first, _, rest = text.strip().partition("\n")
        return first.lstrip("# ").strip(), rest.strip()
    result = subprocess.run(
        ["gh", "issue", "view", str(args.issue_number), "--repo", args.github_repo, "--json", "title,body"],
        capture_output=True, text=True, timeout=60, check=True,
    )
    data = json.loads(result.stdout)
    return data.get("title", ""), data.get("body", "")


def detect_commands(workdir: str, env_report) -> tuple[str | None, str | None]:
    """Full test command and typecheck command for the run worktree."""
    root = Path(workdir)
    if env_report.project_type == "javascript":
        pkg = json.loads((root / "package.json").read_text())
        pm = env_report.package_manager or "npm"
        bootstrap = env_report.runner_bootstrap
        test_cmd = f"{pm} run test" if bootstrap and bootstrap.test_command else None
        typecheck = javascript_preflight_commands(root, pkg, {"run_typecheck": True}, package_manager=pm)
        return test_cmd, (shlex.join(typecheck[0]) if typecheck else None)
    if env_report.project_type == "python":
        return "python3 -m pytest", None
    if (root / "go.mod").is_file():
        return "go test ./...", "go vet ./..."
    return None, None


def quality_lang_key(workdir: str, project_type: str) -> str:
    if (Path(workdir) / "go.mod").is_file():
        return "go"
    return QUALITY_LANG.get(project_type, "")


def scan_forbidden(workdir: str, changed: list[str], forbidden: list[str]) -> list[str]:
    """Forbidden-pattern hits in changed files, for languages ATM's quality module does not know."""
    hits = []
    for rel in changed:
        path = Path(workdir) / rel
        if not path.is_file():
            continue
        for number, line in enumerate(path.read_text(errors="replace").splitlines(), start=1):
            for pattern in forbidden:
                if pattern in line:
                    hits.append(f"{rel}:{number} {pattern!r}")
    return hits


SCOPE_RULE = ("If the fix needs a change outside SCOPE, do not make it. Finish the unit inside SCOPE "
              "and report the rest under follow_ups, each with a failing test that proves the gap.")
FOLLOW_UP_RULE = "A follow-up without a red test is discarded. Do not weaken or skip existing tests to make one."


def build_brief(*, title, body, worktree, base_ref, test_cmd, typecheck_cmd, forbidden, scope=None) -> str:
    patterns = ", ".join(f"`{p}`" for p in forbidden) or "none configured"
    typecheck_line = f"`{typecheck_cmd}`" if typecheck_cmd else "none detected"
    allowed = (", ".join(f"`{p}`" for p in scope) + " (test files are always allowed)") if scope else \
        "open (no restriction beyond this repository)"
    return f"""# Bug fix brief

## GOAL
Fix this issue.

### {title}

{body}

## SCOPE
- Repository: `{worktree}` (git worktree at `{base_ref}`).
- Only edit files under that directory. No files outside it.
- Allowed paths: {allowed}
- {SCOPE_RULE}

## ACCEPTANCE
1. Write a failing test that reproduces the bug before changing source. The test must call the
   existing code path the issue describes (the function or handler that misbehaves today), not only
   a new helper you create: with the fix removed, the test must fail on behavior, not on a missing module.
2. Fix the source.
3. The test passes.
4. The full test command passes.
5. Typecheck passes, if the repo has one.

## VERIFY
- Tests: `{test_cmd or "not detected"}`
- Typecheck: {typecheck_line}

## FORBIDDEN
- These patterns in changed files: {patterns}
- `git commit`, `git push`, `git rebase`, any `gh` command.
- Deleting, skipping or weakening existing tests.
- `sleep` in tests.

## FOLLOW-UPS
- {FOLLOW_UP_RULE}
- A follow-up is a pair: the test file on disk AND its entry in the report. Write the test file first,
  then declare it. One half without the other is discarded.
- The red test must fail today, on behavior, when run from its `red_test` path.

## REPORT
Your final message must be ONLY this JSON object, nothing else:
{{"test_file": "<repo-relative path of the regression test>", "changed_files": ["<path>"], "summary": "<one paragraph>", "commands_run": ["<command>"], "follow_ups": [{{"title": "<gap>", "paths": ["<path outside SCOPE>"], "red_test": "<repo-relative path where the test would live>"}}]}}
Use `"follow_ups": []` when there are none. Write each follow-up's red test under
`.atm/follow-ups/<n>/<red_test>` (n is its 1-based position in follow_ups), never at `<red_test>` itself,
so this unit's test suite never sees it.
"""


MISSING_MODULE_MARKERS = (
    "cannot find module", "module not found", "could not resolve", "cannot find package",
    "no module named", "modulenotfounderror", "failed to resolve import",
)


def red_failed_on_missing_module(red_output: str) -> str | None:
    """A red phase that fails because a new module is absent proves nothing about the bug."""
    lowered = red_output.lower()
    for marker in MISSING_MODULE_MARKERS:
        if marker in lowered:
            return marker
    return None


ATM_DIR = ".atm/"
PATH_TOKEN = re.compile(r"[\w./-]+")
BACKTICK_IDENT = re.compile(r"`([A-Za-z_][A-Za-z0-9_]*)`")


def is_atm_path(path: str) -> bool:
    return path == ".atm" or path.startswith(ATM_DIR)


def definition_patterns(name: str) -> list[str]:
    """Top-level definitions of `name` in Go, TS/JS and Python, as git grep -E patterns."""
    n = re.escape(name)
    return [rf"^func (\([^)]*\) )?{n}\(", rf"^(export )?(async )?function {n}\(",
            rf"^(export )?const {n} =", rf"^(async )?def {n}\("]


def derive_scope(text: str, worktree: str) -> list[str]:
    """Tracked paths the issue names, plus files defining the identifiers it backticks."""
    tracked = set(git_lines(worktree, "ls-files"))
    paths = {t for t in (tok.rstrip(".,:;").removeprefix("./") for tok in PATH_TOKEN.findall(text)) if t in tracked}
    for name in sorted(set(BACKTICK_IDENT.findall(text))):
        args = [arg for pattern in definition_patterns(name) for arg in ("-e", pattern)]
        paths.update(git_lines(worktree, "grep", "-l", "-E", *args))
    return sorted(p for p in paths if not is_atm_path(p))


def resolve_scope(globs: list[str], title: str, body: str, worktree: str) -> tuple[str, list[str]]:
    if globs:
        return "flag", list(globs)
    paths = derive_scope(f"{title}\n{body}", worktree)
    return ("issue", paths) if paths else ("open", [])


def check_scope(workdir: str, base_sha: str, config: dict, globs: list[str]) -> tuple[bool, str]:
    """Every touched non-test file outside .atm/ must match a SCOPE glob; empty globs mean open."""
    if not globs:
        return True, "scope open"
    touched = set(git_lines(workdir, "diff", "--name-only", base_sha))
    touched |= set(git_lines(workdir, "ls-files", "--others", "--exclude-standard"))
    outside = sorted(p for p in touched if not is_test_file_path(p, config) and not is_atm_path(p)
                     and not any(fnmatch.fnmatch(p, g) for g in globs))
    if outside:
        return False, "outside SCOPE: " + ", ".join(outside)
    return True, "inside SCOPE: " + ", ".join(globs)


def run_test_file(rel: str, workdir: str, config: dict) -> tuple[int | None, str]:
    """Run one test file the way the red/green phase does."""
    timeout = config.get("timeouts", {}).get("test_run", 300)
    try:
        result = subprocess.run(verification.single_test_argv(rel, config), cwd=workdir,
                                capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired:
        return None, f"timeout after {timeout}s"
    except OSError as exc:
        return None, str(exc)
    return result.returncode, result.stdout + result.stderr


def follow_up_verdict(index: int, rel: str, workdir: str, config: dict) -> tuple[bool, str]:
    """Copy the red test into place, run it, remove it. Accept only a behavioral failure."""
    if not rel or is_atm_path(rel) or not is_test_file_path(rel, config):
        return False, f"red_test {rel!r} is not a test file path outside .atm/"
    source = Path(workdir) / ".atm" / "follow-ups" / str(index) / rel
    try:
        target = resolve_repo_path(rel, workdir)
    except ValueError as exc:
        return False, str(exc)
    if not source.is_file():
        return False, f"no red test at .atm/follow-ups/{index}/{rel}"
    if target.exists():
        return False, f"{rel} already exists; a red test may not replace a file"
    missing, parent = [], target.parent
    while not parent.exists():
        missing.append(parent)
        parent = parent.parent
    target.parent.mkdir(parents=True, exist_ok=True)
    try:
        shutil.copy2(source, target)
        code, output = run_test_file(rel, workdir, config)
    finally:
        target.unlink(missing_ok=True)
        for directory in missing:
            directory.rmdir()
    if code is None:
        return False, f"red test could not run: {output}"
    if code == 0:
        return False, "red test passed; it proves no gap"
    marker = red_failed_on_missing_module(output)
    if marker:
        return False, f"red test fails on a missing module ({marker}), not on behavior"
    return True, f"red test fails on behavior (exit {code})"


def follow_up_tests_on_disk(workdir: str) -> dict[int, str]:
    """Red tests the agent wrote under .atm/follow-ups/<n>/<rel>, declared or not."""
    found: dict[int, str] = {}
    root = Path(workdir) / ".atm" / "follow-ups"
    if not root.is_dir():
        return found
    for slot in sorted(root.iterdir()):
        if not slot.name.isdigit():
            continue
        tests = [p for p in slot.rglob("*") if p.is_file()]
        if len(tests) == 1:
            found[int(slot.name)] = tests[0].relative_to(slot).as_posix()
    return found


def validate_follow_ups(report: dict | None, workdir: str, config: dict, log) -> list[dict]:
    """A follow-up is a pair: the declared entry and the red test on disk. Either half alone is
    recorded as rejected; a test found on disk without a declaration is still validated, because
    the test is the evidence and the declaration is only its label."""
    items = (report or {}).get("follow_ups")
    declared = [i if isinstance(i, dict) else {} for i in (items if isinstance(items, list) else [])]
    on_disk = follow_up_tests_on_disk(workdir)
    records = []
    for index in sorted(set(range(1, len(declared) + 1)) | set(on_disk)):
        item = declared[index - 1] if index <= len(declared) else None
        rel = item.get("red_test") if item else None
        rel = rel.removeprefix("./") if isinstance(rel, str) else ""
        if not rel and index in on_disk:
            rel = on_disk[index]
        record = {"title": item.get("title") if item else None, "paths": item.get("paths") if item else [],
                  "red_test": rel, "declared": item is not None, "on_disk": index in on_disk}
        if item is None:
            record["title"] = f"undeclared follow-up {index}: {rel}"
        record["accepted"], record["reason"] = follow_up_verdict(index, rel, workdir, config)
        if record["accepted"] and item is None:
            record["reason"] += " (test found on disk, not declared in the report)"
        if not record["accepted"]:
            log("follow_up_rejected", record)
        records.append(record)
    return records


def make_logger(path: str, harness: str):
    fh = open(path, "a", encoding="utf-8")

    def write(event) -> None:
        entry = {"ts": datetime.now(timezone.utc).isoformat(), "harness": harness, "event": event}
        fh.write(json.dumps(entry, ensure_ascii=False) + "\n")
        fh.flush()

    def log(name: str, data: dict) -> None:
        write({"type": f"atm.{name}", **data})

    return write, log, fh


def harness_command(args, worktree: str, brief: str, schema_path: str, last_msg_path: str) -> tuple[list[str], str | None]:
    """Return argv and the stdin payload."""
    if args.harness == "claude":
        # --include-partial-messages is what keeps the thinking text in the stream; without it
        # the assistant events carry empty thinking blocks.
        cmd = [args.harness_bin or "claude", "-p", "--output-format", "stream-json", "--verbose",
               "--include-partial-messages", "--permission-mode", "acceptEdits",
               "--allowedTools", "Read", "Edit", "Write", "Bash", "Glob", "Grep"]
        if args.model:
            cmd += ["--model", args.model]
        return cmd, brief
    cmd = [args.harness_bin or "codex", "exec", "--json", "-C", worktree, "--sandbox", "workspace-write",
           "--output-schema", schema_path, "-o", last_msg_path]
    if args.model:
        cmd += ["-c", f"model={json.dumps(args.model)}"]
    return cmd + [brief], None


def _snippet(text, n: int = 160) -> str:
    return " ".join(str(text).split())[:n]


def summarize_event(event) -> str | None:
    if not isinstance(event, dict):
        return f"[raw] {_snippet(event)}" if str(event).strip() else None
    kind = event.get("type", "")
    if kind == "stream_event":
        return None  # partial chunks are logged, not printed
    if kind == "system":
        return f"[{event.get('subtype', 'system')}] model={event.get('model', '?')}"
    if kind in ("assistant", "user"):
        parts = []
        for block in (event.get("message") or {}).get("content") or []:
            if not isinstance(block, dict):
                continue
            if block.get("type") == "text":
                parts.append(f"[text] {_snippet(block.get('text', ''))}")
            elif block.get("type") == "tool_use":
                inp = block.get("input") or {}
                arg = inp.get("command") or inp.get("file_path") or inp.get("pattern") or inp.get("path") or ""
                parts.append(f"[tool] {block.get('name')}: {_snippet(arg, 120)}")
            elif block.get("type") == "tool_result":
                parts.append(f"[tool_result] {_snippet(block.get('content', ''), 100)}")
        return " | ".join(parts) or None
    if kind == "result":
        return f"[result] {event.get('subtype')} turns={event.get('num_turns')} error={event.get('is_error')}"
    item = event.get("item") or {}
    if item.get("type") == "agent_message":
        return f"[text] {_snippet(item.get('text', ''))}"
    if item.get("type") == "command_execution":
        return f"[tool] shell ({kind}): {_snippet(item.get('command', ''), 120)}"
    if item.get("type") == "file_change":
        return f"[edit] {[c.get('path') for c in item.get('changes') or []]}"
    return f"[{kind or 'event'}]"


def final_text(event, harness: str) -> str | None:
    if not isinstance(event, dict):
        return None
    if harness == "claude" and event.get("type") == "result":
        return event.get("result")
    item = event.get("item") or {}
    if harness == "codex" and item.get("type") == "agent_message":
        return item.get("text")
    return None


def run_harness(cmd, stdin_text, *, cwd, timeout, harness, write) -> dict:
    env = os.environ.copy()
    env.pop("CLAUDE_CODE_CHILD_SESSION", None)
    start = time.monotonic()
    proc = subprocess.Popen(
        cmd, cwd=cwd, env=env, text=True, bufsize=1, start_new_session=True,
        stdin=subprocess.PIPE if stdin_text is not None else subprocess.DEVNULL,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
    )
    timed_out = threading.Event()

    def kill_group() -> None:
        timed_out.set()
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass

    timer = threading.Timer(timeout, kill_group)
    timer.start()
    if stdin_text is not None:
        try:
            proc.stdin.write(stdin_text)
            proc.stdin.close()
        except BrokenPipeError:
            pass
    last_text = None
    try:
        for line in iter(proc.stdout.readline, ""):
            line = line.rstrip("\n")
            if not line:
                continue
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                event = line
            write(event)
            summary = summarize_event(event)
            if summary:
                print(f"  {summary}", flush=True)
            last_text = final_text(event, harness) or last_text
        exit_code = proc.wait()
    finally:
        timer.cancel()
    return {
        "exit_code": exit_code,
        "timed_out": timed_out.is_set(),
        "duration_seconds": round(time.monotonic() - start, 2),
        "final_text": last_text,
    }


def extract_report(text: str | None) -> tuple[dict | None, str | None]:
    """Last top-level JSON object in the text."""
    if not text:
        return None, "no final message"
    decoder = json.JSONDecoder()
    found, i = None, 0
    while (i := text.find("{", i)) != -1:
        try:
            obj, end = decoder.raw_decode(text, i)
        except json.JSONDecodeError:
            i += 1
            continue
        if isinstance(obj, dict):
            found = obj
        i = end
    return (found, None) if found is not None else (None, "no JSON object in final message")


def git_lines(workdir: str, *args: str) -> list[str]:
    result = subprocess.run(["git", *args], cwd=workdir, capture_output=True, text=True)
    return [line.strip() for line in result.stdout.splitlines() if line.strip()]


def pick_test_file(report: dict | None, workdir: str, config: dict) -> str | None:
    hint = (report or {}).get("test_file")
    if isinstance(hint, str) and hint:
        try:
            if resolve_repo_path(hint, workdir).is_file():
                return os.path.relpath(resolve_repo_path(hint, workdir), workdir)
        except ValueError:
            pass
    added = git_lines(workdir, "ls-files", "--others", "--exclude-standard")
    tests = sorted(p for p in added if is_test_file_path(p, config) and not is_atm_path(p))
    return tests[0] if tests else None


def check_gate(workdir: str, base_sha: str, config: dict) -> tuple[bool, str]:
    head = git_lines(workdir, "rev-parse", "HEAD")
    if head != [base_sha]:
        return False, "harness moved HEAD (created commits)"
    modified = [p for p in git_lines(workdir, "diff", "--name-only", base_sha) if not is_test_file_path(p, config)]
    if not modified:
        return False, "diff does not touch any pre-existing source file"
    return True, f"touches pre-existing source: {', '.join(modified)}"


def run_command(command: str | None, workdir: str, config: dict, timeout: int) -> bool | None:
    if not command:
        return None
    env = build_command_env(config)
    env.setdefault("CI", "1")
    try:
        result = subprocess.run(shlex.split(command), cwd=workdir, env=env, capture_output=True,
                                text=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        return False
    return result.returncode == 0


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    # Stale .pyc files survive same-size edits restored within one second by the
    # red/green stash, which makes the oracle run the wrong code.
    os.environ["PYTHONDONTWRITEBYTECODE"] = "1"
    config = load_config(args.config)
    # The recommended-tools preflight serves ATM's own tool loop; the harness brings its own tools.
    config.setdefault("tooling", {})["recommended"] = []
    artifact_dir = Path(args.artifact_dir).resolve()
    log_dir = Path(args.log_dir).resolve()
    artifact_dir.mkdir(parents=True, exist_ok=True)
    log_dir.mkdir(parents=True, exist_ok=True)
    log_path = log_dir / f"worker-{datetime.now().strftime('%Y%m%d-%H%M%S')}.jsonl"
    write, log, log_fh = make_logger(str(log_path), args.harness)

    try:
        title, body = load_issue(args)
        wt = prepare_run_worktree(args.repo, base_ref=args.base_ref, run_root=args.run_root)
        env_report = prepare_environment(wt.workdir, config)
    except (WorktreePrepError, EnvironmentPrepError, subprocess.CalledProcessError, OSError) as exc:
        log("prepare_failed", {"error": str(exc)})
        print(f"[PREPARE] FAILED: {exc}")
        return 2
    worktree = wt.workdir
    if env_report.runner_bootstrap:
        config.setdefault("runner", {})["bootstrap"] = env_report.runner_bootstrap.to_log_dict()
    base_sha = git_lines(worktree, "rev-parse", "HEAD")[0]
    test_cmd, typecheck_cmd = detect_commands(worktree, env_report)
    lang_key = quality_lang_key(worktree, env_report.project_type)
    forbidden = config.get("quality", {}).get(lang_key, {}).get("forbidden", [])
    scope_source, scope = resolve_scope(args.scope, title, body, worktree)
    log("scope", {"source": scope_source, "paths": scope})
    brief = build_brief(title=title, body=body, worktree=worktree, base_ref=args.base_ref,
                        test_cmd=test_cmd, typecheck_cmd=typecheck_cmd, forbidden=forbidden, scope=scope)
    brief_path = artifact_dir / "brief.md"
    brief_path.write_text(brief)
    log("prepared", {"worktree": worktree, "base_sha": base_sha, "test_command": test_cmd,
                     "typecheck_command": typecheck_cmd, "environment": env_report.to_log_dict()})
    print(f"[PREPARE] worktree={worktree} base={args.base_ref}@{base_sha[:10]} tests={test_cmd!r}")
    if args.dry_run:
        print(brief)
        return 0

    schema_path = artifact_dir / "report.schema.json"
    if args.harness == "codex":
        schema_path.write_text(json.dumps(REPORT_SCHEMA, indent=2))
    last_msg_path = artifact_dir / "last-message.txt"
    cmd, stdin_text = harness_command(args, worktree, brief, str(schema_path), str(last_msg_path))
    print(f"[HARNESS] {args.harness} timeout={args.timeout}s log={log_path}")
    try:
        run = run_harness(cmd, stdin_text, cwd=worktree, timeout=args.timeout, harness=args.harness, write=write)
    except OSError as exc:
        run = {"exit_code": None, "timed_out": False, "duration_seconds": 0, "final_text": None}
        log("harness_failed", {"error": str(exc)})
    if args.harness == "codex" and last_msg_path.is_file():
        run["final_text"] = last_msg_path.read_text() or run["final_text"]
    harness_report, parse_error = extract_report(run["final_text"])
    log("harness_done", {k: v for k, v in run.items() if k != "final_text"} | {"report": harness_report,
                                                                                "report_parse_error": parse_error})

    changed = [p for p in quality.get_changed_files(worktree) if not is_atm_path(p)]
    test_file = pick_test_file(harness_report, worktree, config)
    if test_file:
        red_outputs: list[str] = []

        def log_capturing_red(name: str, data: dict) -> None:
            if name == "verify":
                red_outputs.append(str(data.get("red_output_full") or data.get("red_output", "")))
            log(name, data)

        verified, verify_msg = verification.verify_red_green(
            test_file, workdir=worktree, config=config, emit=print, log=log_capturing_red,
            apply_mechanical_edits=lambda edits, wd: 0,
        )
        invalid_red = red_failed_on_missing_module(red_outputs[-1] if red_outputs else "")
        if verified and invalid_red:
            verified = False
            verify_msg = f"INVALID RED: without the fix the test fails on a missing module ({invalid_red}), not on behavior"
            log("verify_invalid_red", {"test_file": test_file, "marker": invalid_red})
    else:
        verified, verify_msg = False, "no test file found"
    quality_ok, quality_msg = quality.run_quality_checks(
        test_file or (changed[0] if changed else ""), workdir=worktree, config=config, log=log,
        is_test_file_path=lambda p: is_test_file_path(p, config), get_changed_files_fn=lambda: changed,
    )
    if quality_ok and lang_key not in QUALITY_LANG.values():
        hits = scan_forbidden(worktree, changed, forbidden)
        if hits:
            quality_ok, quality_msg = False, "forbidden patterns: " + "; ".join(hits[:5])
            log("quality_forbidden", {"hits": hits})
    gate_ok, gate_msg = check_gate(worktree, base_sha, config)
    timeout = int(config.get("environment", {}).get("timeout", 300))
    full_tests_ok = run_command(test_cmd, worktree, config, timeout)
    typecheck_ok = run_command(typecheck_cmd, worktree, config, timeout)
    scope_ok, scope_msg = check_scope(worktree, base_sha, config, scope)
    follow_ups = validate_follow_ups(harness_report, worktree, config, log)
    accepted = [f for f in follow_ups if f["accepted"]]
    if accepted:
        (artifact_dir / "follow-ups.json").write_text(json.dumps(accepted, indent=2))

    result = {
        "harness": args.harness, "model": args.model, "base_ref": args.base_ref, "worktree": worktree,
        "brief": str(brief_path), "log": str(log_path), "changed_files": changed, "test_file": test_file,
        "verified": {"ok": verified, "message": verify_msg},
        "quality_ok": {"ok": quality_ok, "message": quality_msg},
        "gate_ok": {"ok": gate_ok, "message": gate_msg},
        "scope_ok": {"ok": scope_ok, "message": scope_msg}, "follow_ups": follow_ups,
        "full_tests_ok": full_tests_ok, "typecheck_ok": typecheck_ok,
        "duration_seconds": run["duration_seconds"], "timed_out": run["timed_out"],
        "harness_exit_code": run["exit_code"], "report_parse_error": parse_error,
    }
    (artifact_dir / "report.json").write_text(json.dumps(result, indent=2))
    log("report", result)
    log_fh.close()
    passed = verified and quality_ok and gate_ok and scope_ok and full_tests_ok is not False and typecheck_ok is not False
    print("\n=== HARNESS WORKER SUMMARY ===")
    print(f"harness:    {args.harness} model={args.model or 'default'} exit={run['exit_code']}")
    print(f"duration:   {run['duration_seconds']}s timed_out={run['timed_out']}")
    print(f"worktree:   {worktree}")
    print(f"changed:    {', '.join(changed) or 'none'}")
    print(f"test_file:  {test_file or 'none'} (report parse: {parse_error or 'ok'})")
    print(f"red/green:  {'PASS' if verified else 'FAIL'} {_snippet(verify_msg, 100)}")
    print(f"quality:    {'PASS' if quality_ok else 'FAIL'} {_snippet(quality_msg, 100)}")
    print(f"gate:       {'PASS' if gate_ok else 'FAIL'} {gate_msg}")
    print(f"scope:      {'PASS' if scope_ok else 'FAIL'} {_snippet(scope_msg, 100)}")
    print(f"follow-ups: {len(accepted)} accepted, {len(follow_ups) - len(accepted)} rejected")
    print(f"full tests: {full_tests_ok} typecheck: {typecheck_ok}")
    print(f"RESULT:     {'PASS' if passed else 'FAIL'} report={artifact_dir / 'report.json'}")
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
