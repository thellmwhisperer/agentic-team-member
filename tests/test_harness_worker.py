"""End-to-end tests for the harness worker spike with a fake harness CLI."""

import json
import os
import subprocess
import sys
import textwrap
from pathlib import Path

from agentic_tdd_runner import harness_worker

REPO_ROOT = Path(__file__).resolve().parents[1]

CONFIG_TOML = """
[timeouts]
test_run = 60
tool_execution = 60

[environment]
enabled = true
install = "never"
require_clean = true
timeout = 120

[quality]
enabled = true

[quality.python]
forbidden = ["type: ignore", "noqa"]

[quality.duplicated_setup_judge]
enabled = false

[tools]
file = "tools.json"
recommended = []
"""

FIXING_HARNESS = """
import json, pathlib, sys
brief = sys.stdin.read()
assert "add returns the difference" in brief
print(json.dumps({"type": "system", "subtype": "init", "model": "fake"}), flush=True)
pathlib.Path("tests/test_add.py").write_text("from calc import add\\n\\n\\ndef test_add_sums():\\n    assert add(2, 3) == 5\\n")
print(json.dumps({"type": "assistant", "message": {"content": [
    {"type": "tool_use", "name": "Write", "input": {"file_path": "tests/test_add.py"}}]}}), flush=True)
calc = pathlib.Path("calc.py")
calc.write_text(calc.read_text().replace("a - b", "a + b"))
print(json.dumps({"type": "assistant", "message": {"content": [
    {"type": "tool_use", "name": "Edit", "input": {"file_path": "calc.py"}},
    {"type": "text", "text": "Fixed add."}]}}), flush=True)
print("not json noise", flush=True)
report = {"test_file": "tests/test_add.py", "changed_files": ["calc.py", "tests/test_add.py"],
          "summary": "add used subtraction", "commands_run": ["python3 -m pytest"]}
print(json.dumps({"type": "result", "subtype": "success", "num_turns": 3, "is_error": False,
                  "result": "Done.\\n" + json.dumps(report)}), flush=True)
"""

HELPER_ONLY_HARNESS = """
import json, pathlib, sys
sys.stdin.read()
pathlib.Path("helpers.py").write_text("def helper():\\n    return 1\\n")
print(json.dumps({"type": "result", "subtype": "success", "result": "I added a helper."}), flush=True)
"""

NEW_MODULE_HARNESS = """
import json, pathlib, sys
sys.stdin.read()
pathlib.Path("calc2.py").write_text("def add(a, b):\\n    return a + b\\n")
pathlib.Path("tests/test_add2.py").write_text("from calc2 import add\\n\\n\\ndef test_add_sums():\\n    assert add(2, 3) == 5\\n")
calc = pathlib.Path("calc.py")
calc.write_text(calc.read_text().replace("a - b", "a + b"))
report = {"test_file": "tests/test_add2.py", "changed_files": ["calc.py", "calc2.py", "tests/test_add2.py"],
          "summary": "moved add to a new module", "commands_run": []}
print(json.dumps({"type": "result", "subtype": "success", "result": json.dumps(report)}), flush=True)
"""

SLEEPING_HARNESS = """
import json, subprocess, sys, time
sys.stdin.read()
print(json.dumps({"type": "system", "subtype": "init"}), flush=True)
subprocess.Popen(["sleep", "60"])  # grandchild holds stdout open unless the whole group dies
time.sleep(60)
"""


def _git(cwd, *args):
    subprocess.run(
        ["git", "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "core.hooksPath=/dev/null", *args],
        cwd=cwd, check=True, capture_output=True,
    )


def _setup(tmp_path, harness_source):
    repo = tmp_path / "target"
    (repo / "tests").mkdir(parents=True)
    (repo / "pyproject.toml").write_text('[project]\nname = "calc"\nversion = "0"\n\n[tool.pytest.ini_options]\n')
    (repo / ".gitignore").write_text("__pycache__/\n.pytest_cache/\n.worktree/\n")
    (repo / "calc.py").write_text("def add(a, b):\n    return a - b\n\n\ndef mul(a, b):\n    return a * b\n")
    (repo / "tests" / "test_mul.py").write_text("from calc import mul\n\n\ndef test_mul():\n    assert mul(2, 3) == 6\n")
    _git(repo, "init", "-q", "-b", "main")
    _git(repo, "add", ".")
    _git(repo, "commit", "-q", "-m", "init")

    config_dir = tmp_path / "config"
    config_dir.mkdir()
    (config_dir / "agent.toml").write_text(CONFIG_TOML)
    (config_dir / "tools.json").write_text("[]")
    issue = tmp_path / "issue.md"
    issue.write_text("# add returns the difference\n\nadd(2, 3) returns -1 instead of 5.\n")
    harness = tmp_path / "fake_harness.py"
    harness.write_text(f"#!{sys.executable}\n" + textwrap.dedent(harness_source))
    harness.chmod(0o755)
    artifacts = tmp_path / "artifacts"
    argv = [
        "--repo", str(repo), "--issue-file", str(issue), "--config", str(config_dir / "agent.toml"),
        "--artifact-dir", str(artifacts), "--log-dir", str(tmp_path / "logs"), "--harness-bin", str(harness),
    ]
    return argv, artifacts, tmp_path / "logs"


def _log_events(log_dir):
    (log_path,) = list(log_dir.glob("worker-*.jsonl"))
    return [json.loads(line) for line in log_path.read_text().splitlines()]


def test_fake_claude_fix_passes_all_gates_via_cli(tmp_path):
    argv, artifacts, log_dir = _setup(tmp_path, FIXING_HARNESS)
    env = {k: v for k, v in os.environ.items() if k != "PYTHONPATH"} | {"PYTHONPATH": str(REPO_ROOT)}
    proc = subprocess.run(
        [sys.executable, "-m", "agentic_tdd_runner.harness_worker", *argv],
        cwd=REPO_ROOT, env=env, capture_output=True, text=True, timeout=180,
    )
    assert proc.returncode == 0, proc.stdout + proc.stderr
    assert "[tool] Write: tests/test_add.py" in proc.stdout

    brief = (artifacts / "brief.md").read_text()
    assert "add returns the difference" in brief
    assert "add(2, 3) returns -1 instead of 5." in brief
    assert "python3 -m pytest" in brief
    assert "`noqa`" in brief

    events = [entry["event"] for entry in _log_events(log_dir)]
    assert all(entry_harness == "claude" for entry_harness in (e["harness"] for e in _log_events(log_dir)))
    assert any(isinstance(e, dict) and e.get("type") == "result" for e in events)
    assert "not json noise" in events

    report = json.loads((artifacts / "report.json").read_text())
    assert report["verified"]["ok"] is True
    assert report["gate_ok"]["ok"] is True
    assert report["quality_ok"]["ok"] is True
    assert report["full_tests_ok"] is True
    assert report["typecheck_ok"] is None
    assert report["test_file"] == "tests/test_add.py"
    assert report["changed_files"] == ["calc.py", "tests/test_add.py"]
    assert report["timed_out"] is False
    assert report["harness_exit_code"] == 0
    assert report["report_parse_error"] is None
    for key in ("harness", "model", "base_ref", "worktree", "brief", "log", "duration_seconds"):
        assert key in report


def test_helper_only_change_fails_verification_and_gate(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, HELPER_ONLY_HARNESS)
    assert harness_worker.main(argv) == 1
    report = json.loads((artifacts / "report.json").read_text())
    assert report["verified"]["ok"] is False
    assert report["gate_ok"]["ok"] is False
    assert report["test_file"] is None
    assert report["report_parse_error"] == "no JSON object in final message"


def test_red_that_fails_on_missing_module_is_not_verified(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, log_dir = _setup(tmp_path, NEW_MODULE_HARNESS)
    assert harness_worker.main(argv) == 1
    report = json.loads((artifacts / "report.json").read_text())
    assert report["verified"]["ok"] is False
    assert "INVALID RED" in report["verified"]["message"]
    assert report["gate_ok"]["ok"] is True  # calc.py was touched; the gate is not the problem here
    assert any(e["event"].get("type") == "atm.verify_invalid_red" for e in _log_events(log_dir))


def test_brief_demands_a_behavioral_red(tmp_path, capsys, monkeypatch):
    argv, artifacts, _ = _setup(tmp_path, FIXING_HARNESS)
    assert harness_worker.main(argv + ["--dry-run"]) == 0
    brief = (artifacts / "brief.md").read_text()
    assert "not on a missing module" in brief


def test_harness_timeout_kills_process_group(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, SLEEPING_HARNESS)
    assert harness_worker.main([*argv, "--timeout", "2"]) == 1
    report = json.loads((artifacts / "report.json").read_text())
    assert report["timed_out"] is True
    assert report["duration_seconds"] < 30
    assert report["verified"]["ok"] is False


def test_dry_run_writes_brief_without_running_harness(tmp_path, capsys, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, SLEEPING_HARNESS)
    assert harness_worker.main([*argv, "--dry-run"]) == 0
    assert "## REPORT" in capsys.readouterr().out
    assert (artifacts / "brief.md").is_file()
    assert not (artifacts / "report.json").exists()


def test_extract_report_takes_last_top_level_object():
    text = 'first {"a": 1} then ```json\n{"test_file": "t.py", "changed_files": [], "x": {"y": 2}}\n```'
    report, error = harness_worker.extract_report(text)
    assert error is None
    assert report["test_file"] == "t.py"


def test_go_repo_gets_go_commands_and_forbidden_scan(tmp_path):
    from types import SimpleNamespace
    repo = tmp_path / "gorepo"
    repo.mkdir()
    (repo / "go.mod").write_text("module example.com/x\n\ngo 1.22\n")
    (repo / "a.go").write_text("package x\n\nfunc A() int { return 1 } //nolint\n")
    env_report = SimpleNamespace(project_type="unknown", package_manager=None, runner_bootstrap=None)
    assert harness_worker.detect_commands(str(repo), env_report) == ("go test ./...", "go vet ./...")
    assert harness_worker.quality_lang_key(str(repo), "unknown") == "go"
    hits = harness_worker.scan_forbidden(str(repo), ["a.go", "missing.go"], ["//nolint", "t.Skip("])
    assert hits == ["a.go:3 '//nolint'"]
