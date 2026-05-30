import os
import unittest
from unittest.mock import patch

from atm_cloud.github_gateway import GitHubGatewayClient, dispatch_tool


class GitHubGatewayTest(unittest.TestCase):
    def test_repo_allowlist_fails_closed_when_unset(self):
        client = GitHubGatewayClient(token="token", transport=RecordingTransport())

        with patch.dict(os.environ, {}, clear=True):
            with self.assertRaisesRegex(ValueError, "GITHUB_REPO_ALLOWLIST"):
                client.get_issue(repo="acme/repo", issue_number=1)

    def test_repo_allowlist_permits_explicit_repo(self):
        transport = RecordingTransport([{"number": 1, "title": "Fix"}])
        client = GitHubGatewayClient(token="token", transport=transport)

        with patch.dict(os.environ, {"GITHUB_REPO_ALLOWLIST": "acme/repo"}, clear=True):
            issue = client.get_issue(repo="acme/repo", issue_number=1)

        self.assertEqual(issue["number"], 1)
        self.assertEqual(transport.requests[0][1], "https://api.github.com/repos/acme/repo/issues/1")

    def test_commit_files_rejects_malformed_entries_before_dispatch(self):
        client = FakeCommitClient()
        payload = {
            "repo": "acme/repo",
            "branch": "atm-agentcore/fix-1",
            "message": "commit",
            "files": ["oops"],
        }

        with self.assertRaisesRegex(ValueError, r"files\[0\]"):
            dispatch_tool("github_commit_files", payload, client)

        self.assertEqual(client.calls, [])

    def test_commit_files_rejects_non_string_content(self):
        client = FakeCommitClient()
        payload = {
            "repo": "acme/repo",
            "branch": "atm-agentcore/fix-1",
            "message": "commit",
            "files": [{"path": "README.md", "content": {"not": "text"}}],
        }

        with self.assertRaisesRegex(ValueError, "content must be a string"):
            dispatch_tool("github_commit_files", payload, client)

        self.assertEqual(client.calls, [])


class RecordingTransport:
    def __init__(self, responses=None):
        self.responses = list(responses or [])
        self.requests = []

    def request_json(self, method, url, *, headers=None, payload=None):
        self.requests.append((method, url, headers, payload))
        return self.responses.pop(0) if self.responses else {}


class FakeCommitClient:
    def __init__(self):
        self.calls = []

    def commit_files(self, **kwargs):
        self.calls.append(kwargs)
        return {"ok": True}


if __name__ == "__main__":
    unittest.main()
