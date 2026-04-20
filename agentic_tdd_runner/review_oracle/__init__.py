"""Review/oracle fact extraction for deterministic cookbook generation."""

from agentic_tdd_runner.review_oracle.facts import (
    collect_repo_facts,
    collect_exact_facts,
    extract_invocation_surface,
    extract_patch_semantics,
    extract_pytest_surface,
    extract_result_surface,
    extract_schema_surface,
    extract_target_identity,
    extract_upstream_return_surface,
)
from agentic_tdd_runner.review_oracle.types import Fact

__all__ = [
    "Fact",
    "collect_repo_facts",
    "collect_exact_facts",
    "extract_invocation_surface",
    "extract_patch_semantics",
    "extract_pytest_surface",
    "extract_result_surface",
    "extract_schema_surface",
    "extract_target_identity",
    "extract_upstream_return_surface",
]
