"""`atm-run.py tail` follows a harness worker log and renders it with shape."""

import json
import os
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
ATM_RUN = REPO_ROOT / "scripts" / "atm-run.py"
TAIL_RUN = REPO_ROOT / "scripts" / "tail-run.py"
SOURCE = "/src/repo"
CLONE = f"{SOURCE}/.worktrees/atm-run-1"


def _write_run(tmp_path: Path, events: list[dict]) -> Path:
    run = tmp_path / "run"
    run.mkdir()
    log = run / "worker-20261005T120000.jsonl"
    log.write_text("".join(json.dumps({"ts": "t", "harness": "claude", "event": e}) + "\n" for e in events))
    return run


def _tail(*args: str) -> subprocess.CompletedProcess:
    env = {**os.environ, "NO_COLOR": "1"}
    return subprocess.run([sys.executable, str(ATM_RUN), "tail", *args], capture_output=True, text=True,
                          env=env, timeout=30)


def _assistant(*blocks):
    return {"type": "assistant", "message": {"content": list(blocks)}}


def _result(content, is_error=False):
    return {"type": "user", "message": {"content": [
        {"type": "tool_result", "tool_use_id": "t", "content": content, "is_error": is_error}]}}


REPORT = {
    "type": "atm.report", "harness": "claude", "model": "m", "effort": "high", "harness_exit_code": 0,
    "duration_seconds": 12.5, "timed_out": False, "max_units": 3,
    "units": [{"unit": 1, "passed": False, "test_file": "tests/test_x.py"}],
    "verified": {"ok": True}, "quality_ok": {"ok": True}, "gate_ok": {"ok": True}, "scope_ok": {"ok": False},
    "full_tests_ok": True, "typecheck_ok": None, "changed_files": ["a.py", "tests/test_x.py"],
}


def test_tail_follows_worker_log_in_run_dir_and_stops_at_report(tmp_path):
    run = _write_run(tmp_path, [
        _assistant({"type": "tool_use", "name": "Bash", "input": {"command": "python3 -m pytest"}}),
        _result("Exit code 1\nE   AssertionError: boom", is_error=True),
        {"type": "atm.report", **{k: v for k, v in REPORT.items() if k != "type"}},
    ])
    proc = _tail(str(run))
    assert proc.returncode == 0, proc.stderr
    assert "▶ #1 Bash" in proc.stdout
    assert "✗ exit 1" in proc.stdout
    assert "RESULT" in proc.stdout


def test_tail_renders_phases_outcomes_checks_and_drops_noise(tmp_path):
    run = _write_run(tmp_path, [
        {"type": "atm.scope", "source": "flag", "paths": ["a.py"]},
        {"type": "atm.prepared", "worktree": CLONE, "base_sha": "2c6b567aeb0f7ad6f", "test_command": "python3 -m pytest"},
        {"type": "atm.unit_started", "unit": 1, "base_sha": "2c6b567aeb0f7ad6f", "scope": ["a.py"]},
        {"type": "system", "subtype": "init", "model": "m"},
        {"type": "system", "subtype": "status", "status": "requesting"},
        {"type": "system", "subtype": "hook_progress"},
        {"type": "system", "subtype": "task_started"},
        {"type": "system", "subtype": "task_notification"},
        {"type": "tool_progress"},
        {"type": "rate_limit_event"},
        {"type": "stream_event", "event": {"type": "content_block_delta"}},
        _assistant({"type": "thinking", "thinking": "Look around first."}),
        _assistant({"type": "tool_use", "name": "Read", "input": {"file_path": f"{CLONE}/a.py"}}),
        _result([{"type": "text", "text": "     1\tdef a():"}]),
        _assistant({"type": "text", "text": "Implementing."}),
        _assistant({"type": "tool_use", "name": "Bash", "input": {"command": "python3 -m pytest -q"}}),
        _result("Traceback (most recent call last):\n  boom"),
        {"type": "result", "subtype": "success", "num_turns": 7, "is_error": False},
        {"type": "atm.harness_done", "exit_code": 0, "timed_out": False, "unit": 1},
        {"type": "atm.verify", "test_file": "tests/test_x.py", "red_passed": False, "green_passed": True,
         "red_output_full": "== test session starts ==\ncollected 2 items\n\ntests/test_x.py F.   [100%]\n\n"
                            "=================== FAILURES ===\n_____ test_x _____\n== 1 failed, 1 passed in 0.1s ==\n",
         "green_output": "== test session starts ==\n\ntests/test_x.py ..   [100%]\n\n==== 2 passed in 0.10s ====\n"},
        REPORT,
    ])
    (run / "command.txt").write_text(f"python3 -m w --repo {SOURCE} --base-ref origin/main --timeout 1800\n")
    proc = _tail(str(next(run.glob("worker-*.jsonl"))))
    out = proc.stdout
    assert proc.returncode == 0, proc.stderr
    for title in ("PREPARE", "AGENT  unit 1 claude timeout=1800s", "RED / GREEN VERIFICATION", "SUMMARY"):
        assert title in out
    assert "worktree  .worktrees/atm-run-1" in out and "base      origin/main@2c6b567aeb" in out
    assert "tests     python3 -m pytest" in out
    assert "~ thinking" in out and "Look around first." in out
    assert "▶ #1 Read  a.py" in out and SOURCE not in out
    assert "✓ ok  1 def a():" in out
    assert "✎ Implementing." in out
    assert "▶ #2 Bash  python3 -m pytest -q" in out and "✗ error  Traceback" in out
    assert "✓ agent finished  turns=7" in out
    assert "✓ test fails without fix (expected)" in out and "✓ test passes with fix" in out
    assert "tests/test_x.py F." in out and "1 failed, 1 passed in 0.1s" in out and "FAILURES" not in out
    assert "unit 1     ✗ FAIL  test=tests/test_x.py" in out
    assert "✓ red/green   ✓ quality   ✓ gate   ✗ scope   ✓ full suite   – typecheck n/a" in out
    assert "changed    a.py, tests/test_x.py" in out
    assert out.rstrip().splitlines()[-1].startswith("✗ RESULT  FAIL")
    for noise in ("init", "status", "hook_progress", "task_started", "task_notification", "tool_progress",
                  "rate_limit", "stream_event", "{"):
        assert noise not in out
    assert "\x1b[" not in out


def test_tail_stops_at_prepare_failed_and_when_exit_txt_appears(tmp_path):
    run = _write_run(tmp_path, [{"type": "atm.prepare_failed", "error": "no such ref"}])
    proc = _tail(str(run))
    assert proc.returncode == 0 and "✗ RESULT  FAIL" in proc.stdout and "no such ref" in proc.stdout

    other = tmp_path / "other"
    other.mkdir()
    (other / "worker-1.jsonl").write_text(json.dumps({"event": {"type": "atm.unit_started", "unit": 1}}) + "\n")
    (other / "exit.txt").write_text("2\n")
    proc = _tail(str(other / "worker-1.jsonl"))
    assert proc.returncode == 0 and "AGENT  unit 1" in proc.stdout and "exit=2" in proc.stdout


def test_tail_refuses_a_path_that_is_not_a_worker_log(tmp_path):
    other = tmp_path / "agent-1.jsonl"
    other.write_text("{}\n")
    for script in (ATM_RUN, TAIL_RUN):
        args = [sys.executable, str(script)] + (["tail"] if script == ATM_RUN else []) + [str(other)]
        proc = subprocess.run(args, capture_output=True, text=True, timeout=30)
        assert proc.returncode == 1
        assert "worker-*.jsonl" in proc.stderr
