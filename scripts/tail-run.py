#!/usr/bin/env python3
"""Follow a harness-worker run log (worker-*.jsonl) in readable form.

    tail-run.py                          newest run under .tmp/harness-worker
    tail-run.py .tmp/harness-worker/X    newest worker-*.jsonl in that run directory
    tail-run.py path/worker-....jsonl    that log
    tail-run.py --no-follow              print what is there and exit

Agent events are rendered the way the worker prints them live. ATM's own events
(`atm.*`) get one line each, more for a finished unit. Following stops when the run
writes its report, fails to prepare, or leaves an exit.txt beside the log.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
import time
from datetime import datetime
from pathlib import Path

HERE = Path(__file__).resolve().parent.parent
RUNS = HERE / ".tmp" / "harness-worker"  # where scripts/atm-run.py puts runs
sys.path.insert(0, str(HERE))

from agentic_tdd_runner.harness_worker import summarize_event  # noqa: E402

END_EVENTS = {"atm.report", "atm.prepare_failed"}
BARE_LABEL = re.compile(r"^\[[\w.-]+\]$")


def newest_log(path: Path) -> Path | None:
    if path.is_file():
        return path
    logs = list(path.glob("worker-*.jsonl")) + list(path.glob("*/worker-*.jsonl")) if path.is_dir() else []
    return max(logs, key=lambda p: p.stat().st_mtime) if logs else None


def ok(flag) -> str:
    return "PASS" if flag else "FAIL"


def unit_lines(e: dict) -> list[str]:
    lines = [f"=== unit {e.get('unit')}: {ok(e.get('passed'))} ==="]
    for key, label in (("verified", "red/green"), ("quality_ok", "quality"), ("gate_ok", "gate"), ("scope_ok", "scope")):
        check = e.get(key) or {}
        lines.append(f"  {label:<10} {ok(check.get('ok'))} {check.get('message', '')}".rstrip())
    lines.append(f"  full tests={e.get('full_tests_ok')} typecheck={e.get('typecheck_ok')} "
                 f"agent exit={e.get('harness_exit_code')} timed_out={e.get('timed_out')} {e.get('duration_seconds')}s")
    lines.append(f"  test file  {e.get('test_file') or 'none'}")
    lines.append(f"  changed    {', '.join(e.get('changed_files') or []) or 'nothing'}")
    for f in e.get("follow_ups") or []:
        state = ("chainable" if f.get("chainable") else "accepted, not chained" if f.get("accepted") else "rejected")
        lines.append(f"  follow-up {f.get('index')}: {state}: {f.get('title')}")
    return lines


def render_atm(name: str, e: dict) -> list[str]:
    if name == "scope":
        return [f"[atm] scope ({e.get('source')}): {', '.join(e.get('paths') or []) or 'open'}"]
    if name == "prepared":
        return [f"[atm] prepared {e.get('worktree')} base={str(e.get('base_sha', ''))[:10]} "
                f"tests={e.get('test_command')!r} typecheck={e.get('typecheck_command')!r}"]
    if name == "prepare_failed":
        return [f"[atm] PREPARE FAILED: {e.get('error')}"]
    if name == "unit_started":
        return ["", f"=== unit {e.get('unit')} started === base={str(e.get('base_sha', ''))[:10]} "
                    f"scope={', '.join(e.get('scope') or []) or 'open'}"]
    if name == "harness_failed":
        return [f"[atm] agent did not start: {e.get('error')}"]
    if name == "harness_done":
        report = e.get("report") or {}
        lines = [f"[atm] unit {e.get('unit')} agent finished: exit={e.get('exit_code')} timed_out={e.get('timed_out')} "
                 f"{e.get('duration_seconds')}s test_file={report.get('test_file')} "
                 f"follow_ups={len(report.get('follow_ups') or [])}"]
        if e.get("report_parse_error"):
            lines.append(f"      no report: {e['report_parse_error']}")
        if report.get("summary"):
            lines.append(f"      summary: {report['summary']}")
        return lines
    if name == "verify":
        red = "PASS (bad)" if e.get("red_passed") else "FAIL (good)"
        green = "PASS (good)" if e.get("green_passed") else "FAIL (bad)"
        return [f"[atm] red/green {e.get('test_file')}: without fix {red}, with fix {green}"]
    if name == "verify_worktree_changed":
        return [f"[atm] worktree changed during verification of {e.get('test_file')}: verdict void"]
    if name == "verify_invalid_red":
        return [f"[atm] invalid red for {e.get('test_file')}: fails on a missing module ({e.get('marker')})"]
    if name == "quality_forbidden":
        return [f"[atm] forbidden patterns: {'; '.join(e.get('hits') or [])}"]
    if name in ("duplicated_setup_judge", "duplicated_setup_judge_error"):
        return [f"[atm] duplicated-setup judge {e.get('file')}: {e.get('decision') or e.get('error')}"]
    if name == "follow_up_rejected":
        return [f"[atm] follow-up {e.get('index')} rejected: {e.get('title')} ({e.get('reason')})"]
    if name == "follow_up_not_chainable":
        return [f"[atm] follow-up {e.get('index')} accepted, not chained: {e.get('title')} ({e.get('chain_reason')})"]
    if name == "unit_staged":
        return [f"[atm] follow-up {e.get('from_follow_up')} becomes unit {e.get('unit')}: red test {e.get('red_test')}"]
    if name == "unit_done":
        return unit_lines(e)
    if name == "report":
        units = e.get("units") or []
        passed = bool(units) and all(u.get("passed") for u in units)
        return ["", f"=== RESULT: {ok(passed)} === units={len(units)}/{e.get('max_units')} {e.get('duration_seconds')}s "
                    f"harness={e.get('harness')} model={e.get('model') or 'default'}",
                f"  changed  {', '.join(e.get('changed_files') or []) or 'nothing'}",
                f"  clone    {e.get('worktree')}"]
    return [f"[atm] {name} {json.dumps(e, ensure_ascii=False)[:300]}"]


def render(entry) -> list[str]:
    """Lines for one log entry: {"ts", "harness", "event"}."""
    event = entry.get("event") if isinstance(entry, dict) else entry
    kind = event.get("type", "") if isinstance(event, dict) else ""
    if kind.startswith("atm."):
        try:
            stamp = datetime.fromisoformat(entry["ts"]).astimezone().strftime("%H:%M:%S ")
        except (KeyError, TypeError, ValueError):
            stamp = ""
        lines = render_atm(kind[4:], event)
        return [stamp + line if line and not line.startswith(" ") else line for line in lines]
    summary = summarize_event(event)
    if not summary or BARE_LABEL.match(summary):
        return []  # "[status]", "[rate_limit_event]": bookkeeping the worker prints live, noise here
    return [f"  {summary}"]


def follow(log: Path, keep_following: bool) -> int:
    finished = log.parent / "exit.txt"
    pending = ""
    with log.open(encoding="utf-8", errors="replace") as fh:
        while True:
            chunk = fh.readline()
            if chunk:
                pending += chunk
                if not pending.endswith("\n"):
                    continue  # the worker is mid-write; finish the line first
                line, pending = pending.strip(), ""
                if not line:
                    continue
                try:
                    entry = json.loads(line)
                except json.JSONDecodeError:
                    print(f"  [unparsed] {line[:160]}")
                    continue
                for out in render(entry):
                    print(out, flush=True)
                event = entry.get("event") if isinstance(entry, dict) else None
                if isinstance(event, dict) and event.get("type") in END_EVENTS:
                    return 0
                continue
            if not keep_following or finished.exists():
                return 0
            time.sleep(0.5)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("path", nargs="?", help=f"worker-*.jsonl or a run directory (default: newest under {RUNS})")
    parser.add_argument("--no-follow", action="store_true", help="print the log as it is now and exit")
    args = parser.parse_args(argv)
    where = Path(args.path) if args.path else RUNS
    log = newest_log(where)
    if not log:
        print(f"No worker-*.jsonl log found under {where}", file=sys.stderr)
        return 1
    print(f"Following: {log}\n", flush=True)
    try:
        return follow(log, keep_following=not args.no_follow)
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
