"""Python export and seam helpers."""
from __future__ import annotations


def setter_name(binding: str) -> str:
    return f"__set_{binding}_for_tests"


def is_exported(source_text: str, symbol: str) -> bool:
    return True


def render_seam_setter(binding: str, assignment: dict) -> str:
    name = setter_name(binding)
    return (
        f"def {name}(value):\n"
        f"    global {binding}\n"
        f"    {binding} = value\n"
    )


def prepend_export(line: str) -> str:
    return line
