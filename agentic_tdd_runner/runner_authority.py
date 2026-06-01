"""Runner authority helpers for resolving detected versus configured runners."""

from __future__ import annotations

from collections.abc import Mapping


def override_detected_runner(config_or_runner: Mapping[str, object] | None) -> bool:
    candidate = config_or_runner or {}
    if not isinstance(candidate, Mapping):
        return False
    value = candidate.get("runner", candidate)
    if not isinstance(value, Mapping):
        return False
    return bool(value.get("override_detected", False))
