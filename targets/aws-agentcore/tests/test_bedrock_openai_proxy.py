import json
from io import BytesIO
import unittest

from runtime import bedrock_openai_proxy as proxy


class BedrockOpenAIProxyTest(unittest.TestCase):
    def test_build_converse_request_translates_openai_tools_and_messages(self):
        payload = {
            "model": "bedrock/converse/provider.model-v1:0",
            "messages": [
                {"role": "system", "content": "You are a coding agent."},
                {"role": "user", "content": "Read a file"},
            ],
            "tools": [
                {
                    "type": "function",
                    "function": {
                        "name": "read_file",
                        "description": "Read file",
                        "parameters": {
                            "type": "object",
                            "required": ["path"],
                            "properties": {"path": {"type": "string"}},
                        },
                    },
                }
            ],
            "temperature": 0.1,
            "top_p": 0.9,
        }

        request = proxy.build_converse_request(payload, model_id=proxy.normalize_model_id(payload["model"]))

        self.assertEqual(request["modelId"], "provider.model-v1:0")
        self.assertEqual(request["system"], [{"text": "You are a coding agent."}])
        self.assertEqual(request["messages"], [{"role": "user", "content": [{"text": "Read a file"}]}])
        self.assertEqual(request["inferenceConfig"], {"temperature": 0.1, "topP": 0.9})
        tool = request["toolConfig"]["tools"][0]["toolSpec"]
        self.assertEqual(tool["name"], "read_file")
        self.assertEqual(tool["inputSchema"]["json"]["required"], ["path"])

    def test_tool_round_trip_preserves_ids_and_arguments(self):
        payload = {
            "messages": [
                {
                    "role": "assistant",
                    "content": "",
                    "tool_calls": [
                        {
                            "id": "call_1",
                            "type": "function",
                            "function": {
                                "name": "run_command",
                                "arguments": '{"command":"rg term src"}',
                            },
                        }
                    ],
                },
                {"role": "tool", "tool_call_id": "call_1", "content": "src/file.ts"},
            ]
        }

        _, messages = proxy.convert_messages(payload["messages"])

        self.assertEqual(messages[0]["role"], "assistant")
        self.assertEqual(
            messages[0]["content"][0]["toolUse"],
            {"toolUseId": "call_1", "name": "run_command", "input": {"command": "rg term src"}},
        )
        self.assertEqual(messages[1]["role"], "user")
        self.assertEqual(messages[1]["content"][0]["toolResult"]["toolUseId"], "call_1")

    def test_openai_response_translates_bedrock_tool_use(self):
        bedrock_response = {
            "stopReason": "tool_use",
            "usage": {"inputTokens": 10, "outputTokens": 3, "totalTokens": 13},
            "output": {
                "message": {
                    "role": "assistant",
                    "content": [
                        {"text": "I'll inspect it."},
                        {
                            "toolUse": {
                                "toolUseId": "tooluse_123",
                                "name": "read_file",
                                "input": {"path": "src/app.ts"},
                            }
                        },
                    ],
                }
            },
        }

        response = proxy.openai_response(
            bedrock_response,
            model_id="qwen.qwen3-coder-next",
            elapsed_ms=42,
        )

        choice = response["choices"][0]
        self.assertEqual(choice["finish_reason"], "tool_calls")
        self.assertEqual(choice["message"]["content"], "I'll inspect it.")
        self.assertEqual(choice["message"]["tool_calls"][0]["id"], "tooluse_123")
        self.assertEqual(choice["message"]["tool_calls"][0]["function"]["name"], "read_file")
        self.assertEqual(choice["message"]["tool_calls"][0]["function"]["arguments"], '{"path": "src/app.ts"}')
        self.assertEqual(response["usage"]["total_tokens"], 13)

    def test_openai_response_reports_predicted_tokens_per_second(self):
        bedrock_response = {
            "stopReason": "tool_use",
            "usage": {"inputTokens": 14765, "outputTokens": 445, "totalTokens": 15210},
            "output": {"message": {"role": "assistant", "content": [{"text": "ok"}]}},
        }

        response = proxy.openai_response(
            bedrock_response,
            model_id="provider.model-v1:0",
            elapsed_ms=15600,
        )

        self.assertAlmostEqual(response["timings"]["predicted_per_second"], 28.5, places=1)
        self.assertEqual(response["timings"]["predicted_ms"], 15600)

    def test_compact_messages_merges_consecutive_user_blocks(self):
        messages = [
            {"role": "user", "content": [{"text": "first"}]},
            {"role": "user", "content": [{"text": "second"}]},
            {"role": "assistant", "content": [{"text": "ok"}]},
        ]

        compacted = proxy.compact_messages(messages)

        self.assertEqual(len(compacted), 2)
        self.assertEqual(compacted[0]["content"], [{"text": "first"}, {"text": "second"}])

    def test_chat_completion_rejects_model_override(self):
        client = FakeBedrockClient()
        bedrock_proxy = proxy.BedrockOpenAIProxy(model_id="provider.model-v1:0", client=client)

        with self.assertRaisesRegex(ValueError, "model override"):
            bedrock_proxy.chat_completion(
                {
                    "model": "provider.other-model-v1:0",
                    "messages": [{"role": "user", "content": "hello"}],
                }
            )

        self.assertEqual(client.requests, [])

    def test_chat_completion_accepts_matching_model_alias(self):
        client = FakeBedrockClient()
        bedrock_proxy = proxy.BedrockOpenAIProxy(model_id="provider.model-v1:0", client=client)

        response = bedrock_proxy.chat_completion(
            {
                "model": "bedrock/provider.model-v1:0",
                "messages": [{"role": "user", "content": "hello"}],
            }
        )

        self.assertEqual(client.requests[0]["modelId"], "provider.model-v1:0")
        self.assertEqual(response["model"], "provider.model-v1:0")

    def test_http_handler_returns_400_for_invalid_json(self):
        bedrock_proxy = proxy.BedrockOpenAIProxy(model_id="provider.model-v1:0", client=FakeBedrockClient())

        status, body = proxy.chat_completion_http_response(bedrock_proxy, b"{")

        self.assertEqual(status, 400)
        self.assertEqual(body["error"]["type"], "invalid_request")

    def test_http_handler_returns_400_for_request_validation_errors(self):
        bedrock_proxy = proxy.BedrockOpenAIProxy(model_id="provider.model-v1:0", client=FakeBedrockClient())
        payload = json.dumps({"model": "provider.other-model-v1:0", "messages": []}).encode("utf-8")

        status, body = proxy.chat_completion_http_response(bedrock_proxy, payload)

        self.assertEqual(status, 400)
        self.assertEqual(body["error"]["type"], "invalid_request")

    def test_http_handler_returns_400_for_invalid_content_length(self):
        bedrock_proxy = proxy.BedrockOpenAIProxy(model_id="provider.model-v1:0", client=FakeBedrockClient())

        status, body = proxy.chat_completion_http_response(
            bedrock_proxy,
            content_length="not-a-number",
            body_stream=BytesIO(b"{}"),
        )

        self.assertEqual(status, 400)
        self.assertEqual(body["error"]["type"], "invalid_request")

    def test_sanitize_tool_name_replaces_invalid_chars(self):
        self.assertEqual(proxy.sanitize_tool_name("read_file"), "read_file")
        self.assertEqual(proxy.sanitize_tool_name("functions.read_file"), "functions_read_file")
        self.assertEqual(proxy.sanitize_tool_name("a/b c"), "a_b_c")
        self.assertEqual(proxy.sanitize_tool_name("..."), "tool")

    def test_sanitize_converse_request_rewrites_history_and_specs(self):
        request = {
            "modelId": "provider.model-v1:0",
            "toolConfig": {
                "tools": [
                    {"toolSpec": {"name": "functions.read_file", "description": "Read file"}},
                    {"toolSpec": {"name": "run_command", "description": "Run command"}},
                ]
            },
            "messages": [
                {
                    "role": "assistant",
                    "content": [
                        {"toolUse": {"toolUseId": "x", "name": "functions.read_file", "input": {}}},
                        {"toolUse": {"toolUseId": "y", "name": "run_command", "input": {}}},
                    ],
                }
            ],
        }

        name_map = proxy.sanitize_converse_request(request)

        self.assertEqual(request["toolConfig"]["tools"][0]["toolSpec"]["name"], "functions_read_file")
        self.assertEqual(request["toolConfig"]["tools"][1]["toolSpec"]["name"], "run_command")
        self.assertEqual(request["messages"][0]["content"][0]["toolUse"]["name"], "functions_read_file")
        self.assertEqual(request["messages"][0]["content"][1]["toolUse"]["name"], "run_command")
        self.assertEqual(name_map["functions_read_file"], "functions.read_file")
        self.assertNotIn("run_command", name_map)
        self.assertNotIn("_toolNameMap", request)

    def test_sanitize_converse_request_preserves_valid_name_when_invalid_name_collides(self):
        request = {
            "toolConfig": {
                "tools": [
                    {"toolSpec": {"name": "a.b", "description": "Invalid"}},
                    {"toolSpec": {"name": "a_b", "description": "Valid"}},
                    {"toolSpec": {"name": "a/b", "description": "Also invalid"}},
                ]
            },
            "messages": [],
        }

        name_map = proxy.sanitize_converse_request(request)

        sent = [tool["toolSpec"]["name"] for tool in request["toolConfig"]["tools"]]
        self.assertEqual(len(set(sent)), 3)
        self.assertIn("a_b", sent)
        self.assertEqual(name_map["a_b_2"], "a.b")
        self.assertEqual(name_map["a_b_3"], "a/b")

    def test_openai_response_reverts_sanitized_tool_name(self):
        bedrock_response = {
            "stopReason": "tool_use",
            "usage": {"inputTokens": 1, "outputTokens": 1, "totalTokens": 2},
            "output": {
                "message": {
                    "role": "assistant",
                    "content": [
                        {
                            "toolUse": {
                                "toolUseId": "tooluse_123",
                                "name": "functions_read_file",
                                "input": {"path": "src/app.ts"},
                            }
                        },
                    ],
                }
            },
        }

        response = proxy.openai_response(
            bedrock_response,
            model_id="provider.model-v1:0",
            elapsed_ms=42,
            tool_name_map={"functions_read_file": "functions.read_file"},
        )

        self.assertEqual(
            response["choices"][0]["message"]["tool_calls"][0]["function"]["name"],
            "functions.read_file",
        )

    def test_chat_completion_sanitizes_request_and_reverts_response_tool_name(self):
        client = FakeBedrockClient(
            response={
                "stopReason": "tool_use",
                "usage": {"inputTokens": 1, "outputTokens": 1, "totalTokens": 2},
                "output": {
                    "message": {
                        "role": "assistant",
                        "content": [
                            {"toolUse": {"toolUseId": "tooluse_123", "name": "functions_read_file", "input": {}}}
                        ],
                    }
                },
            }
        )
        bedrock_proxy = proxy.BedrockOpenAIProxy(model_id="provider.model-v1:0", client=client)

        response = bedrock_proxy.chat_completion(
            {
                "messages": [{"role": "user", "content": "hello"}],
                "tools": [
                    {
                        "type": "function",
                        "function": {"name": "functions.read_file", "parameters": {"type": "object"}},
                    }
                ],
            }
        )

        sent_tool = client.requests[0]["toolConfig"]["tools"][0]["toolSpec"]["name"]
        returned_tool = response["choices"][0]["message"]["tool_calls"][0]["function"]["name"]
        self.assertEqual(sent_tool, "functions_read_file")
        self.assertEqual(returned_tool, "functions.read_file")

    def test_http_handler_returns_400_for_bedrock_validation_errors(self):
        bedrock_proxy = proxy.BedrockOpenAIProxy(
            model_id="provider.model-v1:0",
            client=FakeBedrockValidationClient(),
        )
        payload = json.dumps({"messages": [{"role": "user", "content": "hello"}]}).encode("utf-8")

        status, body = proxy.chat_completion_http_response(bedrock_proxy, payload)

        self.assertEqual(status, 400)
        self.assertEqual(body["error"]["type"], "invalid_request")
        self.assertIn("ValidationException", body["error"]["message"])


class FakeBedrockClient:
    def __init__(self, response=None):
        self.requests = []
        self.response = response

    def converse(self, **request):
        self.requests.append(request)
        if self.response is not None:
            return self.response
        return {
            "stopReason": "end_turn",
            "usage": {"inputTokens": 1, "outputTokens": 1, "totalTokens": 2},
            "output": {"message": {"role": "assistant", "content": [{"text": "ok"}]}},
        }


class FakeBedrockValidationClient:
    def converse(self, **request):
        raise FakeClientError("ValidationException", "toolUse.name failed validation")


class FakeClientError(Exception):
    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.response = {"Error": {"Code": code, "Message": message}}


if __name__ == "__main__":
    unittest.main()
