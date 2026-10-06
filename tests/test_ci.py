"""Behavioral checks for CI path selection and workflow contracts."""

import os
import shutil
import subprocess
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent
WORKFLOWS = ROOT / ".github" / "workflows"


def _repo(tmp_path, *, go_mod=False):
    repo = tmp_path / "repo"
    (repo / "scripts").mkdir(parents=True)
    shutil.copy(ROOT / "scripts" / "changed-areas.sh", repo / "scripts" / "changed-areas.sh")
    if go_mod:
        (repo / "go.mod").write_text("module example.com/atm\n\ngo 1.23\n")
    subprocess.run(["git", "init", "-q", str(repo)], check=True)
    subprocess.run(["git", "-C", str(repo), "config", "user.email", "ci@example.com"], check=True)
    subprocess.run(["git", "-C", str(repo), "config", "user.name", "CI test"], check=True)
    subprocess.run(["git", "-C", str(repo), "add", "."], check=True)
    subprocess.run(["git", "-C", str(repo), "commit", "-qm", "base"], check=True)
    base = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"], text=True).strip()
    return repo, base


def _detect(repo, base, tmp_path):
    subprocess.run(["git", "-C", str(repo), "add", "."], check=True)
    subprocess.run(["git", "-C", str(repo), "commit", "-qm", "change"], check=True)
    output = tmp_path / "github-output"
    result = subprocess.run(
        ["sh", "scripts/changed-areas.sh", base],
        cwd=repo,
        env=dict(os.environ, GITHUB_OUTPUT=str(output)),
        capture_output=True,
        text=True,
    )
    assert result.returncode == 0, result.stdout + result.stderr
    return dict(line.split("=", 1) for line in output.read_text().splitlines())


def test_python_changes_select_only_python_checks(tmp_path):
    repo, base = _repo(tmp_path)
    (repo / "runner.py").write_text("value = 1\n")

    assert _detect(repo, base, tmp_path) == {"python": "true", "go": "false"}


def test_go_change_without_module_does_not_select_go_checks(tmp_path):
    repo, base = _repo(tmp_path)
    (repo / "runner.go").write_text("package runner\n")

    assert _detect(repo, base, tmp_path)["go"] == "false"


def test_go_change_with_module_selects_go_checks(tmp_path):
    repo, base = _repo(tmp_path, go_mod=True)
    (repo / "runner.go").write_text("package runner\n")

    assert _detect(repo, base, tmp_path)["go"] == "true"


def test_detector_change_selects_affected_checks(tmp_path):
    repo, base = _repo(tmp_path, go_mod=True)
    script = repo / "scripts" / "changed-areas.sh"
    script.write_text(script.read_text() + "\n")

    assert _detect(repo, base, tmp_path) == {"python": "true", "go": "true"}


def _workflow(name):
    return yaml.load((WORKFLOWS / name).read_text(), Loader=yaml.BaseLoader)


def _steps(job):
    return job["steps"]


def _step_run(job, command):
    return any(step.get("run") == command for step in _steps(job))


def test_pr_workflow_runs_python_matrix_and_keeps_summary_for_filtered_changes():
    workflow = _workflow("pr.yml")
    jobs = workflow["jobs"]
    assert "paths" not in workflow["on"]["pull_request"]
    assert "scripts/changed-areas.sh" in _steps(jobs["changes"])[-1]["run"]
    assert jobs["test"]["needs"] == "changes"
    assert jobs["test"]["if"] == "needs.changes.outputs.python == 'true'"
    assert set(jobs["test"]["strategy"]["matrix"]["python-version"]) == {"3.12", "3.13", "3.14"}
    assert _step_run(jobs["test"], "python -m pytest tests/ -v")
    assert jobs["summary"]["name"] == "Python checks"
    assert jobs["summary"]["needs"] == ["changes", "test"]
    assert jobs["summary"]["if"] == "always()"


def test_go_workflow_skips_without_module_and_keeps_summary():
    workflow = _workflow("go.yml")
    jobs = workflow["jobs"]
    assert "paths" not in workflow["on"]["push"]
    assert "scripts/changed-areas.sh" in _steps(jobs["changes"])[-1]["run"]
    assert "github.event.before" in _steps(jobs["changes"])[-1]["env"]["BASE_SHA"]
    assert jobs["test"]["needs"] == "changes"
    assert jobs["test"]["if"] == "needs.changes.outputs.go == 'true'"
    assert set(jobs["test"]["strategy"]["matrix"]["os"]) == {
        "ubuntu-latest",
        "macos-latest",
        "windows-latest",
    }
    assert _step_run(jobs["test"], "make test")
    assert _step_run(jobs["test"], "make lint")
    assert jobs["summary"]["name"] == "Go checks"
    assert jobs["summary"]["needs"] == ["changes", "test"]
    assert jobs["summary"]["if"] == "always()"


def test_cd_workflow_uses_shared_detector_instead_of_trigger_path_lists():
    workflow = _workflow("cd.yml")
    jobs = workflow["jobs"]
    assert "paths" not in workflow["on"]["push"]
    assert "scripts/changed-areas.sh" in _steps(jobs["changes"])[-1]["run"]
    assert jobs["test"]["needs"] == "changes"
    assert jobs["test"]["if"] == "needs.changes.outputs.python == 'true'"


def test_ci_summary_accepts_success_and_skipped_jobs():
    for results in (("success", "skipped"), ("skipped",)):
        assert subprocess.run(["sh", "scripts/ci-summary.sh", *results], cwd=ROOT).returncode == 0


def test_ci_summary_rejects_failed_or_cancelled_jobs():
    for results in (("success", "failure"), ("cancelled",)):
        assert subprocess.run(
            ["sh", "scripts/ci-summary.sh", *results], cwd=ROOT, capture_output=True
        ).returncode != 0
