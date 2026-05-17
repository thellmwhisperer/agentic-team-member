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
    config_dir = toml_path.parent.resolve()

    with open(toml_path, "rb") as f:
        config = tomllib.load(f)

    # Load tools from JSON, constrained to config directory. Non-file [tools]
    # settings are preserved separately because config["tools"] is the LLM tool list.
    tools_config = config.get("tools", {}) or {}
    if not isinstance(tools_config, dict):
        tools_config = {}
    config["tooling"] = {key: value for key, value in tools_config.items() if key != "file"}
    tools_ref = tools_config.get("file", "tools.json")
    tools_path = (config_dir / tools_ref).resolve()
    try:
        tools_path.relative_to(config_dir)
    except ValueError as exc:
        raise ValueError(f"tools file escapes config directory: {tools_ref}") from exc

    with open(tools_path) as f:
        config["tools"] = json.load(f)

    return config
