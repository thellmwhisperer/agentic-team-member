"""The pane shows what `atm-run.py tail` shows: `atm-run.py run` is the command `--pane` types there."""

import importlib.util
import json
import os
import sys
import textwrap

from tests.test_harness_worker import CONFIG_TOML, FIXING_HARNESS, REPO_ROOT, _git

spec = importlib.util.spec_from_file_location("atm_run", REPO_ROOT / "scripts" / "atm-run.py")
atm_run = importlib.util.module_from_spec(spec)
spec.loader.exec_module(atm_run)

FAKE_GH = f"""#!{sys.executable}
import json
print(json.dumps({{"title": "add returns the difference", "body": "add(2, 3) returns -1 instead of 5."}}))
"""


def test_run_renders_the_pane_with_the_tail_renderer(tmp_path, monkeypatch, capfd):
    repo = tmp_path / "target"
    (repo / "tests").mkdir(parents=True)
    (repo / "pyproject.toml").write_text('[project]\nname = "calc"\nversion = "0"\n\n[tool.pytest.ini_options]\n')
    (repo / ".gitignore").write_text("__pycache__/\n.pytest_cache/\n.worktree/\n")
    (repo / "calc.py").write_text("def add(a, b):\n    return a - b\n")
    (repo / "tests" / "test_calc.py").write_text("from calc import add\n\n\ndef test_add_zero():\n    assert add(1, 0) == 1\n")
    _git(repo, "init", "-q", "-b", "main")
    _git(repo, "add", ".")
    _git(repo, "commit", "-q", "-m", "init")
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    (bin_dir / "gh").write_text(FAKE_GH)
    (bin_dir / "claude").write_text(f"#!{sys.executable}\n" + textwrap.dedent(FIXING_HARNESS))
    for tool in bin_dir.iterdir():
        tool.chmod(0o755)
    (tmp_path / "tools.json").write_text("[]")
    config = tmp_path / "agent.toml"
    config.write_text(CONFIG_TOML + f'\n[launch]\nrepo = "{repo}"\nbase_ref = "main"\n'
                                    'github_repo = "o/r"\nissue_number = 7\n')
    monkeypatch.setenv("PATH", f"{bin_dir}:{os.environ['PATH']}")
    monkeypatch.setenv("NO_COLOR", "1")
    monkeypatch.setattr(atm_run, "RUNS", tmp_path / "runs")
    monkeypatch.chdir(tmp_path)  # main() moves to the repo root; this puts the test back afterwards

    code = atm_run.main(["run", "--config", str(config), "--label", "pane", "--harness", "claude", "--no-deliver"])

    pane = capfd.readouterr().out
    assert code == 0, pane
    assert "▶ #1 Write  tests/test_add.py" in pane
    assert "[tool_result]" not in pane and "[tool]" not in pane
    assert pane.rstrip().splitlines()[-1].startswith("✓ RESULT  PASS")
    stdout_txt = (tmp_path / "runs" / "pane" / "stdout.txt").read_text()
    assert "[PREPARE]" in stdout_txt and "▶ #1 Write" in stdout_txt and "\x1b[" not in stdout_txt
    assert json.loads((tmp_path / "runs" / "pane" / "report.json").read_text())["verified"]["ok"] is True
