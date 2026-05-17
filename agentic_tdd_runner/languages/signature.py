"""Shared signature parsing primitives for language plugins."""
from __future__ import annotations


def parse_signature_params(
    signature: str,
    *,
    strip_optional_marker: bool = False,
    skip_markers: bool = False,
    skip_names: set[str] | None = None,
) -> list[str]:
    """Extract parameter names from a function signature."""
    raw = _extract_param_segment(signature)
    if raw is None:
        return []
    raw = raw.strip()
    if not raw:
        return []

    params = []
    for piece in _split_top_level_params(raw):
        if skip_markers and piece in {"*", "/"}:
            continue
        name = piece.split(":", 1)[0].split("=", 1)[0].strip()
        if name.startswith("..."):
            name = name[3:]
        name = name.lstrip("*")
        if strip_optional_marker:
            name = name.rstrip("?")
        if name and name not in (skip_names or set()):
            params.append(name)
    return params


def _extract_param_segment(signature: str) -> str | None:
    start = signature.find("(")
    if start == -1:
        return None

    depth = 0
    in_string = None
    escaped = False
    for index in range(start, len(signature)):
        ch = signature[index]
        if escaped:
            escaped = False
            continue
        if ch == "\\" and in_string:
            escaped = True
            continue
        if in_string:
            if ch == in_string:
                in_string = None
            continue
        if ch in ("'", '"'):
            in_string = ch
            continue
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
            if depth == 0:
                return signature[start + 1:index]
    return None


def _split_top_level_params(raw: str) -> list[str]:
    params = []
    depth = 0
    in_string = None
    current = []
    for ch in raw:
        if in_string:
            current.append(ch)
            if ch == in_string:
                in_string = None
            continue
        if ch in ("'", '"'):
            in_string = ch
            current.append(ch)
        elif ch in "([{<":
            depth += 1
            current.append(ch)
        elif ch in ")]}>":
            depth -= 1
            current.append(ch)
        elif ch == "," and depth == 0:
            piece = "".join(current).strip()
            if piece:
                params.append(piece)
            current = []
        else:
            current.append(ch)

    piece = "".join(current).strip()
    if piece:
        params.append(piece)
    return params
