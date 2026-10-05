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
import textwrap
import threading
import time
from datetime import datetime, timezone
from pathlib import Path

from agentic_tdd_runner import quality
from agentic_tdd_runner import judge as follow_up_judge, verification
from agentic_tdd_runner.config import load_config
from agentic_tdd_runner.environment import (
    EnvironmentPrepError,
    WorktreePrepError,
    javascript_preflight_commands,
    prepare_environment,
    prepare_run_clone,
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
                "criterion": {"type": "string"},
            },
            "required": ["title", "paths", "red_test", "criterion"],
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
    parser.add_argument("--run-root", help="Directory for run clones; a relative path is taken from REPO (default: REPO/.worktree)")
    parser.add_argument("--issue-number", type=int)
    parser.add_argument("--github-repo", help="owner/repo for --issue-number")
    parser.add_argument("--issue-file", help="File with the issue text (first line is the title)")
    parser.add_argument("--harness", choices=["claude", "codex", "opencode", "pi"], default="claude")
    parser.add_argument("--harness-arg", action="append", default=[], metavar="ARG",
                        help="Extra argument appended to the harness command, before the brief (repeatable), e.g. --harness-arg=--provider --harness-arg=bonsai-mlx")
    parser.add_argument("--env", action="append", default=[], metavar="KEY=VALUE",
                        help="Extra environment for the harness process (repeatable), e.g. OPENCODE_CONFIG=...")
    parser.add_argument("--model")
    parser.add_argument("--effort", choices=["low", "medium", "high", "xhigh", "max"],
                        help="Reasoning effort: claude --effort, codex model_reasoning_effort (codex has no xhigh/max)")
    parser.add_argument("--timeout", type=int, default=1800)
    parser.add_argument("--config", default="config/agent.toml")
    parser.add_argument("--log-dir")
    parser.add_argument("--artifact-dir")
    parser.add_argument("--harness-bin", help="Override the harness executable")
    parser.add_argument("--dry-run", action="store_true", help="Prepare and print the brief only")
    parser.add_argument("--max-units", type=int, help="Chain accepted follow-ups as further units in the same clone, up to this many units (config [harness_worker] max_units, default 3)")
    parser.add_argument("--no-deliver", action="store_true", help="Skip no-mistakes delivery after a green verdict")
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


def scan_forbidden(workdir: str, changed: list[str], forbidden: list[str], base: str) -> list[str]:
    """Forbidden-pattern hits in lines added since base, for languages ATM's quality module does not know."""
    hits = []
    for rel in changed:
        for number, line in quality.added_lines(workdir, rel, base):
            for pattern in forbidden:
                if pattern in line:
                    hits.append(f"{rel}:{number} {pattern!r}")
    return hits


SCOPE_RULE = ("If the fix needs a change outside SCOPE, do not make it. Finish the unit inside SCOPE "
              "and report the rest under follow_ups, each with a failing test that proves the gap.")
FOLLOW_UP_RULE = "A follow-up without a red test is discarded. Do not weaken or skip existing tests to make one."
# House style, 4-oct-2026: the ponytail skill (github.com/DietrichGebert/ponytail, MIT), condensed so the brief
# carries it whatever the harness. If the skill is installed, the agent loads it too; the brief is the floor.
PONYTAIL = """## STYLE: ponytail (full)
You are a lazy senior developer. Lazy means efficient, not careless. The best code is the code never written.
If a `ponytail` skill is installed in this harness, load it. Either way, follow this:
- Understand first, then be lazy: read every file the change touches and trace the real flow end to end
  before writing. Laziness that skips comprehension ships a confident wrong fix.
- Bug fix = root cause, not symptom. Before you edit, grep every caller of the function you are about to
  touch. One guard in the shared function beats a guard in each caller; patching only the path the issue
  names leaves every sibling caller broken. Callers outside SCOPE go to follow-ups, with their red test.
- The ladder, stop at the first rung that holds: does it need to exist? already in this codebase? stdlib?
  native platform feature? installed dependency? one line? only then the minimum that works.
- No unrequested abstractions, no scaffolding for later, deletion over addition, fewest files, shortest
  working diff. Boring over clever.
- Never simplify away validation at trust boundaries, error handling that prevents data loss, security,
  accessibility, or anything the issue asks for.
- A deliberate shortcut with a known ceiling gets a `ponytail:` comment naming the ceiling and the upgrade path.
"""


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

{PONYTAIL}
## FORBIDDEN
- These patterns on lines added since this unit's base commit: {patterns}
- `git commit`, `git push`, `git rebase`, any `gh` command.
- Deleting, skipping or weakening existing tests.
- `sleep` in tests.

## FOLLOW-UPS
- {FOLLOW_UP_RULE}
- A follow-up is a pair: the test file on disk AND its entry in the report. Write the test file first,
  then declare it. One half without the other is discarded.
- The red test must fail today, on behavior, when run from its `red_test` path.

- Each follow-up names `criterion`: one sentence copied verbatim from this issue's acceptance
  criteria that the gap violates. A follow-up whose criterion is not in the issue is not chained.
- The red test must call code that exists in the repository today, not only helpers you add.

## REPORT
Your final message must be ONLY this JSON object, nothing else:
{{"test_file": "<repo-relative path of the regression test>", "changed_files": ["<path>"], "summary": "<one paragraph>", "commands_run": ["<command>"], "follow_ups": [{{"title": "<gap>", "paths": ["<path outside SCOPE>"], "red_test": "<repo-relative path where the test would live>", "criterion": "<sentence copied from the issue>"}}]}}
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


DEFINITION = re.compile(r"\b(?:async\s+def|def|func\s*\([^)]*\)|func|function|class|type|fn)\s+[A-Za-z_]\w*")
CALL_IDENT = re.compile(r"(?<![\w.])([A-Za-z_][A-Za-z0-9_]*)\s*\(")
NOT_SYMBOLS = frozenset("if for while switch return func def class fn print len range make new append cap panic "
                        "recover delete copy string int bool byte error assert expect require describe it test "
                        "Run Fatalf Fatal Errorf Error Helper Cleanup TempDir Setenv Skip Skipf Logf".split())
SOURCE_EXCLUDES = (":!*_test.go", ":!*test_*.py", ":!*_test.py", ":!*.test.*", ":!*.spec.*", ":!.atm/*")


def symbols_defined_in_base(test_source: str, workdir: str, base_sha: str) -> tuple[list[str], list[str]]:
    """Identifiers the test calls, split into those defined in the base commit's production
    source and the rest. A test whose every call is to code this run invented proves nothing
    about the repository as it was."""
    # Definitions inside the test are not calls: `def test_x(`, `func TestX(`, `func (s *S) helper(`.
    body = DEFINITION.sub(" ", test_source)
    names = sorted({n for n in CALL_IDENT.findall(body) if n not in NOT_SYMBOLS and len(n) > 2})
    in_base, elsewhere = [], []
    for name in names:
        args = [arg for pattern in definition_patterns(name) for arg in ("-e", pattern)]
        hits = git_lines(workdir, "grep", "-l", "-E", *args, base_sha, "--", ".", *SOURCE_EXCLUDES)
        (in_base if hits else elsewhere).append(name)
    return in_base, elsewhere


def criterion_in_issue(criterion, issue_text: str) -> tuple[bool, str]:
    """The follow-up's criterion must be a sentence of the issue, compared loosely: lowercase,
    punctuation dropped, and at least three quarters of its words (4+ letters) present in order
    of nothing, just present. Short or missing criteria are refused."""
    if not isinstance(criterion, str) or len(criterion.strip()) < 20:
        return False, "no criterion cited (one sentence from the issue, 20+ characters)"
    words = [w for w in re.sub(r"[^a-z0-9 ]+", " ", criterion.lower()).split() if len(w) >= 4]
    body = set(re.sub(r"[^a-z0-9 ]+", " ", issue_text.lower()).split())
    if not words:
        return False, "criterion has no content words"
    present = [w for w in words if w in body]
    share = len(present) / len(words)
    if share < 0.75:
        return False, f"criterion not found in the issue ({len(present)}/{len(words)} words)"
    return True, f"criterion found in the issue ({len(present)}/{len(words)} words)"


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


def validate_follow_ups(report: dict | None, workdir: str, config: dict, log, *,
                        base_sha: str | None = None, issue_text: str = "", issue: dict | None = None) -> list[dict]:
    """A follow-up is a pair: the declared entry and the red test on disk. Either half alone is
    recorded as rejected; a test found on disk without a declaration is still validated, because
    the test is the evidence and the declaration is only its label.

    `accepted` means the red test proves a gap. `chainable` means it may become the next unit of
    this run: accepted, its test calls code that already exists in the base commit, it cites
    a criterion that is in the issue, and the judge (if enabled) says it serves the issue rather
    than widening it. Slop fails one of the last three."""
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
        record = {"index": index, "title": item.get("title") if item else None,
                  "paths": item.get("paths") if item else [],
                  "red_test": rel, "declared": item is not None, "on_disk": index in on_disk}
        if item is None:
            record["title"] = f"undeclared follow-up {index}: {rel}"
        record["criterion"] = item.get("criterion") if item else None
        record["accepted"], record["reason"] = follow_up_verdict(index, rel, workdir, config)
        if record["accepted"] and item is None:
            record["reason"] += " (test found on disk, not declared in the report)"
        record["chainable"], record["chain_reason"] = False, "red test does not prove a gap"
        if record["accepted"]:
            reasons = []
            source = Path(workdir) / ".atm" / "follow-ups" / str(index) / rel
            if base_sha:
                in_base, elsewhere = symbols_defined_in_base(source.read_text(errors="replace"), workdir, base_sha)
                record["symbols_in_base"], record["symbols_elsewhere"] = in_base, elsewhere
                if not in_base:
                    reasons.append("the test calls no symbol defined in the base commit")
            crit_ok, crit_msg = criterion_in_issue(record["criterion"], issue_text)
            if not crit_ok:
                reasons.append(crit_msg)
            if not reasons:
                # Only a follow-up that passed the mechanical gates is worth a judge call.
                verdict = follow_up_judge.judge_follow_up(issue or {"title": "", "body": issue_text}, record, config)
                record["judge"] = verdict
                if not verdict["ok"]:
                    reasons.append(verdict["reason"])
                elif verdict.get("enabled"):
                    record["chain_reason_judge"] = verdict["reason"]
            record["chainable"] = not reasons
            record["chain_reason"] = "; ".join(reasons) if reasons else (
                "red test, base symbols and criterion all check out" + (f"; {record['chain_reason_judge']}" if record.get("chain_reason_judge") else ""))
        if not record["accepted"]:
            log("follow_up_rejected", record)
        elif not record["chainable"]:
            log("follow_up_not_chainable", record)
        records.append(record)
    return records


def build_unit_brief(*, number, follow_up, issue_title, issue_body, worktree, test_cmd, typecheck_cmd, forbidden) -> str:
    patterns = ", ".join(f"`{p}`" for p in forbidden) or "none configured"
    typecheck_line = f"`{typecheck_cmd}`" if typecheck_cmd else "none detected"
    allowed = ", ".join(f"`{p}`" for p in follow_up.get("paths") or []) or "none"
    return f"""# Follow-up unit {number}

## GOAL
The previous unit of this run is already applied and committed at HEAD. This unit closes one gap it
reported, proven by a red test that already exists and fails today:

- Gap: {follow_up.get("title")}
- Issue criterion it serves: "{follow_up.get("criterion")}"
- Red test: `{follow_up.get("red_test")}` (already on disk; it is this unit's `test_file`)

Make that test pass by fixing the source. Do not edit the red test: it is the contract. If it cannot
pass without changing it, stop and say so in `summary`.

### Original issue, for context
#### {issue_title}

{issue_body}

## SCOPE
- Repository: `{worktree}`.
- Only edit files under that directory. No files outside it.
- Allowed paths: {allowed} (test files are always allowed)
- {SCOPE_RULE}

## ACCEPTANCE
1. `{follow_up.get("red_test")}` passes, unchanged.
2. The full test command passes.
3. Typecheck passes, if the repo has one.

## VERIFY
- Tests: `{test_cmd or "not detected"}`
- Typecheck: {typecheck_line}

{PONYTAIL}
## FORBIDDEN
- These patterns on lines added since this unit's base commit: {patterns}
- `git commit`, `git push`, `git rebase`, any `gh` command.
- Deleting, skipping or weakening existing tests, including the red test.
- `sleep` in tests.

## FOLLOW-UPS
- {FOLLOW_UP_RULE}
- Same contract as the first unit: red test under `.atm/follow-ups/<n>/<red_test>`, declared in the
  report with `criterion` copied from the issue.

## REPORT
Your final message must be ONLY this JSON object, nothing else:
{{"test_file": "{follow_up.get("red_test")}", "changed_files": ["<path>"], "summary": "<one paragraph>", "commands_run": ["<command>"], "follow_ups": []}}
"""


def commit_unit(worktree: str, number: int, title: str, closes: int | None = None) -> str:
    """Record a passed unit in the clone so the next unit's red/green runs against it. `closes` is the GitHub
    issue the run came from: merging the delivered PR then closes it."""
    message = f"atm unit {number}: {title}"[:200] + (f"\n\nCloses #{closes}" if closes else "")
    # Add all, then unstage .atm: an exclude pathspec makes git add exit 1 when the target ignores .atm/.
    subprocess.run(["git", "add", "-A"], cwd=worktree, check=True, capture_output=True)
    subprocess.run(["git", "reset", "-q", "--", ".atm"], cwd=worktree, check=True, capture_output=True)
    subprocess.run(["git", "-c", "user.name=atm", "-c", "user.email=atm@localhost", "-c", "core.hooksPath=/dev/null",
                    "commit", "-q", "--no-verify", "-m", message],
                   cwd=worktree, check=True, capture_output=True)
    return git_lines(worktree, "rev-parse", "HEAD")[0]


PR_URL = re.compile(r"https://github\.com/[\w.-]+/[\w.-]+/pull/\d+")
RUN_ID = re.compile(r'\b(?:run|id): "?([0-9A-HJKMNP-TV-Z]{26})\b')  # `run: "01M..."` from axi run, `id: "01M..."` from axi status
HEAD_SHA = re.compile(r'\bhead_sha: "?([0-9a-f]{40})\b')
# ponytail: axi run returns when --wait elapses even if the pipeline goes on (default 8m; review alone took 8.6 min
# on 5-oct-2026). Two hours covers every run seen so far; a longer one ends as exit 3 and is driven again below.
NO_MISTAKES_WAIT = "2h"
# `--yes` resolves gates only while its `axi run` lives, so delivery drives the run again until it ends. 6 x 2h = 12h.
NO_MISTAKES_MAX_DRIVES = 6
FINAL = re.compile(r'^\s*(?:outcome|status): "?(?:checks-passed|passed|passed-with-skips|failed|cancelled|completed)\b',
                   re.M)


def terminal():
    """The pane's terminal, for the no-mistakes TUI: under atm-run.py the worker's stdout is stdout.txt.
    None without a controlling terminal (CI), and then there is no TUI to show."""
    try:
        return open("/dev/tty", "r+b", buffering=0)
    except OSError:
        return None


def deliver(worktree: str, source_repo: str, title: str, unit_number: int, artifact_dir: Path,
            closes: int | None = None) -> dict:
    """Hand the green clone to no-mistakes: a branch with the work committed, the source repo's
    origin, then `axi run --yes`, again while `axi status` shows no outcome, with `attach` showing the TUI in
    this pane until the run ends. The PR is no-mistakes' job; ATM records what `axi status` says at the end."""
    result = {"tool": "no-mistakes", "branch": None, "head_sha": None, "run_id": None, "pr_url": None}
    origin = git_lines(source_repo, "remote", "get-url", "origin")
    if not origin:
        return result | {"error": f"{source_repo} has no origin remote"}
    slug = re.sub(r"[^a-z0-9]+", "-", title.lower()).strip("-")[:40] or "fix"
    result["branch"] = f"atm/{slug}-{datetime.now().strftime('%Y%m%d-%H%M%S')}"
    try:
        subprocess.run(["git", "checkout", "-q", "-b", result["branch"]], cwd=worktree, check=True, capture_output=True)
        if git_lines(worktree, "status", "--porcelain", "--", ".", ":!.atm"):
            commit_unit(worktree, unit_number, title, closes)
        subprocess.run(["git", "remote", "set-url", "origin", origin[0]], cwd=worktree, check=True, capture_output=True)
    except subprocess.CalledProcessError as exc:
        return result | {"error": f"branch preparation failed: {(exc.stderr or b'').decode(errors='replace').strip()}"}
    try:  # a fresh clone is unknown to no-mistakes; init registers it, and on a registered repo it only refreshes
        init = subprocess.run(["no-mistakes", "init"], cwd=worktree, capture_output=True, text=True)
    except OSError as exc:
        return result | {"error": f"no-mistakes did not start: {exc}"}
    if init.returncode:
        return result | {"error": f"no-mistakes init failed: {(init.stdout + init.stderr).strip()}"}
    run_log = artifact_dir / "no-mistakes-run.txt"
    term, attach = terminal(), None
    try:
        with run_log.open("w") as sink:
            for result["drives"] in range(1, NO_MISTAKES_MAX_DRIVES + 1):
                run = subprocess.Popen(["no-mistakes", "axi", "run", "--yes", "--intent", title, "--wait", NO_MISTAKES_WAIT],
                                       cwd=worktree, stdin=subprocess.DEVNULL, stdout=sink, stderr=subprocess.STDOUT)
                while True:
                    if term and (attach is None or attach.poll() is not None):
                        # ponytail: re-opened while the run lives, because attach exits at once when the daemon has not
                        # registered the run yet, and when the human quits the TUI. At most one start per second.
                        attach = subprocess.Popen(["no-mistakes", "attach"], cwd=worktree, stdin=term, stdout=term, stderr=term)
                    try:
                        result["exit_code"] = run.wait(timeout=1)
                        break
                    except subprocess.TimeoutExpired:
                        pass
                status = subprocess.run(["no-mistakes", "axi", "status"], cwd=worktree, capture_output=True, text=True)
                text = status.stdout + status.stderr
                output = run_log.read_text(errors="replace")
                if status.returncode:
                    detail = text.strip() or f"exit code {status.returncode}"
                    result["error"] = f"no-mistakes status failed: {detail}"
                    break
                if "protected-path-refusal" in text + output:
                    result["error"] = "no-mistakes stopped at a protected-path refusal gate that --yes cannot resolve"
                    break
                if FINAL.search(text + output) or not RUN_ID.search(text):  # ended, or there is no run to drive
                    break
            else:
                result["error"] = f"no-mistakes run had no outcome after {NO_MISTAKES_MAX_DRIVES} drives"
    except OSError as exc:
        return result | {"error": f"no-mistakes did not start: {exc}"}
    finally:
        if attach:
            try:
                attach.wait(timeout=5)  # the TUI may close itself at the end of the run; if not, it is closed here
            except subprocess.TimeoutExpired:
                attach.terminate()
                attach.wait()
        if term:
            term.close()
    (artifact_dir / "no-mistakes-status.txt").write_text(text)
    run_id = RUN_ID.search(text) or RUN_ID.search(output)
    pr = PR_URL.search(text) or PR_URL.search(output)
    head = HEAD_SHA.search(text)
    result.update(run_id=run_id.group(1) if run_id else None, pr_url=pr.group(0) if pr else None,
                  head_sha=head.group(1) if head else git_lines(worktree, "rev-parse", "HEAD")[0])
    return result


def stage_follow_up_as_unit(worktree: str, follow_up: dict, unit_number: int, log) -> str:
    """Move the chained follow-up's red test into place and park this unit's .atm/follow-ups so the
    next unit starts with an empty slot list. Returns the red test's repo-relative path."""
    rel = follow_up["red_test"]
    index = follow_up["index"]
    source = Path(worktree) / ".atm" / "follow-ups" / str(index) / rel
    target = resolve_repo_path(rel, worktree)
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(source, target)
    parked = Path(worktree) / ".atm" / "units" / str(unit_number - 1)
    parked.mkdir(parents=True, exist_ok=True)
    shutil.move(str(Path(worktree) / ".atm" / "follow-ups"), str(parked / "follow-ups"))
    log("unit_staged", {"unit": unit_number, "red_test": rel, "from_follow_up": index})
    return rel


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
    """Return argv and the stdin payload. `--harness-arg` values go right before the brief."""
    cmd, stdin = _harness_command(args, worktree, brief, schema_path, last_msg_path)
    extra = list(getattr(args, "harness_arg", None) or [])
    if not extra:
        return cmd, stdin
    if stdin is not None:
        return cmd + extra, stdin
    return cmd[:-1] + extra + cmd[-1:], stdin


def _harness_command(args, worktree: str, brief: str, schema_path: str, last_msg_path: str) -> tuple[list[str], str | None]:
    if args.harness == "claude":
        # In -p mode the CLI sends thinking display "omitted" unless told otherwise, so Opus returns
        # empty thinking blocks. "summarized" is what the TUI uses (showThinkingSummaries) and the
        # richest mode the API offers for Opus 5.5; there is no "full" display. Verified 4-oct-2026.
        cmd = [args.harness_bin or "claude", "-p", "--output-format", "stream-json", "--verbose",
               "--include-partial-messages", "--thinking-display", "summarized",
               "--permission-mode", "acceptEdits",
               "--allowedTools", "Read", "Edit", "Write", "Bash", "Glob", "Grep"]
        if args.model:
            cmd += ["--model", args.model]
        if getattr(args, "effort", None):
            cmd += ["--effort", args.effort]
        return cmd, brief
    if args.harness == "codex":
        cmd = [args.harness_bin or "codex", "exec", "--json", "-C", worktree, "--sandbox", "workspace-write",
               "--output-schema", schema_path, "-o", last_msg_path]
        if args.model:
            cmd += ["-c", f"model={json.dumps(args.model)}"]
        if getattr(args, "effort", None):
            # Without this Codex inherits ~/.codex/config.toml, which ran every run of 4-oct at "low".
            cmd += ["-c", f"model_reasoning_effort={json.dumps(args.effort)}"]
        return cmd + [brief], None
    if args.harness == "opencode":
        # --pure: no external plugins. The user's global AGENTS.md and skills still load unless
        # OPENCODE_CONFIG / OPENCODE_CONFIG_DIR point elsewhere (pass them with --env): with them in,
        # the prompt to a local 27B was 43k tokens, nine minutes before the first answer (4-oct-2026).
        cmd = [args.harness_bin or "opencode", "run", "--pure", "--format", "json", "--dir", worktree]
        if args.model:
            cmd += ["-m", args.model]
        return cmd + [brief], None
    # pi: bare on purpose. Extensions, skills, prompt templates and context files multiplied the
    # prompt by 27 on a local model; the brief is the whole context the unit needs.
    cmd = [args.harness_bin or "pi", "-p", "--mode", "json", "--no-extensions", "--no-skills",
           "--no-prompt-templates", "--no-context-files", "--no-session"]
    if args.model:
        cmd += ["--model", args.model]
    if getattr(args, "effort", None):
        cmd += ["--thinking", args.effort]
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
        if event.get("subtype") in ("thinking_tokens", "hook_started", "hook_response"):
            return None  # bookkeeping noise; the JSONL log keeps it
        label = f"[{event.get('subtype', 'system')}]"
        return f"{label} model={event['model']}" if event.get("model") else label
    if kind in ("assistant", "user"):
        parts = []
        for block in (event.get("message") or {}).get("content") or []:
            if not isinstance(block, dict):
                continue
            if block.get("type") == "thinking" and block.get("thinking", "").strip():
                # The whole summarized thinking, not a snippet: this is what tells us what the agent is doing.
                parts.append("[thinking]\n" + textwrap.indent(block["thinking"].strip(), "    "))
            elif block.get("type") == "text":
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
    part = event.get("part")
    if isinstance(part, dict):  # opencode
        if kind == "text":
            return f"[text] {_snippet(part.get('text', ''))}"
        if kind in ("tool", "tool_use"):  # opencode 1.18 emits tool_use with part.type == "tool"
            state = part.get("state") or {}
            return f"[tool] {part.get('tool')} ({state.get('status', '')}): {_snippet(json.dumps(state.get('input', {})), 120)}"
        return None
    message = event.get("message")
    if kind == "message_end" and isinstance(message, dict) and message.get("role") == "assistant":  # pi
        parts = []
        for block in message.get("content") or []:
            if not isinstance(block, dict):
                continue
            if block.get("type") == "thinking" and block.get("thinking", "").strip():
                parts.append("[thinking]\n" + textwrap.indent(block["thinking"].strip(), "    "))
            elif block.get("type") == "text" and block.get("text", "").strip():
                parts.append(f"[text] {_snippet(block['text'])}")
            elif block.get("type") == "toolCall":
                parts.append(f"[tool] {block.get('name')}: {_snippet(json.dumps(block.get('arguments', {})), 120)}")
        return " | ".join(parts) or None
    if kind in ("message_update", "message_start", "message_end", "turn_start", "turn_end", "agent_start",
                "agent_end", "agent_settled", "session", "tool_execution_start", "tool_execution_update",
                "tool_execution_end"):
        return None  # pi bookkeeping, and user/custom message_end
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
    if harness == "opencode" and event.get("type") == "text":
        return (event.get("part") or {}).get("text")
    if harness == "pi" and event.get("type") == "message_end":
        message = event.get("message") or {}
        if message.get("role") == "assistant":
            texts = [b.get("text", "") for b in message.get("content") or [] if isinstance(b, dict) and b.get("type") == "text"]
            return "\n".join(t for t in texts if t) or None
    return None


def run_harness(cmd, stdin_text, *, cwd, timeout, harness, write, extra_env: dict | None = None) -> dict:
    env = os.environ.copy()
    env.pop("CLAUDE_CODE_CHILD_SESSION", None)
    env.update(extra_env or {})
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
            write(event)  # the log is the stream; scripts/tail-run.py is what renders it
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


def run_command(command: str | None, workdir: str, config: dict, timeout: int, log) -> tuple[bool | None, str | None]:
    """(ok, tail): on failure, the last 60 lines of combined output, also logged as command_failed."""
    if not command:
        return None, None
    env = build_command_env(config)
    env.setdefault("CI", "1")
    try:
        result = subprocess.run(shlex.split(command), cwd=workdir, env=env, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, text=True, timeout=timeout)
    except subprocess.TimeoutExpired as exc:
        output, failure = exc.output, {"timeout": timeout}
    except OSError as exc:
        output, failure = str(exc), {"error": str(exc)}
    else:
        if result.returncode == 0:
            return True, None
        output, failure = result.stdout, {"exit_code": result.returncode}
    if isinstance(output, bytes):  # TimeoutExpired keeps raw bytes even with text=True
        output = output.decode(errors="replace")
    tail = "\n".join((output or "").splitlines()[-60:])
    log("command_failed", {"command": command, **failure, "output_tail": tail})
    return False, tail


def worktree_fingerprint(workdir: str) -> str:
    """Hash of the content of every path that differs from HEAD, tracked or untracked.
    Staging is ignored: restore_worktree unstages everything, and that is not a change."""
    import hashlib
    h = hashlib.sha256()
    git = lambda *a: subprocess.run(["git", *a, "-z"], cwd=workdir, capture_output=True).stdout.split(b"\0")
    paths = set(git("diff", "HEAD", "--name-only", "--no-renames")) | set(git("ls-files", "--others", "--exclude-standard"))
    for rel in sorted(paths - {b""}):
        path = Path(workdir) / os.fsdecode(rel)
        h.update(rel + b"\0")
        if path.is_symlink():
            h.update(b"symlink\0" + os.fsencode(os.readlink(path)))
        elif path.is_file():
            h.update(str(path.stat().st_mode & 0o111).encode() + b"\0")
            h.update(path.read_bytes())
        else:
            h.update(b"\0deleted\0")
    return h.hexdigest()


# slopslint v0.3.0's families for a standing (non-duplication, non-orphan) tombstone.
SLOP_FAMILIES = ("agent_artifact_in_repo", "documented_as_convention", "environment_layout_coupling", "format_churn",
                 "inline_foreign_language", "mock_heavy_test", "runtime_dependency", "self_validating_test",
                 "speculative_feature", "speculative_hardening", "subprocess_foreign_interpreter", "test_weakening")
PONYTAIL_SCHEMA = {
    "type": "object",
    "properties": {
        "findings": {"type": "array", "items": {
            "type": "object",
            "properties": {"file": {"type": "string"}, "family": {"type": "string"}, "finding": {"type": "string"}},
            "required": ["file", "family", "finding"],
            "additionalProperties": False,
        }},
        "summary": {"type": "string"},
    },
    "required": ["findings", "summary"],
    "additionalProperties": False,
}


def build_ponytail_brief(*, title, body, worktree, diff, test_cmd) -> str:
    return f"""# Ponytail pass

## GOAL
The run below fixed this issue and passed every gate. Review its diff against the base commit for
over-engineering and cut what the fix does not need. If nothing is worth cutting, change nothing.

### {title}

{body}

## RULES
If a `ponytail-review` skill is installed in this harness, load it. Either way, follow this:
- You may only cut: delete, shrink, replace with the standard library, a native platform feature or a
  helper that already exists in this repository. No new behaviour, no new files.
- Only touch files in the diff, in `{worktree}`.
- Hunt: code for cases nobody has, reinvented standard library, unrequested abstractions or indirection,
  options with one value, parameters nobody passes, checks for states that cannot happen, parsing whose
  only output is cosmetic, functions nothing calls.
- Keep: validation at trust boundaries, error handling that prevents data loss, security, accessibility,
  anything the issue asks for, and every test. Do not delete, skip or weaken a test.
- Every test must still pass: `{test_cmd or "not detected"}`. ATM re-runs every gate and throws the pass
  away if one fails or if the diff does not get shorter.
- FORBIDDEN: `git commit`, `git push`, `git rebase`, any `gh` command.

## DIFF
```diff
{diff}```

## REPORT
Your final message must be ONLY this JSON object, nothing else, one finding per cut you made:
{{"findings": [{{"file": "<repo-relative path you cut in>", "family": "<one of: {', '.join(SLOP_FAMILIES)}>", "finding": "<one line: what you cut and what replaces it>"}}], "summary": "<one paragraph>"}}
"""


def snapshot_commit(workdir: str) -> str:
    """The whole worktree (tracked and untracked, .atm/ excluded) as a dangling commit; the worktree is not touched."""
    subprocess.run(["git", "add", "-A"], cwd=workdir, capture_output=True)
    sha = subprocess.run(["git", "stash", "create"], cwd=workdir, capture_output=True, text=True).stdout.strip()
    subprocess.run(["git", "reset", "-q"], cwd=workdir, capture_output=True)
    return sha or git_lines(workdir, "rev-parse", "HEAD")[0]


def net_added_lines(workdir: str, base: str, snapshot: str) -> int:
    rows = (line.split("\t") for line in git_lines(workdir, "diff", "--numstat", base, snapshot, "--", ".", ":!.atm"))
    return sum(int(added) - int(deleted) for added, deleted, _ in rows if added != "-")


def lint_command(workdir: str) -> str | None:
    """The target's lint: `commands.lint` of its tracked .no-mistakes.yaml, run by sh."""
    path = Path(workdir, ".no-mistakes.yaml")
    # ponytail: one plain one-line value; a quoted or block scalar would reach sh as is and fail the pass, never pass it.
    match = re.search(r"^\s+lint:\s*(.+)$", path.read_text(), re.M) if path.is_file() else None
    return f"sh -c {shlex.quote(match.group(1).strip())}" if match else None


def write_tombstones(workdir: str, findings: list[dict]) -> list[str]:
    """One standing slopslint tombstone per kept ponytail finding, when the target has .slop/."""
    if not Path(workdir, ".slop").is_dir():
        return []
    stamp, written = datetime.now(), []
    for n, finding in enumerate(findings, start=1):
        artifact = str(finding.get("file") or "").removeprefix("./")
        try:
            if not artifact or not resolve_repo_path(artifact, workdir).is_file():
                continue  # slopslint refuses an artifact that is not a file in the repo
        except ValueError:
            continue
        family = finding.get("family") if finding.get("family") in SLOP_FAMILIES else "speculative_feature"
        example = str(finding.get("finding") or "cut by the ponytail pass")
        tid = f"T-PONYTAIL-{stamp:%Y%m%d-%H%M%S}-{n}"
        q = json.dumps  # a JSON string is a valid YAML scalar
        rel = f".slop/tombstones/{tid}.yml"
        Path(workdir, rel).parent.mkdir(parents=True, exist_ok=True)
        Path(workdir, rel).write_text(f"""schema: 1
id: {tid}
status: accepted
category: alien_code
title: {q(_snippet(example, 100))}
created_at: {stamp:%Y-%m-%d}
incident:
  pattern: {q(example)}
  what_went_wrong: "An ATM run wrote it and ATM's ponytail pass cut it before delivery."
  root_cause: "The coding agent wrote more than the issue needed."
  rule_established: "Cut by the ponytail pass; it does not come back."
  evidence:
    - family: {family}
      example: {q(example)}
      artifact: {artifact}
match:
  family: {family}
  artifact: {artifact}
""")
        written.append(rel)
    return written


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
        closes = None if args.issue_file else args.issue_number  # load_issue read it from GitHub
        wt = prepare_run_clone(args.repo, base_ref=args.base_ref, run_root=args.run_root)
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
    last_msg_path = artifact_dir / "last-message.txt"
    issue_text = f"{title}\n{body}"
    max_units = args.max_units or int(config.get("harness_worker", {}).get("max_units", 3))
    timeout = int(config.get("environment", {}).get("timeout", 300))

    def call_harness(harness_brief: str, schema: dict) -> tuple[dict, dict | None, str | None]:
        if args.harness == "codex":
            schema_path.write_text(json.dumps(schema, indent=2))
        cmd, stdin_text = harness_command(args, worktree, harness_brief, str(schema_path), str(last_msg_path))
        try:
            run = run_harness(cmd, stdin_text, cwd=worktree, timeout=args.timeout, harness=args.harness, write=write,
                              extra_env=dict(kv.split("=", 1) for kv in args.env if "=" in kv))
        except OSError as exc:
            run = {"exit_code": None, "timed_out": False, "duration_seconds": 0, "final_text": None}
            log("harness_failed", {"error": str(exc)})
        if args.harness == "codex" and last_msg_path.is_file():
            run["final_text"] = last_msg_path.read_text() or run["final_text"]
        return run, *extract_report(run["final_text"])

    def red_green(test_file: str | None) -> tuple[bool, str]:
        if not test_file:
            return False, "no test file found"
        red_outputs: list[str] = []

        def log_capturing_red(name: str, data: dict) -> None:
            if name == "verify":
                red_outputs.append(str(data.get("red_output_full") or data.get("red_output", "")))
            log(name, data)

        before = worktree_fingerprint(worktree)
        verified, verify_msg = verification.verify_red_green(
            test_file, workdir=worktree, config=config, emit=print, log=log_capturing_red,
        )
        if worktree_fingerprint(worktree) != before:
            # The verdict is about code that is no longer what the agent left: never trust it.
            log("verify_worktree_changed", {"test_file": test_file})
            return False, "WORKTREE CHANGED DURING VERIFICATION: the red/green verdict is void"
        invalid_red = red_failed_on_missing_module(red_outputs[-1] if red_outputs else "")
        if verified and invalid_red:
            log("verify_invalid_red", {"test_file": test_file, "marker": invalid_red})
            return False, f"INVALID RED: without the fix the test fails on a missing module ({invalid_red}), not on behavior"
        return verified, verify_msg

    def run_checks(test_file: str | None, changed: list[str], check_scope_globs: list[str], base: str) -> dict:
        """Every gate after red/green, against `base`; `ok` is the conjunction."""
        quality_ok, quality_msg = quality.run_quality_checks(
            test_file or (changed[0] if changed else ""), workdir=worktree, config=config, log=log,
            is_test_file_path=lambda p: is_test_file_path(p, config), get_changed_files_fn=lambda: changed,
            base_ref=base,
        )
        if quality_ok and lang_key not in QUALITY_LANG.values():
            hits = scan_forbidden(worktree, changed, forbidden, base)
            if hits:
                quality_ok, quality_msg = False, "forbidden patterns: " + "; ".join(hits[:5])
                log("quality_forbidden", {"hits": hits})
        gate_ok, gate_msg = check_gate(worktree, base, config)
        full_tests_ok, full_tests_tail = run_command(test_cmd, worktree, config, timeout, log)
        typecheck_ok, typecheck_tail = run_command(typecheck_cmd, worktree, config, timeout, log)
        scope_ok, scope_msg = check_scope(worktree, base, config, check_scope_globs)
        return {
            "ok": quality_ok and gate_ok and scope_ok and full_tests_ok is not False and typecheck_ok is not False,
            "quality_ok": {"ok": quality_ok, "message": quality_msg}, "gate_ok": {"ok": gate_ok, "message": gate_msg},
            "scope_ok": {"ok": scope_ok, "message": scope_msg}, "full_tests_ok": full_tests_ok,
            "typecheck_ok": typecheck_ok, "full_tests_tail": full_tests_tail, "typecheck_tail": typecheck_tail,
        }

    def run_unit(number: int, unit_brief: str, unit_scope: list[str], unit_base: str, forced_test: str | None) -> dict:
        print(f"[HARNESS] unit {number} {args.harness} timeout={args.timeout}s log={log_path}")
        log("unit_started", {"unit": number, "base_sha": unit_base, "scope": unit_scope})
        run, harness_report, parse_error = call_harness(unit_brief, REPORT_SCHEMA)
        log("harness_done", {k: v for k, v in run.items() if k != "final_text"} | {"unit": number, "report": harness_report,
                                                                                    "report_parse_error": parse_error})
        changed = [p for p in quality.get_changed_files(worktree) if not is_atm_path(p)]
        test_file = forced_test or pick_test_file(harness_report, worktree, config)
        verified, verify_msg = red_green(test_file)
        checks = run_checks(test_file, changed, unit_scope, unit_base)
        follow_ups = validate_follow_ups(harness_report, worktree, config, log, base_sha=unit_base, issue_text=issue_text,
                                         issue={"title": title, "body": body})
        for f in follow_ups:
            f["unit"] = number
        unit = {
            "unit": number, "base_sha": unit_base, "scope": unit_scope, "changed_files": changed, "test_file": test_file,
            "verified": {"ok": verified, "message": verify_msg}, "follow_ups": follow_ups,
            **{k: v for k, v in checks.items() if k != "ok"},
            "duration_seconds": run["duration_seconds"], "timed_out": run["timed_out"],
            "harness_exit_code": run["exit_code"], "report_parse_error": parse_error, "passed": verified and checks["ok"],
        }
        log("unit_done", unit)
        return unit

    def ponytail_pass(units: list[dict]) -> dict:
        """One harness call that may only cut the run's diff, gated like a unit; anything short of a shorter,
        fully green diff brings the pre-ponytail worktree back."""
        head = git_lines(worktree, "rev-parse", "HEAD")[0]
        before = snapshot_commit(worktree)
        run_files = git_lines(worktree, "diff", "--name-only", base_sha, before, "--", ".", ":!.atm")
        diff = subprocess.run(["git", "diff", base_sha, before, "--", ".", ":!.atm"], cwd=worktree,
                              capture_output=True, text=True).stdout
        pony_brief = build_ponytail_brief(title=title, body=body, worktree=worktree, diff=diff, test_cmd=test_cmd)
        (artifact_dir / "brief-ponytail.md").write_text(pony_brief)
        print(f"[PONYTAIL] {args.harness} timeout={args.timeout}s", flush=True)
        log("ponytail_started", {"head": head, "snapshot": before})
        run, pony_report, parse_error = call_harness(pony_brief, PONYTAIL_SCHEMA)
        log("harness_done", {k: v for k, v in run.items() if k != "final_text"} | {"unit": "ponytail", "report": pony_report,
                                                                                    "report_parse_error": parse_error})
        findings = [f for f in (pony_report or {}).get("findings") or [] if isinstance(f, dict)]
        after = snapshot_commit(worktree)
        record = {"findings": findings, "net_lines_before": net_added_lines(worktree, base_sha, before),
                  "net_lines_after": net_added_lines(worktree, base_sha, after), "kept": False, "tombstones": []}
        if git_lines(worktree, "rev-parse", "HEAD") != [head]:
            reason = "harness moved HEAD (created commits)"
        elif record["net_lines_after"] >= record["net_lines_before"]:
            reason = "did not reduce the run's net added lines"
        else:
            # With HEAD at the base commit the clone looks like one unit holding the whole run.
            subprocess.run(["git", "reset", "-q", base_sha], cwd=worktree, check=True, capture_output=True)
            try:
                failed = [f"unit {u['unit']} red/green: {msg}" for u in units
                          for ok, msg in [red_green(u["test_file"])] if not ok]
                changed = [p for p in quality.get_changed_files(worktree) if not is_atm_path(p)]
                checks = run_checks(units[0]["test_file"], changed, run_files, base_sha)
                failed += [f"{name} failed: {checks[name]['message']}" for name in ("quality_ok", "gate_ok", "scope_ok")
                           if not checks[name]["ok"]]
                failed += [f"{name} failed" for name in ("full_tests_ok", "typecheck_ok") if checks[name] is False]
                lint_ok, _ = run_command(lint_command(worktree), worktree, config, timeout, log)
                failed += ["lint failed"] if lint_ok is False else []
            finally:
                subprocess.run(["git", "reset", "-q", head], cwd=worktree, check=True, capture_output=True)
            reason = "; ".join(failed)
        if reason:  # throw away everything since the snapshot, new files included, and bring the snapshot back
            subprocess.run(["git", "add", "-A"], cwd=worktree, capture_output=True)
            subprocess.run(["git", "reset", "--hard", "-q", head], cwd=worktree, capture_output=True)
            verification.restore_worktree(worktree, before)
        else:
            record["kept"], reason = True, "shorter diff, every gate passed"
            record["tombstones"] = write_tombstones(worktree, findings)
        record["reason"] = reason
        log("ponytail", record)
        return record

    units: list[dict] = []
    unit_brief, unit_scope, unit_base, forced_test = brief, scope, base_sha, None
    while True:
        number = len(units) + 1
        unit = run_unit(number, unit_brief, unit_scope, unit_base, forced_test)
        units.append(unit)
        if not unit["passed"] or number >= max_units:
            break
        chainable = [f for f in unit["follow_ups"] if f.get("chainable")]
        if not chainable:
            break
        nxt = chainable[0]
        unit_base = commit_unit(worktree, number, title if number == 1 else str(units[-1].get("title") or "follow-up"),
                                closes)
        unit["committed_as"] = unit_base
        forced_test = stage_follow_up_as_unit(worktree, nxt, number + 1, log)
        nxt["chained_as_unit"] = number + 1
        unit_scope = list(nxt.get("paths") or [])
        unit_brief = build_unit_brief(number=number + 1, follow_up=nxt, issue_title=title, issue_body=body,
                                      worktree=worktree, test_cmd=test_cmd, typecheck_cmd=typecheck_cmd, forbidden=forbidden)
        (artifact_dir / f"brief-unit-{number + 1}.md").write_text(unit_brief)
        units[-1]["title"] = nxt.get("title")
        print(f"[CHAIN] follow-up {nxt['index']} of unit {number} becomes unit {number + 1}: {nxt.get('title')!r} scope={unit_scope}")

    all_follow_ups = [f for u in units for f in u["follow_ups"]]
    accepted = [f for f in all_follow_ups if f["accepted"] and not f.get("chained_as_unit")]
    if accepted:
        (artifact_dir / "follow-ups.json").write_text(json.dumps(accepted, indent=2))
    changed_all = sorted({p for u in units for p in u["changed_files"]} | {
        p for u in units if u.get("committed_as") for p in git_lines(worktree, "diff", "--name-only", base_sha, u["committed_as"])
        if not is_atm_path(p)})
    failed_unit = next((u for u in units if not u["passed"]), None)
    last = units[-1]
    passed = failed_unit is None
    ponytail = ponytail_pass(units) if passed else None
    result = {
        "harness": args.harness, "model": args.model, "effort": args.effort, "base_ref": args.base_ref, "base_sha": base_sha,
        "head_sha": git_lines(worktree, "rev-parse", "HEAD")[0], "worktree": worktree,
        "brief": str(brief_path), "log": str(log_path), "changed_files": changed_all, "test_file": units[0]["test_file"],
        "verified": {"ok": all(u["verified"]["ok"] for u in units),
                     "message": (failed_unit or last)["verified"]["message"]},
        "quality_ok": {"ok": all(u["quality_ok"]["ok"] for u in units), "message": (failed_unit or last)["quality_ok"]["message"]},
        "gate_ok": {"ok": all(u["gate_ok"]["ok"] for u in units), "message": (failed_unit or last)["gate_ok"]["message"]},
        "scope_ok": {"ok": all(u["scope_ok"]["ok"] for u in units), "message": (failed_unit or last)["scope_ok"]["message"]},
        "follow_ups": all_follow_ups, "units": units, "max_units": max_units,
        "full_tests_ok": last["full_tests_ok"], "typecheck_ok": last["typecheck_ok"],
        "duration_seconds": sum(u["duration_seconds"] for u in units), "timed_out": any(u["timed_out"] for u in units),
        "harness_exit_code": last["harness_exit_code"], "report_parse_error": last["report_parse_error"],
    }
    if ponytail:
        result["ponytail"] = ponytail
    delivery = None
    if passed and not args.no_deliver:
        print(f"[DELIVER] no-mistakes in {worktree}", flush=True)
        delivery = result["delivery"] = deliver(worktree, args.repo, title, len(units), artifact_dir, closes)
        log("delivery", delivery)
    (artifact_dir / "report.json").write_text(json.dumps(result, indent=2))
    log("report", result)
    log_fh.close()
    print("\n=== HARNESS WORKER SUMMARY ===")
    print(f"harness:    {args.harness} model={args.model or 'default'} effort={args.effort or 'default'} exit={last['harness_exit_code']}")
    print(f"duration:   {result['duration_seconds']}s timed_out={result['timed_out']} units={len(units)}/{max_units}")
    print(f"worktree:   {worktree}")
    for u in units:
        print(f"unit {u['unit']}:     {'PASS' if u['passed'] else 'FAIL'} test={u['test_file'] or 'none'} "
              f"changed={', '.join(u['changed_files']) or 'none'}")
        print(f"            red/green {'PASS' if u['verified']['ok'] else 'FAIL'} {_snippet(u['verified']['message'], 90)}")
        print(f"            quality {'PASS' if u['quality_ok']['ok'] else 'FAIL'} gate {'PASS' if u['gate_ok']['ok'] else 'FAIL'} "
              f"scope {'PASS' if u['scope_ok']['ok'] else 'FAIL'} full={u['full_tests_ok']} typecheck={u['typecheck_ok']}")
        for label, tail in (("full suite", u["full_tests_tail"]), ("typecheck", u["typecheck_tail"])):
            if tail is not None:
                print(f"            {label} output (last 60 lines):\n{textwrap.indent(tail, '              ')}")
        for f in u["follow_ups"]:
            state = (f"chained as unit {f['chained_as_unit']}" if f.get("chained_as_unit")
                     else "accepted, not chained: " + f.get("chain_reason", "") if f["accepted"]
                     else "rejected: " + f.get("reason", ""))
            print(f"            follow-up {f['index']}: {state}")
    print(f"changed:    {', '.join(changed_all) or 'none'}")
    if ponytail:
        saved = ponytail["net_lines_before"] - ponytail["net_lines_after"] if ponytail["kept"] else 0
        print(f"ponytail:   {'kept' if ponytail['kept'] else 'discarded'}, {saved} net lines saved ({ponytail['reason']})")
    if delivery:
        print(f"delivery:   branch={delivery['branch']} run={delivery['run_id']} pr={delivery['pr_url'] or 'none'} "
              f"{delivery.get('error', '')}")
    print(f"RESULT:     {'PASS' if passed else 'FAIL'} report={artifact_dir / 'report.json'}")
    if not passed:
        return 1
    return 3 if delivery and (delivery.get("error") or not delivery["pr_url"]) else 0


if __name__ == "__main__":
    raise SystemExit(main())
