import json
import os
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest.mock import patch

from atm_cloud.github_gateway import load_github_token
from runtime.atm_agent import (
    configure_github_auth_from_env,
    configure_git_https_auth,
    configure_github_packages_auth,
    load_roca_token,
)


class SecretLoadingTest(unittest.TestCase):
    def test_github_token_prefers_plain_env_for_local_dev(self):
        with patch.dict(os.environ, {"GITHUB_TOKEN": "plain"}, clear=True):
            self.assertEqual(load_github_token(), "plain")

    def test_roca_token_prefers_plain_env_for_local_dev(self):
        with patch.dict(os.environ, {"ROCA_CLOUD_API_TOKEN": "plain"}, clear=True):
            self.assertEqual(load_roca_token(), "plain")

    def test_github_token_can_load_secret_json(self):
        fake_client = FakeSecretsClient({"SecretString": json.dumps({"token": "secret-token"})})
        with patch.dict(os.environ, {"GITHUB_TOKEN_SECRET_ARN": "arn"}, clear=True):
            with patch.dict(sys.modules, {"boto3": fake_boto3(fake_client)}):
                self.assertEqual(load_github_token(), "secret-token")

    def test_roca_token_can_load_secret_json(self):
        fake_client = FakeSecretsClient({"SecretString": json.dumps({"token": "secret-token"})})
        with patch.dict(os.environ, {"ROCA_CLOUD_API_TOKEN_SECRET_ARN": "arn"}, clear=True):
            with patch.dict(sys.modules, {"boto3": fake_boto3(fake_client)}):
                self.assertEqual(load_roca_token(), "secret-token")

    def test_runtime_sets_gh_token_from_secret_for_full_harness(self):
        fake_client = FakeSecretsClient({"SecretString": json.dumps({"token": "secret-token"})})
        with tempfile.TemporaryDirectory() as tmp:
            npmrc_path = str(Path(tmp) / ".npmrc")
            askpass_path = str(Path(tmp) / "git-askpass.sh")
            env = {
                "GITHUB_TOKEN_SECRET_ARN": "arn",
                "ATM_NPMRC_PATH": npmrc_path,
                "ATM_GIT_ASKPASS_PATH": askpass_path,
                "ATM_ENABLE_GITHUB_PACKAGES_AUTH": "true",
                "ATM_NPM_SCOPE": "@acme",
            }
            with patch.dict(os.environ, env, clear=True):
                with patch.dict(sys.modules, {"boto3": fake_boto3(fake_client)}):
                    configure_github_auth_from_env()

                self.assertEqual(os.environ["GH_TOKEN"], "secret-token")
                self.assertEqual(os.environ["GITHUB_TOKEN"], "secret-token")
                self.assertEqual(os.environ["NPM_CONFIG_USERCONFIG"], npmrc_path)
                self.assertEqual(os.environ["GIT_ASKPASS"], askpass_path)
                self.assertEqual(os.environ["GIT_TERMINAL_PROMPT"], "0")
                self.assertIn("@acme:registry=https://npm.pkg.github.com", Path(npmrc_path).read_text())

    def test_runtime_writes_github_packages_npmrc_for_bun_install(self):
        with tempfile.TemporaryDirectory() as tmp:
            npmrc_path = Path(tmp) / ".npmrc"
            env = {
                "ATM_ENABLE_GITHUB_PACKAGES_AUTH": "true",
                "ATM_NPM_SCOPE": "@acme",
                "ATM_NPMRC_PATH": str(npmrc_path),
            }
            with patch.dict(os.environ, env, clear=True):
                configure_github_packages_auth("package-token")

            self.assertEqual(
                npmrc_path.read_text(),
                "@acme:registry=https://npm.pkg.github.com\n"
                "//npm.pkg.github.com/:_authToken=package-token\n",
            )
            self.assertEqual(oct(npmrc_path.stat().st_mode & 0o777), "0o600")

    def test_existing_github_token_also_configures_packages_auth(self):
        with tempfile.TemporaryDirectory() as tmp:
            npmrc_path = Path(tmp) / ".npmrc"
            env = {
                "GITHUB_TOKEN": "plain-token",
                "ATM_ENABLE_GITHUB_PACKAGES_AUTH": "true",
                "ATM_NPM_SCOPE": "@acme",
                "ATM_NPMRC_PATH": str(npmrc_path),
            }
            with patch.dict(os.environ, env, clear=True):
                configure_github_auth_from_env()

            self.assertIn("_authToken=plain-token", npmrc_path.read_text())

    def test_github_packages_auth_can_be_disabled(self):
        with tempfile.TemporaryDirectory() as tmp:
            npmrc_path = Path(tmp) / ".npmrc"
            env = {"ATM_ENABLE_GITHUB_PACKAGES_AUTH": "false", "ATM_NPMRC_PATH": str(npmrc_path)}
            with patch.dict(os.environ, env, clear=True):
                configure_github_packages_auth("package-token")

            self.assertFalse(npmrc_path.exists())

    def test_runtime_writes_git_askpass_without_persisting_token(self):
        with tempfile.TemporaryDirectory() as tmp:
            askpass_path = Path(tmp) / "git-askpass.sh"
            with patch.dict(os.environ, {"ATM_GIT_ASKPASS_PATH": str(askpass_path)}, clear=True):
                configure_git_https_auth("git-token")

                content = askpass_path.read_text()
                self.assertIn("x-access-token", content)
                self.assertIn("$GITHUB_TOKEN", content)
                self.assertNotIn("git-token", content)
                self.assertEqual(os.environ["GITHUB_TOKEN"], "git-token")
                self.assertEqual(os.environ["GIT_ASKPASS"], str(askpass_path))
                self.assertEqual(oct(askpass_path.stat().st_mode & 0o777), "0o700")

    def test_git_https_auth_can_be_disabled(self):
        with tempfile.TemporaryDirectory() as tmp:
            askpass_path = Path(tmp) / "git-askpass.sh"
            env = {"ATM_ENABLE_GIT_HTTPS_AUTH": "false", "ATM_GIT_ASKPASS_PATH": str(askpass_path)}
            with patch.dict(os.environ, env, clear=True):
                configure_git_https_auth("git-token")

            self.assertFalse(askpass_path.exists())


class FakeSecretsClient:
    def __init__(self, response):
        self.response = response

    def get_secret_value(self, SecretId):
        return self.response


def fake_boto3(client):
    return types.SimpleNamespace(client=lambda service: client)
