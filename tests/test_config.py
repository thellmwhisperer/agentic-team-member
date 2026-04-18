"""Tests for config loading from TOML + JSON."""
import json
from pathlib import Path

from agentic_tdd_runner.config import load_config


class TestProductionConfig:
    """The shipped config/agent.toml loads without errors."""

    def test_production_toml_loads(self):
        prod = Path(__file__).parent.parent / "config" / "agent.toml"
        cfg = load_config(prod)
        assert len(cfg["tools"]) > 0
        assert "system" in cfg["prompt"]
        assert "Never move the direct call to the function under test" in cfg["prompt"]["system"]


class TestLoadConfig:
    """load_config reads agent.toml + tools.json into a unified config dict."""

    def test_loads_llm_settings(self, tmp_path):
        _write_config(tmp_path)
        cfg = load_config(tmp_path / "agent.toml")
        assert cfg["llm"]["model"] == "test-model"
        assert cfg["llm"]["temperature"] == 0.6

    def test_loads_system_prompt(self, tmp_path):
        _write_config(tmp_path)
        cfg = load_config(tmp_path / "agent.toml")
        assert "senior software engineer" in cfg["prompt"]["system"]

    def test_loads_tools_from_json(self, tmp_path):
        _write_config(tmp_path)
        cfg = load_config(tmp_path / "agent.toml")
        assert len(cfg["tools"]) == 2
        assert cfg["tools"][0]["function"]["name"] == "read_file"

    def test_loads_agent_settings(self, tmp_path):
        _write_config(tmp_path)
        cfg = load_config(tmp_path / "agent.toml")
        assert cfg["agent"]["max_steps"] == 30
        assert cfg["agent"]["max_tool_output"] == 4000

    def test_loads_nudge_and_messages(self, tmp_path):
        _write_config(tmp_path)
        cfg = load_config(tmp_path / "agent.toml")
        assert "DONE" in cfg["prompt"]["nudge"]
        assert "test file" in cfg["prompt"]["no_test_found"]

    def test_loads_verification_settings(self, tmp_path):
        _write_config(tmp_path)
        cfg = load_config(tmp_path / "agent.toml")
        assert cfg["verification"]["max_rejections"] == 3

    def test_quality_section_optional(self, tmp_path):
        """Config without [quality] loads fine (backward compat)."""
        _write_config(tmp_path)
        cfg = load_config(tmp_path / "agent.toml")
        # No quality section in minimal config → empty dict or missing
        assert cfg.get("quality", {}).get("enabled", False) is False

    def test_production_quality_section(self):
        """Production config has quality section with expected structure."""
        prod = Path(__file__).parent.parent / "config" / "agent.toml"
        cfg = load_config(prod)
        assert cfg["quality"]["enabled"] is True
        assert cfg["quality"]["max_fix_rounds"] == 3
        assert "as any" in cfg["quality"]["typescript"]["forbidden"]
        assert ": any" in cfg["quality"]["typescript"]["forbidden"]
        assert "type: ignore" in cfg["quality"]["python"]["forbidden"]

    def test_production_pr_section(self):
        """Production config has pr section."""
        prod = Path(__file__).parent.parent / "config" / "agent.toml"
        cfg = load_config(prod)
        assert cfg["pr"]["enabled"] is True
        assert cfg["pr"]["base_branch"] == "main"
        assert cfg["timeouts"]["pr_create"] == 120

    def test_tools_path_relative_to_toml(self, tmp_path):
        """tools.json path in TOML is relative to the TOML file's directory."""
        sub = tmp_path / "nested"
        sub.mkdir()
        _write_config(sub)
        cfg = load_config(sub / "agent.toml")
        assert len(cfg["tools"]) == 2


def _write_config(directory: Path):
    """Write minimal agent.toml + tools.json for testing."""
    directory.mkdir(parents=True, exist_ok=True)

    toml = directory / "agent.toml"
    toml.write_text("""\
[agent]
max_steps = 30
max_tool_output = 4000

[llm]
url = "http://localhost:9999/v1/chat/completions"
model = "test-model"
temperature = 0.6
top_p = 0.95
top_k = 20

[prompt]
system = \"\"\"
You are a senior software engineer fixing a bug.
\"\"\"

nudge = "Continue. If all tests pass, say DONE."

no_test_found = "You said DONE but I can't find a test file."

[verification]
max_rejections = 3

[tools]
file = "tools.json"
""")

    tools = directory / "tools.json"
    tools.write_text(json.dumps([
        {
            "type": "function",
            "function": {
                "name": "read_file",
                "description": "Read a file.",
                "parameters": {"type": "object", "required": ["path"], "properties": {"path": {"type": "string"}}}
            }
        },
        {
            "type": "function",
            "function": {
                "name": "run_command",
                "description": "Run a command.",
                "parameters": {"type": "object", "required": ["command"], "properties": {"command": {"type": "string"}}}
            }
        },
    ]))
