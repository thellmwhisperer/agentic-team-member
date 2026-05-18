"""Tests for config loading from TOML + JSON."""
import json
import tomllib
from pathlib import Path

import pytest

from agentic_tdd_runner.config import load_config


CONFIG_DIR = Path(__file__).parent.parent / "config"
BENCHMARK_CONFIGS = sorted(path.name for path in CONFIG_DIR.glob("agent-r*.toml"))


class TestProductionConfig:
    """The shipped config/agent.toml loads without errors."""

    def test_production_toml_loads(self):
        prod = CONFIG_DIR / "agent.toml"
        cfg = load_config(prod)
        assert len(cfg["tools"]) > 0
        assert "system" in cfg["prompt"]
        assert "Never move the direct call to the function under test" in cfg["prompt"]["system"]

    def test_system_prompt_tells_agent_to_use_issue_location_hints(self):
        """Bug reports often name the file/function/lines. Without an explicit
        nudge, models default to broad keyword search and waste exploration
        steps on large files (observed on roca-madre's 1500-line server.py)."""
        prod = CONFIG_DIR / "agent.toml"
        cfg = load_config(prod)
        system = cfg["prompt"]["system"].lower()
        assert "issue" in system, "prompt must reference the issue as a source"
        assert "discovery targets as hypotheses" in system
        assert "broad keyword search" in system, (
            "prompt must explicitly steer the agent away from broad keyword search "
            "and toward the location described in the issue"
        )

    def test_system_prompt_tells_agent_not_to_cd_inside_run_command(self):
        """The harness already controls cwd. Chaining `cd x && y` wastes a tool
        call because the shell validator rejects `cd` as an executable."""
        prod = CONFIG_DIR / "agent.toml"
        cfg = load_config(prod)
        system = cfg["prompt"]["system"].lower()
        assert "cwd" in system
        assert "cd" in system
        assert any(marker in system for marker in ("never", "do not", "don't"))
        assert "relative" in system or "directly" in system

    def test_run_command_tool_description_explains_cwd_and_cd_rejection(self):
        prod = CONFIG_DIR / "agent.toml"
        cfg = load_config(prod)
        run_command = next(
            tool["function"]
            for tool in cfg["tools"]
            if tool["function"]["name"] == "run_command"
        )
        description = run_command["description"].lower()
        assert "cwd already set" in description
        assert "cd" in description
        assert any(marker in description for marker in ("do not", "never", "don't"))
        assert "relative" in description or "directly" in description

    def test_system_prompt_tells_agent_to_reuse_fixtures_and_write_narrow_regression(self):
        """Observed on the Python run (agent-20260419-000440.jsonl): after
        opening an existing test module, the agent expanded into a broad
        mini-suite and got pulled into investigating unrelated failures in the
        same file. Prompt must tell the agent to borrow fixtures/patterns from
        the existing module but write its regression as a narrow, focused test
        — not an ambient exploration of the whole module's behavior."""
        prod = CONFIG_DIR / "agent.toml"
        cfg = load_config(prod)
        system = cfg["prompt"]["system"].lower()
        assert "reuse" in system or "borrow" in system, (
            "prompt must steer the agent toward reusing existing fixtures/patterns"
        )
        assert "narrow" in system, (
            "prompt must tell the agent to write a NARROW regression test, not a mini-suite"
        )

    def test_system_prompt_bounds_extra_tests_to_grounded_evidence(self):
        """Runs should start with the reported regression, then add more cases
        only when the issue/code/types give evidence. This keeps useful edge
        coverage without letting the model invent a broad suite."""
        prod = CONFIG_DIR / "agent.toml"
        cfg = load_config(prod)
        system = cfg["prompt"]["system"].lower()
        assert "one focused regression test first" in system
        assert "contrastive fixtures" in system
        assert "add up to two extra tests only when grounded" in system
        assert "do not invent domain edge cases" in system
        assert "do not invent the callback signature" in system


class TestLoadConfig:
    """load_config reads agent.toml + tools.json into a unified config dict."""

    def test_loads_llm_settings(self, tmp_path):
        _write_config(tmp_path)
        cfg = load_config(tmp_path / "agent.toml")
        assert cfg["llm"]["model"] == "test-model"
        assert cfg["llm"]["temperature"] == 0.6

    def test_production_disables_unbounded_thinking(self):
        prod = CONFIG_DIR / "agent.toml"
        cfg = load_config(prod)
        assert cfg["llm"]["thinking_budget_tokens"] == 0

    def test_loads_system_prompt(self, tmp_path):
        _write_config(tmp_path)
        cfg = load_config(tmp_path / "agent.toml")
        assert "senior software engineer" in cfg["prompt"]["system"]

    def test_loads_tools_from_json(self, tmp_path):
        _write_config(tmp_path)
        cfg = load_config(tmp_path / "agent.toml")
        assert len(cfg["tools"]) == 2
        assert cfg["tools"][0]["function"]["name"] == "read_file"
        assert cfg["tooling"]["recommended"] == ["rg"]

    def test_production_preserves_tooling_settings(self):
        prod = CONFIG_DIR / "agent.toml"
        cfg = load_config(prod)
        assert cfg["tooling"]["recommended"] == ["rg"]

    def test_loads_agent_settings(self, tmp_path):
        _write_config(tmp_path)
        cfg = load_config(tmp_path / "agent.toml")
        assert cfg["agent"]["max_steps"] == 30
        assert cfg["agent"]["max_tool_output"] == 4000

    def test_production_agent_non_apply_warning_threshold(self):
        prod = CONFIG_DIR / "agent.toml"
        cfg = load_config(prod)
        assert cfg["agent"]["non_apply_step_warning_threshold"] == 5

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
        prod = CONFIG_DIR / "agent.toml"
        cfg = load_config(prod)
        assert cfg["quality"]["enabled"] is True
        assert cfg["quality"]["max_fix_rounds"] == 3
        assert "as any" in cfg["quality"]["typescript"]["forbidden"]
        assert "as never" in cfg["quality"]["typescript"]["forbidden"]
        assert "{} as" in cfg["quality"]["typescript"]["forbidden"]
        assert ": any" in cfg["quality"]["typescript"]["forbidden"]
        assert "type: ignore" in cfg["quality"]["python"]["forbidden"]

    def test_production_pr_section(self):
        """Production config has pr section."""
        prod = CONFIG_DIR / "agent.toml"
        cfg = load_config(prod)
        assert cfg["pr"]["enabled"] is True
        assert cfg["pr"]["base_branch"] == "main"
        assert cfg["timeouts"]["pr_create"] == 120

    def test_benchmark_configs_discovered(self):
        assert BENCHMARK_CONFIGS, "No benchmark configs found under config/agent-r*.toml"

    @pytest.mark.parametrize("filename", BENCHMARK_CONFIGS)
    def test_benchmark_configs_have_required_sections(self, filename):
        config_path = CONFIG_DIR / filename
        with config_path.open("rb") as fh:
            cfg = tomllib.load(fh)
        for section in ("llm", "runner", "timeouts", "prompt", "agent"):
            assert section in cfg
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
recommended = ["rg"]
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
