"""Tests for CI and the no-mistakes gate: Go changes run Go checks, Python changes run Python checks."""

import os
import re
import shutil
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
WORKFLOWS = ROOT / ".github" / "workflows"


def _stub(bin_dir, name, log):
    path = bin_dir / name
    path.write_text(f'#!/bin/sh\necho "{name} $*" >> "{log}"\n')
    path.chmod(0o755)


def _go_repo(tmp_path, *scripts):
    """A copy of the given scripts in a repo with go.mod, and stubs for every tool they call."""
    repo, bin_dir, log = tmp_path / "repo", tmp_path / "bin", tmp_path / "calls.log"
    (repo / "scripts").mkdir(parents=True)
    bin_dir.mkdir()
    for script in scripts:
        shutil.copy(ROOT / "scripts" / script, repo / "scripts" / script)
    (repo / "go.mod").write_text("module example.com/atm\n\ngo 1.23\n")
    for tool in ("uv", "uvx", "go", "golangci-lint"):
        _stub(bin_dir, tool, log)
    _stub(repo / "scripts", "slopslint.sh", log)
    env = dict(os.environ, PATH=f"{bin_dir}{os.pathsep}{os.environ['PATH']}")
    return repo, env, log


def _lint_command():
    return re.search(r"^  lint: (.+)$", (ROOT / ".no-mistakes.yaml").read_text(), re.M).group(1)


def test_test_script_runs_go_tests_when_go_mod_exists(tmp_path):
    repo, env, log = _go_repo(tmp_path, "test.sh")

    result = subprocess.run(["sh", "scripts/test.sh"], cwd=repo, env=env, capture_output=True, text=True)

    assert result.returncode == 0, result.stdout + result.stderr
    assert "go test ./..." in log.read_text().splitlines()


def test_gate_lint_is_a_script_that_runs_golangci_lint_when_go_mod_exists(tmp_path):
    lint = _lint_command()
    assert re.fullmatch(r"scripts/[\w.-]+", lint), f"commands.lint must be a script, not {lint!r}"
    repo, env, log = _go_repo(tmp_path, Path(lint).name)

    result = subprocess.run(["sh", lint], cwd=repo, env=env, capture_output=True, text=True)

    assert result.returncode == 0, result.stdout + result.stderr
    calls = log.read_text().splitlines()
    assert "golangci-lint run" in calls
    assert any(c.startswith("uvx ruff check") for c in calls), calls


def test_gate_lint_skips_golangci_lint_without_go_mod(tmp_path):
    lint = _lint_command()
    repo, env, log = _go_repo(tmp_path, Path(lint).name)
    (repo / "go.mod").unlink()

    result = subprocess.run(["sh", lint], cwd=repo, env=env, capture_output=True, text=True)

    assert result.returncode == 0, result.stdout + result.stderr
    assert "golangci-lint run" not in log.read_text().splitlines()


def _job_paths(workflow):
    """Paths listed under the workflow's `paths:` keys and its paths-filter `filters:`."""
    return set(re.findall(r"^\s+- ['\"]?([^'\"\s]+)['\"]?$", workflow, re.M))


def test_python_workflows_are_filtered_to_python_paths():
    for name in ("pr.yml", "cd.yml"):
        workflow = (WORKFLOWS / name).read_text()
        paths = _job_paths(workflow)
        for path in ("pyproject.toml", "ruff.toml", ".python-version", "scripts/test.sh", f".github/workflows/{name}"):
            assert path in paths, f"{name} does not filter on {path}"
        assert {"**.py", "**/*.py"} & paths, f"{name} does not filter on Python sources"
        assert not {"**.go", "**/*.go", "go.mod"} & paths, f"{name} runs for Go changes"


def test_go_workflow_runs_make_test_and_lint_on_three_platforms():
    workflow = (WORKFLOWS / "go.yml").read_text()

    for os_name in ("ubuntu-latest", "macos-latest", "windows-latest"):
        assert os_name in workflow
    assert re.search(r"^\s+run: make test$", workflow, re.M)
    assert re.search(r"^\s+run: make lint$", workflow, re.M)
    assert "go-version-file: go.mod" in workflow
    paths = _job_paths(workflow)
    for path in ("go.mod", "go.sum", ".golangci.yml", "Makefile", ".github/workflows/go.yml"):
        assert path in paths, f"go.yml does not filter on {path}"
    assert {"**.go", "**/*.go"} & paths


def test_pull_request_workflows_report_an_always_present_summary_check():
    for name in ("pr.yml", "go.yml"):
        workflow = (WORKFLOWS / name).read_text()
        trigger = workflow.split("\njobs:", 1)[0]
        pull_request = trigger.split("pull_request:", 1)[1].split("\n  push:", 1)[0]
        assert "paths" not in pull_request, f"{name} must start on every PR so its summary check is always present"
        assert "if: always()" in workflow, f"{name} has no summary job that runs when the filtered jobs skip"
        assert "scripts/ci-summary.sh" in workflow


def _summary(*results):
    return subprocess.run(["sh", "scripts/ci-summary.sh", *results], cwd=ROOT, capture_output=True, text=True)


def test_ci_summary_is_green_when_filtered_jobs_succeed_or_skip():
    assert _summary("success", "skipped").returncode == 0
    assert _summary("skipped").returncode == 0


def test_ci_summary_is_red_when_a_job_fails_or_is_cancelled():
    assert _summary("success", "failure").returncode != 0
    assert _summary("cancelled").returncode != 0
