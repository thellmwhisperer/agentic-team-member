"""Tests for config loading from TOML."""
from pathlib import Path

from agentic_tdd_runner.config import load_config


CONFIG_DIR = Path(__file__).parent.parent / "config"


class TestLoadConfig:
    """load_config reads agent.toml into a config dict."""

    def test_loads_environment_settings(self, tmp_path):
        _write_config(tmp_path)
        cfg = load_config(tmp_path / "agent.toml")
        assert cfg["environment"]["install"] == "never"
        assert cfg["environment"]["timeout"] == 120

    def test_loads_quality_failed_template(self, tmp_path):
        _write_config(tmp_path)
        cfg = load_config(tmp_path / "agent.toml")
        assert "{details}" in cfg["prompt"]["quality_failed"]

    def test_quality_section_optional(self, tmp_path):
        """Config without [quality] loads fine (backward compat)."""
        _write_config(tmp_path)
        cfg = load_config(tmp_path / "agent.toml")
        assert cfg.get("quality", {}).get("enabled", False) is False

    def test_production_quality_section(self):
        """Production config has quality section with expected structure."""
        cfg = load_config(CONFIG_DIR / "agent.toml")
        assert cfg["quality"]["enabled"] is True
        assert "as any" in cfg["quality"]["typescript"]["forbidden"]
        assert "as never" in cfg["quality"]["typescript"]["forbidden"]
        assert "{} as" in cfg["quality"]["typescript"]["forbidden"]
        assert ": any" in cfg["quality"]["typescript"]["forbidden"]
        assert "type: ignore" in cfg["quality"]["python"]["forbidden"]

    def test_production_timeouts(self):
        cfg = load_config(CONFIG_DIR / "agent.toml")
        assert cfg["timeouts"]["test_run"] == 30
        assert cfg["timeouts"]["tool_execution"] == 60


def _write_config(directory: Path):
    """Write a minimal agent.toml for testing."""
    directory.mkdir(parents=True, exist_ok=True)
    (directory / "agent.toml").write_text("""\
[environment]
install = "never"
timeout = 120

[prompt]
quality_failed = "QUALITY CHECK FAILED.\\n\\n{details}"
""")
