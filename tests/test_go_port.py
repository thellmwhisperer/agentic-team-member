"""The Go port's behaviour contract (#150 step 1), seen from the Python gate that decides until step 2."""

import json
import shutil
import subprocess
import sys
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).resolve().parents[1]


@pytest.mark.skipif(shutil.which("go") is None, reason="no Go toolchain on PATH")
def test_go_contract_passes_and_every_skip_names_the_step_that_unskips_it():
    proc = subprocess.run(["go", "test", "-json", "./..."], cwd=REPO_ROOT, capture_output=True, text=True,
                          timeout=600)
    assert proc.returncode == 0, proc.stdout[-4000:] + proc.stderr
    events = [json.loads(line) for line in proc.stdout.splitlines() if line.startswith("{")]
    outcome = {(e["Package"], e["Test"]): e["Action"] for e in events
               if e.get("Test") and e["Action"] in ("pass", "fail", "skip")}
    output = {}
    for e in events:
        if e.get("Test") and e["Action"] == "output":
            output[e["Package"], e["Test"]] = output.get((e["Package"], e["Test"]), "") + e["Output"]
    e2e = "github.com/thellmwhisperer/agentic-team-member/internal/e2e"
    for scenario in ("fixing", "helper-only", "new-module", "scope-breaking", "follow-up", "ponytail-cuts"):
        assert outcome.get((e2e, f"TestFakeAgentPlaysTheScenario/{scenario}")) == "pass", scenario
    for protocol in ("claude", "codex", "opencode", "pi"):
        assert outcome.get((e2e, f"TestFakeAgentSpeaksTheProtocolOfItsName/{protocol}")) == "pass", protocol
    for name in ("TestGhFailsClosed",):
        assert outcome.get((e2e, name)) == "pass", name
    if sys.platform != "win32":
        assert outcome.get((e2e, "TestSleepingAgentHoldsStdoutThroughAGrandchild")) == "pass"
    skipped = [key for key, action in outcome.items() if action == "skip"]
    assert (e2e, "TestRunWritesEveryStepStartBeforeItsEnd") in skipped
    for key in skipped:
        assert "unskipped by #150" in output.get(key, ""), key
