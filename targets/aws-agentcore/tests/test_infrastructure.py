import json
import unittest
from pathlib import Path


TARGET_ROOT = Path(__file__).resolve().parents[1]
REPO_ROOT = TARGET_ROOT.parents[1]


class InfrastructureTest(unittest.TestCase):
    def test_cdk_scaffold_files_are_present(self):
        expected = [
            "cdk.json",
            "requirements-cdk.txt",
            "infrastructure/__init__.py",
            "infrastructure/app.py",
            "infrastructure/atm_agentcore_stack.py",
            "infrastructure/gateway/github-tools.json",
        ]

        for relative_path in expected:
            self.assertTrue((TARGET_ROOT / relative_path).is_file(), relative_path)

    def test_cdk_json_uses_generic_placeholders(self):
        cdk_json = json.loads((TARGET_ROOT / "cdk.json").read_text())

        self.assertEqual(cdk_json["app"], "python3 infrastructure/app.py")
        context = cdk_json["context"]
        self.assertEqual(context["atmStackName"], "AtmAgentCoreStack")
        self.assertEqual(context["atmRunnerModelId"], "REPLACE_WITH_BEDROCK_MODEL_ID")
        self.assertEqual(context["githubRepoAllowlist"], "owner/repo")
        self.assertEqual(context["npmScope"], "")
        self.assertEqual(context["githubTokenSecretName"], "atm-agentcore/github-token")
        self.assertEqual(context["rocaTokenSecretName"], "")
        self.assertNotIn("thellmwhisperer", json.dumps(cdk_json))
        self.assertNotIn("llm-whisperer", json.dumps(cdk_json))
        self.assertNotIn("872364107658", json.dumps(cdk_json))
        self.assertNotIn("/Volumes/", json.dumps(cdk_json))

    def test_stack_builds_runtime_image_from_monorepo_root(self):
        stack = (TARGET_ROOT / "infrastructure" / "atm_agentcore_stack.py").read_text()

        self.assertIn("repo_root = Path(__file__).resolve().parents[3]", stack)
        self.assertIn('directory=str(repo_root)', stack)
        self.assertIn('file="targets/aws-agentcore/Dockerfile"', stack)
        self.assertIn('"targets/aws-agentcore/cdk.out"', stack)
        self.assertIn("log_group=logs.LogGroup", stack)
        self.assertIn("RemovalPolicy", stack)
        self.assertIn("removal_policy=RemovalPolicy.DESTROY", stack)
        self.assertNotIn("log_retention=", stack)
        self.assertIn("resources=[runtime_image.repository.repository_arn]", stack)
        self.assertIn("env_value = os.environ.get(env_var)", stack)
        self.assertIn('"ATM_HARNESS_MODULE": "agentic_tdd_runner.agent"', stack)
        self.assertIn('_context_value(node, "npmScope", "ATM_NPM_SCOPE", "")', stack)
        self.assertNotIn('"ATM_NPM_SCOPE": "@thellmwhisperer"', stack)
        self.assertNotIn("github_token_secret.grant_read(runtime_role)", stack)
        self.assertNotIn("roca_token_secret.grant_read(runtime_role)", stack)
        self.assertNotIn("vendor/agentic-team-member", stack)
        self.assertNotIn("/Volumes/", stack)
        self.assertNotIn("llm-whisperer", stack)
        self.assertNotIn("872364107658", stack)

    def test_gateway_tool_schema_names_match_dispatch_allowlist(self):
        schema = json.loads((TARGET_ROOT / "infrastructure" / "gateway" / "github-tools.json").read_text())

        names = {tool["name"] for tool in schema}

        self.assertEqual(
            names,
            {
                "github_get_issue",
                "github_create_branch",
                "github_commit_files",
                "github_open_pr",
                "github_comment_issue",
            },
        )
        for tool in schema:
            self.assertIn("inputSchema", tool)
            self.assertEqual(tool["inputSchema"]["type"], "object")
            self.assertIs(tool["inputSchema"]["additionalProperties"], False)
        commit_files = next(tool for tool in schema if tool["name"] == "github_commit_files")
        file_item_schema = commit_files["inputSchema"]["properties"]["files"]["items"]
        self.assertIs(file_item_schema["additionalProperties"], False)

    def test_makefile_exposes_synth_but_not_deploy(self):
        makefile = (TARGET_ROOT / "Makefile").read_text()

        self.assertIn("cdk-synth:", makefile)
        self.assertIn('$(CDK) synth -a "$(CDK_APP)" -o "$(CDK_OUT)"', makefile)
        self.assertIn("CDK_APP ?= $(PYTHON) infrastructure/app.py", makefile)
        self.assertIn("CDK_OUT ?= ../../.atm/cdk.out", makefile)
        self.assertNotIn("$(CDK) deploy", makefile)

    def test_cdk_outputs_are_ignored(self):
        gitignore = (REPO_ROOT / ".gitignore").read_text()
        dockerignore = (REPO_ROOT / ".dockerignore").read_text()

        self.assertIn("cdk.out/", gitignore)
        self.assertIn("cdk.out/", dockerignore)

    def test_pytest_ini_preserves_default_norecursedirs(self):
        pytest_ini = (REPO_ROOT / "pytest.ini").read_text()

        for expected in [
            "*.egg",
            ".*",
            "_darcs",
            "build",
            "CVS",
            "dist",
            "node_modules",
            "venv",
            "{arch}",
            "cdk.out",
        ]:
            self.assertIn(expected, pytest_ini)


if __name__ == "__main__":
    unittest.main()
