"""Cookbook generator: deterministic mock/scaffold recipes for the TDD agent.

Reads source files directly (no model involvement) and produces a text section
suitable for injection into the agent's system prompt.
"""
from __future__ import annotations

import re
from collections.abc import Mapping
from copy import deepcopy
from pathlib import Path

from agentic_tdd_runner.compiler import (
    _build_assertion_surface,
    _build_pre_test_source_edits,
    _compile_dependency,
    _compute_source_import_path,
    _enrich_module_load_dependencies,
    _extract_signature,
    _extract_target_snippet,
    _observed_members,
    _render_module_mocks,
    build_contract,
)
from agentic_tdd_runner.compiler.parser import _find_symbol_line_with_source
from agentic_tdd_runner.languages import get_language
from agentic_tdd_runner.languages.capabilities import LanguageCapabilities
from agentic_tdd_runner.repo_profile import (
    DependencyContractProfile,
    EventFrameworkProfile,
    RepoProfile,
    load_repo_profile,
)


def _build_contract_for_symbol(
    source_path: str,
    symbol: str,
    project_root: str,
    *,
    test_path: str | None = None,
    line_start: int | None = None,
    line_end: int | None = None,
    config: dict | None = None,
) -> tuple[dict, LanguageCapabilities, list[dict]]:
    """Shared pipeline: build contract + lang + seam edits for a symbol.

    Returns (contract, lang, seam_edits).
    """
    full_path = Path(project_root) / source_path
    source_text = full_path.read_text()
    repo_profile = load_repo_profile(project_root)

    lang = get_language(source_path)
    if lang is None:
        raise ValueError(f"Unsupported file type: {source_path}")

    imports = lang.parse_imports(source_text)
    assignments = lang.parse_assignments(source_text)
    signature = _extract_signature(source_text, symbol)

    if line_start is not None:
        sym_line, sym_line_source = line_start, "definition"
    else:
        sym_line, sym_line_source = _find_symbol_line_with_source(source_text, symbol)
    fn_end = line_end or _find_function_end(source_text, sym_line, lang=lang)

    owner_class = None
    if sym_line and lang.name == "python":
        lines = source_text.splitlines()
        sym_line_text = lines[sym_line - 1] if sym_line <= len(lines) else ""
        if sym_line_text.startswith((" ", "\t")):
            for i in range(sym_line - 2, -1, -1):
                m = re.match(r"^class\s+(\w+)", lines[i])
                if m:
                    owner_class = m.group(1)
                    break

    target = {
        "symbol": symbol,
        "kind": "method" if owner_class else "function",
        "owner_class": owner_class,
        "source_path": source_path,
        "line_start": sym_line or 1,
        "line_end": fn_end or len(source_text.splitlines()),
        "line_source": sym_line_source or "fallback",
        "signature": signature,
    }
    snippet = _extract_target_snippet(source_text, target)

    deps = _discover_dependencies(snippet, imports, assignments, exclude_symbol=symbol)
    snippet_bindings = {d["binding"] for d in deps}
    for binding, assignment in assignments.items():
        if binding in snippet_bindings or binding == symbol:
            continue
        called = assignment.get("called_symbol")
        if not called:
            continue
        factory_import = imports.get(called)
        if factory_import:
            deps.append({"binding": binding, "source_module": factory_import["source_module"]})

    module_load_dependencies = []
    execution_dependencies = []
    injection_plan = []
    seen_module = set()
    seen_execution = set()
    seen_injection = set()

    for dep in deps:
        compiled = _compile_dependency(dep, snippet, imports, assignments)
        module_entry = compiled.get("module_load")
        if module_entry and module_entry["binding"] not in seen_module:
            module_load_dependencies.append(module_entry)
            seen_module.add(module_entry["binding"])
        execution_entry = compiled.get("execution")
        if execution_entry and execution_entry["binding"] not in seen_execution:
            execution_dependencies.append(execution_entry)
            seen_execution.add(execution_entry["binding"])
        injection_entry = compiled.get("injection")
        if injection_entry and injection_entry["binding"] not in seen_injection:
            injection_plan.append(injection_entry)
            seen_injection.add(injection_entry["binding"])

    module_load_dependencies = _enrich_module_load_dependencies(
        module_load_dependencies, imports, source_text,
    )

    assertion_surface, assertion_gaps = _build_assertion_surface(None, snippet, symbol)

    inferred_binding = assertion_surface.get("binding")
    if (
        inferred_binding
        and inferred_binding != symbol
        and inferred_binding not in seen_module
        and inferred_binding not in seen_execution
    ):
        compiled = _compile_dependency(
            {"binding": inferred_binding}, snippet, imports, assignments,
        )
        execution_entry = compiled.get("execution")
        if execution_entry and execution_entry["binding"] not in seen_execution:
            execution_dependencies.append(execution_entry)
            seen_execution.add(execution_entry["binding"])
        injection_entry = compiled.get("injection")
        if injection_entry and injection_entry["binding"] not in seen_injection:
            injection_plan.append(injection_entry)
            seen_injection.add(injection_entry["binding"])

    seam_edits = []
    pre_test_source_edits = _build_pre_test_source_edits(
        target, source_text,
        exported_hint=lang.is_exported(source_text, symbol),
        seam_edits=seam_edits,
    )

    resolved_test_path = test_path or lang.test_path(source_path, symbol)
    runner = _runner_for_contract(lang, config)

    callback_registrations = _discover_callback_registrations(source_text, symbol)
    callback_contract_facts = _discover_callback_contract_facts(
        source_text,
        symbol,
        repo_profile=repo_profile,
        registrations=callback_registrations,
    )
    repo_profile_facts = repo_profile.render_facts(
        source_path=source_path,
        symbol=symbol,
        source_text=source_text,
    )

    facts = {
        "target": target,
        "test_file": {
            "path": resolved_test_path,
            "runner": runner,
            "source_import_path": _compute_source_import_path(
                resolved_test_path, source_path,
            ),
        },
        "pre_test_source_edits": pre_test_source_edits,
        "module_load_dependencies": module_load_dependencies,
        "execution_dependencies": execution_dependencies,
        "injection_plan": injection_plan,
        "assertion_surface": assertion_surface,
        "callback_registrations": callback_registrations,
        "callback_contract_facts": callback_contract_facts,
        "repo_profile_facts": repo_profile_facts,
        "pattern_files": [],
        "gaps": assertion_gaps,
    }

    contract = build_contract(facts)
    return contract, lang, seam_edits


def generate_cookbook(
    source_path: str,
    symbol: str,
    project_root: str,
    *,
    test_path: str | None = None,
    line_start: int | None = None,
    line_end: int | None = None,
    config: dict | None = None,
) -> str:
    """Generate a text cookbook section for injection into the TDD agent's system prompt."""
    contract, lang, _seam_edits = _build_contract_for_symbol(
        source_path, symbol, project_root,
        test_path=test_path, line_start=line_start, line_end=line_end, config=config,
    )
    return _render_cookbook_text(contract, lang)


def build_episode_context(
    source_path: str,
    symbol: str,
    project_root: str,
    *,
    test_path: str | None = None,
    line_start: int | None = None,
    line_end: int | None = None,
    config: dict | None = None,
) -> dict:
    """Return structured episode data for the phased runner."""
    contract, lang, seam_edits = _build_contract_for_symbol(
        source_path, symbol, project_root,
        test_path=test_path, line_start=line_start, line_end=line_end, config=config,
    )
    runner = contract["test_file"]["runner"]
    resolved_test_path = contract["test_file"]["path"]
    source_import_path = contract["test_file"].get("source_import_path", "")

    mocks_text = _render_module_mocks(contract.get("module_load_dependencies", [])) if runner == "bun:test" else ""

    assertion_surface = contract.get("assertion_surface", {})
    assertion_hint = ""
    if (
        assertion_surface.get("kind") == "outbound_call_arguments"
        and assertion_surface.get("binding")
        and assertion_surface.get("member")
    ):
        assertion_hint = (
            f"assert on {assertion_surface['binding']}.{assertion_surface['member']} "
            "with toHaveBeenCalledWith"
        )
    elif assertion_surface.get("kind") == "return_value":
        assertion_hint = "assert on the return value"

    target = contract.get("target", {})
    function_line_range = {
        "start": target.get("line_start", 1),
        "end": target.get("line_end", target.get("line_start", 1)),
        "source": target.get("line_source", "fallback"),
    }

    return {
        "source_file": source_path,
        "target_symbol": symbol,
        "test_file": resolved_test_path,
        "source_import_path": source_import_path,
        "runner": runner,
        "mocks_text": mocks_text,
        "pre_test_source_edits": deepcopy(contract.get("pre_test_source_edits", [])),
        "conditional_source_edits": deepcopy(seam_edits),
        "assertion_hint": assertion_hint,
        "function_line_range": function_line_range,
        "cookbook_text": _render_cookbook_text(contract, lang),
    }


def build_system_prompt(
    base_prompt: str,
    issue_text: str,
    *,
    source_path: str | None = None,
    symbol: str | None = None,
    project_root: str | None = None,
    config: dict | None = None,
) -> str:
    """Build a system prompt with an optional cookbook section injected."""
    if not source_path or not symbol or not project_root:
        return base_prompt

    cookbook = generate_cookbook(source_path, symbol, project_root, config=config)
    return f"{base_prompt}\n\n{cookbook}"


def _runner_for_contract(lang: LanguageCapabilities, config: dict | None) -> str:
    runner_config = (config or {}).get("runner", {}) if isinstance(config, dict) else {}
    runner_config = runner_config if isinstance(runner_config, dict) else {}
    bootstrap = runner_config.get("bootstrap")
    return (
        _text_value(bootstrap, "test_runner")
        or _text_value(runner_config, "framework")
        or str(getattr(lang, "runner", "") or "")
    )


def _text_value(container: object, key: str) -> str | None:
    value = container.get(key) if isinstance(container, Mapping) else getattr(container, key, None)
    return value.strip() if isinstance(value, str) and value.strip() else None


def _find_function_end(source_text: str, start_line: int | None, *, lang=None) -> int | None:
    """Find the closing brace/dedent of a function starting at start_line."""
    if not start_line:
        return None
    lines = source_text.splitlines()
    if start_line > len(lines):
        return None

    is_python = lang.name == "python" if lang else not any(
        line.rstrip().endswith("{") for line in lines[start_line - 1: start_line + 2]
    )

    if is_python:
        header = lines[start_line - 1]
        base_indent = len(header) - len(header.lstrip(" \t"))
        for i in range(start_line, len(lines)):
            line = lines[i]
            if not line.strip():
                continue
            indent = len(line) - len(line.lstrip(" \t"))
            if i > start_line and indent <= base_indent:
                return i
        return len(lines)

    # TS/JS: brace counting
    depth = 0
    started = False
    for i in range(start_line - 1, len(lines)):
        for ch in lines[i]:
            if ch == "{":
                depth += 1
                started = True
            elif ch == "}":
                depth -= 1
                if started and depth == 0:
                    return i + 1
    return len(lines)


def _discover_dependencies(snippet, imports, assignments, *, exclude_symbol=None):
    """Scan the snippet for all bindings (from imports and assignments) that are used."""
    deps = []
    seen = set()
    all_bindings = set(imports.keys()) | set(assignments.keys())
    if exclude_symbol:
        all_bindings.discard(exclude_symbol)
    for binding in sorted(all_bindings):
        if binding in seen:
            continue
        pattern = rf"\b{re.escape(binding)}\b"
        if re.search(pattern, snippet):
            dep = {"binding": binding}
            import_meta = imports.get(binding)
            if import_meta:
                dep["source_module"] = import_meta["source_module"]
            deps.append(dep)
            seen.add(binding)
    return deps


def _discover_callback_registrations(source_text: str, symbol: str) -> list[dict]:
    """Find simple framework/listener registrations that pass the target symbol.

    This is intentionally conservative: it records deterministic evidence for the
    model to inspect, but it does not try to infer third-party callback types.
    """
    registrations = []
    pattern = re.compile(
        rf"(?P<call>[\w$.\]\)]+\.(?:on|once|addEventListener|subscribe|use)\("
        rf"(?P<args>[^\n;]*\b{re.escape(symbol)}\b[^\n;]*)\))"
    )
    for line_number, line in enumerate(source_text.splitlines(), start=1):
        match = pattern.search(line)
        if not match:
            continue
        registrations.append({
            "line": line_number,
            "call": match.group("call").strip(),
        })
    return registrations


def _discover_callback_contract_facts(
    source_text: str,
    symbol: str,
    *,
    repo_profile: RepoProfile,
    registrations: list[dict],
) -> list[str]:
    """Derive dependency-backed callback facts from the repo profile."""
    facts: list[str] = []
    if not registrations or repo_profile.is_empty:
        return facts

    frameworks = _callback_framework_profiles(source_text, repo_profile)
    contracts = {contract.id: contract for contract in repo_profile.dependency_contracts}
    for registration in registrations:
        event = _event_name_from_registration(registration.get("call", ""), symbol)
        if not event:
            continue
        for framework in frameworks:
            contract = contracts.get(framework.dependency_contract or "")
            if contract is None:
                continue
            facts.extend(_profile_event_contract_facts(framework, contract, event))
    return _dedupe_preserve_order(facts)


def _callback_framework_profiles(
    source_text: str,
    repo_profile: RepoProfile,
) -> list[EventFrameworkProfile]:
    frameworks: list[EventFrameworkProfile] = []
    for framework in repo_profile.event_frameworks:
        if framework.kind != "callback_event":
            continue
        import_specs = framework.imports or (framework.module,)
        if _source_imports_any_module(source_text, import_specs):
            frameworks.append(framework)
    return frameworks


def _source_imports_any_module(source_text: str, import_specs: tuple[str, ...]) -> bool:
    for spec in import_specs:
        escaped = re.escape(spec)
        patterns = (
            rf"\bfrom\s+['\"]{escaped}['\"]",
            rf"\bimport\b[^\n;]*\bfrom\s+['\"]{escaped}['\"]",
            rf"\bimport\s+['\"]{escaped}['\"]",
            rf"\brequire\(\s*['\"]{escaped}['\"]\s*\)",
        )
        if any(re.search(pattern, source_text) for pattern in patterns):
            return True
    return False


def _event_name_from_registration(call: str, symbol: str) -> str | None:
    pattern = re.compile(
        rf"\.(?:on|once)\(\s*['\"](?P<event>[^'\"]+)['\"]\s*,\s*{re.escape(symbol)}\b"
    )
    match = pattern.search(call)
    if not match:
        return None
    return match.group("event")


def _profile_event_contract_facts(
    framework: EventFrameworkProfile,
    contract: DependencyContractProfile,
    event_name: str,
) -> list[str]:
    facts: list[str] = []
    for event in contract.events:
        if event.name != event_name:
            continue
        arg_names = [_profile_arg_name(arg) for arg in event.args]
        module = contract.module or framework.module
        facts.append(f"{module} source emits `{event.name}({', '.join(arg_names)})`.")
        arg_sources = dict(event.arg_sources)
        for index, arg_name in enumerate(arg_names, start=1):
            source = arg_sources.get(arg_name)
            if source:
                facts.append(f"Argument {index} is `{arg_name}`, derived from `{source}`.")
        signature = ", ".join(arg for arg in event.args if ":" in arg)
        if signature:
            facts.append(f"{module} type declarations expose `{event.name}({signature})`.")
    return facts


def _profile_arg_name(arg: str) -> str:
    return arg.split(":", 1)[0].strip()


def _dedupe_preserve_order(values: list[str]) -> list[str]:
    seen: set[str] = set()
    deduped: list[str] = []
    for value in values:
        if value in seen:
            continue
        seen.add(value)
        deduped.append(value)
    return deduped


def _render_cookbook_text(contract: dict, lang) -> str:
    """Render a contract as human-readable text for the agent's system prompt."""
    parts = []
    target = contract["target"]
    owner_class = target.get("owner_class")
    if owner_class:
        parts.append(f"## Mock Cookbook for {owner_class}.{target['symbol']}")
        parts.append("")
        parts.append(f"**Note:** `{target['symbol']}` is a method of `{owner_class}`. "
                      f"Import `{owner_class}` and test via `{owner_class}().{target['symbol']}(...)`.")
        parts.append("")
    else:
        parts.append(f"## Mock Cookbook for {target['symbol']}")
        parts.append("")

    parts.append("### Guardrails")
    parts.append("- Write the first failing test against the real callable contract from source.")
    parts.append("- Do not change the target's runtime signature just to fit the test scaffold.")
    parts.append("- For callbacks, handlers, and framework listeners: preserve the production contract, find the registration/caller before writing the test, and use exported framework types or overloads for the real signature.")
    parts.append("- Do not use loose `Record`, `unknown`, `any`, empty-object casts like `{} as SomeType`, or cast-only test payloads; create the minimal structural payload or declare a typed value that TypeScript can check.")
    parts.append("- For value-selection bugs, use contrastive fixtures only when the issue or source shows competing inputs; preserve fallback values only when the real contract requires them.")
    parts.append("- Use the issue text, source registration, and reactive compiler/test feedback before dependency lookup. Only inspect dependency type files after those sources leave a concrete gap or contradict each other.")
    parts.append("- Before the first failing test, apply only the mechanical exports listed below.")
    parts.append("- Do not add production `__set...ForTests` setters or other test-only APIs. Prefer a public caller/registration path, a module mock, or the smallest pure helper/predicate for the bug.")
    parts.append("- For module-load mocks, assert against the named `*_spy` variables emitted below; do not import and patch the mocked factory after importing the target.")
    if contract["test_file"]["runner"] == "bun:test":
        parts.append("")
        parts.append("### Bun Specifics")
        parts.append("- For Bun spies/mocks, use `mock(() => undefined)` for void placeholders; do not silence mock typing with cast-only returns.")
    parts.append("")

    callback_registrations = contract.get("callback_registrations", [])
    if callback_registrations:
        parts.append("### Callback Contract Evidence")
        parts.append("- Before writing the test, use these registrations plus any issue-provided callback contract. Do not invent callback parameters or re-derive a contract that the issue/cookbook already states.")
        for registration in callback_registrations[:5]:
            parts.append(f"- line {registration['line']}: `{registration['call']}`")
        for fact in contract.get("callback_contract_facts", []):
            parts.append(f"- {fact}")
        parts.append("")

    repo_profile_facts = contract.get("repo_profile_facts", [])
    if repo_profile_facts:
        parts.append("### Repo Profile Facts")
        parts.append("- Stable repo facts from `.atm/profile.toml`; use them for setup and contracts, not as per-issue fixes.")
        for fact in repo_profile_facts:
            parts.append(f"- {fact}")
        parts.append("")

    parts.append("### Test Scope")
    parts.append("- Write one focused regression test first: the exact reported bug or acceptance path.")
    parts.append("- Add contrastive fixtures when they distinguish issue-grounded competing values.")
    parts.append("- Add up to two evidence-backed extra tests after the first red/green regression when grounded in explicit acceptance criteria, a visible code branch, an existing test pattern, a fallback path, or a public type/framework contract.")
    parts.append("- Each extra test must protect a distinct branch or contract, not repeat the same behavior.")
    parts.append("- Do not invent domain edge cases just to make a larger suite.")
    parts.append("")

    # Source edits
    edits = contract.get("pre_test_source_edits", [])
    if edits:
        parts.append("### Source Edits (apply before testing)")
        for edit in edits:
            parts.append(f"EDIT: {edit['path']}")
            parts.append("OLD:")
            parts.append(edit["old"])
            parts.append("NEW:")
            parts.append(edit["new"])
            parts.append("")

    # Module mocks (bun:test only — pytest uses unittest.mock in the scaffold)
    runner = contract["test_file"]["runner"]
    mock_text = (
        _render_module_mocks(contract.get("module_load_dependencies", []), declare_spies=True)
        if runner == "bun:test"
        else ""
    )
    if mock_text:
        parts.append("### Module Mocks (paste before source import)")
        parts.append("```ts")
        parts.append(mock_text)
        parts.append("```")
        parts.append("")

    # Dependency seams
    seam_deps = [
        dep for dep in contract.get("execution_dependencies", [])
        if dep.get("strategy") == "set_test_seam"
    ]
    if seam_deps:
        parts.append("### Dependency Injection Notes")
        parts.append("- Direct source-level test-seam setters are not generated. Prefer a public caller/registration path, a module mock, or the smallest pure helper/predicate for the bug.")
        for dep in seam_deps:
            binding = dep["binding"]
            members = dep.get("observed_members", [])
            members_str = ", ".join(members) if members else "..."
            parts.append(
                f"- `{binding}` is observed through `{members_str}`; do not add a production test-only setter for it."
            )
        parts.append("")

    # Assertion
    assertion = contract.get("assertion_surface", {})
    if assertion.get("kind") == "outbound_call_arguments":
        parts.append("### Assertion")
        parts.append(
            f"Assert on `{assertion['binding']}.{assertion['member']}` "
            f"with `toHaveBeenCalledWith(...)`"
        )
        parts.append("")

    # Test scaffold
    scaffold = contract.get("scaffold", {})
    rendered = scaffold.get("rendered_test", "")
    if owner_class and runner == "pytest":
        # Override scaffold for class methods
        import_path = contract["test_file"].get("source_import_path", "")
        sig = target.get("signature", f"{target['symbol']}(self)")
        # Extract params excluding self
        param_match = re.search(r"\((.*?)\)", sig)
        params = []
        if param_match:
            for p in param_match.group(1).split(","):
                name = p.strip().split(":")[0].split("=")[0].strip()
                if name and name != "self":
                    params.append(name)
        param_setup = "\n".join(f"    {p} = ..." for p in params)
        param_args = ", ".join(params)
        rendered = (
            f"from {import_path} import {owner_class}\n\n\n"
            f"def test_{target['symbol']}():\n"
            f"{param_setup}\n\n"
            f"    obj = {owner_class}()\n"
            f"    result = obj.{target['symbol']}({param_args})\n\n"
            f"    assert result == ...\n"
        )
    if rendered:
        if callback_registrations and "__todoValue" in rendered:
            parts.append("### Test Construction")
            parts.append("- Build the regression test from the issue acceptance criteria and callback contract evidence above. No placeholder scaffold is emitted for callback targets because copied fake argument values can mislead the model.")
            parts.append("")
            rendered = ""
    if rendered:
        code_lang = "python" if runner == "pytest" else "ts"
        parts.append(f"### Test Scaffold ({contract['test_file']['path']})")
        parts.append(f"```{code_lang}")
        parts.append(rendered.rstrip())
        parts.append("```")
        parts.append("")
    elif f"unsupported_runner:{runner or 'unknown'}" in scaffold.get("todo_slots", []):
        parts.append("### Test Scaffold")
        parts.append(f"- No test scaffold was emitted for runner `{runner or 'unknown'}`.")
        parts.append("- Build the regression test from existing tests and runner facts.")
        parts.append("")

    return "\n".join(parts)
