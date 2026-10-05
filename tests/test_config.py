"""The shipped worker config loads and every table in it has a reader."""
import shutil
from pathlib import Path

from agentic_tdd_runner import judge
from agentic_tdd_runner.config import load_config

CONFIG = Path(__file__).parent.parent / "config" / "agent.toml"


def test_production_config_loads_alone(tmp_path):
    """A copy with nothing beside it loads: the worker needs no tools.json."""
    copy = tmp_path / "agent.toml"
    shutil.copy(CONFIG, copy)
    assert load_config(copy) == load_config(CONFIG)


def test_production_config_has_only_tables_the_code_reads():
    assert set(load_config(CONFIG)) == {
        "timeouts", "runner", "environment", "prompt", "quality", "tools", "harness_worker", "follow_ups",
    }
    assert set(load_config(CONFIG)["prompt"]) == {"quality_failed"}


def test_production_timeouts_cover_verification_and_quality():
    timeouts = load_config(CONFIG)["timeouts"]
    assert timeouts["test_run"] > 0  # verification.verify_red_green indexes it directly
    assert timeouts["tool_execution"] > 0  # so does quality.run_quality_checks


def test_production_quality_section():
    cfg = load_config(CONFIG)
    assert cfg["quality"]["enabled"] is True
    for pattern in ("as any", "as never", "{} as", ": any"):
        assert pattern in cfg["quality"]["typescript"]["forbidden"]
    assert "type: ignore" in cfg["quality"]["python"]["forbidden"]
    assert "{details}" in cfg["prompt"]["quality_failed"]


def test_production_worker_defaults_match_the_code():
    cfg = load_config(CONFIG)
    assert cfg["harness_worker"]["max_units"] == 3  # harness_worker.main falls back to 3
    assert cfg["follow_ups"]["judge"] == judge.DEFAULTS
    assert judge.settings(cfg)["enabled"] is False


def test_tools_table_is_returned_as_written(tmp_path):
    toml = tmp_path / "agent.toml"
    toml.write_text('[tools]\nrecommended = ["rg"]\npath_dirs = ["/x"]\n')
    assert load_config(toml)["tools"] == {"recommended": ["rg"], "path_dirs": ["/x"]}
