"""Generate structured `.atm/profile.toml` facts from repository evidence."""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from agentic_tdd_runner.repo_profile import (
    DependencyContractProfile,
    DependencyEventContract,
    EventFrameworkProfile,
    ImportExpectationProfile,
    PROFILE_RELATIVE_PATH,
    RepoProfile,
    RunnerProfile,
    SCHEMA_VERSION,
)
from agentic_tdd_runner.runner_bootstrap import inspect_runner_bootstrap


JS_TS_SOURCE_SUFFIXES = {".ts", ".tsx", ".js", ".jsx", ".mts", ".cts"}
SKIP_DIRS = {
    ".atm",
    ".git",
    ".next",
    ".tmp",
    ".workspace",
    ".worktree",
    ".worktrees",
    "build",
    "coverage",
    "dist",
    "node_modules",
}


@dataclass(frozen=True)
class ProfileFragments:
    event_frameworks: tuple[EventFrameworkProfile, ...] = ()
    dependency_contracts: tuple[DependencyContractProfile, ...] = ()
    import_expectations: tuple[ImportExpectationProfile, ...] = ()


def infer_repo_profile(project_root: str | Path) -> RepoProfile:
    root = Path(project_root).expanduser().resolve()
    fragments = tuple(detector(root) for detector in (_infer_js_ts_profile_fragments,))
    return RepoProfile(
        runner=_infer_runner(root),
        event_frameworks=tuple(item for fragment in fragments for item in fragment.event_frameworks),
        dependency_contracts=tuple(item for fragment in fragments for item in fragment.dependency_contracts),
        import_expectations=tuple(item for fragment in fragments for item in fragment.import_expectations),
    )


def _infer_js_ts_profile_fragments(root: Path) -> ProfileFragments:
    if not _nearest_package_json(root):
        return ProfileFragments()

    source_files = _iter_js_ts_source_files(root)
    module_events = _js_ts_registered_events_by_imported_module(root, source_files)

    frameworks: list[EventFrameworkProfile] = []
    contracts: list[DependencyContractProfile] = []
    import_expectations: list[ImportExpectationProfile] = []
    for module_name in sorted(module_events):
        contract_id = f"{_module_id(module_name)}-events"
        events, contract_sources = _infer_js_ts_dependency_events(
            root,
            module_name=module_name,
            event_names=sorted(module_events[module_name]),
        )
        if not events:
            continue
        frameworks.append(
            EventFrameworkProfile(
                id=_module_id(module_name),
                kind="callback_event",
                module=module_name,
                imports=(module_name,),
                registrations=("on", "once"),
                dependency_contract=contract_id,
                contract_sources=tuple(contract_sources),
            ),
        )
        contracts.append(
            DependencyContractProfile(
                id=contract_id,
                module=module_name,
                contract_mode="inline",
                import_specs=(module_name,),
                events=tuple(events),
            ),
        )
        import_expectations.append(
            ImportExpectationProfile(
                module=module_name,
                expected_imports=(module_name,),
                applies_when="callback_contract",
            ),
        )

    return ProfileFragments(
        event_frameworks=tuple(frameworks),
        dependency_contracts=tuple(contracts),
        import_expectations=tuple(import_expectations),
    )


def render_repo_profile(profile: RepoProfile) -> str:
    lines: list[str] = [
        "[profile]",
        f"schema_version = {_toml_string(SCHEMA_VERSION)}",
    ]

    if profile.runner != RunnerProfile():
        lines.extend(["", "[runner]"])
        if profile.runner.test_runner:
            lines.append(f"test_runner = {_toml_string(profile.runner.test_runner)}")
        if profile.runner.test_command:
            lines.append(f"test_command = {_toml_string(profile.runner.test_command)}")
        if profile.runner.typecheck_command:
            lines.append(f"typecheck_command = {_toml_string(profile.runner.typecheck_command)}")

    for framework in profile.event_frameworks:
        lines.extend([
            "",
            "[[event_frameworks]]",
            f"id = {_toml_string(framework.id)}",
            f"kind = {_toml_string(framework.kind)}",
            f"module = {_toml_string(framework.module)}",
        ])
        if framework.imports:
            lines.append(f"imports = {_toml_list(framework.imports)}")
        if framework.registrations:
            lines.append(f"registrations = {_toml_list(framework.registrations)}")
        if framework.dependency_contract:
            lines.append(f"dependency_contract = {_toml_string(framework.dependency_contract)}")
        if framework.contract_sources:
            lines.append(f"contract_sources = {_toml_multiline_list(framework.contract_sources)}")

    for contract in profile.dependency_contracts:
        lines.extend([
            "",
            "[[dependency_contracts]]",
            f"id = {_toml_string(contract.id)}",
            f"module = {_toml_string(contract.module)}",
            f"contract_mode = {_toml_string(contract.contract_mode)}",
        ])
        if contract.import_specs:
            lines.append(f"import_specs = {_toml_list(contract.import_specs)}")
        if contract.events:
            lines.append("events = [")
            for event in contract.events:
                lines.append(f"  {_toml_event(event)},")
            lines.append("]")

    for expectation in profile.import_expectations:
        lines.extend([
            "",
            "[[import_expectations]]",
            f"module = {_toml_string(expectation.module)}",
            f"expected_imports = {_toml_list(expectation.expected_imports)}",
        ])
        if expectation.applies_when:
            lines.append(f"applies_when = {_toml_string(expectation.applies_when)}")

    return "\n".join(lines).rstrip() + "\n"


def generate_profile(project_root: str | Path, *, update: bool = False) -> Path:
    root = Path(project_root).expanduser().resolve()
    profile_path = root / PROFILE_RELATIVE_PATH
    if profile_path.exists() and not update:
        raise FileExistsError(f"profile already exists: {profile_path}")
    profile_path.parent.mkdir(parents=True, exist_ok=True)
    profile_path.write_text(render_repo_profile(infer_repo_profile(root)))
    return profile_path


def _infer_runner(root: Path) -> RunnerProfile:
    report = inspect_runner_bootstrap(root)
    return RunnerProfile(
        test_runner=report.test_runner,
        test_command=report.test_command,
        typecheck_command=_infer_typecheck_command(root, package_manager=report.package_manager),
    )


def _infer_typecheck_command(root: Path, *, package_manager: str | None) -> str | None:
    package_json = _nearest_package_json(root)
    if not package_json:
        return None
    pkg = _read_package_json(package_json)
    scripts = pkg.get("scripts", {}) if isinstance(pkg.get("scripts"), dict) else {}
    package_manager = package_manager or "npm"
    if "typecheck" in scripts:
        return f"{package_manager} run typecheck"
    deps = _all_dependencies(pkg)
    if "typescript" in deps:
        return "npx tsc --noEmit"
    return None


def _js_ts_registered_events_by_imported_module(
    root: Path,
    source_files: list[Path],
) -> dict[str, set[str]]:
    module_events: dict[str, set[str]] = {}
    for source_file in source_files:
        text = _read_text(source_file)
        if not text:
            continue
        imports = _js_ts_external_imports(text)
        if not imports:
            continue
        events = _js_ts_callback_registration_event_names(text)
        if not events:
            continue
        for module_name in imports:
            if not _has_node_dependency_type_or_source(root, module_name):
                continue
            module_events.setdefault(module_name, set()).update(events)
    return module_events


def _infer_js_ts_dependency_events(
    root: Path,
    *,
    module_name: str,
    event_names: list[str],
) -> tuple[list[DependencyEventContract], list[str]]:
    type_path, type_text = _read_node_module_types(root, module_name)
    source_files = _read_node_module_sources(root, module_name)

    events: list[DependencyEventContract] = []
    used_source_paths: set[Path] = set()
    for event_name in event_names:
        typed_params = _extract_typescript_event_typed_params(type_text, event_name) if type_text else []
        source_path, source_text, source_args = _first_js_ts_source_event(source_files, event_name)
        if not typed_params and not source_args:
            continue
        if source_path:
            used_source_paths.add(source_path)
        arg_sources = _extract_js_ts_arg_sources(
            source_text,
            source_args,
            typed_params=typed_params,
        ) if source_text and source_args else {}
        args = _merge_event_arg_names(
            typed_params=typed_params,
            source_args=source_args,
            arg_sources=arg_sources,
        )
        events.append(
            DependencyEventContract(
                name=event_name,
                args=tuple(args),
                arg_sources=tuple((name, arg_sources[name]) for name in args if name in arg_sources),
            ),
        )
    contract_sources = [
        _relative(root, path)
        for path in (*sorted(used_source_paths), type_path)
        if path is not None
    ]
    return events, contract_sources


def _merge_event_arg_names(
    *,
    typed_params: list[tuple[str, str]],
    source_args: list[str],
    arg_sources: dict[str, str],
) -> list[str]:
    if not typed_params:
        return source_args
    args = [name for name, _type_name in typed_params]
    if len(source_args) != len(typed_params):
        return args
    for index, source_name in enumerate(source_args):
        if source_name in arg_sources:
            args[index] = source_name
    return args


def _extract_js_ts_arg_sources(
    source_text: str,
    source_args: list[str],
    *,
    typed_params: list[tuple[str, str]],
) -> dict[str, str]:
    sources: dict[str, str] = {}
    for index, arg in enumerate(source_args):
        if typed_params and index < len(typed_params) and typed_params[index][0] == arg:
            continue
        match = re.search(
            rf"\b(?:const|let|var)\s+{re.escape(arg)}\s*=\s*(?P<expr>[^;\n]+)",
            source_text,
        )
        if match and _is_strong_runtime_arg_name(arg, match.group("expr")):
            sources[arg] = _normalize_source_expression(match.group("expr"))
    return sources


def _is_strong_runtime_arg_name(arg_name: str, expression: str) -> bool:
    name = arg_name.lower()
    if re.search(r"(count|month|streak|retry|total|viewers|amount|number)", name):
        return True
    return bool(re.search(r"~~|\|\|\s*0|\?\?\s*0", expression))


def _normalize_source_expression(expression: str) -> str:
    expr = re.sub(r"\s+", " ", expression).strip()
    if expr.startswith("~~(") and expr.endswith(")"):
        expr = expr[3:-1].strip()
    return expr


def _extract_typescript_event_typed_params(type_text: str, event_name: str) -> list[tuple[str, str]]:
    match = re.search(
        rf"^\s*{re.escape(event_name)}\s*\((?P<params>.*?)\)\s*:\s*[^;]+;",
        type_text,
        re.MULTILINE | re.DOTALL,
    )
    if not match:
        return []
    return _typescript_signature_param_types(match.group("params"))


def _typescript_signature_param_types(params: str) -> list[tuple[str, str]]:
    pairs: list[tuple[str, str]] = []
    for param in params.split(","):
        match = re.search(
            r"\b(?P<name>[A-Za-z_$][\w$]*)\s*:\s*(?P<type>[A-Za-z_$][\w$]*)\b",
            param,
        )
        if match:
            pairs.append((match.group("name"), match.group("type")))
    return pairs


def _extract_js_ts_source_event_args(source_text: str, event_name: str) -> list[str]:
    emits_match = re.search(
        rf"\.emits\(\s*\[(?P<events>[^\]]*['\"]{re.escape(event_name)}['\"][^\]]*)\]\s*,\s*\[\s*\[(?P<args>[^\]]+)\]\s*\]",
        source_text,
        re.DOTALL,
    )
    if emits_match:
        return _split_arg_names(emits_match.group("args"))

    emit_match = re.search(
        rf"\.emit\(\s*['\"]{re.escape(event_name)}['\"]\s*,(?P<args>[^;\n]+)\)",
        source_text,
        re.DOTALL,
    )
    if emit_match:
        return _split_arg_names(emit_match.group("args"))
    return []


def _first_js_ts_source_event(
    source_files: list[tuple[Path, str]],
    event_name: str,
) -> tuple[Path | None, str, list[str]]:
    for path, source_text in source_files:
        args = _extract_js_ts_source_event_args(source_text, event_name)
        if args:
            return path, source_text, args
    return None, "", []


def _split_arg_names(args_text: str) -> list[str]:
    names: list[str] = []
    for raw in args_text.split(","):
        name = raw.strip()
        if re.match(r"^[A-Za-z_$][\w$]*$", name):
            names.append(name)
    return names


def _js_ts_external_imports(source_text: str) -> set[str]:
    modules: set[str] = set()
    for match in re.finditer(r"\bfrom\s+['\"](?P<module>[^'\"]+)['\"]", source_text):
        module = match.group("module")
        if _is_external_module(module):
            modules.add(module)
    for match in re.finditer(r"\bimport\s*\(\s*['\"](?P<module>[^'\"]+)['\"]\s*\)", source_text):
        module = match.group("module")
        if _is_external_module(module):
            modules.add(module)
    for match in re.finditer(r"\brequire\(\s*['\"](?P<module>[^'\"]+)['\"]\s*\)", source_text):
        module = match.group("module")
        if _is_external_module(module):
            modules.add(module)
    return modules


def _js_ts_callback_registration_event_names(source_text: str) -> set[str]:
    return {
        match.group("event")
        for match in re.finditer(
            r"\.\s*(?:on|once)\(\s*['\"](?P<event>[^'\"]+)['\"]\s*,",
            source_text,
        )
    }


def _has_node_dependency_type_or_source(root: Path, module_name: str) -> bool:
    type_path, _type_text = _read_node_module_types(root, module_name)
    return type_path is not None or bool(_read_node_module_sources(root, module_name))


def _read_node_module_types(root: Path, module_name: str) -> tuple[Path | None, str]:
    types_module = _types_package_name(module_name)
    candidates = [
        root / "node_modules" / "@types" / types_module / "index.d.ts",
        root / "node_modules" / module_name / "index.d.ts",
        root / "node_modules" / module_name / "types.d.ts",
    ]
    for candidate in candidates:
        if candidate.is_file():
            return candidate, _read_text(candidate)
    return None, ""


def _read_node_module_sources(root: Path, module_name: str) -> list[tuple[Path, str]]:
    module_root = root / "node_modules" / module_name
    if not module_root.is_dir():
        return []
    candidates: list[Path] = []
    package_json = module_root / "package.json"
    if package_json.is_file():
        pkg = _read_package_json(package_json)
        for field in ("module", "main"):
            value = pkg.get(field)
            if isinstance(value, str):
                candidates.append(module_root / value)
    candidates.extend(sorted(
        path
        for path in module_root.rglob("*.js")
        if path.is_file()
    ))
    seen: set[Path] = set()
    sources: list[tuple[Path, str]] = []
    for candidate in candidates:
        if candidate in seen:
            continue
        seen.add(candidate)
        if candidate.is_file():
            sources.append((candidate, _read_text(candidate)))
    return sources


def _iter_js_ts_source_files(root: Path) -> list[Path]:
    files: list[Path] = []
    for path in root.rglob("*"):
        if not path.is_file() or path.suffix not in JS_TS_SOURCE_SUFFIXES:
            continue
        if any(part in SKIP_DIRS for part in path.relative_to(root).parts):
            continue
        files.append(path)
    return sorted(files)


def _nearest_package_json(root: Path) -> Path | None:
    current = root if root.is_dir() else root.parent
    while True:
        path = current / "package.json"
        if path.is_file():
            return path
        if current.parent == current:
            return None
        current = current.parent


def _read_package_json(path: Path) -> dict[str, Any]:
    try:
        data = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError):
        return {}
    return data if isinstance(data, dict) else {}


def _read_text(path: Path) -> str:
    try:
        return path.read_text(errors="ignore")
    except OSError:
        return ""


def _all_dependencies(pkg: dict[str, Any]) -> set[str]:
    deps: set[str] = set()
    for key in ("dependencies", "devDependencies", "optionalDependencies"):
        value = pkg.get(key)
        if isinstance(value, dict):
            deps.update(str(name) for name in value)
    return deps


def _module_id(module_name: str) -> str:
    name = module_name.rsplit("/", 1)[-1]
    if name.endswith(".js"):
        name = name[:-3]
    return re.sub(r"[^A-Za-z0-9]+", "-", name).strip("-") or "dependency"


def _types_package_name(module_name: str) -> str:
    if module_name.startswith("@") and "/" in module_name:
        scope, name = module_name[1:].split("/", 1)
        return f"{scope}__{name}"
    return module_name


def _is_external_module(module_name: str) -> bool:
    return not module_name.startswith((".", "/"))


def _relative(root: Path, path: Path) -> str:
    try:
        return path.relative_to(root).as_posix()
    except ValueError:
        return path.as_posix()


def _toml_event(event: DependencyEventContract) -> str:
    parts = [
        f"name = {_toml_string(event.name)}",
        f"args = {_toml_list(event.args)}",
    ]
    if event.arg_sources:
        parts.append(f"arg_sources = {_toml_inline_table(event.arg_sources)}")
    return "{ " + ", ".join(parts) + " }"


def _toml_inline_table(items: tuple[tuple[str, str], ...]) -> str:
    return "{ " + ", ".join(
        f"{key} = {_toml_string(value)}"
        for key, value in items
    ) + " }"


def _toml_multiline_list(values: tuple[str, ...]) -> str:
    if not values:
        return "[]"
    inner = "\n".join(f"  {_toml_string(value)}," for value in values)
    return "[\n" + inner + "\n]"


def _toml_list(values) -> str:
    return "[" + ", ".join(_toml_string(str(value)) for value in values) + "]"


def _toml_string(value: str) -> str:
    return json.dumps(value)
