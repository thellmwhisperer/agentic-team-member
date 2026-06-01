"""Language plugin registry."""
from __future__ import annotations

from pathlib import PurePosixPath

from agentic_tdd_runner.languages.capabilities import LanguageCapabilities


_registry: dict[str, LanguageCapabilities] = {}


def register(extensions: list[str], plugin: LanguageCapabilities) -> None:
    for ext in extensions:
        _registry[ext] = plugin


def get_language(file_path: str) -> LanguageCapabilities | None:
    ext = PurePosixPath(file_path).suffix
    return _registry.get(ext)


def get_language_by_name(name: str) -> LanguageCapabilities | None:
    for plugin in plugins():
        if getattr(plugin, "name", None) == name:
            return plugin
    return None


def plugins() -> list[LanguageCapabilities]:
    seen: set[int] = set()
    result: list[LanguageCapabilities] = []
    for plugin in _registry.values():
        ident = id(plugin)
        if ident in seen:
            continue
        result.append(plugin)
        seen.add(ident)
    return result


def supported_extensions() -> list[str]:
    return sorted(_registry.keys())


# Auto-register built-in languages
from agentic_tdd_runner.languages import typescript, python  # noqa: E402, F401
