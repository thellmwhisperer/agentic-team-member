"""The slop gate runs with every test run, so pytest, CI, no-mistakes and ATM's own verdict all enforce it."""
import subprocess
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent


def test_slop_gate_holds_the_committed_ceilings():
    result = subprocess.run([str(REPO / "scripts" / "slopslint.sh"), "check", "--classify", "--enforce"],
                            cwd=REPO, capture_output=True, text=True, timeout=300)
    assert result.returncode == 0, result.stderr[-2000:] or result.stdout[-2000:]
