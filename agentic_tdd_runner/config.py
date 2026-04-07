"""Config loader — reads agent.toml + tools.json."""
from __future__ import annotations

import json
import tomllib
from pathlib import Path


def load_config(toml_path: str | Path) -> dict:
    """Load agent config from a TOML file.

    The TOML file references a tools JSON file via [tools].file,
    resolved relative to the TOML file's directory.
    """
    toml_path = Path(toml_path)
    with open(toml_path, "rb") as f:
        config = tomllib.load(f)

    # Load tools from JSON
    tools_ref = config.get("tools", {}).get("file", "tools.json")
    tools_path = toml_path.parent / tools_ref
    with open(tools_path) as f:
        config["tools"] = json.load(f)

    return config
