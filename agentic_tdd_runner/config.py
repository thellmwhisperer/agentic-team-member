"""Config loader — reads agent.toml."""
from __future__ import annotations

import tomllib
from pathlib import Path


def load_config(toml_path: str | Path) -> dict:
    """Load agent config from a TOML file."""
    with open(toml_path, "rb") as f:
        return tomllib.load(f)


def runs_dir(config: dict) -> Path:
    """[runs].dir, the directory holding one subdirectory per run. `~` is expanded; it must be absolute."""
    raw = str(config.get("runs", {}).get("dir", "~/.atm/runs"))
    path = Path(raw).expanduser()
    if not path.is_absolute():
        raise ValueError(f"[runs].dir must be an absolute path, got {raw!r}")
    return path
