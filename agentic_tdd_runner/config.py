"""Config loader — reads agent.toml."""
from __future__ import annotations

import tomllib
from pathlib import Path


def load_config(toml_path: str | Path) -> dict:
    """Load agent config from a TOML file."""
    with open(toml_path, "rb") as f:
        return tomllib.load(f)
