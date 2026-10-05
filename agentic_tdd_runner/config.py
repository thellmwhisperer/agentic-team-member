"""Config loader for the worker TOML."""
from __future__ import annotations

import tomllib
from pathlib import Path


def load_config(toml_path: str | Path) -> dict:
    with open(toml_path, "rb") as f:
        return tomllib.load(f)
