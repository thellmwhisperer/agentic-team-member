import json
import unittest

from atm_cloud.roca import RocaMcpClient


class FakeHttp:
    def __init__(self):
        self.requests = []

    def post_json(self, url, payload, headers):
        self.requests.append((url, payload, headers))
        return {
            "jsonrpc": "2.0",
            "id": payload["id"],
            "result": {"content": [{"type": "text", "text": json.dumps({"ok": True})}]},
        }


class RocaMcpClientTest(unittest.TestCase):
    def test_query_uses_mcp_tools_call(self):
        http = FakeHttp()
        client = RocaMcpClient("https://roca.example/mcp", "secret", http=http)

        result = client.query(query="latest handoff", project="aws", limit=3)

        self.assertEqual(result, {"ok": True})
        url, payload, headers = http.requests[0]
        self.assertEqual(url, "https://roca.example/mcp")
        self.assertEqual(headers["authorization"], "Bearer secret")
        self.assertEqual(payload["method"], "tools/call")
        self.assertEqual(payload["params"]["name"], "roca_query")
        self.assertEqual(payload["params"]["arguments"]["project"], "aws")

    def test_store_uses_roca_store_tool(self):
        http = FakeHttp()
        client = RocaMcpClient("https://roca.example/mcp", "secret", http=http)

        client.store(
            layer="handoff",
            project="aws",
            source_agent="atm-aws-agentcore",
            content="done",
            metadata={"run_id": "r1"},
        )

        _, payload, _ = http.requests[0]
        self.assertEqual(payload["params"]["name"], "roca_store")
        self.assertEqual(payload["params"]["arguments"]["metadata"], {"run_id": "r1"})
