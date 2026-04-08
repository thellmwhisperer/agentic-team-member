"""Language plugin registry."""
from __future__ import annotations

from pathlib import PurePosixPath

_registry: dict[str, object] = {}


def register(extensions: list[str], plugin):
    for ext in extensions:
        _registry[ext] = plugin


def get_language(file_path: str):
    ext = PurePosixPath(file_path).suffix
    return _registry.get(ext)


def supported_extensions() -> list[str]:
    return sorted(_registry.keys())


# Auto-register built-in languages
from agentic_tdd_runner.languages import typescript, python  # noqa: E402, F401
