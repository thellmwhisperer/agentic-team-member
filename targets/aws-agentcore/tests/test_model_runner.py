import os
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from runtime import model_runner


class ModelRunnerTest(unittest.TestCase):
    def setUp(self):
        model_runner._PROXY_PROCESS = None

    def test_bedrock_model_id_preserves_raw_model_id(self):
        self.assertEqual(
            model_runner.bedrock_model_id("qwen.qwen3-coder-480b-a35b-v1:0"),
            "qwen.qwen3-coder-480b-a35b-v1:0",
        )

    def test_bedrock_model_id_strips_old_prefixes(self):
        self.assertEqual(
            model_runner.bedrock_model_id("bedrock/converse/custom-model"),
            "custom-model",
        )
        self.assertEqual(
            model_runner.bedrock_model_id("bedrock/custom-model"),
            "custom-model",
        )

    def test_render_agentcore_config_routes_llm_to_bedrock_proxy(self):
        source = """[agent]
permission_driven = false

[llm]
url = "http://127.0.0.1:11435/v1/chat/completions"
model = "qwen3.6-27b-mtp"

[quality.duplicated_setup_judge]
enabled = true
url = "http://127.0.0.1:11434/api/chat"

[pr]
base_branch = "main"
branch_prefix = "atm/fix-"
"""
        with tempfile.TemporaryDirectory() as tmp:
            source_dir = Path(tmp) / "config"
            target_dir = Path(tmp) / "runtime"
            source_dir.mkdir()
            source_path = source_dir / "agent.toml"
            target_path = target_dir / "agentcore.toml"
            source_path.write_text(source)
            (source_dir / "tools.json").write_text('[{"type":"function"}]\n')

            model_runner.render_agentcore_config(
                source_path,
                target_path,
                model="qwen.qwen3-coder-480b-a35b-v1:0",
                url="http://127.0.0.1:11435/v1/chat/completions",
                branch_prefix="atm-agentcore/",
                permission_driven=True,
            )

            rendered = target_path.read_text()
            copied_tools = (target_dir / "tools.json").read_text()

        self.assertIn('model = "qwen.qwen3-coder-480b-a35b-v1:0"', rendered)
        self.assertIn('url = "http://127.0.0.1:11435/v1/chat/completions"', rendered)
        self.assertIn("[agent]\npermission_driven = true", rendered)
        self.assertIn("[quality.duplicated_setup_judge]\nenabled = false", rendered)
        self.assertIn('branch_prefix = "atm-agentcore/"', rendered)
        self.assertEqual(copied_tools, '[{"type":"function"}]\n')

    def test_render_agentcore_config_inserts_missing_permission_driven_key(self):
        source = """[agent]

[llm]
url = "local"
model = "local-model"
"""
        with tempfile.TemporaryDirectory() as tmp:
            source_path = Path(tmp) / "agent.toml"
            target_path = Path(tmp) / "agentcore.toml"
            source_path.write_text(source)
            (Path(tmp) / "tools.json").write_text("[]\n")

            model_runner.render_agentcore_config(
                source_path,
                target_path,
                model="qwen.qwen3-coder-480b-a35b-v1:0",
                url="http://127.0.0.1:11435/v1/chat/completions",
                permission_driven=True,
            )

            rendered = target_path.read_text()

        self.assertIn("[agent]\n\npermission_driven = true", rendered)

    def test_render_agentcore_config_overrides_max_steps(self):
        source = """[agent]
max_steps = 50
permission_driven = false

[llm]
url = "local"
model = "local-model"
"""
        with tempfile.TemporaryDirectory() as tmp:
            source_path = Path(tmp) / "agent.toml"
            target_path = Path(tmp) / "agentcore.toml"
            source_path.write_text(source)
            (Path(tmp) / "tools.json").write_text("[]\n")

            model_runner.render_agentcore_config(
                source_path,
                target_path,
                model="qwen.qwen3-coder-480b-a35b-v1:0",
                url="http://127.0.0.1:11435/v1/chat/completions",
                max_steps=100,
            )

            rendered = target_path.read_text()

        self.assertIn("max_steps = 100", rendered)

    def test_env_int_or_none_reads_positive_integer(self):
        with patch.dict(os.environ, {"ATM_MAX_STEPS": "100"}, clear=True):
            self.assertEqual(model_runner.env_int_or_none("ATM_MAX_STEPS"), 100)

    def test_env_int_or_none_returns_none_for_blank_value(self):
        with patch.dict(os.environ, {"ATM_MAX_STEPS": "  "}, clear=True):
            self.assertIsNone(model_runner.env_int_or_none("ATM_MAX_STEPS"))

    def test_env_int_or_none_rejects_invalid_integer_with_name(self):
        with patch.dict(os.environ, {"ATM_MAX_STEPS": "abc"}, clear=True):
            with self.assertRaisesRegex(ValueError, "ATM_MAX_STEPS.*abc"):
                model_runner.env_int_or_none("ATM_MAX_STEPS")

    def test_env_int_or_none_rejects_non_positive_integer(self):
        for value in ("0", "-1"):
            with self.subTest(value=value):
                with patch.dict(os.environ, {"ATM_MAX_STEPS": value}, clear=True):
                    with self.assertRaisesRegex(ValueError, "positive integer"):
                        model_runner.env_int_or_none("ATM_MAX_STEPS")

    def test_configure_model_runner_writes_config_and_starts_proxy(self):
        source = """[agent]
permission_driven = false

[llm]
url = "local"
model = "local-model"
"""
        with tempfile.TemporaryDirectory() as tmp:
            source_path = Path(tmp) / "agent.toml"
            target_path = Path(tmp) / "agentcore.toml"
            source_path.write_text(source)
            (Path(tmp) / "tools.json").write_text("[]\n")
            env = {
                "ATM_ENABLE_BEDROCK_PROXY": "true",
                "ATM_BASE_CONFIG": str(source_path),
                "ATM_CONFIG": str(target_path),
                "ATM_BEDROCK_MODEL_ID": "qwen.qwen3-coder-480b-a35b-v1:0",
                "ATM_PERMISSION_DRIVEN": "true",
                "ATM_MAX_STEPS": "100",
            }
            with patch.dict(os.environ, env, clear=True):
                with patch.object(model_runner, "ensure_bedrock_proxy", return_value="proc") as proxy:
                    result = model_runner.configure_model_runner_from_env()
                configured_path = os.environ["ATM_CONFIG"]

            self.assertEqual(result, "proc")
            proxy.assert_called_once_with(
                host="127.0.0.1",
                port=11435,
                model="qwen.qwen3-coder-480b-a35b-v1:0",
            )
            self.assertEqual(configured_path, str(target_path))
            self.assertIn("qwen.qwen3-coder-480b-a35b-v1:0", target_path.read_text())
            self.assertIn("permission_driven = true", target_path.read_text())
            self.assertIn("max_steps = 100", target_path.read_text())

    def test_proxy_launch_uses_native_bedrock_proxy(self):
        with patch.dict(os.environ, {"AWS_REGION": "eu-west-2"}, clear=True):
            with patch.object(model_runner, "wait_for_tcp"):
                with patch.object(model_runner.subprocess, "Popen") as popen:
                    model_runner._PROXY_PROCESS = None
                    popen.return_value = FakeProcess()

                    model_runner.ensure_bedrock_proxy(
                        host="127.0.0.1",
                        port=11435,
                        model="qwen.qwen3-coder-480b-a35b-v1:0",
                    )

        command = popen.call_args.args[0]
        self.assertIn("-m", command)
        self.assertIn("runtime.bedrock_openai_proxy", command)
        self.assertIn("qwen.qwen3-coder-480b-a35b-v1:0", command)
        self.assertIn("eu-west-2", command)

    def test_proxy_launch_detects_process_exit_after_tcp_probe(self):
        with patch.object(model_runner, "wait_for_tcp"):
            with patch.object(model_runner.subprocess, "Popen") as popen:
                popen.return_value = DeadProcess()

                with self.assertRaisesRegex(RuntimeError, "exited before becoming ready"):
                    model_runner.ensure_bedrock_proxy(
                        host="127.0.0.1",
                        port=11435,
                        model="qwen.qwen3-coder-480b-a35b-v1:0",
                    )


class FakeProcess:
    def poll(self):
        return None


class DeadProcess:
    def poll(self):
        return 1
