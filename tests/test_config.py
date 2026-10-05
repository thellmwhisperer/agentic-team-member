"""Tests for config loading from TOML."""
from pathlib import Path

from unittest.mock import Mock

from agentic_tdd_runner.config import load_config
from agentic_tdd_runner import environment, harness_worker, paths, quality, verification


CONFIG_DIR = Path(__file__).parent.parent / "config"
def _leaf_keys(table: dict, parents: tuple[str, ...] = ()):
    for key, value in table.items():
        if isinstance(value, dict):
            yield from _leaf_keys(value, (*parents, key))
        else:
            yield (*parents, key)


def test_every_production_config_key_is_read_by_the_package(monkeypatch, tmp_path):
    """Exercise the worker consumers and record actual config lookups."""
    reads = set()

    class TrackedDict(dict):
        def __init__(self, values, path=()):
            super().__init__({key: TrackedDict(value, (*path, key)) if isinstance(value, dict)
                              else value for key, value in values.items()})
            self.path = path

        def __getitem__(self, key):
            if key in self:
                reads.add((*self.path, key))
            return super().__getitem__(key)

        def get(self, key, default=None):
            if key in self:
                reads.add((*self.path, key))
            return super().get(key, default)

    config = TrackedDict(load_config(CONFIG_DIR / "agent.toml"))
    paths.is_test_file_path("example.test.ts", config)
    paths.test_runner_command_for_file("example.test.ts", config)
    paths.test_runner_command_for_file("example.spec.rb", config)
    config["runner"]["override_detected"] = True
    paths.test_runner_command_for_file("example.test.ts", config)
    config["runner"]["override_detected"] = False
    verification.single_test_argv("example.test.ts", config)

    monkeypatch.setattr(environment, "inspect_runner_bootstrap", lambda root: Mock(package_manager="npm", lockfile=None))
    monkeypatch.setattr(environment, "_preflight_recommended_tools", lambda *args: None)
    monkeypatch.setattr(environment, "_require_git_worktree", lambda *args, **kwargs: None)
    monkeypatch.setattr(environment, "_require_clean_worktree", lambda *args, **kwargs: None)
    monkeypatch.setattr(environment, "detect_project_type", lambda root: "javascript")
    monkeypatch.setattr(environment, "_read_package_json", lambda root: {"scripts": {"typecheck": "tsc"}})
    monkeypatch.setattr(environment, "_ensure_generated_test_config", lambda *args: None)
    monkeypatch.setattr(environment, "_run_step", lambda *args, **kwargs: None)
    environment.prepare_environment(str(tmp_path), config)

    monkeypatch.setattr(quality, "added_lines", lambda *args: [(1, "type: ignore")])
    monkeypatch.setattr(quality, "_duplicated_setup_findings", lambda *args, **kwargs: [])
    monkeypatch.setattr(quality, "detect_production_test_only_exports", lambda *args: [])
    monkeypatch.setattr(quality, "detect_side_effect_shape_changes", lambda *args: [])
    monkeypatch.setattr(quality, "detect_parsed_metadata_without_original_fallback", lambda *args: [])
    monkeypatch.setattr(quality, "detect_empty_object_type_assertions", lambda *args: [])
    monkeypatch.setattr(quality.subprocess, "run", lambda *args, **kwargs: Mock(returncode=0))
    monkeypatch.setattr(harness_worker.subprocess, "run", lambda *args, **kwargs: Mock(returncode=0, stdout="", stderr=""))
    harness_worker.run_test_file("example.test.ts", str(tmp_path), config)
    for file in ("source.py", "source.ts"):
        quality.run_quality_checks(file, workdir=str(tmp_path), config=config,
                                   log=lambda *args: None, is_test_file_path=lambda path: False,
                                   detect_quality_tools_fn=lambda lang: [{"name": "check", "command": "true"}],
                                   get_changed_files_fn=lambda: [file])
    monkeypatch.setattr(quality.requests, "post", lambda *args, **kwargs: Mock(json=lambda: {"message": {"content": "NO"}}))
    quality.judge_duplicated_setup("example.py", "def test_a(): pass", ["setup"],
                                   config=config, log=lambda *args: None)

    missing = set(_leaf_keys(config)) - reads
    assert missing == set(), {"unread_keys": sorted(".".join(key) for key in missing)}


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
