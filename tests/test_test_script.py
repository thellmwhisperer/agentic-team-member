"""Tests for scripts/test.sh: the tests run under Python 3.12 whatever `python3` is first on PATH."""

import os
import re
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def test_runs_pytest_even_when_first_python3_is_too_old(tmp_path):
    stub = tmp_path / "python3"
    stub.write_text("#!/bin/sh\necho 'Python 3.9.6'\nexit 1\n")
    stub.chmod(0o755)
    env = dict(os.environ, PATH=f"{tmp_path}{os.pathsep}{os.environ['PATH']}")

    result = subprocess.run(
        ["sh", "scripts/test.sh", "--collect-only", "-q", "tests/test_config.py"],
        cwd=ROOT, env=env, capture_output=True, text=True,
    )

    assert result.returncode == 0, result.stdout + result.stderr
    # scripts/test.sh adds its own -q, so pytest lists tests per file: "tests/test_config.py: 6"
    assert re.search(r"^tests/test_config\.py: [1-9]", result.stdout, re.M), result.stdout
