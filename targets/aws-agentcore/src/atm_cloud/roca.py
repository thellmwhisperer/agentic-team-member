from __future__ import annotations

import json
import urllib.request
from typing import Any
from urllib.parse import urlparse


class UrlLibHttp:
    """Small JSON HTTP transport so the runtime has no requests dependency."""

    def post_json(self, url: str, payload: dict[str, Any], headers: dict[str, str]) -> dict[str, Any]:
        body = json.dumps(payload).encode("utf-8")
        request = urllib.request.Request(
            url,
            data=body,
            headers={**headers, "content-type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=30) as response:
            return json.loads(response.read().decode("utf-8"))


class RocaMcpClient:
    """JSON-RPC MCP client for the Roca Cloud endpoint."""

    def __init__(self, mcp_url: str, api_token: str, *, http: Any | None = None):
        parsed = urlparse(mcp_url)
        if parsed.scheme != "https" or not parsed.netloc:
            raise ValueError("ROCA Cloud MCP URL must be an https URL")
        self.mcp_url = mcp_url
        self.api_token = api_token
        self.http = http or UrlLibHttp()
        self._next_id = 1

    def store(
        self,
        *,
        layer: str,
        content: str,
        project: str | None = None,
        origin: str = "agent",
        source_agent: str = "atm-aws-agentcore",
        metadata: dict[str, Any] | None = None,
    ) -> Any:
        arguments: dict[str, Any] = {
            "layer": layer,
            "content": content,
            "origin": origin,
            "source_agent": source_agent,
        }
        if project:
            arguments["project"] = project
        if metadata is not None:
            arguments["metadata"] = metadata
        return self._call_tool("roca_store", arguments)

    def _call_tool(self, name: str, arguments: dict[str, Any]) -> Any:
        request_id = self._next_id
        self._next_id += 1
        payload = {
            "jsonrpc": "2.0",
            "id": request_id,
            "method": "tools/call",
            "params": {"name": name, "arguments": arguments},
        }
        response = self.http.post_json(
            self.mcp_url,
            payload,
            {"authorization": f"Bearer {self.api_token}"},
        )
        if "error" in response:
            raise RuntimeError(response["error"])
        return _unwrap_mcp_result(response.get("result"))


def _unwrap_mcp_result(result: Any) -> Any:
    if not isinstance(result, dict):
        return result
    content = result.get("content")
    if not content:
        return result
    first = content[0]
    if not isinstance(first, dict) or "text" not in first:
        return result
    text = first["text"]
    try:
        return json.loads(text)
    except (TypeError, json.JSONDecodeError):
        return text
