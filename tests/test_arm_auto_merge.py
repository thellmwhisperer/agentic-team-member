"""Tests for scripts/arm-auto-merge.sh: High-risk PRs wait for the risk-reviewed label."""

import os
import subprocess
from pathlib import Path

import pytest

SCRIPT = Path(__file__).resolve().parent.parent / "scripts" / "arm-auto-merge.sh"

FAKE_GH = """#!/bin/sh
echo "$*" >> "$GH_LOG"
case "$*" in
  "pr view"*"--json body"*) cat "$GH_BODY" ;;
  "pr view"*"--json labels"*) cat "$GH_LABELS" ;;
esac
"""


def run_script(tmp_path, body, labels=""):
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
    )
    result = subprocess.run(["sh", str(SCRIPT), "42"], env=env, capture_output=True, text=True, check=True)
    merges = [line for line in log.read_text().splitlines() if line.startswith("pr merge")]
    return result.stdout, merges


@pytest.mark.parametrize("body", [
    "## Summary\nx\n\n## Risk Assessment\n🚨 High: x\n",
    "## Summary\nx\n",
    "## Risk Assessment\n\n",
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
