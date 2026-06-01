"""Python path helpers."""
from __future__ import annotations

from pathlib import PurePosixPath


def test_path(source_path: str, symbol: str) -> str:
    source = PurePosixPath(source_path)
    return (source.parent / f"test_{symbol}.py").as_posix()


def import_path(test_path: str, source_path: str) -> str:
    parts = list(PurePosixPath(source_path).parts)
    if parts and parts[-1].endswith(".py"):
        parts[-1] = parts[-1][:-3]
    if parts and parts[-1] == "__init__":
        parts = parts[:-1]
    return ".".join(part for part in parts if part)
