"""Shared types for review/oracle fact extraction."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any


@dataclass(frozen=True)
class Fact:
    """A single deterministic fact surfaced by the review/oracle pipeline.

    The dataclass is frozen so fact fields stay stable after creation, but
    nested payloads inside ``value`` are not deep-frozen in this first pass.
    """

    name: str
    value: Any
    derivation_rule: str
    inputs_used: tuple[str, ...]
    confidence_class: str
