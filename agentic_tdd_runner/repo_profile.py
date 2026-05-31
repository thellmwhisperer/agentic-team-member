"""Repo profile loader for stable, per-repository ATM facts.

The profile is intentionally structured and small. It is not a free-form prompt:
the cookbook may consume these fields, validate them, and render targeted setup
facts without letting per-bug lore leak into the generic harness.
"""
from __future__ import annotations

import re
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any


PROFILE_RELATIVE_PATH = ".atm/profile.toml"
SCHEMA_VERSION = "repo-profile.v1"

_TOP_LEVEL_KEYS = {
    "profile",
    "runner",
    "event_frameworks",
    "dependency_contracts",
    "mock_recipes",
    "import_expectations",
    "test_seams",
}
_PROFILE_KEYS = {"schema_version"}
_RUNNER_KEYS = {"test_runner", "test_command", "typecheck_command"}
_EVENT_FRAMEWORK_KEYS = {
    "id",
    "kind",
    "module",
    "imports",
    "registrations",
    "dependency_contract",
    "contract_sources",
}
_DEPENDENCY_CONTRACT_KEYS = {
    "id",
    "module",
    "contract_mode",
    "contract_sources",
    "events",
    "import_specs",
    "side_effect",
    "reason",
    "mock_recipe",
}
_DEPENDENCY_EVENT_KEYS = {"name", "args", "arg_sources"}
_MOCK_RECIPE_KEYS = {"id", "module", "exports"}
_MOCK_EXPORT_KEYS = {"name", "kind", "members"}
_IMPORT_EXPECTATION_KEYS = {"module", "expected_imports", "applies_when"}
_TEST_SEAM_KEYS = {"source", "symbol", "preferred", "avoid"}
_KNOWN_SIDE_EFFECTS = {"import_time"}
_KNOWN_EVENT_FRAMEWORK_KINDS = {"callback_event"}
_KNOWN_CONTRACT_MODES = {"derived", "inline"}
_KNOWN_MOCK_EXPORT_KINDS = {
    "function",
    "function_returns_object",
    "object",
    "class",
    "value",
}


class RepoProfileError(ValueError):
    """Raised when `.atm/profile.toml` is present but invalid."""


@dataclass(frozen=True)
class RunnerProfile:
    test_runner: str | None = None
    test_command: str | None = None
    typecheck_command: str | None = None


@dataclass(frozen=True)
class EventFrameworkProfile:
    id: str
    kind: str
    module: str
    imports: tuple[str, ...] = field(default_factory=tuple)
    registrations: tuple[str, ...] = field(default_factory=tuple)
    dependency_contract: str | None = None
    contract_sources: tuple[str, ...] = field(default_factory=tuple)


@dataclass(frozen=True)
class DependencyEventContract:
    name: str
    args: tuple[str, ...] = field(default_factory=tuple)
    arg_sources: tuple[tuple[str, str], ...] = field(default_factory=tuple)


@dataclass(frozen=True)
class DependencyContractProfile:
    id: str
    module: str
    contract_mode: str = "inline"
    contract_sources: tuple[str, ...] = field(default_factory=tuple)
    events: tuple[DependencyEventContract, ...] = field(default_factory=tuple)
    import_specs: tuple[str, ...] = field(default_factory=tuple)
    side_effect: str | None = None
    reason: str | None = None
    mock_recipe: str | None = None


@dataclass(frozen=True)
class MockExportRecipe:
    name: str
    kind: str
    members: tuple[str, ...] = field(default_factory=tuple)


@dataclass(frozen=True)
class MockRecipeProfile:
    id: str
    module: str
    exports: tuple[MockExportRecipe, ...] = field(default_factory=tuple)


@dataclass(frozen=True)
class ImportExpectationProfile:
    module: str
    expected_imports: tuple[str, ...] = field(default_factory=tuple)
    applies_when: str | None = None


@dataclass(frozen=True)
class TestSeamProfile:
    source: str
    symbol: str | None = None
    preferred: tuple[str, ...] = field(default_factory=tuple)
    avoid: tuple[str, ...] = field(default_factory=tuple)


@dataclass(frozen=True)
class RepoProfile:
    path: Path | None = None
    runner: RunnerProfile = field(default_factory=RunnerProfile)
    event_frameworks: tuple[EventFrameworkProfile, ...] = field(default_factory=tuple)
    dependency_contracts: tuple[DependencyContractProfile, ...] = field(default_factory=tuple)
    mock_recipes: tuple[MockRecipeProfile, ...] = field(default_factory=tuple)
    import_expectations: tuple[ImportExpectationProfile, ...] = field(default_factory=tuple)
    test_seams: tuple[TestSeamProfile, ...] = field(default_factory=tuple)

    @property
    def is_empty(self) -> bool:
        return not any(
            (
                self.runner != RunnerProfile(),
                self.event_frameworks,
                self.dependency_contracts,
                self.mock_recipes,
                self.import_expectations,
                self.test_seams,
            )
        )

    def mock_recipe(self, recipe_id: str | None) -> MockRecipeProfile | None:
        if not recipe_id:
            return None
        for recipe in self.mock_recipes:
            if recipe.id == recipe_id:
                return recipe
        return None

    def render_facts(
        self,
        *,
        source_path: str | None = None,
        symbol: str | None = None,
        source_text: str | None = None,
    ) -> list[str]:
        """Return short cookbook facts relevant to a target source file."""
        if self.is_empty:
            return []

        facts: list[str] = []
        for framework in self.event_frameworks:
            if source_text is not None and not _source_imports_any(source_text, framework.imports):
                continue
            registrations = ", ".join(framework.registrations) or "unspecified"
            sources = ", ".join(framework.contract_sources) or "profile"
            dependency = (
                f"; dependency contract `{framework.dependency_contract}`"
                if framework.dependency_contract
                else ""
            )
            facts.append(
                f"event framework `{framework.id}` ({framework.kind}) uses module "
                f"`{framework.module}`; registrations: {registrations}; "
                f"contract sources: {sources}{dependency}."
            )

        seen_recipes: set[str] = set()
        for dep in self.dependency_contracts:
            if source_text is not None and not _source_imports_any(source_text, dep.import_specs):
                continue
            mode = f" contract mode `{dep.contract_mode}`;"
            effect = f" side effect `{dep.side_effect}`;" if dep.side_effect else ""
            reason = f" reason: {dep.reason};" if dep.reason else ""
            recipe = f" mock recipe `{dep.mock_recipe}`." if dep.mock_recipe else "."
            facts.append(f"dependency `{dep.id}` imports `{dep.module}`;{mode}{effect}{reason}{recipe}")
            for event in dep.events:
                args = ", ".join(event.args)
                facts.append(f"dependency `{dep.id}` declares event `{event.name}({args})`.")
                for arg, source in event.arg_sources:
                    facts.append(f"event `{event.name}` argument `{arg}` comes from `{source}`.")
            if dep.mock_recipe:
                seen_recipes.add(dep.mock_recipe)

        for recipe_id in sorted(seen_recipes):
            recipe = self.mock_recipe(recipe_id)
            if recipe is None:
                continue
            exports = ", ".join(_format_mock_export(export) for export in recipe.exports)
            facts.append(
                f"mock `{recipe.module}` before importing the target; exports: {exports}."
            )

        for seam in self.test_seams:
            if source_path and seam.source != source_path:
                continue
            if symbol and seam.symbol and seam.symbol != symbol:
                continue
            preferred = "; ".join(seam.preferred)
            avoid = "; ".join(seam.avoid)
            if preferred:
                facts.append(f"test seam for `{seam.source}`: prefer {preferred}.")
            if avoid:
                facts.append(f"test seam for `{seam.source}`: avoid {avoid}.")

        return facts


def load_repo_profile(project_root: str | Path) -> RepoProfile:
    """Load `.atm/profile.toml` from a target repo, or return an empty profile."""
    path = Path(project_root) / PROFILE_RELATIVE_PATH
    if not path.is_file():
        return RepoProfile()
    return read_repo_profile(path)


def read_repo_profile(path: str | Path) -> RepoProfile:
    profile_path = Path(path)
    try:
        with profile_path.open("rb") as fh:
            data = tomllib.load(fh)
    except tomllib.TOMLDecodeError as exc:
        raise RepoProfileError(f"{profile_path}: invalid TOML: {exc}") from exc
    if not isinstance(data, dict):
        raise RepoProfileError(f"{profile_path}: profile must be a TOML table")
    return parse_repo_profile(data, path=profile_path)


def parse_repo_profile(data: dict[str, Any], *, path: Path | None = None) -> RepoProfile:
    _reject_unknown_keys(data, _TOP_LEVEL_KEYS, "profile")
    profile_meta = _table(data.get("profile"), "profile", default={})
    _reject_unknown_keys(profile_meta, _PROFILE_KEYS, "profile")
    schema_version = profile_meta.get("schema_version")
    if schema_version not in (None, SCHEMA_VERSION):
        raise RepoProfileError(
            f"profile.schema_version must be `{SCHEMA_VERSION}` when set"
        )

    runner = _parse_runner(data.get("runner"))
    event_frameworks = tuple(
        _parse_event_framework(entry, index)
        for index, entry in enumerate(_table_list(data.get("event_frameworks"), "event_frameworks"), start=1)
    )
    dependency_contracts = tuple(
        _parse_dependency_contract(entry, index)
        for index, entry in enumerate(_table_list(data.get("dependency_contracts"), "dependency_contracts"), start=1)
    )
    mock_recipes = tuple(
        _parse_mock_recipe(entry, index)
        for index, entry in enumerate(_table_list(data.get("mock_recipes"), "mock_recipes"), start=1)
    )
    import_expectations = tuple(
        _parse_import_expectation(entry, index)
        for index, entry in enumerate(_table_list(data.get("import_expectations"), "import_expectations"), start=1)
    )
    test_seams = tuple(
        _parse_test_seam(entry, index)
        for index, entry in enumerate(_table_list(data.get("test_seams"), "test_seams"), start=1)
    )

    _require_unique_ids("event_frameworks", [entry.id for entry in event_frameworks])
    _require_unique_ids("dependency_contracts", [entry.id for entry in dependency_contracts])
    _require_unique_ids("mock_recipes", [entry.id for entry in mock_recipes])

    dependency_ids = {dep.id for dep in dependency_contracts}
    for framework in event_frameworks:
        if framework.dependency_contract and framework.dependency_contract not in dependency_ids:
            raise RepoProfileError(
                f"event_frameworks `{framework.id}` references unknown dependency_contract `{framework.dependency_contract}`"
            )

    recipe_ids = {recipe.id for recipe in mock_recipes}
    for dep in dependency_contracts:
        if dep.contract_mode == "derived" and not dep.contract_sources:
            raise RepoProfileError(
                f"dependency_contracts `{dep.id}` with contract_mode `derived` requires contract_sources"
            )
        if dep.mock_recipe and dep.mock_recipe not in recipe_ids:
            raise RepoProfileError(
                f"dependency_contracts `{dep.id}` references unknown mock_recipe `{dep.mock_recipe}`"
            )

    return RepoProfile(
        path=path,
        runner=runner,
        event_frameworks=event_frameworks,
        dependency_contracts=dependency_contracts,
        mock_recipes=mock_recipes,
        import_expectations=import_expectations,
        test_seams=test_seams,
    )


def _parse_runner(raw: Any) -> RunnerProfile:
    table = _table(raw, "runner", default={})
    _reject_unknown_keys(table, _RUNNER_KEYS, "runner")
    return RunnerProfile(
        test_runner=_optional_text(table, "test_runner", "runner"),
        test_command=_optional_text(table, "test_command", "runner"),
        typecheck_command=_optional_text(table, "typecheck_command", "runner"),
    )


def _parse_event_framework(raw: Any, index: int) -> EventFrameworkProfile:
    section = f"event_frameworks[{index}]"
    table = _required_table(raw, section)
    _reject_unknown_keys(table, _EVENT_FRAMEWORK_KEYS, section)
    module = _required_text(table, "module", section)
    kind = _required_text(table, "kind", section)
    if kind not in _KNOWN_EVENT_FRAMEWORK_KINDS:
        raise RepoProfileError(
            f"{section}.kind must be one of {sorted(_KNOWN_EVENT_FRAMEWORK_KINDS)}"
        )
    return EventFrameworkProfile(
        id=_required_id(table, section),
        kind=kind,
        module=module,
        imports=_text_list(table, "imports", section, default=[module]),
        registrations=_text_list(table, "registrations", section, default=["on", "once"]),
        dependency_contract=_optional_text(table, "dependency_contract", section),
        contract_sources=_text_list(table, "contract_sources", section, default=[]),
    )


def _parse_dependency_contract(raw: Any, index: int) -> DependencyContractProfile:
    section = f"dependency_contracts[{index}]"
    table = _required_table(raw, section)
    _reject_unknown_keys(table, _DEPENDENCY_CONTRACT_KEYS, section)
    side_effect = _optional_text(table, "side_effect", section)
    if side_effect and side_effect not in _KNOWN_SIDE_EFFECTS:
        raise RepoProfileError(
            f"{section}.side_effect must be one of {sorted(_KNOWN_SIDE_EFFECTS)}"
        )
    contract_mode = _optional_text(table, "contract_mode", section) or "inline"
    if contract_mode not in _KNOWN_CONTRACT_MODES:
        raise RepoProfileError(
            f"{section}.contract_mode must be one of {sorted(_KNOWN_CONTRACT_MODES)}"
        )
    module = _required_text(table, "module", section)
    return DependencyContractProfile(
        id=_required_id(table, section),
        module=module,
        contract_mode=contract_mode,
        contract_sources=_text_list(table, "contract_sources", section, default=[]),
        events=tuple(
            _parse_dependency_event(entry, section, event_index)
            for event_index, entry in enumerate(_table_list(table.get("events"), f"{section}.events"), start=1)
        ),
        import_specs=_text_list(table, "import_specs", section, default=[module]),
        side_effect=side_effect,
        reason=_optional_text(table, "reason", section, max_len=160),
        mock_recipe=_optional_text(table, "mock_recipe", section),
    )


def _parse_dependency_event(raw: Any, section: str, index: int) -> DependencyEventContract:
    event_section = f"{section}.events[{index}]"
    table = _required_table(raw, event_section)
    _reject_unknown_keys(table, _DEPENDENCY_EVENT_KEYS, event_section)
    return DependencyEventContract(
        name=_required_text(table, "name", event_section),
        args=_text_list(table, "args", event_section, default=[]),
        arg_sources=_arg_sources(table.get("arg_sources"), event_section),
    )


def _parse_mock_recipe(raw: Any, index: int) -> MockRecipeProfile:
    section = f"mock_recipes[{index}]"
    table = _required_table(raw, section)
    _reject_unknown_keys(table, _MOCK_RECIPE_KEYS, section)
    exports = tuple(
        _parse_mock_export(entry, section, export_index)
        for export_index, entry in enumerate(_table_list(table.get("exports"), f"{section}.exports"), start=1)
    )
    if not exports:
        raise RepoProfileError(f"{section}.exports must include at least one export")
    return MockRecipeProfile(
        id=_required_id(table, section),
        module=_required_text(table, "module", section),
        exports=exports,
    )


def _parse_mock_export(raw: Any, section: str, index: int) -> MockExportRecipe:
    export_section = f"{section}.exports[{index}]"
    table = _required_table(raw, export_section)
    _reject_unknown_keys(table, _MOCK_EXPORT_KEYS, export_section)
    kind = _required_text(table, "kind", export_section)
    if kind not in _KNOWN_MOCK_EXPORT_KINDS:
        raise RepoProfileError(
            f"{export_section}.kind must be one of {sorted(_KNOWN_MOCK_EXPORT_KINDS)}"
        )
    return MockExportRecipe(
        name=_required_text(table, "name", export_section),
        kind=kind,
        members=_text_list(table, "members", export_section, default=[]),
    )


def _parse_import_expectation(raw: Any, index: int) -> ImportExpectationProfile:
    section = f"import_expectations[{index}]"
    table = _required_table(raw, section)
    _reject_unknown_keys(table, _IMPORT_EXPECTATION_KEYS, section)
    module = _required_text(table, "module", section)
    return ImportExpectationProfile(
        module=module,
        expected_imports=_text_list(table, "expected_imports", section, default=[module]),
        applies_when=_optional_text(table, "applies_when", section),
    )


def _parse_test_seam(raw: Any, index: int) -> TestSeamProfile:
    section = f"test_seams[{index}]"
    table = _required_table(raw, section)
    _reject_unknown_keys(table, _TEST_SEAM_KEYS, section)
    return TestSeamProfile(
        source=_required_text(table, "source", section),
        symbol=_optional_text(table, "symbol", section),
        preferred=_text_list(table, "preferred", section, default=[], max_len=140),
        avoid=_text_list(table, "avoid", section, default=[], max_len=140),
    )


def _table(raw: Any, section: str, *, default: dict[str, Any] | None = None) -> dict[str, Any]:
    if raw is None:
        return {} if default is None else default
    return _required_table(raw, section)


def _required_table(raw: Any, section: str) -> dict[str, Any]:
    if not isinstance(raw, dict):
        raise RepoProfileError(f"{section} must be a TOML table")
    return raw


def _table_list(raw: Any, section: str) -> list[dict[str, Any]]:
    if raw is None:
        return []
    if not isinstance(raw, list):
        raise RepoProfileError(f"{section} must be an array of tables")
    for index, item in enumerate(raw, start=1):
        if not isinstance(item, dict):
            raise RepoProfileError(f"{section}[{index}] must be a TOML table")
    return raw


def _reject_unknown_keys(table: dict[str, Any], allowed: set[str], section: str) -> None:
    unknown = sorted(set(table) - allowed)
    if unknown:
        joined = ", ".join(unknown)
        raise RepoProfileError(f"{section} has unsupported keys: {joined}")


def _required_id(table: dict[str, Any], section: str) -> str:
    value = _required_text(table, "id", section)
    if not re.match(r"^[a-z0-9][a-z0-9_.-]*$", value):
        raise RepoProfileError(f"{section}.id must be lowercase kebab/dot/snake case")
    return value


def _required_text(
    table: dict[str, Any],
    key: str,
    section: str,
    *,
    max_len: int = 120,
) -> str:
    value = _optional_text(table, key, section, max_len=max_len)
    if value is None:
        raise RepoProfileError(f"{section}.{key} is required")
    return value


def _optional_text(
    table: dict[str, Any],
    key: str,
    section: str,
    *,
    max_len: int = 120,
) -> str | None:
    if key not in table:
        return None
    value = table[key]
    if not isinstance(value, str) or not value.strip():
        raise RepoProfileError(f"{section}.{key} must be a non-empty string")
    value = value.strip()
    if "\n" in value:
        raise RepoProfileError(f"{section}.{key} must be a single-line string")
    if len(value) > max_len:
        raise RepoProfileError(f"{section}.{key} must be at most {max_len} chars")
    return value


def _text_list(
    table: dict[str, Any],
    key: str,
    section: str,
    *,
    default: list[str],
    max_len: int = 120,
) -> tuple[str, ...]:
    if key not in table:
        return tuple(default)
    values = table[key]
    if not isinstance(values, list):
        raise RepoProfileError(f"{section}.{key} must be a list of strings")
    normalized: list[str] = []
    for index, value in enumerate(values, start=1):
        if not isinstance(value, str) or not value.strip():
            raise RepoProfileError(f"{section}.{key}[{index}] must be a non-empty string")
        stripped = value.strip()
        if "\n" in stripped:
            raise RepoProfileError(f"{section}.{key}[{index}] must be single-line")
        if len(stripped) > max_len:
            raise RepoProfileError(
                f"{section}.{key}[{index}] must be at most {max_len} chars"
            )
        if stripped not in normalized:
            normalized.append(stripped)
    return tuple(normalized)


def _arg_sources(raw: Any, section: str) -> tuple[tuple[str, str], ...]:
    if raw is None:
        return ()
    if not isinstance(raw, dict):
        raise RepoProfileError(f"{section}.arg_sources must be a table")
    pairs: list[tuple[str, str]] = []
    for key, value in raw.items():
        if not isinstance(key, str) or not key.strip():
            raise RepoProfileError(f"{section}.arg_sources keys must be non-empty strings")
        if not isinstance(value, str) or not value.strip():
            raise RepoProfileError(f"{section}.arg_sources.{key} must be a non-empty string")
        key = key.strip()
        value = value.strip()
        if len(key) > 80:
            raise RepoProfileError(f"{section}.arg_sources key `{key}` is too long")
        if "\n" in value or len(value) > 160:
            raise RepoProfileError(f"{section}.arg_sources.{key} must be a short single-line string")
        pairs.append((key, value))
    return tuple(sorted(pairs))


def _require_unique_ids(section: str, values: list[str]) -> None:
    seen: set[str] = set()
    for value in values:
        if value in seen:
            raise RepoProfileError(f"{section} contains duplicate id `{value}`")
        seen.add(value)


def _source_imports_any(source_text: str, imports: tuple[str, ...]) -> bool:
    return any(_source_imports(source_text, spec) for spec in imports)


def _source_imports(source_text: str, spec: str) -> bool:
    escaped = re.escape(spec)
    patterns = (
        rf"\bfrom\s+['\"]{escaped}['\"]",
        rf"\bimport\s+[^;\n]*\s+from\s+['\"]{escaped}['\"]",
        rf"\brequire\(\s*['\"]{escaped}['\"]\s*\)",
    )
    return any(re.search(pattern, source_text) for pattern in patterns)


def _format_mock_export(export: MockExportRecipe) -> str:
    if export.members:
        members = ", ".join(export.members)
        return f"{export.name} ({export.kind}: {members})"
    return f"{export.name} ({export.kind})"
