"""`atm-run.py run` launches one harness-worker run."""

import subprocess
import sys
from pathlib import Path

ATM_RUN = Path(__file__).resolve().parents[1] / "scripts" / "atm-run.py"


def test_run_skips_delivery_with_a_boolean_no_deliver_flag():
    proc = subprocess.run([sys.executable, str(ATM_RUN), "run", "--help"], capture_output=True, text=True, timeout=30)
    assert proc.returncode == 0, proc.stderr
    assert "--no-deliver" in proc.stdout
    assert "--deliver" not in proc.stdout
