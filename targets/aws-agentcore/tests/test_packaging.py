import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


TARGET_ROOT = Path(__file__).resolve().parents[1]
REPO_ROOT = TARGET_ROOT.parents[1]


class PackagingTest(unittest.TestCase):
    def test_dockerfile_copies_canonical_harness_from_monorepo_root(self):
        dockerfile = (TARGET_ROOT / "Dockerfile").read_text()

        self.assertIn("FROM python:3.14-slim-trixie", dockerfile)
        self.assertIn("USER atm", dockerfile)
        self.assertIn("agentic_tdd_runner ./agentic_tdd_runner", dockerfile)
        self.assertIn("config ./config", dockerfile)
        self.assertIn("targets/aws-agentcore/runtime", dockerfile)
        self.assertIn("targets/aws-agentcore/src", dockerfile)
        self.assertNotIn("ATM_SOURCE_REPO=/app", dockerfile)
        self.assertNotIn("vendor", dockerfile.lower())
        self.assertNotIn("/Volumes/", dockerfile)
        self.assertNotIn("llm-whisperer", dockerfile)

    def test_makefile_builds_image_from_monorepo_context(self):
        makefile = (TARGET_ROOT / "Makefile").read_text()

        self.assertIn("BUILD_CONTEXT ?= ../..", makefile)
        self.assertIn("build -f Dockerfile", makefile)
        self.assertIn("$(BUILD_CONTEXT)", makefile)

    def test_dockerignore_excludes_local_state_and_secrets(self):
        dockerignore = (REPO_ROOT / ".dockerignore").read_text()

        for pattern in [".git", ".env.*.local", "*.local.toml", ".atm/", ".worktrees/"]:
            self.assertIn(pattern, dockerignore)

    def test_smoke_runtime_script_exercises_dry_run_entrypoint(self):
        script = TARGET_ROOT / "scripts" / "smoke-runtime.py"

        with tempfile.TemporaryDirectory() as home:
            env = {
                **os.environ,
                "HOME": home,
                "ATM_ENABLE_BEDROCK_PROXY": "false",
                "ATM_ENABLE_GIT_HTTPS_AUTH": "false",
                "PYTHONPATH": f"{TARGET_ROOT / 'src'}:{TARGET_ROOT}:{REPO_ROOT}",
            }
            completed = subprocess.run(
                [sys.executable, str(script)],
                capture_output=True,
                text=True,
                env=env,
                check=False,
            )

        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn('"status": "succeeded"', completed.stdout)


if __name__ == "__main__":
    unittest.main()
