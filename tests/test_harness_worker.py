"""End-to-end tests for the harness worker spike with a fake harness CLI."""

import json
import os
import subprocess
import sys
import textwrap
from pathlib import Path

import pytest

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

[monitor]
command = "true"
"""

FIXING_HARNESS = """
import json, pathlib, sys
brief = sys.stdin.read()
if brief.startswith("# Ponytail pass"):
    print(json.dumps({"type": "result", "subtype": "success", "result": json.dumps({
        "findings": [], "summary": "No cuts found"})}), flush=True)
    raise SystemExit(0)
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


SCOPE_BREAKING_HARNESS = FIXING_HARNESS.replace(
    'print("not json noise", flush=True)',
    'pathlib.Path("other.py").write_text("def other():\\n    return 2\\n")',
)

FOLLOW_UP_HARNESS = """
import json, pathlib, sys
sys.stdin.read()
pathlib.Path("tests/test_add.py").write_text("from calc import add\\n\\n\\ndef test_add_sums():\\n    assert add(2, 3) == 5\\n")
calc = pathlib.Path("calc.py")
calc.write_text(calc.read_text().replace("a - b", "a + b"))
follow_ups = []
for n, (rel, source) in enumerate(RED_TESTS, start=1):
    path = pathlib.Path(".atm/follow-ups", str(n), rel)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(source)
    follow_ups.append({"title": f"gap {n}", "paths": ["calc.py"], "red_test": rel})
report = {"test_file": "tests/test_add.py", "changed_files": ["calc.py", "tests/test_add.py"],
          "summary": "fixed add", "commands_run": [], "follow_ups": follow_ups}
print(json.dumps({"type": "result", "subtype": "success", "result": json.dumps(report)}), flush=True)
"""

MUL_NEG_TEST = "from calc import mul\n\n\ndef test_mul_negative():\n    assert mul(-1, 1) == -1\n"
PASSING_TEST = "from calc import mul\n\n\ndef test_mul_positive():\n    assert mul(2, 3) == 6\n"
MISSING_MODULE_TEST = "from nowhere import thing\n\n\ndef test_thing():\n    assert thing() == 1\n"


def _follow_up_harness(red_tests):
    return FOLLOW_UP_HARNESS.replace("RED_TESTS", repr(red_tests))


def _git(cwd, *args):
    subprocess.run(
        ["git", "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "core.hooksPath=/dev/null", *args],
        cwd=cwd, check=True, capture_output=True,
    )


def _setup(tmp_path, harness_source, issue_text="add(2, 3) returns -1 instead of 5."):
    repo = tmp_path / "target"
    (repo / "tests").mkdir(parents=True)
    (repo / "pyproject.toml").write_text('[project]\nname = "calc"\nversion = "0"\n\n[tool.pytest.ini_options]\n')
    (repo / ".gitignore").write_text("__pycache__/\n.pytest_cache/\n.worktree/\n")
    (repo / "calc.py").write_text("def add(a, b):\n    return a - b\n\n\ndef mul(a, b):\n    return abs(a * b)\n")
    (repo / "other.py").write_text("def other():\n    return 1\n")
    (repo / "tests" / "test_mul.py").write_text("from calc import mul\n\n\ndef test_mul():\n    assert mul(2, 3) == 6\n")
    _git(repo, "init", "-q", "-b", "main")
    _git(repo, "add", ".")
    _git(repo, "commit", "-q", "-m", "init")

    config_dir = tmp_path / "config"
    config_dir.mkdir()
    (config_dir / "agent.toml").write_text(CONFIG_TOML)
    (config_dir / "tools.json").write_text("[]")
    issue = tmp_path / "issue.md"
    issue.write_text(f"# add returns the difference\n\n{issue_text}\n")
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
    assert "[tool]" not in proc.stdout  # the agent's stream is the log's; tail-run.py renders it

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
    assert report["ponytail"]["kept"] is False and "did not reduce" in report["ponytail"]["reason"]
    for key in ("harness", "model", "base_ref", "worktree", "brief", "log", "duration_seconds"):
        assert key in report


def test_failed_full_suite_keeps_its_output(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    failing = f"{sys.executable} -c \"print('FULL SUITE EXPLODED'); raise SystemExit(1)\""
    monkeypatch.setattr(harness_worker, "detect_commands", lambda workdir, env_report: (failing, None))
    argv, artifacts, log_dir = _setup(tmp_path, FIXING_HARNESS)
    assert harness_worker.main(argv) == 1
    (unit,) = json.loads((artifacts / "report.json").read_text())["units"]
    assert unit["full_tests_ok"] is False
    assert "FULL SUITE EXPLODED" in unit["full_tests_tail"]
    assert unit["typecheck_tail"] is None
    (event,) = [e["event"] for e in _log_events(log_dir)
                if isinstance(e["event"], dict) and e["event"].get("type") == "atm.command_failed"]
    assert (event["command"], event["exit_code"]) == (failing, 1)
    assert "FULL SUITE EXPLODED" in event["output_tail"]
    assert "FULL SUITE EXPLODED" in capsys.readouterr().out.split("=== HARNESS WORKER SUMMARY ===")[1]


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
    assert "(no module named 'calc2')" in report["verified"]["message"]


QUOTED_MARKER_RED = """\
=================================== FAILURES ===================================
____________________________ test_summary_lists_follow_ups ____________________________
E       AssertionError: assert 'follow-ups: 1' in 'SUMMARY\\nfollow-up 2: rejected: red test fails on a missing module (no module named), not on behavior\\n'
---------------------------- Captured stdout call -----------------------------
SUMMARY
follow-up 2: rejected: red test fails on a missing module (no module named), not on behavior
ModuleNotFoundError: No module named 'quoted'
Error: Cannot find module 'quoted'
=========================== short test summary info ============================
FAILED tests/test_harness_worker.py::test_summary_lists_follow_ups - AssertionError
"""


@pytest.mark.parametrize("output, expected", [
    (QUOTED_MARKER_RED, None),
    ("_ ERROR collecting tests/test_calc.py _\nImportError while importing test module 'tests/test_calc.py'.\n"
     "Traceback:\n    import calc_new\nE   ModuleNotFoundError: No module named 'calc_new'\n",
     "no module named 'calc_new'"),
    ("_ ERROR collecting tests/test_calc.py _\nImportError: cannot import name 'widget' from 'pkg'\n",
     "cannot import name 'widget' from 'pkg'"),
    ("Error [ERR_MODULE_NOT_FOUND]: Cannot find module '/repo/src/calc.js' imported from /repo/calc.test.js\n",
     "cannot find module '/repo/src/calc.js'"),
    ("FAIL src/calc.test.ts\nCannot find module './calc' from 'src/calc.test.ts'\n",
     "cannot find module './calc'"),
    (" FAIL  src/calc.test.ts\nError: Failed to resolve import \"./calc\" from \"src/calc.test.ts\". Does the file exist?\n",
     "failed to resolve import './calc'"),
    ("FAIL src/calc.test.ts\nFailed to resolve import \"./calc\" from \"src/calc.test.ts\". Does the file exist?\n",
     "failed to resolve import './calc'"),
])
def test_red_missing_module_needs_a_real_import_error(output, expected):
    assert harness_worker.red_failed_on_missing_module(output) == expected


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


def test_label_names_the_run_directory_and_is_refused_once_used(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    monkeypatch.chdir(tmp_path)
    argv, _, _ = _setup(tmp_path, FIXING_HARNESS)
    i = argv.index("--artifact-dir")
    argv = [*argv[:i], *argv[i + 2:], "--label", "same"]
    run = tmp_path / ".tmp" / "harness-worker" / "same"

    assert harness_worker.main(argv) == 0
    assert json.loads((run / "report.json").read_text())["verified"]["ok"] is True
    command = (run / "command.txt").read_text()
    assert f"cd {tmp_path} && " in command and "-m agentic_tdd_runner.harness_worker" in command
    assert command.rstrip().endswith("--label same")

    capsys.readouterr()
    assert harness_worker.main(argv) == 2
    assert f"label already used: {run}" in capsys.readouterr().err


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
    hits = harness_worker.scan_forbidden(str(repo), ["a.go", "missing.go"], ["//nolint", "t.Skip("], "HEAD")
    assert hits == ["a.go:3 '//nolint'"]


def test_claude_command_keeps_thinking_and_summary_skips_partials():
    from types import SimpleNamespace
    args = SimpleNamespace(harness="claude", harness_bin=None, model=None)
    cmd, _ = harness_worker.harness_command(args, "/w", "brief", "/s.json", "/m.txt")
    assert "--include-partial-messages" in cmd
    assert cmd[cmd.index("--thinking-display") + 1] == "summarized"
    assert harness_worker.summarize_event({"type": "stream_event", "event": {}}) is None


def test_red_green_command_template_can_name_the_test_dir(tmp_path, monkeypatch):
    from agentic_tdd_runner import verification
    seen = []

    def fake_run(argv, **kwargs):
        seen.append(list(argv))
        return SimpleNamespace(returncode=1 if len(seen) == 1 else 0, stdout="", stderr="")

    from types import SimpleNamespace
    monkeypatch.setattr(verification.subprocess, "run", lambda argv, **kw: fake_run(argv, **kw))
    config = {"timeouts": {"test_run": 5}, "runner": {"command": "go test {test_dir}", "override_detected": True}}
    (tmp_path / "pkg").mkdir()
    (tmp_path / "pkg" / "x_test.go").write_text("package pkg\n")
    verification.verify_red_green("pkg/x_test.go", workdir=str(tmp_path), config=config, emit=lambda s: None,
                                  log=lambda n, d: None)
    test_runs = [a for a in seen if a[:2] == ["go", "test"]]
    assert test_runs and all(a == ["go", "test", "./pkg"] for a in test_runs)


def _scope_event(log_dir):
    events = [e["event"] for e in _log_events(log_dir) if isinstance(e["event"], dict)]
    (event,) = [e for e in events if e.get("type") == "atm.scope"]
    return event


def test_scope_flag_rejects_changes_outside_scope(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, log_dir = _setup(tmp_path, SCOPE_BREAKING_HARNESS)
    assert harness_worker.main([*argv, "--scope", "calc.py"]) == 1
    report = json.loads((artifacts / "report.json").read_text())
    assert report["scope_ok"]["ok"] is False
    assert report["scope_ok"]["message"] == "outside SCOPE: other.py"
    assert report["verified"]["ok"] is True  # the fix itself is fine; SCOPE alone fails the run
    assert _scope_event(log_dir)["source"] == "flag"
    brief = (artifacts / "brief.md").read_text()
    assert "`calc.py`" in brief
    assert harness_worker.SCOPE_RULE in brief


def test_scope_derived_from_issue_text(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, log_dir = _setup(tmp_path, FIXING_HARNESS, issue_text="add(2, 3) in calc.py returns -1.")
    assert harness_worker.main(argv) == 0
    event = _scope_event(log_dir)
    assert (event["source"], event["paths"]) == ("issue", ["calc.py"])
    report = json.loads((artifacts / "report.json").read_text())
    assert report["scope_ok"]["ok"] is True
    assert report["follow_ups"] == []
    assert not (artifacts / "follow-ups.json").exists()


def test_scope_open_when_issue_names_nothing(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, log_dir = _setup(tmp_path, FIXING_HARNESS)
    assert harness_worker.main(argv) == 0
    event = _scope_event(log_dir)
    assert (event["source"], event["paths"]) == ("open", [])
    assert "Allowed paths: open" in (artifacts / "brief.md").read_text()
    assert json.loads((artifacts / "report.json").read_text())["scope_ok"] == {"ok": True, "message": "scope open"}


def test_derive_scope_finds_backticked_definitions(tmp_path):
    repo = tmp_path / "r"
    (repo / "pkg").mkdir(parents=True)
    (repo / "pkg" / "s.go").write_text("package pkg\n\nfunc (s *S) Load(x int) int { return x }\n")
    (repo / "web.ts").write_text("export const render = () => 1\n")
    (repo / "lib.py").write_text("def helper():\n    return 1\n")
    (repo / "uses.py").write_text("from lib import helper\nhelper()\n")
    _git(repo, "init", "-q", "-b", "main")
    _git(repo, "add", ".")
    text = "`Load` breaks, `render` too, `helper` and `missing`; see lib.py, pkg/nope.go."
    assert harness_worker.derive_scope(text, str(repo)) == ["lib.py", "pkg/s.go", "web.ts"]


def test_follow_up_with_behavioral_red_test_is_accepted(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, _follow_up_harness([("tests/test_mul_neg.py", MUL_NEG_TEST)]))
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    (follow_up,) = report["follow_ups"]
    assert follow_up["accepted"] is True, follow_up
    assert follow_up["red_test"] == "tests/test_mul_neg.py"
    assert json.loads((artifacts / "follow-ups.json").read_text()) == [follow_up]
    worktree = Path(report["worktree"])
    assert not (worktree / "tests" / "test_mul_neg.py").exists()
    assert report["full_tests_ok"] is True
    after = subprocess.run([sys.executable, "-m", "pytest", "-q", "-p", "no:cacheprovider"], cwd=worktree,
                           capture_output=True, text=True, timeout=120)
    assert after.returncode == 0, after.stdout + after.stderr


def test_follow_ups_without_a_behavioral_red_are_rejected(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    red_tests = [("tests/test_mul_pos.py", PASSING_TEST), ("tests/test_thing.py", MISSING_MODULE_TEST)]
    argv, artifacts, log_dir = _setup(tmp_path, _follow_up_harness(red_tests))
    assert harness_worker.main(argv) == 0  # a rejected follow-up does not fail the run
    report = json.loads((artifacts / "report.json").read_text())
    passing, missing = report["follow_ups"]
    assert passing["accepted"] is False and "passed" in passing["reason"]
    assert missing["accepted"] is False and "missing module" in missing["reason"]
    assert not (artifacts / "follow-ups.json").exists()
    rejected = [e["event"] for e in _log_events(log_dir) if e["event"].get("type") == "atm.follow_up_rejected"]
    assert len(rejected) == 2


def test_follow_up_test_on_disk_without_declaration_is_still_validated(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    # Same fake as the accepted case, but the report declares no follow-ups.
    harness = _follow_up_harness([("tests/test_mul_neg.py", MUL_NEG_TEST)]).replace(
        '"follow_ups": follow_ups}', '"follow_ups": []}')
    assert '"follow_ups": []}' in harness
    argv, artifacts, log_dir = _setup(tmp_path, harness)
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    (fu,) = report["follow_ups"]
    assert fu["accepted"] is True and fu["declared"] is False and fu["on_disk"] is True
    assert fu["red_test"] == "tests/test_mul_neg.py"
    assert "not declared" in fu["reason"]


def test_relative_run_root_resolves_against_target_repo(tmp_path, monkeypatch):
    from agentic_tdd_runner.environment import _default_run_worktree_path

    repo = tmp_path / "target"
    repo.mkdir()
    elsewhere = tmp_path / "caller"
    elsewhere.mkdir()
    monkeypatch.chdir(elsewhere)

    relative = _default_run_worktree_path(repo, ".worktrees")
    absolute = _default_run_worktree_path(repo, str(tmp_path / "runs"))

    assert relative.parent == (repo / ".worktrees").resolve()
    assert absolute.parent == (tmp_path / "runs").resolve()
    assert not str(relative).startswith(str(elsewhere.resolve()))


def test_snapshot_parks_and_restores_without_touching_the_shared_stash(tmp_path):
    from agentic_tdd_runner import verification

    repo = tmp_path / "repo"
    repo.mkdir()
    (repo / "a.py").write_text("x = 1\n")
    _git(repo, "init", "-q", "-b", "main")
    _git(repo, "add", ".")
    _git(repo, "commit", "-q", "-m", "init")
    (repo / "a.py").write_text("x = 2\n")
    (repo / "new_test.py").write_text("def test(): pass\n")
    (repo / ".atm" / "follow-ups" / "1").mkdir(parents=True)
    (repo / ".atm" / "follow-ups" / "1" / "red_test.py").write_text("assert False\n")

    def status():
        return subprocess.run(["git", "status", "--porcelain", "-uall"], cwd=repo, capture_output=True, text=True).stdout

    def stash_list():
        return subprocess.run(["git", "stash", "list"], cwd=repo, capture_output=True, text=True).stdout

    before = status()
    sha = verification.snapshot_worktree(str(repo))
    assert sha
    assert status() == ""
    assert (repo / "a.py").read_text() == "x = 1\n"
    assert not (repo / ".atm").exists()
    assert stash_list() == ""

    assert verification.restore_worktree(str(repo), sha)
    assert status() == before
    assert (repo / "a.py").read_text() == "x = 2\n"
    assert (repo / ".atm" / "follow-ups" / "1" / "red_test.py").read_text() == "assert False\n"
    assert stash_list() == ""


def test_fingerprint_ignores_staging_and_survives_snapshot_restore(tmp_path):
    from agentic_tdd_runner import verification

    repo = tmp_path / "repo"
    repo.mkdir()
    (repo / "a.py").write_text("x = 1\n")
    (repo / "b.py").write_text("y = 1\n")
    _git(repo, "init", "-q", "-b", "main")
    _git(repo, "add", ".")
    _git(repo, "commit", "-q", "-m", "init")
    # The agent stages a deletion and an addition, as atm-3-config-opus did on #98 (5-oct-2026).
    _git(repo, "rm", "-q", "b.py")
    (repo / "c.py").write_text("z = 1\n")
    _git(repo, "add", "c.py")

    before = harness_worker.worktree_fingerprint(str(repo))
    _git(repo, "reset", "-q")
    assert harness_worker.worktree_fingerprint(str(repo)) == before

    _git(repo, "add", "-A")
    sha = verification.snapshot_worktree(str(repo))
    assert verification.restore_worktree(str(repo), sha)
    assert harness_worker.worktree_fingerprint(str(repo)) == before

    (repo / "c.py").write_text("z = 2\n")
    assert harness_worker.worktree_fingerprint(str(repo)) != before
    (repo / "c.py").write_text("z = 1\n")
    (repo / "a.py").write_text("x = 2\n")
    assert harness_worker.worktree_fingerprint(str(repo)) != before
    (repo / "a.py").write_text("x = 1\n")
    (repo / "a.py").unlink()
    assert harness_worker.worktree_fingerprint(str(repo)) != before
    (repo / "a.py").write_text("x = 1\n")
    (repo / "d.py").write_text("")
    assert harness_worker.worktree_fingerprint(str(repo)) != before
    (repo / "d.py").unlink()
    (repo / "a.py").chmod(0o755)
    assert harness_worker.worktree_fingerprint(str(repo)) != before


def test_fingerprint_distinguishes_symlink_targets_with_identical_contents(tmp_path):
    repo = tmp_path / "repo"
    repo.mkdir()
    (repo / "a.txt").write_text("same\n")
    (repo / "b.txt").write_text("same\n")
    _git(repo, "init", "-q", "-b", "main")
    _git(repo, "add", ".")
    _git(repo, "commit", "-q", "-m", "init")
    link = repo / "link.txt"
    link.symlink_to("a.txt")

    before = harness_worker.worktree_fingerprint(str(repo))
    link.unlink()
    link.symlink_to("b.txt")

    assert harness_worker.worktree_fingerprint(str(repo)) != before


def test_verdict_is_void_when_the_worktree_changes_during_verification(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, log_dir = _setup(tmp_path, FIXING_HARNESS)
    from agentic_tdd_runner import verification

    real = verification.verify_red_green

    def swapped(test_file, *, workdir, **kw):
        ok, msg = real(test_file, workdir=workdir, **kw)
        # Another run's stash lands here, as the shared stash stack did on 4-oct-2026.
        (Path(workdir) / "calc.py").write_text("def add(a, b):\n    return a + b + 0\n\n\ndef mul(a, b):\n    return a * b\n")
        return ok, msg

    monkeypatch.setattr(verification, "verify_red_green", swapped)
    assert harness_worker.main(argv) == 1
    report = json.loads((artifacts / "report.json").read_text())
    assert report["verified"]["ok"] is False
    assert "WORKTREE CHANGED" in report["verified"]["message"]
    assert any(isinstance(e["event"], dict) and e["event"].get("type") == "atm.verify_worktree_changed" for e in _log_events(log_dir))


def test_summary_prints_the_whole_thinking_and_hides_token_bookkeeping():
    event = {"type": "assistant", "message": {"content": [
        {"type": "thinking", "thinking": "First I read the hook.\nThen I write the red test."},
        {"type": "text", "text": "Done."},
    ]}}
    out = harness_worker.summarize_event(event)
    assert "[thinking]" in out
    assert "First I read the hook." in out and "Then I write the red test." in out
    assert harness_worker.summarize_event({"type": "system", "subtype": "thinking_tokens"}) is None
    assert harness_worker.summarize_event({"type": "system", "subtype": "init", "model": "m"}) == "[init] model=m"
    assert harness_worker.summarize_event({"type": "system", "subtype": "status"}) == "[status]"


def test_run_clone_is_a_separate_repo_detached_at_the_source_base_ref(tmp_path):
    from agentic_tdd_runner.environment import prepare_run_clone, WorktreePrepError

    source = tmp_path / "source"
    source.mkdir()
    (source / "a.txt").write_text("1\n")
    _git(source, "init", "-q", "-b", "main")
    _git(source, "add", ".")
    _git(source, "commit", "-q", "-m", "one")
    base = subprocess.run(["git", "rev-parse", "HEAD"], cwd=source, capture_output=True, text=True).stdout.strip()
    # The source carries a remote-tracking ref and a stash entry, like la-roca does.
    _git(source, "update-ref", "refs/remotes/origin/integration", base)
    (source / "a.txt").write_text("2\n")
    _git(source, "stash", "-q")
    assert subprocess.run(["git", "stash", "list"], cwd=source, capture_output=True, text=True).stdout

    report = prepare_run_clone(str(source), base_ref="origin/integration", run_root=str(tmp_path / "runs"))
    clone = Path(report.workdir)

    assert clone.parent == (tmp_path / "runs").resolve()
    assert (clone / ".git").is_dir()  # a real repo, not a worktree pointer file
    head = subprocess.run(["git", "rev-parse", "HEAD"], cwd=clone, capture_output=True, text=True).stdout.strip()
    assert head == base
    assert (clone / "a.txt").read_text() == "1\n"
    assert subprocess.run(["git", "stash", "list"], cwd=clone, capture_output=True, text=True).stdout == ""
    assert subprocess.run(["git", "status", "--porcelain"], cwd=clone, capture_output=True, text=True).stdout == ""
    # A stash in the clone never reaches the source, and vice versa.
    (clone / "a.txt").write_text("clone\n")
    _git(clone, "stash", "-q")
    assert subprocess.run(["git", "stash", "list"], cwd=source, capture_output=True, text=True).stdout.count("\n") == 1

    try:
        prepare_run_clone(str(source), base_ref="origin/nope", run_root=str(tmp_path / "runs"))
    except WorktreePrepError as exc:
        assert "origin/nope" in str(exc)
    else:
        raise AssertionError("an unknown base ref must be refused before cloning")

    # A second run in the same second gets its own directory, never the first one's.
    second = prepare_run_clone(str(source), base_ref="origin/integration", run_root=str(tmp_path / "runs"))
    assert Path(second.workdir) != clone
    assert Path(second.workdir).parent == clone.parent


CHAIN_HARNESS = """
import json, pathlib, sys
brief = sys.stdin.read()
calc = pathlib.Path("calc.py")
if "# Follow-up unit 2" in brief:
    assert "tests/test_mul_neg.py" in brief and "already applied and committed" in brief
    assert "a + b" in calc.read_text()  # unit 1 is in place
    calc.write_text(calc.read_text().replace("abs(a * b)", "a * b"))
    report = {"test_file": "tests/test_mul_neg.py", "changed_files": ["calc.py"], "summary": "mul kept the sign",
              "commands_run": [], "follow_ups": []}
else:
    pathlib.Path("tests/test_add.py").write_text("from calc import add\\n\\n\\ndef test_add_sums():\\n    assert add(2, 3) == 5\\n")
    calc.write_text(calc.read_text().replace("a - b", "a + b"))
    red = pathlib.Path(".atm/follow-ups/1/tests/test_mul_neg.py")
    red.parent.mkdir(parents=True, exist_ok=True)
    red.write_text(MUL_NEG_TEST)
    report = {"test_file": "tests/test_add.py", "changed_files": ["calc.py", "tests/test_add.py"], "summary": "fixed add",
              "commands_run": [], "follow_ups": [{"title": "mul drops the sign", "paths": ["calc.py"],
                                                  "red_test": "tests/test_mul_neg.py", "criterion": CRITERION}]}
print(json.dumps({"type": "result", "subtype": "success", "result": json.dumps(report)}), flush=True)
"""


def _chain_harness(criterion="add(2, 3) returns -1 instead of 5."):
    return CHAIN_HARNESS.replace("MUL_NEG_TEST", repr(MUL_NEG_TEST)).replace("CRITERION", repr(criterion))


def test_chainable_follow_up_becomes_unit_two_in_the_same_clone(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, log_dir = _setup(tmp_path, _chain_harness())
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    assert [u["unit"] for u in report["units"]] == [1, 2]
    assert all(u["passed"] for u in report["units"])
    one, two = report["units"]
    assert one["committed_as"] and one["committed_as"] != report["base_sha"]
    assert two["base_sha"] == one["committed_as"] and two["test_file"] == "tests/test_mul_neg.py"
    assert two["scope"] == ["calc.py"] and two["verified"]["ok"] is True
    (fu,) = one["follow_ups"]
    assert fu["chainable"] is True and fu["chained_as_unit"] == 2 and fu["symbols_in_base"] == ["mul"]
    assert report["head_sha"] == one["committed_as"]  # the last unit stays uncommitted, like a single-unit run
    assert not (artifacts / "follow-ups.json").exists()  # nothing left over: the only follow-up was chained
    assert (artifacts / "brief-unit-2.md").read_text().startswith("# Follow-up unit 2")
    worktree = Path(report["worktree"])
    assert (worktree / "tests" / "test_mul_neg.py").is_file()
    assert (worktree / ".atm" / "units" / "1" / "follow-ups" / "1" / "tests" / "test_mul_neg.py").is_file()
    assert "calc.py" in report["changed_files"] and "tests/test_add.py" in report["changed_files"]
    after = subprocess.run([sys.executable, "-m", "pytest", "-q", "-p", "no:cacheprovider"], cwd=worktree,
                           capture_output=True, text=True, timeout=120)
    assert after.returncode == 0, after.stdout + after.stderr
    staged = [e["event"] for e in _log_events(log_dir) if e["event"].get("type") == "atm.unit_staged"]
    assert staged == [{"type": "atm.unit_staged", "unit": 2, "red_test": "tests/test_mul_neg.py", "from_follow_up": 1}]


def test_follow_up_whose_criterion_is_not_in_the_issue_is_not_chained(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, _chain_harness("multiplication must preserve negative signs always"))
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    assert len(report["units"]) == 1
    (fu,) = report["follow_ups"]
    assert fu["accepted"] is True and fu["chainable"] is False
    assert "criterion not found in the issue" in fu["chain_reason"]
    assert json.loads((artifacts / "follow-ups.json").read_text()) == [fu]


def test_follow_up_without_criterion_is_accepted_but_not_chained(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, _follow_up_harness([("tests/test_mul_neg.py", MUL_NEG_TEST)]))
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    (fu,) = report["follow_ups"]
    assert fu["accepted"] is True and fu["chainable"] is False and "no criterion" in fu["chain_reason"]
    assert len(report["units"]) == 1


HELPER_ONLY_RED = "from helpers import new_thing\n\n\ndef test_new_thing():\n    assert new_thing() == 2\n"


def test_follow_up_test_that_calls_only_new_code_is_not_chained(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    harness = CHAIN_HARNESS.replace(
        "red.write_text(MUL_NEG_TEST)",
        'pathlib.Path("helpers.py").write_text("def new_thing():\\n    return 1\\n"); red.write_text(HELPER_ONLY_RED)',
    ).replace("HELPER_ONLY_RED", repr(HELPER_ONLY_RED)).replace("CRITERION", repr("add(2, 3) returns -1 instead of 5."))
    assert "HELPER_ONLY_RED" not in harness and "new_thing" in harness
    argv, artifacts, _ = _setup(tmp_path, harness)
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    (fu,) = report["follow_ups"]
    assert fu["accepted"] is True, fu  # the red test does fail on behavior
    assert fu["chainable"] is False and "no symbol defined in the base commit" in fu["chain_reason"]
    assert fu["symbols_in_base"] == [] and fu["symbols_elsewhere"] == ["new_thing"]
    assert len(report["units"]) == 1


def test_max_units_caps_the_chain(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, _chain_harness())
    assert harness_worker.main([*argv, "--max-units", "1"]) == 0
    report = json.loads((artifacts / "report.json").read_text())
    assert len(report["units"]) == 1
    (fu,) = report["follow_ups"]
    assert fu["chainable"] is True and "chained_as_unit" not in fu
    assert json.loads((artifacts / "follow-ups.json").read_text()) == [fu]


def test_criterion_and_symbol_gates_in_isolation(tmp_path):
    ok, msg = harness_worker.criterion_in_issue("Claude hook install and uninstall edit the resolved target",
                                                "## Acceptance\n- Claude hook install and uninstall edit the resolved regular-file target.")
    assert ok, msg
    assert harness_worker.criterion_in_issue("short", "short")[0] is False
    assert harness_worker.criterion_in_issue(None, "anything at all here")[0] is False
    assert harness_worker.criterion_in_issue("the moon is made of cheese and nobody knows", "install the hook")[0] is False
    repo = tmp_path / "r"
    repo.mkdir()
    (repo / "calc.py").write_text("def add(a, b):\n    return a + b\n")
    (repo / "test_calc.py").write_text("def only_in_tests():\n    pass\n")
    _git(repo, "init", "-q", "-b", "main"); _git(repo, "add", "."); _git(repo, "commit", "-q", "-m", "i")
    sha = subprocess.run(["git", "rev-parse", "HEAD"], cwd=repo, capture_output=True, text=True).stdout.strip()
    in_base, elsewhere = harness_worker.symbols_defined_in_base(
        "from calc import add\ndef test_x():\n    assert add(1, 2) == 3\n    only_in_tests()\n    brand_new()\n", str(repo), sha)
    assert in_base == ["add"] and elsewhere == ["brand_new", "only_in_tests"]


def test_judge_gate_in_isolation(monkeypatch, tmp_path):
    from agentic_tdd_runner import judge
    issue = {"title": "t", "body": "b"}
    fu = {"title": "gap", "paths": ["a.go"], "criterion": "c"}
    assert judge.judge_follow_up(issue, fu, {}) == {"enabled": False, "ok": True, "reason": "judge disabled"}
    cfg = {"follow_ups": {"judge": {"enabled": True, "threshold": 0.6, "api_key_file": str(tmp_path / "nokey")}}}
    monkeypatch.delenv("TYPESAFE_API_KEY", raising=False)
    closed = judge.judge_follow_up(issue, fu, cfg)
    assert closed["ok"] is False and "no API key" in closed["reason"]
    monkeypatch.setenv("TYPESAFE_API_KEY", "k")
    calls = []

    def fake_post(url, body, key, timeout):
        calls.append((url, body, key))
        return {"model": "jev-1.13.0", "answers": {"serves": {"type": "noul", "noul": 0.81},
                                                   "fit": {"type": "choice", "choice": "within",
                                                           "probabilities": {"within": 0.9, "adjacent": 0.1, "outside": 0.0}}}}

    v = judge.judge_follow_up(issue, fu, cfg, post=fake_post)
    assert v["ok"] is True and v["serves"] == 0.81 and v["fit"] == "within" and v["model"] == "jev-1.13.0"
    assert calls[0][2] == "k" and calls[0][1]["state"]["follow_up"]["title"] == "gap"
    assert set(calls[0][1]["questions"]) == {"serves", "fit"}
    low = judge.judge_follow_up(issue, fu, cfg, post=lambda *a: {"answers": {"serves": {"noul": 0.12}, "fit": {"choice": "adjacent"}}})
    assert low["ok"] is False and "below threshold" in low["reason"]

    def boom(*a):
        raise OSError("down")

    failed = judge.judge_follow_up(issue, fu, cfg, post=boom)
    assert failed["ok"] is False and "judge call failed" in failed["reason"]


def _judge_config(tmp_path, argv):
    config_path = Path(argv[argv.index("--config") + 1])
    config_path.write_text(config_path.read_text() + '\n[follow_ups.judge]\nenabled = true\nthreshold = 0.6\n')


def test_judge_below_threshold_keeps_the_follow_up_but_does_not_chain(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, _chain_harness())
    _judge_config(tmp_path, argv)
    seen = []
    monkeypatch.setattr(harness_worker.follow_up_judge, "judge_follow_up",
                        lambda issue, fu, config, **kw: (seen.append((issue, fu["title"])) or
                                                         {"enabled": True, "ok": False, "serves": 0.1, "fit": "adjacent",
                                                          "threshold": 0.6, "reason": "judge: serves=0.10 fit=adjacent threshold=0.60 (below threshold)"}))
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    assert len(report["units"]) == 1
    (fu,) = report["follow_ups"]
    assert fu["accepted"] is True and fu["chainable"] is False
    assert fu["judge"]["serves"] == 0.1 and "below threshold" in fu["chain_reason"]
    assert seen == [({"title": "add returns the difference", "body": "add(2, 3) returns -1 instead of 5."}, "mul drops the sign")]


def test_judge_above_threshold_chains(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, _chain_harness())
    _judge_config(tmp_path, argv)
    monkeypatch.setattr(harness_worker.follow_up_judge, "judge_follow_up",
                        lambda issue, fu, config, **kw: {"enabled": True, "ok": True, "serves": 0.8, "fit": "within",
                                                         "threshold": 0.6, "reason": "judge: serves=0.80 fit=within threshold=0.60"})
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    assert len(report["units"]) == 2
    (fu,) = report["units"][0]["follow_ups"]
    assert fu["chained_as_unit"] == 2 and "judge: serves=0.80" in fu["chain_reason"]


def test_judge_is_not_called_for_follow_ups_that_fail_the_mechanical_gates(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, _chain_harness("multiplication must preserve negative signs always"))
    _judge_config(tmp_path, argv)
    monkeypatch.setattr(harness_worker.follow_up_judge, "judge_follow_up",
                        lambda *a, **kw: (_ for _ in ()).throw(AssertionError("judge must not run")))
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    (fu,) = report["follow_ups"]
    assert fu["chainable"] is False and "judge" not in fu


def test_effort_reaches_both_harnesses():
    args = harness_worker.parse_args(["--repo", "r", "--issue-file", "i.md", "--harness", "claude", "--effort", "high"])
    cmd, _ = harness_worker.harness_command(args, "/wt", "brief", "/s.json", "/m.txt")
    assert cmd[cmd.index("--effort") + 1] == "high"
    args = harness_worker.parse_args(["--repo", "r", "--issue-file", "i.md", "--harness", "codex", "--model", "gpt-6.1-sol", "--effort", "high"])
    cmd, _ = harness_worker.harness_command(args, "/wt", "brief", "/s.json", "/m.txt")
    assert 'model="gpt-6.1-sol"' in cmd and 'model_reasoning_effort="high"' in cmd
    args = harness_worker.parse_args(["--repo", "r", "--issue-file", "i.md", "--harness", "codex"])
    cmd, _ = harness_worker.harness_command(args, "/wt", "brief", "/s.json", "/m.txt")
    assert not any("model_reasoning_effort" in c for c in cmd)


def test_both_briefs_carry_the_ponytail_style(tmp_path, capsys, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, _chain_harness())
    assert harness_worker.main(argv) == 0
    for name in ("brief.md", "brief-unit-2.md"):
        text = (artifacts / name).read_text()
        assert "## STYLE: ponytail (full)" in text, name
        assert "grep every caller of the function" in text, name
        assert text.index("## STYLE: ponytail") < text.index("## FORBIDDEN"), name


OPENCODE_HARNESS = """
import json, pathlib, sys
assert sys.argv[1:3] == ["run", "--pure"] and "--format" in sys.argv and "--dir" in sys.argv
brief = sys.argv[-1]
assert "add returns the difference" in brief
pathlib.Path("tests/test_add.py").write_text("from calc import add\\n\\n\\ndef test_add_sums():\\n    assert add(2, 3) == 5\\n")
calc = pathlib.Path("calc.py")
calc.write_text(calc.read_text().replace("a - b", "a + b"))
print(json.dumps({"type": "step_start", "part": {"id": "p1"}}), flush=True)
print(json.dumps({"type": "tool_use", "part": {"type": "tool", "tool": "edit", "state": {"status": "completed", "input": {"filePath": "calc.py"}}}}), flush=True)
report = {"test_file": "tests/test_add.py", "changed_files": ["calc.py", "tests/test_add.py"], "summary": "fixed", "commands_run": [], "follow_ups": []}
print(json.dumps({"type": "text", "part": {"text": "Done.\\n" + json.dumps(report)}}), flush=True)
print(json.dumps({"type": "step_finish", "part": {"reason": "stop"}}), flush=True)
"""

PI_HARNESS = """
import json, pathlib, sys
assert sys.argv[1:3] == ["-p", "--mode"] and "--no-skills" in sys.argv and "--no-extensions" in sys.argv
brief = sys.argv[-1]
assert "add returns the difference" in brief
pathlib.Path("tests/test_add.py").write_text("from calc import add\\n\\n\\ndef test_add_sums():\\n    assert add(2, 3) == 5\\n")
calc = pathlib.Path("calc.py")
calc.write_text(calc.read_text().replace("a - b", "a + b"))
print(json.dumps({"type": "session", "id": "s"}), flush=True)
print(json.dumps({"type": "message_update", "assistantMessageEvent": {"type": "text_delta", "delta": "D"}}), flush=True)
report = {"test_file": "tests/test_add.py", "changed_files": ["calc.py", "tests/test_add.py"], "summary": "fixed", "commands_run": [], "follow_ups": []}
print(json.dumps({"type": "message_end", "message": {"role": "assistant", "content": [
    {"type": "thinking", "thinking": "trace the flow"}, {"type": "text", "text": "Done.\\n" + json.dumps(report)}]}}), flush=True)
print(json.dumps({"type": "agent_end"}), flush=True)
"""


def _tail(log_dir) -> str:
    """What the pane shows for the run: the worker log rendered by scripts/tail-run.py."""
    return subprocess.run([sys.executable, str(REPO_ROOT / "scripts" / "tail-run.py"), str(log_dir)],
                          capture_output=True, text=True, timeout=30, env={**os.environ, "NO_COLOR": "1"}).stdout


def test_opencode_harness_fix_passes_the_gates(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, log_dir = _setup(tmp_path, OPENCODE_HARNESS)
    assert harness_worker.main([*argv, "--harness", "opencode", "--model", "ollama/qwen3.8:27b-mlx"]) == 0
    report = json.loads((artifacts / "report.json").read_text())
    assert report["harness"] == "opencode" and report["verified"]["ok"] is True
    out = _tail(log_dir)
    assert "[tool] edit (completed)" in out and "[text] Done." in out


def test_pi_harness_fix_passes_the_gates_and_prints_thinking(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, log_dir = _setup(tmp_path, PI_HARNESS)
    assert harness_worker.main([*argv, "--harness", "pi", "--model", "ollama/qwen3.8:27b-mlx", "--effort", "low"]) == 0
    report = json.loads((artifacts / "report.json").read_text())
    assert report["harness"] == "pi" and report["verified"]["ok"] is True
    out = _tail(log_dir)
    assert "[thinking]" in out and "trace the flow" in out and "[text] Done." in out


def test_env_flag_reaches_the_harness_process(tmp_path, monkeypatch):
    harness = OPENCODE_HARNESS.replace('brief = sys.argv[-1]', 'import os; assert os.environ["OPENCODE_CONFIG"] == "/x/opencode.json"; brief = sys.argv[-1]')
    argv, artifacts, _ = _setup(tmp_path, harness)
    assert harness_worker.main([*argv, "--harness", "opencode", "--env", "OPENCODE_CONFIG=/x/opencode.json"]) == 0
    args = harness_worker.parse_args(["--repo", "r", "--issue-file", "i.md", "--harness", "pi", "--effort", "high", "--model", "ollama/q"])
    cmd, stdin = harness_worker.harness_command(args, "/wt", "brief", "/s.json", "/m.txt")
    assert stdin is None and cmd[-1] == "brief" and cmd[cmd.index("--thinking") + 1] == "high" and "--no-session" in cmd


def test_harness_arg_lands_before_the_brief():
    args = harness_worker.parse_args(["--repo", "r", "--issue-file", "i.md", "--harness", "pi", "--model", "/m",
                                      "--harness-arg=--provider", "--harness-arg=bonsai-mlx", "--harness-arg=-e", "--harness-arg=/x/p.ts"])
    cmd, _ = harness_worker.harness_command(args, "/wt", "the brief", "/s", "/m")
    assert cmd[-5:] == ["--provider", "bonsai-mlx", "-e", "/x/p.ts", "the brief"]
    args = harness_worker.parse_args(["--repo", "r", "--issue-file", "i.md", "--harness", "claude", "--harness-arg=--verbose-x"])
    cmd, stdin = harness_worker.harness_command(args, "/wt", "the brief", "/s", "/m")
    assert cmd[-1] == "--verbose-x" and stdin == "the brief"


def _delivery_setup(tmp_path, monkeypatch, harness=FIXING_HARNESS, command="true"):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, harness)
    _git(tmp_path / "target", "remote", "add", "origin", "https://github.com/owner/calc.git")
    with (tmp_path / "config" / "agent.toml").open("a") as fh:
        fh.write(f"\n[delivery]\ncommand = {json.dumps(command)}\n")
    return argv, artifacts


def test_delivery_runs_the_configured_command_and_exits_4_when_it_fails(tmp_path, monkeypatch, capsys):
    argv, artifacts = _delivery_setup(tmp_path, monkeypatch, command="printf 'gate: waiting\\n'; exit 3")
    assert harness_worker.main(argv) == 4
    delivery = json.loads((artifacts / "report.json").read_text())["delivery"]
    assert delivery["exit_code"] == 3 and delivery["command"] == "printf 'gate: waiting\\n'; exit 3"
    assert delivery["duration_seconds"] >= 0
    assert "gate: waiting" in Path(delivery["output"]).read_text()
    assert "gate: waiting" in capsys.readouterr().out  # the pane


def test_delivery_command_gets_the_committed_branch_and_the_atm_environment(tmp_path, monkeypatch):
    command = ('printf "%s\\n" "$ATM_TITLE" "$ATM_ISSUE" "$ATM_BRANCH" "$ATM_CLONE" "$ATM_REPORT" '
               '"$(git branch --show-current)" "$(git remote get-url origin)" "$(git status --porcelain)"')
    argv, artifacts = _delivery_setup(tmp_path, monkeypatch, command=command)
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    delivery, clone = report["delivery"], report["worktree"]
    assert delivery["exit_code"] == 0 and delivery["branch"].startswith("atm/add-returns-the-difference-")
    assert Path(delivery["output"]).read_text().split("\n") == [
        "add returns the difference", "", delivery["branch"], clone, str(artifacts.resolve() / "report.json"),
        delivery["branch"], "https://github.com/owner/calc.git", "", ""]
    committed = subprocess.run(["git", "show", "--name-only", "--format=", "HEAD"], cwd=clone, capture_output=True, text=True).stdout.split()
    assert sorted(committed) == ["calc.py", "tests/test_add.py"]


def test_delivery_command_gets_configured_tool_path(tmp_path, monkeypatch):
    argv, artifacts = _delivery_setup(tmp_path, monkeypatch, command="delivery-probe")
    tool_dir = tmp_path / "delivery-tools"
    tool_dir.mkdir()
    tool = tool_dir / "delivery-probe"
    tool.write_text("#!/bin/sh\nprintf 'configured delivery tool\\n'\n")
    tool.chmod(0o755)
    with (tmp_path / "config" / "agent.toml").open("a") as fh:
        fh.write(f"\n[tooling]\npath_dirs = [{json.dumps(str(tool_dir))}]\n")

    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    assert report["delivery"]["exit_code"] == 0
    assert Path(report["delivery"]["output"]).read_text() == "configured delivery tool\n"


def test_delivery_command_reads_the_committed_head_in_atm_report(tmp_path, monkeypatch):
    command = ("python3 -c 'import json, os, subprocess; report=json.load(open(os.environ[\"ATM_REPORT\"])); "
               "head=subprocess.check_output([\"git\", \"rev-parse\", \"HEAD\"], text=True).strip(); "
               "print(report[\"head_sha\"] == head)'")
    argv, artifacts = _delivery_setup(tmp_path, monkeypatch, command=command)

    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    assert Path(report["delivery"]["output"]).read_text() == "True\n"
    assert report["head_sha"] == subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=report["worktree"], capture_output=True, text=True, check=True
    ).stdout.strip()


def test_green_run_with_a_follow_up_in_atm_delivers_when_the_target_does_not_ignore_atm(tmp_path, monkeypatch):
    harness = _follow_up_harness([("tests/test_mul_neg.py", MUL_NEG_TEST)])
    argv, artifacts = _delivery_setup(tmp_path, monkeypatch, harness, command="test -z \"$(git status --porcelain)\"")
    ignored = subprocess.run(
        ["git", "check-ignore", "-q", "--no-index", "--", ".atm/follow-ups/1/tests/test_mul_neg.py"],
        cwd=tmp_path / "target",
    )
    assert ignored.returncode == 1
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    assert report["delivery"]["exit_code"] == 0
    assert (Path(report["worktree"]) / ".atm" / "follow-ups" / "1" / "tests" / "test_mul_neg.py").is_file()


def test_without_a_delivery_command_the_run_stops_at_the_verdict(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, FIXING_HARNESS)
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    assert report["delivery"] == {"skipped": "no [delivery].command"}
    assert subprocess.run(["git", "status", "--porcelain"], cwd=report["worktree"], capture_output=True, text=True).stdout


UNUSED = "\n\ndef unused(x):\n    if x is None:\n        return 0\n    return x\n"
FIXED_CALC = "def add(a, b):\n    return a + b\n\n\ndef mul(a, b):\n    return abs(a * b)\n"
PONYTAIL_HARNESS = """
import json, pathlib, sys
brief = sys.stdin.read()
calc = pathlib.Path("calc.py")
if brief.startswith("# Ponytail pass"):
    assert "+def unused(x):" in brief  # the run's diff against the base commit
    calc.write_text(calc.read_text().replace(UNUSED, ""))
    BREAK
    report = {"findings": [{"file": "calc.py", "family": "speculative_feature",
                            "finding": "unused() has no caller: deleted"}], "summary": "cut one function"}
    MUTATE
else:
    pathlib.Path("tests/test_add.py").write_text("from calc import add\\n\\n\\ndef test_add_sums():\\n    assert add(2, 3) == 5\\n")
    calc.write_text(calc.read_text().replace("a - b", "a + b") + UNUSED)
    report = {"test_file": "tests/test_add.py", "changed_files": ["calc.py", "tests/test_add.py"],
              "summary": "fixed add", "commands_run": [], "follow_ups": []}
print(json.dumps({"type": "result", "subtype": "success", "result": json.dumps(report)}), flush=True)
"""


def _ponytail_harness(breaks="pass", mutation="pass"):
    return PONYTAIL_HARNESS.replace("UNUSED", repr(UNUSED)).replace("BREAK", breaks).replace("MUTATE", mutation)


def _committed(clone, *args):
    return subprocess.run(["git", *args], cwd=clone, capture_output=True, text=True, check=True).stdout


def _commit_slop(source):
    (source / ".slop" / "tombstones").mkdir(parents=True)
    (source / ".slop" / "tombstones" / "README.md").write_text("# Tombstones\n")
    _git(source, "add", ".slop")
    _git(source, "commit", "-q", "-m", "slop")


def test_ponytail_pass_keeps_a_cut_that_passes_every_gate_and_tombstones_it(tmp_path, monkeypatch, capsys):
    argv, artifacts = _delivery_setup(tmp_path, monkeypatch, _ponytail_harness())
    _commit_slop(tmp_path / "target")
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    pony = report["ponytail"]
    assert pony["kept"] is True, pony
    assert pony["net_lines_after"] < pony["net_lines_before"]
    assert pony["findings"] == [{"file": "calc.py", "family": "speculative_feature",
                                 "finding": "unused() has no caller: deleted"}]
    clone = report["worktree"]
    assert _committed(clone, "show", "HEAD:calc.py") == FIXED_CALC
    committed = _committed(clone, "show", "--name-only", "--format=", "HEAD").split()
    (tombstone,) = [p for p in committed if p.startswith(".slop/tombstones/T-")]
    assert sorted(set(committed) - {tombstone}) == ["calc.py"]  # the cut; the unit commit below holds the rest
    assert sorted(_committed(clone, "show", "--name-only", "--format=", "HEAD~1").split()) == ["calc.py", "tests/test_add.py"]
    text = _committed(clone, "show", f"HEAD:{tombstone}")
    assert "family: speculative_feature" in text and "artifact: calc.py" in text
    assert 'example: "unused() has no caller: deleted"' in text and "status: accepted" in text
    assert pony["tombstones"] == [tombstone]
    summary = capsys.readouterr().out.split("=== HARNESS WORKER SUMMARY ===")[1]
    assert f"ponytail:   kept, {pony['net_lines_before'] - pony['net_lines_after']} net lines saved" in summary


def test_kept_ponytail_cut_is_its_own_commit_and_delivery_gets_its_findings(tmp_path, monkeypatch):
    argv, artifacts = _delivery_setup(tmp_path, monkeypatch, _ponytail_harness(), command='printf "%s" "$ATM_PONYTAIL"')
    _commit_slop(tmp_path / "target")
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    clone, pony = report["worktree"], report["ponytail"]
    assert _committed(clone, "log", "--format=%s", f"{report['base_sha']}..HEAD").splitlines() == [
        "ponytail: 1 cuts", "atm unit 1: add returns the difference"]
    line = "- calc.py: unused() has no caller: deleted (speculative_feature)"
    assert line in _committed(clone, "log", "-1", "--format=%B")
    assert sorted(_committed(clone, "show", "--name-only", "--format=", "HEAD").split()) == sorted(["calc.py", *pony["tombstones"]])
    assert _committed(clone, "show", "HEAD~1:calc.py") == FIXED_CALC + UNUSED  # the unit commit, pre-ponytail
    assert pony["commit"] == report["head_sha"] == _committed(clone, "rev-parse", "HEAD").strip()
    assert Path(report["delivery"]["output"]).read_text() == line


@pytest.mark.parametrize("mutation", [
    'report["findings"] = []',
    'report["findings"][0]["file"] = "calc.py\\nother.py"',
    'report["findings"][0]["file"] = "calc.py\\n"',
    'report["findings"][0]["finding"] = "\\n"',
])
def test_unusable_ponytail_findings_discard_the_cut_and_delivery_lines(tmp_path, monkeypatch, mutation):
    argv, artifacts = _delivery_setup(tmp_path, monkeypatch, _ponytail_harness(mutation=mutation),
                                      command='printf "[%s]" "$ATM_PONYTAIL"')
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    pony, clone = report["ponytail"], report["worktree"]
    assert pony["kept"] is False and pony["commit"] is None
    assert "invalid ponytail report" in pony["reason"]
    assert Path(report["delivery"]["output"]).read_text() == "[]"
    assert _committed(clone, "log", "--format=%s", f"{report['base_sha']}..HEAD").splitlines() == [
        "atm unit 1: add returns the difference"]


def test_empty_findings_are_valid_when_the_ponytail_makes_no_cut(tmp_path, monkeypatch):
    harness = _ponytail_harness(breaks="calc.write_text(calc.read_text() + UNUSED)",
                                mutation='report["findings"] = []')
    argv, artifacts, _ = _setup(tmp_path, harness)
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    pony = report["ponytail"]
    assert pony["kept"] is False
    assert pony["reason"] == "did not reduce the run's net added lines"
    assert subprocess.run(["git", "status", "--porcelain"], cwd=report["worktree"],
                          capture_output=True, text=True).stdout.strip()


def test_ponytail_cut_that_breaks_the_unit_test_is_discarded(tmp_path, monkeypatch, capsys):
    breaks = 'calc.write_text(calc.read_text().replace("a + b", "a - b"))'
    argv, artifacts = _delivery_setup(tmp_path, monkeypatch, _ponytail_harness(breaks), command='printf "[%s]" "$ATM_PONYTAIL"')
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    pony = report["ponytail"]
    assert pony["kept"] is False and "red/green" in pony["reason"], pony
    assert pony["commit"] is None and Path(report["delivery"]["output"]).read_text() == "[]"
    assert _committed(report["worktree"], "log", "--format=%s", f"{report['base_sha']}..HEAD").splitlines() == [
        "atm unit 1: add returns the difference"]
    clone = report["worktree"]
    assert _committed(clone, "show", "HEAD:calc.py") == FIXED_CALC + UNUSED  # the pre-ponytail diff, delivered
    assert sorted(_committed(clone, "show", "--name-only", "--format=", "HEAD").split()) == ["calc.py", "tests/test_add.py"]
    assert "ponytail:   discarded, 0 net lines saved" in capsys.readouterr().out


def test_ponytail_cut_that_fails_the_target_lint_is_discarded(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, _ponytail_harness())
    with (tmp_path / "config" / "agent.toml").open("a") as fh:
        fh.write('\n[delivery]\nlint_command = "echo LINT BROKE && exit 3"\n')
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    assert report["ponytail"]["kept"] is False and "lint" in report["ponytail"]["reason"]
    assert (Path(report["worktree"]) / "calc.py").read_text() == FIXED_CALC + UNUSED


def test_ponytail_cut_that_passes_the_target_lint_is_kept(tmp_path, monkeypatch):
    argv, artifacts, _ = _setup(tmp_path, _ponytail_harness())
    with (tmp_path / "config" / "agent.toml").open("a") as fh:
        fh.write('\n[delivery]\nlint_command = \"true\"\n')
    assert harness_worker.main(argv) == 0
    assert json.loads((artifacts / "report.json").read_text())["ponytail"]["kept"] is True


def test_ponytail_failure_discards_a_shortened_diff(tmp_path, monkeypatch):
    argv, artifacts, _ = _setup(tmp_path, _ponytail_harness())
    run_harness = harness_worker.run_harness

    def failing_ponytail(*args, **kwargs):
        result = run_harness(*args, **kwargs)
        if args[1].startswith("# Ponytail pass"):
            result["exit_code"] = 1
        return result

    monkeypatch.setattr(harness_worker, "run_harness", failing_ponytail)
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    assert report["ponytail"]["kept"] is False
    assert "harness exited 1" in report["ponytail"]["reason"]
    assert (Path(report["worktree"]) / "calc.py").read_text() == FIXED_CALC + UNUSED


def test_report_changed_files_reflects_files_removed_by_ponytail(tmp_path, monkeypatch):
    harness = _ponytail_harness().replace(
        'calc.write_text(calc.read_text().replace("a - b", "a + b") + UNUSED)',
        'calc.write_text(calc.read_text().replace("a - b", "a + b") + UNUSED)\n'
        '    pathlib.Path("extra.py").write_text("x = 1\\n")',
    ).replace(
        'calc.write_text(calc.read_text().replace(UNUSED, ""))',
        'calc.write_text(calc.read_text().replace(UNUSED, ""))\n'
        '    pathlib.Path("extra.py").unlink()',
    )
    argv, artifacts, _ = _setup(tmp_path, harness)
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    assert report["ponytail"]["kept"] is True
    assert "extra.py" not in report["changed_files"]


@pytest.mark.parametrize("from_github", [True, False])
def test_unit_commit_of_an_issue_run_closes_the_issue(tmp_path, monkeypatch, from_github):
    argv, artifacts = _delivery_setup(tmp_path, monkeypatch, command='printf "$ATM_ISSUE"')
    if from_github:
        i = argv.index("--issue-file")
        argv[i:i + 2] = ["--issue-number", "7", "--github-repo", "owner/calc"]
        monkeypatch.setattr(harness_worker, "load_issue", lambda args: (
            "add returns the difference", "add(2, 3) returns -1 instead of 5."))
    assert harness_worker.main(argv) == 0
    report = json.loads((artifacts / "report.json").read_text())
    clone = report["worktree"]
    assert Path(report["delivery"]["output"]).read_text() == ("7" if from_github else "")
    message = subprocess.run(["git", "log", "-1", "--format=%B"], cwd=clone, capture_output=True, text=True).stdout.strip()
    expected = "atm unit 1: add returns the difference"
    assert message == (f"{expected}\n\nCloses #7" if from_github else expected)


def test_scan_forbidden_reports_only_lines_added_since_base(tmp_path):
    marker = "no" + "qa"  # spelled apart so this file never carries the forbidden pattern itself
    repo = tmp_path / "repo"
    repo.mkdir()
    (repo / "a.py").write_text(f"import os  # {marker}\n")
    _git(repo, "init", "-q")
    _git(repo, "add", ".")
    _git(repo, "commit", "-q", "-m", "base")
    with (repo / "a.py").open("a") as fh:
        fh.write(f"x = 1\nimport sys  # {marker}\n")
    (repo / "b.py").write_text(f"import re  # {marker}\n")
    hits = harness_worker.scan_forbidden(str(repo), ["a.py", "b.py"], [marker], "HEAD")
    assert hits == [f"a.py:3 {marker!r}", f"b.py:1 {marker!r}"]


@pytest.mark.parametrize("gitignore", [".atm/\n", ""])
def test_commit_unit_never_stages_atm_even_when_ignored(tmp_path, gitignore):
    repo = tmp_path / "repo"
    repo.mkdir()
    (repo / ".gitignore").write_text(gitignore)
    (repo / "a.py").write_text("x = 1\n")
    _git(repo, "init", "-q")
    _git(repo, "add", ".")
    _git(repo, "commit", "-q", "-m", "base")
    (repo / "a.py").write_text("x = 2\n")
    (repo / ".atm" / "follow-ups" / "1").mkdir(parents=True)
    (repo / ".atm" / "follow-ups" / "1" / "test_gap.py").write_text("def test_gap():\n    assert False\n")
    harness_worker.commit_unit(str(repo), 1, "fix")
    files = subprocess.run(["git", "show", "--name-only", "--format=", "HEAD"], cwd=repo, capture_output=True,
                           text=True, check=True).stdout.split()
    assert files == ["a.py"]


def test_full_suite_says_it_started_before_it_runs_and_how_it_ended_after(tmp_path, monkeypatch, capsys):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    real = harness_worker.run_command

    def pytest_prints(command, *args, **kwargs):
        if command == "python3 -m pytest":
            print("PYTEST OUTPUT")
        return real(command, *args, **kwargs)

    monkeypatch.setattr(harness_worker, "run_command", pytest_prints)
    argv, _, log_dir = _setup(tmp_path, FIXING_HARNESS)
    assert harness_worker.main(argv) == 0
    out = capsys.readouterr().out
    assert "▶ full suite" in out and "✓ full suite" in out
    assert out.index("▶ full suite") < out.index("PYTEST OUTPUT") < out.index("✓ full suite")
    assert "▶ red half" in out and "✓ red half" in out and "▶ green half" in out and "✓ green half" in out

    steps = [e for e in (entry["event"] for entry in _log_events(log_dir))
             if isinstance(e, dict) and e.get("type") == "atm.step"]
    names = {e["step"] for e in steps}
    for name in ("prepare", "agent unit 1", "red half", "green half", "quality", "gate", "scope", "full suite",
                 "follow-up validation", "ponytail snapshot", "ponytail agent"):
        assert name in names, name
    assert [e["state"] for e in steps if e["step"] == "ponytail snapshot"] == ["started", "passed", "started", "passed"]
    open_steps = []
    for e in steps:  # every end closes the innermost started step: nothing ends without its start first
        if e["state"] == "started":
            open_steps.append(e["step"])
        else:
            assert open_steps.pop() == e["step"] and e["state"] in ("passed", "failed")
    assert open_steps == []


def test_prepare_failure_emits_a_failed_end_event(tmp_path, monkeypatch):
    argv, _, log_dir = _setup(tmp_path, FIXING_HARNESS)

    def invalid_package(*_):
        raise json.JSONDecodeError("invalid package", "{", 0)

    monkeypatch.setattr(harness_worker, "detect_commands", invalid_package)
    with pytest.raises(json.JSONDecodeError):
        harness_worker.main(argv)
    steps = [entry["event"] for entry in _log_events(log_dir)
             if isinstance(entry.get("event"), dict) and entry["event"].get("type") == "atm.step"
             and entry["event"].get("step") == "prepare"]
    assert [event["state"] for event in steps] == ["started", "failed"]


def test_monitor_command_runs_at_every_start_and_end_with_the_step_in_its_environment(tmp_path, monkeypatch):
    monkeypatch.setenv("PYTHONDONTWRITEBYTECODE", "1")
    argv, artifacts, _ = _setup(tmp_path, FIXING_HARNESS)
    seen = tmp_path / "monitor.txt"
    config = Path(argv[argv.index("--config") + 1])
    config.write_text(CONFIG_TOML.replace(
        'command = "true"',
        f'command = \'echo "$ATM_LABEL|$ATM_STEP|$ATM_STATE|$ATM_DURATION|$ATM_REPORT" >> {seen}; exit 7\''))
    assert harness_worker.main([*argv, "--label", "mon"]) == 0  # the monitor's exit code is ignored
    rows = [line.split("|") for line in seen.read_text().splitlines()]
    assert {row[0] for row in rows} == {"mon"}
    assert {row[4] for row in rows} == {str(artifacts.resolve() / "report.json")}
    assert ["full suite", "started", "0"] in [row[1:4] for row in rows]
    (ended,) = [row for row in rows if row[1] == "full suite" and row[2] != "started"]
    assert ended[2] == "passed" and float(ended[3]) >= 0


def test_worker_refuses_to_start_when_nobody_can_watch_it(tmp_path, monkeypatch, capsys):
    argv, artifacts, _ = _setup(tmp_path, FIXING_HARNESS)
    Path(argv[argv.index("--config") + 1]).write_text(CONFIG_TOML.replace('command = "true"', 'command = ""'))
    assert harness_worker.main(argv) == 2
    assert "no monitor: stdout is not a terminal and [monitor].command is empty" in capsys.readouterr().err
    assert not artifacts.exists()


def test_a_silent_step_gets_one_elapsed_line_every_30_seconds(tmp_path, monkeypatch, capsys):
    events = []
    renderer = harness_worker.load_renderer()(tmp_path / "worker-1.jsonl")
    clock = [100.0]
    monkeypatch.setattr(harness_worker.time, "monotonic", lambda: clock[0])
    run_log = harness_worker.RunLog(str(tmp_path / "worker-1.jsonl"), "claude", render=events.append,
                                    visible=renderer.visible)
    run_log.begin("full suite")
    t0 = run_log.quiet_since
    clock[0] = t0 + 29
    run_log.write({"type": "tool_progress"})
    for offset in (29, 30, 45, 60, 89, 90):
        run_log.tick(t0 + offset)
    elapsed = [e["elapsed_seconds"] for e in events if e["type"] == "atm.step_elapsed"]
    assert elapsed == [30, 60, 90]
    run_log.end(True)
    assert [e["state"] for e in events if e["type"] == "atm.step"] == ["started", "passed"]
