#!/usr/bin/env python3
"""Follow a harness-worker log (``worker-*.jsonl``) and render it with shape.

    tail-run.py                  # newest run under .tmp/harness-worker
    tail-run.py --label <label>  # that run
    tail-run.py <path>           # a worker-*.jsonl, or the run directory holding one

Phases (PREPARE, AGENT, RED / GREEN VERIFICATION, SUMMARY), one numbered line per tool call with
its outcome under it, and the verdict. Stops at the report, at a failed prepare, or when exit.txt
appears next to the log. Colour only when stdout is a terminal and NO_COLOR is unset; every state
also has a symbol and a word.
"""
from __future__ import annotations

import argparse
import fnmatch
import json
import os
import re
import shlex
import sys
import textwrap
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent.parent
RUNS = HERE / ".tmp" / "harness-worker"
sys.path.insert(0, str(HERE))
from agentic_tdd_runner.harness_worker import _snippet, summarize_event  # after the sys.path line on purpose

RULE = "─" * 48
NOISE = {"hook_progress", "status", "tool_progress", "task_started", "task_notification", "rate_limit_event", "init",
         "stream_event", "thinking_tokens", "hook_started", "hook_response"}
COLOR = sys.stdout.isatty() and not os.environ.get("NO_COLOR")


def paint(code: str, text: str) -> str:
    return f"\x1b[{code}m{text}\x1b[0m" if COLOR else text


def bold(t): return paint("1", t)
def dim(t): return paint("2", t)
def red(t): return paint("31", t)
def green(t): return paint("32", t)
def cyan(t): return paint("36", t)


def mark(ok) -> str:
    return green("✓") if ok else "–" if ok is None else red("✗")


def shown(path) -> str:
    try:
        return str(Path(path).resolve().relative_to(Path.cwd()))
    except ValueError:
        return str(path)


class Renderer:
    def __init__(self, log: Path):
        self.log = log
        self.harness = "harness"
        self.n = 0
        self.clone = ""
        # base ref, source repo and timeout are not in the log; atm-run.py writes the worker command beside it.
        cmd = log.parent / "command.txt"
        argv = shlex.split(cmd.read_text()) if cmd.exists() else []
        self.flags = {a: b for a, b in zip(argv, argv[1:]) if a.startswith("--")}

    def strip(self, text) -> str:
        return str(text).replace(self.clone + "/", "") if self.clone else str(text)

    def phase(self, title: str) -> None:
        print(f"\n{RULE}\n{bold(title)}\n{RULE}")

    def render(self, event) -> bool:
        """Print one event. True when the run is over."""
        if not isinstance(event, dict):
            if str(event).strip():
                print(dim(f"  {_snippet(event)}"))
            return False
        kind = event.get("type", "")
        if kind in NOISE or event.get("subtype") in NOISE:
            return False
        handler = getattr(self, "on_" + kind.replace(".", "_"), None)
        if handler:
            return bool(handler(event))
        if not kind.startswith("atm."):  # other harnesses: the pane's own one-liner
            summary = summarize_event(event)
            if summary:
                print(dim(f"  {self.strip(summary)}"))
            outcome = self.tool_outcome(kind, event)
            if outcome:
                print(f"    {outcome}")
        return False

    def tool_outcome(self, kind, event) -> str | None:
        item = event.get("item") or {}
        part = event.get("part") or {}
        if item.get("type") == "command_execution" and kind.endswith("completed"):
            code = item.get("exit_code")
            output = _snippet(self.strip(item.get("aggregated_output") or ""), 120)
            status = green(f"✓ exit {code}") if code == 0 else red(f"✗ exit {code}")
            return f"{status}  {output}".rstrip()
        if isinstance(part, dict) and part.get("type") == "tool":
            state = part.get("state") or {}
            status = state.get("status")
            if status in ("completed", "error"):
                success = status == "completed"
                mark = green("✓ completed") if success else red("✗ failed")
                output = _snippet(self.strip(state.get("output") or state.get("error") or ""), 120)
                return f"{mark}  {output}".rstrip()
        if kind == "tool_execution_end":
            result = event.get("result") or {}
            failed = bool(result.get("isError"))
            mark = red("✗ failed") if failed else green("✓ completed")
            content = result.get("content") or []
            output = " ".join(c.get("text", "") for c in content if isinstance(c, dict))
            return f"{event.get('toolName') or 'tool'}  {mark}  {_snippet(self.strip(output), 120)}".rstrip()
        return None

    def on_atm_prepared(self, e):
        self.clone = e.get("worktree") or ""
        repo = self.flags.get("--repo")
        worktree = os.path.relpath(self.clone, repo) if repo and self.clone.startswith(repo.rstrip("/") + "/") else self.clone
        base = (e.get("base_sha") or "")[:10]
        self.phase("PREPARE")
        print(f"  worktree  {worktree}")
        print(f"  base      {self.flags['--base-ref'] + '@' if '--base-ref' in self.flags else ''}{base}")
        print(f"  tests     {e.get('test_command') or 'none'}")

    def on_atm_prepare_failed(self, e):
        self.phase("PREPARE")
        print(f"\n{red('✗ RESULT')}  {red('FAIL')}  prepare failed: {e.get('error')}")
        return True

    def on_atm_unit_started(self, e):
        timeout = f" timeout={self.flags['--timeout']}s" if "--timeout" in self.flags else ""
        self.phase(f"AGENT  unit {e.get('unit')} {self.harness}{timeout}")

    def on_assistant(self, e):
        for block in (e.get("message") or {}).get("content") or []:
            kind = block.get("type") if isinstance(block, dict) else None
            if kind == "thinking" and block.get("thinking", "").strip():
                lines = block["thinking"].strip().splitlines()
                print("\n" + dim("  ~ thinking"))
                for line in lines[:6] + (["…"] if len(lines) > 6 else []):
                    print(dim(f"    {line}"))
            elif kind == "text" and block.get("text", "").strip():
                print("\n" + bold(f"  ✎ {_snippet(block['text'])}"))
            elif kind == "tool_use":
                self.n += 1
                inp = block.get("input") or {}
                arg = inp.get("command") or inp.get("file_path") or inp.get("pattern") or inp.get("path") or ""
                print("\n" + cyan(f"  ▶ #{self.n} {block.get('name')}  {_snippet(self.strip(arg), 120)}"))

    def on_user(self, e):
        for block in (e.get("message") or {}).get("content") or []:
            if not isinstance(block, dict) or block.get("type") != "tool_result":
                continue
            content = block.get("content")
            if isinstance(content, list):
                content = "\n".join(c.get("text", "") for c in content if isinstance(c, dict))
            lines = self.strip(content or "").splitlines()
            code = re.match(r"Exit code (\d+)", lines[0]) if lines else None
            if code:
                lines = lines[1:]
            errors = [line for line in lines if line.startswith("E ")]
            text = " ".join(" ".join(errors or lines).split())
            text = text[:95] + " ..." if len(text) > 95 else text
            if code and code.group(1) != "0":
                print(red(f"    ✗ exit {code.group(1)}") + f"  {text}")
            elif block.get("is_error") or errors or "Traceback" in text or "hook error" in text:
                print(red("    ✗ error") + f"  {text}")
            else:
                print(green("    ✓ ok") + f"  {text}")

    def on_result(self, e):
        if e.get("is_error"):
            print("\n" + red(f"  ✗ agent failed  {e.get('subtype')} turns={e.get('num_turns')}"))
        else:
            print("\n" + green(f"  ✓ agent finished  turns={e.get('num_turns')}"))

    def on_atm_harness_done(self, e):
        if e.get("exit_code") != 0 or e.get("timed_out"):
            print("\n" + red(f"  ✗ agent failed  exit={e.get('exit_code')} timed_out={e.get('timed_out')}"))

    def on_atm_harness_failed(self, e):
        print("\n" + red(f"  ✗ agent failed  {e.get('error')}"))

    def on_atm_verify(self, e):
        self.phase("RED / GREEN VERIFICATION")
        red_ok, green_ok = not e.get("red_passed"), bool(e.get("green_passed"))
        print("  ▶ without the fix")
        print(f"    {mark(red_ok)} " + ("test fails without fix (expected)  FAIL (good)" if red_ok
                                         else "test passes without fix (bad)  PASS (BAD!)"))
        self.pytest_lines(e.get("red_output_full") or e.get("red_output"))
        print("  ▶ with the fix")
        print(f"    {mark(green_ok)} " + ("test passes with fix  PASS (good)" if green_ok
                                           else "test fails with fix  FAIL (BAD!)"))
        self.pytest_lines(e.get("green_output"))

    @staticmethod
    def pytest_lines(output) -> None:
        """Only the progress line and the final count from pytest; anything else is shown as is, briefly."""
        lines = str(output or "").splitlines()
        progress = [line for line in lines if re.match(r"\S+ [.FEsxX]+\s+\[\s*\d+%\]", line)]
        counts = [line.strip("= ") for line in lines if re.match(r"=+ .*\d+ (passed|failed|error).* in [\d.]+s", line)]
        for line in (progress + counts) or [_snippet(output or "", 95)]:
            print(dim(f"      {' '.join(line.split())}"))

    def on_atm_verify_invalid_red(self, e):
        print(red(f"    ✗ invalid red  fails on a missing module ({e.get('marker')}), not on behavior"))

    def on_atm_verify_worktree_changed(self, _):
        print(red("    ✗ worktree changed during verification  the verdict is void"))

    def on_atm_report(self, r):
        units = r.get("units") or []
        passed = bool(units) and all(u.get("passed") for u in units)
        self.phase("SUMMARY")
        print(f"  harness    {r.get('harness')} model={r.get('model') or 'default'} "
              f"effort={r.get('effort') or 'default'} exit={r.get('harness_exit_code')}")
        print(f"  duration   {r.get('duration_seconds')}s timed_out={r.get('timed_out')} "
              f"units={len(units)}/{r.get('max_units', '-')}")
        for u in units:
            word = green("✓ PASS") if u.get("passed") else red("✗ FAIL")
            print(f"  {'unit ' + str(u.get('unit')):<10} {word}  test={u.get('test_file') or 'none'}")
            for label, tail in (("full suite", u.get("full_tests_tail")), ("typecheck", u.get("typecheck_tail"))):
                if tail is not None:
                    print(f"    {label} output (last 60 lines):\n{textwrap.indent(tail, '      ')}")
        checks = [("red/green", (r.get("verified") or {}).get("ok")), ("quality", (r.get("quality_ok") or {}).get("ok")),
                  ("gate", (r.get("gate_ok") or {}).get("ok")), ("scope", (r.get("scope_ok") or {}).get("ok")),
                  ("full suite", r.get("full_tests_ok")), ("typecheck", r.get("typecheck_ok"))]
        print("  checks     " + "   ".join(f"{mark(ok)} {name}{' n/a' if ok is None else ''}" for name, ok in checks))
        print(f"  changed    {', '.join(r.get('changed_files') or []) or 'none'}")
        verdict = green("✓ RESULT") + "  " + green("PASS") if passed else red("✗ RESULT") + "  " + red("FAIL")
        print(f"\n{verdict}  report={shown(self.log.parent / 'report.json')}")
        return True


def refuse(message: str) -> None:
    print(message, file=sys.stderr)
    sys.exit(1)


def find_log(path: str | None, label: str | None) -> Path:
    if path and not Path(path).is_dir():
        if not fnmatch.fnmatch(Path(path).name, "worker-*.jsonl") or not Path(path).is_file():
            refuse(f"not a worker-*.jsonl: {path}")
        return Path(path)
    where = Path(path) if path else RUNS / label if label else RUNS
    logs = list(where.glob("worker-*.jsonl" if path or label else "*/worker-*.jsonl"))
    if not logs:
        refuse(f"no worker-*.jsonl under {where}")
    return max(logs, key=lambda p: p.stat().st_mtime)


def follow(log: Path) -> int:
    renderer = Renderer(log)
    exit_txt = log.parent / "exit.txt"
    pending = ""
    with log.open(encoding="utf-8") as fh:
        while True:
            pending += fh.readline()
            if pending.endswith("\n"):
                line, pending = pending.strip(), ""
                if not line:
                    continue
                try:
                    entry = json.loads(line)
                except json.JSONDecodeError:
                    print(dim(f"  [unparsed] {_snippet(line, 120)}"))
                    continue
                renderer.harness = entry.get("harness") or "harness"
                if renderer.render(entry.get("event")):
                    return 0
            elif exit_txt.exists():  # the worker ended without a report; everything it wrote is read
                print(f"\n{red('✗ RESULT')}  {red('FAIL')}  worker exit={exit_txt.read_text().strip()} without a report")
                return 0
            else:
                time.sleep(0.5)


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("path", nargs="?", help="a worker-*.jsonl, or the run directory holding one")
    p.add_argument("--label", help="run label under .tmp/harness-worker")
    args = p.parse_args(argv)
    log = find_log(args.path, args.label)
    print(f"Following: {shown(log)}")
    try:
        return follow(log)
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
