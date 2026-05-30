import unittest

from atm_cloud.job import AtmJob, JobValidationError


class AtmJobContractTest(unittest.TestCase):
    def test_builds_defaults_for_issue_job(self):
        job = AtmJob.from_dict({"repo": "acme/example", "issue_number": 42})

        self.assertEqual(job.repo, "acme/example")
        self.assertEqual(job.issue_number, 42)
        self.assertEqual(job.base_branch, "main")
        self.assertEqual(job.mode, "pull_request")
        self.assertEqual(job.roca_project, "acme/example")
        self.assertEqual(job.run_id, "acme-example-42-1")
        self.assertEqual(job.branch_name, "atm-agentcore/acme-example-42-1")

    def test_accepts_explicit_run_id_and_roca_project(self):
        job = AtmJob.from_dict(
            {
                "repo": "acme/payments",
                "issue_number": 9,
                "run_id": "payments-tax-fix",
                "roca_project": "client-a",
                "mode": "dry_run",
            }
        )

        self.assertEqual(job.run_id, "payments-tax-fix")
        self.assertEqual(job.roca_project, "client-a")
        self.assertEqual(job.branch_name, "atm-agentcore/payments-tax-fix")

    def test_blank_roca_project_defaults_to_repo(self):
        job = AtmJob.from_dict({"repo": "acme/example", "issue_number": 42, "roca_project": "   "})

        self.assertEqual(job.roca_project, "acme/example")

    def test_rejects_invalid_repo_slug(self):
        with self.assertRaises(JobValidationError):
            AtmJob.from_dict({"repo": "not-a-slug", "issue_number": 1})

    def test_rejects_unsafe_run_id(self):
        with self.assertRaises(JobValidationError):
            AtmJob.from_dict(
                {
                    "repo": "acme/payments",
                    "issue_number": 1,
                    "run_id": "../escape",
                }
            )
