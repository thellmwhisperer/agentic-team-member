"""Compiler package — deterministic contract builder for the TDD runner."""
from __future__ import annotations

from copy import deepcopy

from agentic_tdd_runner.compiler.parser import (
    _compute_source_import_path,
    _extract_signature,
    _extract_target_snippet,
    _find_symbol_line,
)
from agentic_tdd_runner.compiler.analyzer import (
    _build_assertion_surface,
    _compile_dependency,
    _dedupe_gaps,
    _enrich_module_load_dependencies,
    _gaps_from_injection_plan,
    _has_blocking_gaps,
    _observed_members,
)
from agentic_tdd_runner.compiler.renderer import (
    _build_scaffold,
    _render_module_mocks,
)
from agentic_tdd_runner.compiler.edits import (
    _build_pre_test_source_edits,
    _setter_name,
)


SCHEMA_VERSION = "contract.v2"


def build_contract(facts):
    """Build a deterministic contract from normalized discovery facts."""
    target = deepcopy(facts["target"])
    test_file = deepcopy(facts["test_file"])
    pre_test_source_edits = deepcopy(facts.get("pre_test_source_edits", []))
    module_load_dependencies = deepcopy(facts.get("module_load_dependencies", []))
    execution_dependencies = deepcopy(facts.get("execution_dependencies", []))
    injection_plan = deepcopy(facts.get("injection_plan", []))
    assertion_surface = deepcopy(facts["assertion_surface"])
    callback_registrations = deepcopy(facts.get("callback_registrations", []))
    callback_contract_facts = deepcopy(facts.get("callback_contract_facts", []))
    repo_profile_facts = deepcopy(facts.get("repo_profile_facts", []))
    pattern_files = deepcopy(facts.get("pattern_files", []))
    gaps = deepcopy(facts.get("gaps", []))

    test_file.setdefault(
        "source_import_path",
        _compute_source_import_path(test_file["path"], target["source_path"]),
    )

    gaps.extend(_gaps_from_injection_plan(injection_plan))
    gaps = _dedupe_gaps(gaps)

    contract = {
        "schema_version": SCHEMA_VERSION,
        "target": target,
        "test_file": test_file,
        "pre_test_source_edits": pre_test_source_edits,
        "module_load_dependencies": module_load_dependencies,
        "execution_dependencies": execution_dependencies,
        "injection_plan": injection_plan,
        "assertion_surface": assertion_surface,
        "callback_registrations": callback_registrations,
        "callback_contract_facts": callback_contract_facts,
        "repo_profile_facts": repo_profile_facts,
        "pattern_files": pattern_files,
        "gaps": gaps,
    }
    contract["ready"] = not _has_blocking_gaps(gaps)
    contract["scaffold"] = _build_scaffold(contract)
    return contract
