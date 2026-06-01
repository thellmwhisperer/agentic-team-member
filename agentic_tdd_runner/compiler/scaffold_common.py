from __future__ import annotations

import re


def _empty_scaffold(reason: str) -> dict:
    return {
        "imports_block": "",
        "module_mocks_block": "",
        "arrange_block": "",
        "act_block": "",
        "assert_block": "",
        "todo_slots": [reason],
        "rendered_test": "",
    }


def _safe_identifier(value: object) -> str:
    return re.sub(r"\W+", "_", str(value)).strip("_") or "value"


def _indent_block(text: str, spaces: int) -> str:
    indent = " " * spaces
    return "\n".join(f"{indent}{line}" if line else "" for line in text.splitlines())
