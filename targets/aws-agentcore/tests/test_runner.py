import os
import unittest
from unittest.mock import patch

from atm_cloud.job import AtmJob
from atm_cloud.runner import AtmCloudRunner, CommandResult
from atm_cloud.runner import SubprocessAtmHarness, _extract_pr_url, _run_log_dir, _safe_repo_cache_name


class FakeMemory:
    def __init__(self):
        self.queries = []
        self.stores = []

    def query(self, **kwargs):
        self.queries.append(kwargs)
        return {"items": [{"id": 1, "content": "previous handoff"}]}

    def store(self, **kwargs):
        self.stores.append(kwargs)
        return {"id": 2}


class FakeHarness:
    def __init__(self, result):
        self.result = result
        self.calls = []

    def run(self, job, context):
        self.calls.append((job, context))
        return self.result


class FailingQueryMemory(FakeMemory):
    def query(self, **kwargs):
        self.queries.append(kwargs)
        raise RuntimeError("roca query unavailable")


class FailingStoreMemory(FakeMemory):
    def store(self, **kwargs):
        self.stores.append(kwargs)
        raise RuntimeError("roca store unavailable")


class FailingHarness:
    def __init__(self):
        self.calls = []

    def run(self, job, context):
        self.calls.append((job, context))
        raise RuntimeError("harness timed out")


class AtmCloudRunnerTest(unittest.TestCase):
    def test_successful_run_reads_context_and_stores_handoff(self):
        memory = FakeMemory()
        harness = FakeHarness(
            CommandResult(
                exit_code=0,
                stdout="PR: https://github.com/acme/repo/pull/5",
                stderr="",
                metadata={"pr_url": "https://github.com/acme/repo/pull/5"},
            )
        )
        runner = AtmCloudRunner(memory=memory, harness=harness)
        job = AtmJob.from_dict({"repo": "acme/repo", "issue_number": 12})

        result = runner.run(job)

        self.assertEqual(result.status, "succeeded")
        self.assertEqual([event.phase for event in result.events], ["memory_context", "atm_harness", "memory_handoff"])
        self.assertEqual(memory.queries[0]["project"], "acme/repo")
        self.assertEqual(harness.calls[0][1]["items"][0]["content"], "previous handoff")
        self.assertIn("succeeded", memory.stores[0]["content"])
        self.assertEqual(memory.stores[0]["metadata"]["pr_url"], "https://github.com/acme/repo/pull/5")

    def test_failed_run_still_writes_handoff(self):
        memory = FakeMemory()
        harness = FakeHarness(CommandResult(exit_code=2, stdout="", stderr="tests failed"))
        runner = AtmCloudRunner(memory=memory, harness=harness)
        job = AtmJob.from_dict({"repo": "acme/repo", "issue_number": 12})

        result = runner.run(job)

        self.assertEqual(result.status, "failed")
        self.assertIn("failed", memory.stores[0]["content"])
        self.assertEqual(memory.stores[0]["metadata"]["exit_code"], 2)

    def test_failed_run_uses_stdout_as_failure_excerpt_when_stderr_is_empty(self):
        memory = FakeMemory()
        harness = FakeHarness(CommandResult(exit_code=1, stdout="quality gate failed", stderr=""))
        runner = AtmCloudRunner(memory=memory, harness=harness)
        job = AtmJob.from_dict({"repo": "acme/repo", "issue_number": 12})

        runner.run(job)

        self.assertIn("quality gate failed", memory.stores[0]["content"])

    def test_successful_run_extracts_pr_url_from_harness_output(self):
        memory = FakeMemory()
        harness = FakeHarness(
            CommandResult(
                exit_code=0,
                stdout="done\n  [PR] https://github.com/acme/repo/pull/7\n",
                stderr="",
            )
        )
        runner = AtmCloudRunner(memory=memory, harness=harness)
        job = AtmJob.from_dict({"repo": "acme/repo", "issue_number": 12})

        runner.run(job)

        self.assertEqual(memory.stores[0]["metadata"]["pr_url"], "https://github.com/acme/repo/pull/7")
        self.assertIn("PR: https://github.com/acme/repo/pull/7.", memory.stores[0]["content"])

    def test_memory_query_failure_uses_empty_context_and_continues(self):
        memory = FailingQueryMemory()
        harness = FakeHarness(CommandResult(exit_code=0, stdout="ok", stderr=""))
        runner = AtmCloudRunner(memory=memory, harness=harness)
        job = AtmJob.from_dict({"repo": "acme/repo", "issue_number": 12})

        result = runner.run(job)

        self.assertEqual(result.status, "succeeded")
        self.assertEqual(result.events[0].phase, "memory_context")
        self.assertEqual(result.events[0].status, "failed")
        self.assertEqual(harness.calls[0][1], {"items": []})
        self.assertIn("roca query unavailable", result.events[0].metadata["error"])

    def test_harness_exception_returns_deterministic_failure(self):
        memory = FakeMemory()
        harness = FailingHarness()
        runner = AtmCloudRunner(memory=memory, harness=harness)
        job = AtmJob.from_dict({"repo": "acme/repo", "issue_number": 12})

        result = runner.run(job)

        self.assertEqual(result.status, "failed")
        self.assertEqual(result.command.exit_code, 1)
        self.assertEqual(result.events[1].phase, "atm_harness")
        self.assertEqual(result.events[1].status, "failed")
        self.assertEqual(result.command.metadata["exception"], "RuntimeError")
        self.assertIn("harness timed out", memory.stores[0]["content"])

    def test_memory_store_failure_returns_failed_result_event(self):
        memory = FailingStoreMemory()
        harness = FakeHarness(CommandResult(exit_code=0, stdout="ok", stderr=""))
        runner = AtmCloudRunner(memory=memory, harness=harness)
        job = AtmJob.from_dict({"repo": "acme/repo", "issue_number": 12})

        result = runner.run(job)

        self.assertEqual(result.status, "failed")
        self.assertEqual(result.command.exit_code, 0)
        self.assertEqual(result.events[-1].phase, "memory_handoff")
        self.assertEqual(result.events[-1].status, "failed")
        self.assertIn("roca store unavailable", result.events[-1].metadata["error"])

    def test_dry_run_skips_harness_and_writes_handoff(self):
        memory = FakeMemory()
        harness = FakeHarness(CommandResult(exit_code=99, stdout="", stderr="should not run"))
        runner = AtmCloudRunner(memory=memory, harness=harness)
        job = AtmJob.from_dict({"repo": "acme/repo", "issue_number": 12, "mode": "dry_run"})

        result = runner.run(job)

        self.assertEqual(result.status, "succeeded")
        self.assertEqual([event.phase for event in result.events], ["memory_context", "dry_run", "memory_handoff"])
        self.assertEqual(harness.calls, [])
        self.assertTrue(memory.stores[0]["metadata"]["dry_run"])

    def test_subprocess_harness_module_is_configurable(self):
        harness = SubprocessAtmHarness(python="python", module="custom.atm", timeout=1)
        job = AtmJob.from_dict({"repo": "acme/repo", "issue_number": 12})

        with self.assertRaises(FileNotFoundError):
            # Force subprocess to fail before execution so we can inspect the command through
            # the deterministic constructor path in a real runtime this module must exist.
            SubprocessAtmHarness(
                python="/missing/python",
                module="custom.atm",
                repo_root="/tmp/repo",
            ).run(job, {})

        self.assertEqual(harness.module, "custom.atm")

    def test_subprocess_harness_clones_repo_cache_when_repo_root_not_supplied(self):
        harness = SubprocessAtmHarness(python="python", module="custom.atm", timeout=1)
        job = AtmJob.from_dict({"repo": "acme/repo", "issue_number": 12})

        env = {"ATM_RUN_NONCE": "run-123"}
        with patch.dict("os.environ", env):
            with patch("atm_cloud.runner.ensure_github_repo_cache", return_value="/tmp/repos/acme__repo"):
                with patch("atm_cloud.runner.subprocess.run") as run:
                    run.return_value.returncode = 0
                    run.return_value.stdout = ""
                    run.return_value.stderr = ""
                    result = harness.run(job, {})

        self.assertEqual(result.exit_code, 0)
        command = result.metadata["command"]
        self.assertIn("--repo", command)
        self.assertIn("/tmp/repos/acme__repo", command)
        self.assertIn("--log-dir", command)
        self.assertIn("/tmp/atm-agentcore/logs/run-123", command)
        self.assertEqual(result.metadata["log_dir"], "/tmp/atm-agentcore/logs/run-123")

    def test_subprocess_harness_passes_github_token_to_child_env(self):
        harness = SubprocessAtmHarness(
            python="python",
            module="custom.atm",
            repo_root="/tmp/repo",
            timeout=1,
            github_token="secret-token",
        )
        job = AtmJob.from_dict({"repo": "acme/repo", "issue_number": 12})

        with patch.dict("os.environ", {}, clear=True):
            with patch("atm_cloud.runner.subprocess.run") as run:
                run.return_value.returncode = 0
                run.return_value.stdout = ""
                run.return_value.stderr = ""
                harness.run(job, {})

        env = run.call_args.kwargs["env"]
        self.assertEqual(env["GH_TOKEN"], "secret-token")
        self.assertEqual(env["GITHUB_TOKEN"], "secret-token")
        self.assertNotIn("GH_TOKEN", os.environ)

    def test_subprocess_harness_uses_rendered_runtime_config(self):
        harness = SubprocessAtmHarness(python="python", module="custom.atm", repo_root="/tmp/repo", timeout=1)
        job = AtmJob.from_dict({"repo": "acme/repo", "issue_number": 12})

        with patch.dict("os.environ", {"ATM_CONFIG": "/tmp/atm-agentcore/agentcore-agent.toml"}):
            with patch("atm_cloud.runner.subprocess.run") as run:
                run.return_value.returncode = 0
                run.return_value.stdout = ""
                run.return_value.stderr = ""
                result = harness.run(job, {})

        command = result.metadata["command"]
        self.assertIn("--config", command)
        self.assertIn("/tmp/atm-agentcore/agentcore-agent.toml", command)

    def test_job_config_ref_overrides_runtime_config(self):
        harness = SubprocessAtmHarness(python="python", module="custom.atm", repo_root="/tmp/repo", timeout=1)
        job = AtmJob.from_dict(
            {
                "repo": "acme/repo",
                "issue_number": 12,
                "config_ref": "/app/config/custom.toml",
            }
        )

        with patch.dict("os.environ", {"ATM_CONFIG": "/tmp/atm-agentcore/agentcore-agent.toml"}):
            with patch("atm_cloud.runner.subprocess.run") as run:
                run.return_value.returncode = 0
                run.return_value.stdout = ""
                run.return_value.stderr = ""
                result = harness.run(job, {})

        command = result.metadata["command"]
        self.assertIn("--config", command)
        self.assertIn("/app/config/custom.toml", command)
        self.assertNotIn("/tmp/atm-agentcore/agentcore-agent.toml", command)

    def test_repo_cache_name_is_safe(self):
        self.assertEqual(
            _safe_repo_cache_name("acme/example-repo"),
            "acme__example-repo",
        )

    def test_extract_pr_url(self):
        self.assertEqual(
            _extract_pr_url("opened https://github.com/acme/repo/pull/8", ""),
            "https://github.com/acme/repo/pull/8",
        )

    def test_run_log_dir_is_sanitized(self):
        with patch.dict("os.environ", {"ATM_LOG_DIR": "/tmp/custom-logs"}):
            self.assertEqual(_run_log_dir("repo/issue 41"), "/tmp/custom-logs/repo-issue-41")
