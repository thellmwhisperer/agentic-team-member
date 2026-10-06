"""Tests for scripts/arm-auto-merge.sh: High-risk PRs wait for the risk-reviewed label."""

import os
import subprocess
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "scripts" / "arm-auto-merge.sh"
WORKFLOW = ROOT / ".github" / "workflows" / "auto-merge.yml"

FAKE_GH = """#!/bin/sh
echo "$*" >> "$GH_LOG"
case "$*" in
  "pr view"*"--json body"*) [ "${GH_FAIL_BODY:-0}" = 0 ] || exit 42; cat "$GH_BODY" ;;
  "pr view"*"--json labels"*) [ "${GH_FAIL_LABELS:-0}" = 0 ] || exit 42; cat "$GH_LABELS" ;;
esac
"""


def run_script(tmp_path, body, labels="", token="pat", check=True, fail_body=False, fail_labels=False):
    bin_dir = tmp_path / "bin"
    bin_dir.mkdir()
    gh = bin_dir / "gh"
    gh.write_text(FAKE_GH)
    gh.chmod(0o755)
    (tmp_path / "body").write_text(body)
    (tmp_path / "labels").write_text(labels)
    log = tmp_path / "gh.log"
    log.touch()
    env = dict(
        os.environ,
        PATH=f"{bin_dir}{os.pathsep}{os.environ['PATH']}",
        GH_LOG=str(log),
        GH_BODY=str(tmp_path / "body"),
        GH_LABELS=str(tmp_path / "labels"),
        GITHUB_REPOSITORY="owner/repo",
        GH_TOKEN=token,
        GH_FAIL_BODY="1" if fail_body else "0",
        GH_FAIL_LABELS="1" if fail_labels else "0",
    )
    result = subprocess.run(["sh", str(SCRIPT), "42"], env=env, capture_output=True, text=True, check=check)
    if not check:
        return result, log.read_text()
    merges = [line for line in log.read_text().splitlines() if line.startswith("pr merge")]
    return result.stdout, merges


@pytest.mark.parametrize("body", [
    "## Summary\nx\n\n## Risk Assessment\n🚨 High: x\n",
    "## Summary\nx\n",
    "## Risk Assessment\n\n",
    "## Risk Assessment Notes\nLow: not the assessment\n\n## Risk Assessment\n🚨 High: x\n",
    "## Risk Assessment\n\n## Low-priority tests\nThis section is not the risk level.\n",
    "## Risk Assessment\n\n## Risk Assessment\nLow: not the first assessment\n",
])
def test_high_or_unknown_risk_without_label_does_not_arm(tmp_path, body):
    stdout, merges = run_script(tmp_path, body)

    assert merges == []
    assert "risk high: waiting for risk-reviewed" in stdout


@pytest.mark.parametrize("body,labels", [
    ("## Risk Assessment\n🚨 High: x\n", "bug\nrisk-reviewed\n"),
    ("## Summary\nx\n", "risk-reviewed\n"),
    ("## Risk Assessment\n✅ Low: x\n", ""),
    ("## Risk Assessment\r\n\r\n⚠️ Medium: x\r\n", ""),
])
def test_low_medium_or_reviewed_risk_arms_once(tmp_path, body, labels):
    _, merges = run_script(tmp_path, body, labels)

    assert merges == ["pr merge 42 --auto --rebase --repo owner/repo"]


def test_empty_token_fails_before_calling_gh(tmp_path):
    result, gh_calls = run_script(tmp_path, "## Risk Assessment\n✅ Low: x\n", token="", check=False)

    assert result.returncode == 1
    assert "AUTO_MERGE_TOKEN is not set" in result.stdout + result.stderr
    assert gh_calls == ""


@pytest.mark.parametrize("body,fail_body,fail_labels", [
    ("## Risk Assessment\n✅ Low: x\n", True, False),
    ("## Risk Assessment\n🚨 High: x\n", False, True),
])
def test_failed_github_queries_fail_script(tmp_path, body, fail_body, fail_labels):
    result, gh_calls = run_script(
        tmp_path, body, check=False, fail_body=fail_body, fail_labels=fail_labels
    )

    assert result.returncode == 42
    assert "pr merge" not in gh_calls


def test_workflow_arms_with_auto_merge_token():
    lines = [line.strip() for line in WORKFLOW.read_text().splitlines()]

    assert "GH_TOKEN: ${{ secrets.AUTO_MERGE_TOKEN }}" in lines
    assert "GH_TOKEN: ${{ github.token }}" not in lines
