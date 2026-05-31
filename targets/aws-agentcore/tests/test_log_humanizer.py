import json
import unittest

from atm_cloud.log_humanizer import format_message, strip_cloudwatch_prefix


class LogHumanizerTest(unittest.TestCase):
    def test_strips_cloudwatch_prefix(self):
        line = (
            "2026-05-29T15:01:01.000000+00:00 "
            "2026/05/29/[runtime-logs]abc "
            "--- Step 7 | 0.8s | 5210->22 tok | 26.8 tok/s | finish=tool_calls ---"
        )

        self.assertEqual(
            strip_cloudwatch_prefix(line),
            "--- Step 7 | 0.8s | 5210->22 tok | 26.8 tok/s | finish=tool_calls ---",
        )

    def test_formats_step_tool_and_permission(self):
        self.assertEqual(
            format_message("--- Step 7 | 0.8s | 5210->22 tok | 26.8 tok/s | finish=tool_calls ---"),
            ["[step 07] 0.8s  5210->22 tok  26.8 tok/s  finish=tool_calls"],
        )
        self.assertEqual(
            format_message('  [TOOL] ask_harness({"intent": "write_regression_test"})'),
            ["  tool: ask_harness intent=write_regression_test"],
        )
        self.assertEqual(
            format_message(
                "    PERMISSION GRANTED: create or edit only `src/events/routeMessage.test.ts`."
            ),
            ["  permission granted: create or edit only `src/events/routeMessage.test.ts`."],
        )

    def test_formats_target_challenge(self):
        self.assertEqual(
            format_message(
                "    TARGET CHALLENGE ACCEPTED: rerouted from `a::b` to `src/events/client.ts::routeMessage`."
            ),
            ["  target challenge accepted: rerouted from `a::b` to `src/events/client.ts::routeMessage`."],
        )

    def test_drops_discovery_json_noise(self):
        self.assertEqual(format_message('      "rank_tokens": ['), [])
        self.assertEqual(format_message("      ]"), [])
        self.assertEqual(format_message("      ],"), [])
        self.assertEqual(
            format_message("    +export function __setClientForTests(value: unknown): void {"),
            [],
        )

    def test_formats_model_say_lines(self):
        self.assertEqual(
            format_message("  [SAY] Now let me fix the source code."),
            ["  model: Now let me fix the source code."],
        )

    def test_formats_pr_and_agentcore_completion(self):
        self.assertEqual(
            format_message("  [PR] https://github.com/acme/example/pull/79"),
            ["[pr] https://github.com/acme/example/pull/79"],
        )

        raw = json.dumps(
            {
                "timestamp": "2026-05-29T15:01:45.103Z",
                "level": "INFO",
                "message": "Invocation completed successfully (240.535s)",
                "logger": "bedrock_agentcore.app",
            }
        )
        self.assertEqual(
            format_message(raw),
            ["[agentcore] Invocation completed successfully (240.535s)"],
        )
