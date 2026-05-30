from __future__ import annotations

import argparse
import json
import logging
import re
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from uuid import uuid4


LOGGER = logging.getLogger("bedrock_openai_proxy")
TOOL_NAME_PATTERN = re.compile(r"^[A-Za-z0-9_-]+$")
INVALID_TOOL_NAME_CHARS = re.compile(r"[^A-Za-z0-9_-]+")


class BedrockOpenAIProxy:
    def __init__(self, *, model_id: str, region: str | None = None, client: Any | None = None):
        self.model_id = normalize_model_id(model_id)
        self.client = client or _bedrock_client(region)

    def chat_completion(self, payload: dict[str, Any]) -> dict[str, Any]:
        requested_model = payload.get("model")
        if requested_model:
            requested_model_id = normalize_model_id(requested_model)
            if requested_model_id != self.model_id:
                raise ValueError("model override is not allowed for this proxy")
        model_id = self.model_id
        request = build_converse_request(payload, model_id=model_id)
        tool_name_map = sanitize_converse_request(request)
        started = time.monotonic()
        try:
            response = self.client.converse(**request)
        except Exception as exc:
            bedrock_message = bedrock_error_message(exc)
            if bedrock_message:
                raise ValueError(bedrock_message) from exc
            raise
        elapsed_ms = int((time.monotonic() - started) * 1000)
        return openai_response(
            response,
            model_id=model_id,
            elapsed_ms=elapsed_ms,
            tool_name_map=tool_name_map,
        )


def normalize_model_id(model: str) -> str:
    if not isinstance(model, str):
        raise ValueError("model must be a string")
    value = model.strip()
    if not value:
        raise ValueError("model cannot be empty")
    for prefix in ("bedrock/converse/", "bedrock/"):
        if value.startswith(prefix):
            return value.removeprefix(prefix)
    return value


def sanitize_tool_name(name: str) -> str:
    sanitized = INVALID_TOOL_NAME_CHARS.sub("_", name).strip("_")
    sanitized = re.sub(r"_+", "_", sanitized)
    return sanitized or "tool"


def sanitize_converse_request(request: dict[str, Any]) -> dict[str, str]:
    refs = list(tool_name_refs(request))
    originals = list(dict.fromkeys(container[key] for container, key in refs))
    valid_names = {name for name in originals if TOOL_NAME_PATTERN.fullmatch(name)}
    used = set(valid_names)
    by_original: dict[str, str] = {}
    reverse_map: dict[str, str] = {}

    for original in originals:
        if TOOL_NAME_PATTERN.fullmatch(original):
            by_original[original] = original
            continue
        base = sanitize_tool_name(original)
        candidate = base
        suffix = 2
        while candidate in used:
            candidate = f"{base}_{suffix}"
            suffix += 1
        used.add(candidate)
        by_original[original] = candidate
        reverse_map[candidate] = original

    for container, key in refs:
        container[key] = by_original[container[key]]
    return reverse_map


def tool_name_refs(request: dict[str, Any]):
    tool_config = request.get("toolConfig") or {}
    for tool in tool_config.get("tools") or []:
        tool_spec = tool.get("toolSpec") or {}
        if isinstance(tool_spec.get("name"), str):
            yield tool_spec, "name"

    for message in request.get("messages") or []:
        for block in message.get("content") or []:
            tool_use = block.get("toolUse") if isinstance(block, dict) else None
            if isinstance(tool_use, dict) and isinstance(tool_use.get("name"), str):
                yield tool_use, "name"


def build_converse_request(payload: dict[str, Any], *, model_id: str) -> dict[str, Any]:
    system, messages = convert_messages(payload.get("messages") or [])
    request: dict[str, Any] = {
        "modelId": model_id,
        "messages": compact_messages(messages),
    }
    if system:
        request["system"] = system

    inference_config = inference_config_from_payload(payload)
    if inference_config:
        request["inferenceConfig"] = inference_config

    tool_config = tool_config_from_openai(payload.get("tools") or [])
    if tool_config:
        request["toolConfig"] = tool_config
    return request


def convert_messages(messages: list[dict[str, Any]]) -> tuple[list[dict[str, str]], list[dict[str, Any]]]:
    system: list[dict[str, str]] = []
    converted: list[dict[str, Any]] = []

    for message in messages:
        role = message.get("role")
        if role == "system":
            text = text_content(message.get("content"))
            if text:
                system.append({"text": text})
            continue

        if role == "tool":
            converted.append(
                {
                    "role": "user",
                    "content": [
                        {
                            "toolResult": {
                                "toolUseId": message["tool_call_id"],
                                "content": [{"text": text_content(message.get("content"))}],
                                "status": "success",
                            }
                        }
                    ],
                }
            )
            continue

        if role not in {"user", "assistant"}:
            continue

        content_blocks = content_blocks_from_message(message)
        if content_blocks:
            converted.append({"role": role, "content": content_blocks})

    if not converted:
        converted.append({"role": "user", "content": [{"text": ""}]})
    return system, converted


def content_blocks_from_message(message: dict[str, Any]) -> list[dict[str, Any]]:
    blocks: list[dict[str, Any]] = []
    text = text_content(message.get("content"))
    if text:
        blocks.append({"text": text})

    for tool_call in message.get("tool_calls") or []:
        function = tool_call.get("function") or {}
        blocks.append(
            {
                "toolUse": {
                    "toolUseId": tool_call["id"],
                    "name": function["name"],
                    "input": parse_arguments(function.get("arguments")),
                }
            }
        )
    return blocks


def compact_messages(messages: list[dict[str, Any]]) -> list[dict[str, Any]]:
    compacted: list[dict[str, Any]] = []
    for message in messages:
        if compacted and compacted[-1]["role"] == message["role"]:
            compacted[-1]["content"].extend(message["content"])
        else:
            compacted.append({"role": message["role"], "content": list(message["content"])})
    return compacted


def tool_config_from_openai(tools: list[dict[str, Any]]) -> dict[str, Any] | None:
    converted = []
    for tool in tools:
        if tool.get("type") != "function":
            continue
        function = tool.get("function") or {}
        name = function.get("name")
        if not name:
            continue
        converted.append(
            {
                "toolSpec": {
                    "name": name,
                    "description": function.get("description") or name,
                    "inputSchema": {"json": function.get("parameters") or {"type": "object"}},
                }
            }
        )
    return {"tools": converted} if converted else None


def inference_config_from_payload(payload: dict[str, Any]) -> dict[str, Any]:
    config: dict[str, Any] = {}
    if "temperature" in payload:
        config["temperature"] = float(payload["temperature"])
    if "top_p" in payload:
        config["topP"] = float(payload["top_p"])
    if "max_tokens" in payload:
        config["maxTokens"] = int(payload["max_tokens"])
    return config


def openai_response(
    response: dict[str, Any],
    *,
    model_id: str,
    elapsed_ms: int,
    tool_name_map: dict[str, str] | None = None,
) -> dict[str, Any]:
    message = response.get("output", {}).get("message", {})
    content_blocks = message.get("content") or []
    text_parts: list[str] = []
    tool_calls: list[dict[str, Any]] = []

    for block in content_blocks:
        if "text" in block:
            text_parts.append(block["text"])
        if "toolUse" in block:
            tool_use = block["toolUse"]
            tool_name = tool_use["name"]
            tool_calls.append(
                {
                    "id": tool_use["toolUseId"],
                    "type": "function",
                    "function": {
                        "name": (tool_name_map or {}).get(tool_name, tool_name),
                        "arguments": json.dumps(tool_use.get("input") or {}),
                    },
                }
            )

    assistant_message: dict[str, Any] = {
        "role": "assistant",
        "content": "\n".join(part for part in text_parts if part),
    }
    if tool_calls:
        assistant_message["tool_calls"] = tool_calls

    usage = response.get("usage") or {}
    prompt_tokens = usage.get("inputTokens", 0)
    completion_tokens = usage.get("outputTokens", 0)
    prompt_per_second = tokens_per_second(prompt_tokens, elapsed_ms)
    predicted_per_second = tokens_per_second(completion_tokens, elapsed_ms)

    return {
        "id": f"chatcmpl-{uuid4().hex}",
        "object": "chat.completion",
        "created": int(time.time()),
        "model": model_id,
        "choices": [
            {
                "index": 0,
                "message": assistant_message,
                "finish_reason": finish_reason(response.get("stopReason")),
            }
        ],
        "usage": {
            "prompt_tokens": prompt_tokens,
            "completion_tokens": completion_tokens,
            "total_tokens": usage.get("totalTokens", prompt_tokens + completion_tokens),
        },
        "timings": {
            "prompt_ms": None,
            "predicted_ms": elapsed_ms,
            "prompt_per_second": prompt_per_second,
            "predicted_per_second": predicted_per_second,
        },
    }


def tokens_per_second(tokens: int | float | None, elapsed_ms: int | float | None) -> float:
    if not tokens or not elapsed_ms or elapsed_ms <= 0:
        return 0.0
    return float(tokens) * 1000.0 / float(elapsed_ms)


def finish_reason(stop_reason: str | None) -> str:
    if stop_reason == "tool_use":
        return "tool_calls"
    if stop_reason == "max_tokens":
        return "length"
    return "stop"


def text_content(value: Any) -> str:
    if value is None:
        return ""
    if isinstance(value, str):
        return value
    if isinstance(value, list):
        parts = []
        for item in value:
            if isinstance(item, dict) and item.get("type") == "text":
                parts.append(str(item.get("text") or ""))
        return "\n".join(part for part in parts if part)
    return str(value)


def parse_arguments(value: Any) -> dict[str, Any]:
    if isinstance(value, dict):
        return value
    if not value:
        return {}
    try:
        parsed = json.loads(value)
    except json.JSONDecodeError:
        return {}
    return parsed if isinstance(parsed, dict) else {}


def bedrock_error_message(exc: Exception) -> str | None:
    response = getattr(exc, "response", None)
    if not isinstance(response, dict):
        return None
    error = response.get("Error")
    if not isinstance(error, dict):
        return None
    code = str(error.get("Code") or exc.__class__.__name__)
    message = str(error.get("Message") or exc)
    return f"{code}: {message}"


def _bedrock_client(region: str | None = None) -> Any:
    import boto3

    kwargs = {"service_name": "bedrock-runtime"}
    if region:
        kwargs["region_name"] = region
    return boto3.client(**kwargs)


def chat_completion_http_response(
    proxy: BedrockOpenAIProxy,
    raw_body: bytes | None = None,
    *,
    content_length: str | None = None,
    body_stream: Any | None = None,
) -> tuple[int, dict[str, Any]]:
    try:
        if body_stream is not None:
            length = int(content_length or "0")
            if length < 0:
                raise ValueError("content-length must be non-negative")
            raw_body = body_stream.read(length)
        raw_body = raw_body or b""
        payload = json.loads(raw_body.decode("utf-8") or "{}")
        return 200, proxy.chat_completion(payload)
    except json.JSONDecodeError as exc:
        return 400, {"error": {"message": str(exc), "type": "invalid_request"}}
    except (KeyError, TypeError, ValueError) as exc:
        return 400, {"error": {"message": str(exc), "type": "invalid_request"}}
    except Exception as exc:  # pragma: no cover - defensive HTTP boundary.
        LOGGER.exception("bedrock proxy request failed")
        return 500, {"error": {"message": str(exc), "type": exc.__class__.__name__}}


def make_handler(proxy: BedrockOpenAIProxy) -> type[BaseHTTPRequestHandler]:
    class Handler(BaseHTTPRequestHandler):
        def do_GET(self) -> None:
            if self.path == "/health":
                self.respond(200, {"ok": True, "model": proxy.model_id})
                return
            self.respond(404, {"error": "not found"})

        def do_POST(self) -> None:
            if self.path != "/v1/chat/completions":
                self.respond(404, {"error": "not found"})
                return
            status, response = chat_completion_http_response(
                proxy,
                content_length=self.headers.get("content-length"),
                body_stream=self.rfile,
            )
            self.respond(status, response)

        def log_message(self, fmt: str, *args: Any) -> None:
            LOGGER.info("%s - %s", self.address_string(), fmt % args)

        def respond(self, status: int, payload: dict[str, Any]) -> None:
            body = json.dumps(payload).encode("utf-8")
            self.send_response(status)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    return Handler


def serve(*, host: str, port: int, model_id: str, region: str | None = None) -> None:
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    proxy = BedrockOpenAIProxy(model_id=model_id, region=region)
    server = ThreadingHTTPServer((host, port), make_handler(proxy))
    LOGGER.info("Bedrock OpenAI proxy listening on http://%s:%s for %s", host, port, proxy.model_id)
    server.serve_forever()


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="OpenAI-compatible proxy backed by Bedrock Converse.")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=11435)
    parser.add_argument("--model", required=True)
    parser.add_argument("--region")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    serve(host=args.host, port=args.port, model_id=args.model, region=args.region)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
