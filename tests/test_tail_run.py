"""scripts/tail-run.py renders harness-worker logs."""
import importlib.util
import json
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent.parent / "scripts" / "tail-run.py"
spec = importlib.util.spec_from_file_location("tail_run", SCRIPT)
tail_run = importlib.util.module_from_spec(spec)
spec.loader.exec_module(tail_run)


def write_log(path: Path, events: list) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("".join(json.dumps({"ts": "2026-10-04T12:00:00+00:00", "harness": "claude", "event": e}) + "\n"
                            for e in events))
    return path


UNIT = {"type": "atm.unit_done", "unit": 1, "passed": True, "test_file": "t_test.go", "changed_files": ["a.go"],
        "verified": {"ok": True, "message": "VERIFIED"}, "quality_ok": {"ok": True, "message": "ok"},
        "gate_ok": {"ok": True, "message": "ok"}, "scope_ok": {"ok": False, "message": "outside SCOPE: b.go"},
        "follow_ups": [{"index": 1, "accepted": False, "title": "gap"}]}


def test_no_log_exits_1_with_message(tmp_path, capsys):
    assert tail_run.main([str(tmp_path)]) == 1
    assert "No worker-*.jsonl log found" in capsys.readouterr().err


def test_a_file_that_is_not_a_worker_log_is_refused_instead_of_followed(tmp_path, capsys):
    other = tmp_path / "stdout.txt"
    other.write_text("agent chatter with no terminal event\n")
    assert tail_run.main([str(other)]) == 1
    assert f"Not a worker-*.jsonl log: {other}" in capsys.readouterr().err


def test_renders_worker_and_agent_events_and_stops_at_report(tmp_path, capsys):
    run = tmp_path / "run"
    write_log(run / "worker-20261004-120000.jsonl", [
        {"type": "atm.scope", "source": "flag", "paths": ["a.go"]},
        {"type": "system", "subtype": "status"},
        {"type": "assistant", "message": {"content": [{"type": "tool_use", "name": "Read", "input": {"file_path": "a.go"}}]}},
        {"type": "atm.verify", "test_file": "t_test.go", "red_passed": False, "green_passed": True},
        UNIT,
        {"type": "atm.report", "units": [UNIT], "max_units": 3, "changed_files": ["a.go"]},
    ])
    # Following (the default) must still end: the report closes the run.
    assert tail_run.main([str(tmp_path)]) == 0
    out = capsys.readouterr().out
    assert "[atm] scope (flag): a.go" in out
    assert "[tool] Read: a.go" in out
    assert "[status]" not in out
    assert "without fix FAIL (good), with fix PASS (good)" in out
    assert "scope      FAIL outside SCOPE: b.go" in out
    assert "follow-up 1: rejected: gap" in out
    assert "=== RESULT: PASS === units=1/3" in out

