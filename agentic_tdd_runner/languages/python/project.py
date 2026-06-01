from __future__ import annotations

from pathlib import Path


PROJECT_COMMANDS = {"python", "python3", "pip", "pip3", "pytest"}


def detect_project_type(root: Path) -> str | None:
    if (root / "pyproject.toml").is_file() or (root / "requirements.txt").is_file():
        return "python"
    return None


def shell_project_commands() -> set[str]:
    return set(PROJECT_COMMANDS)
