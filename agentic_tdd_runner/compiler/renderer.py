from __future__ import annotations

from agentic_tdd_runner.compiler.scaffold_common import _empty_scaffold
from agentic_tdd_runner.languages import get_language, plugins


def _build_scaffold(contract: dict) -> dict:
    lang = _language_for_contract(contract)
    build = getattr(lang, "build_scaffold", None)
    if callable(build):
        return build(contract)
    runner = ((contract or {}).get("test_file") or {}).get("runner") or "unknown"
    return _empty_scaffold(f"unsupported_runner:{runner}")


def _render_module_mocks(
    contract: dict,
    *,
    declare_spies: bool = False,
) -> str:
    lang = _language_for_contract(contract)
    render = getattr(lang, "render_module_mocks", None)
    if callable(render):
        return render(contract, declare_spies=declare_spies)
    return ""


def _render_assertion(
    assertion_surface: dict,
    *,
    runner: str,
    call_params: list[str] | None = None,
) -> str:
    for lang in plugins():
        if not lang.supports_test_runner(runner):
            continue
        render = getattr(lang, "render_scaffold_assertion", None)
        if callable(render):
            return render(assertion_surface, call_params=call_params)
    return ""


def _language_for_contract(contract: dict):
    if not isinstance(contract, dict):
        return None
    target = (contract or {}).get("target") or {}
    test_file = (contract or {}).get("test_file") or {}
    path = str(target.get("source_path") or test_file.get("path") or "")
    return get_language(path)
