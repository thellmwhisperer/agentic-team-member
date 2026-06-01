"""TypeScript path helpers."""
from __future__ import annotations

from pathlib import PurePosixPath


def test_path(source_path: str, symbol: str) -> str:
    source = PurePosixPath(source_path)
    return (source.parent / f"{symbol}.test{source.suffix}").as_posix()


def import_path(test_path: str, source_path: str) -> str:
    test_dir = PurePosixPath(test_path).parent
    source_no_ext = PurePosixPath(source_path).with_suffix("")
    target_parts = source_no_ext.parts
    start_parts = test_dir.parts
    common = 0
    for left, right in zip(target_parts, start_parts):
        if left != right:
            break
        common += 1
    up = [".."] * (len(start_parts) - common)
    down = list(target_parts[common:])
    parts = up + down
    rel = "/".join(parts) if parts else "."
    if not rel.startswith("."):
        rel = f"./{rel}"
    return rel
