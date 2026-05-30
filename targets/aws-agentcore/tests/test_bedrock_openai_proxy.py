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


if __name__ == "__main__":
    unittest.main()
