"""TypeScript / bun:test language plugin."""
from __future__ import annotations

import json
import posixpath
import re
import shlex
from pathlib import Path, PurePosixPath
from typing import Any

from agentic_tdd_runner.apply_patch import ApplyPatchError, apply_patch_touched_paths
from agentic_tdd_runner.languages import register
from agentic_tdd_runner.languages.signature import parse_signature_params

_TS_NAMED_IMPORT_RE = re.compile(
    r"^\s*import\s*{([^}]+)}\s*from\s*['\"]([^'\"]+)['\"]\s*;?",
    re.MULTILINE,
)
_TS_DEFAULT_IMPORT_RE = re.compile(
    r"^\s*import\s+([A-Za-z_]\w*)\s+from\s+['\"]([^'\"]+)['\"]\s*;?",
    re.MULTILINE,
)
_TS_NAMESPACE_IMPORT_RE = re.compile(
    r"^\s*import\s+\*\s+as\s+([A-Za-z_]\w*)\s+from\s+['\"]([^'\"]+)['\"]\s*;?",
    re.MULTILINE,
)
_TOP_LEVEL_TS_ASSIGN_RE = re.compile(
    r"^(?:export\s+)?(const|let|var)\s+([A-Za-z_]\w*)"
    r"(?:\s*:\s*([^=;]+))?(?:\s*=\s*(.+?))?;?\s*$"
)
BUN_TEST_API_IMPORT = 'import { beforeEach, describe, expect, mock, test } from "bun:test";'
TEST_FILE_PATTERNS = ["*.test.ts", "*.test.tsx", "*.test.js", "*.test.jsx"]
TEST_RUNNERS = {"bun:test", "node:test", "vitest", "jest"}
PACKAGE_MANAGERS = {"bun", "npm", "pnpm", "yarn"}
_BUN_TEST_GLOBALS = {"beforeEach", "describe", "expect", "mock", "test"}
_MISSING_NAME_RE = re.compile(
    r"(?P<file>[^\s:(]+\.(?:test|spec)\.[tj]sx?)"
    r"(?:\(\d+,\d+\)|:\d+:\d+)?(?:\s*-\s*|:\s*)error TS\d+:\s*"
    r"Cannot find name ['\"](?P<name>[A-Za-z_]\w*)['\"]"
)
_BUN_MOCK_RESET_RE = re.compile(
    r"(?P<file>[^\s:(]+\.test\.[tj]sx?)"
    r"(?:\(\d+,\d+\)|:\d+:\d+)?(?:\s*-\s*|:\s*)error TS\d+:\s*"
    r"Property ['\"]reset['\"] does not exist on type ['\"]?MockFunctionState",
)
_TEST_FILE_RE = re.compile(r"(?P<file>[^\s:]+\.test\.[tj]sx?)[:\s]")


class TypeScriptLanguage:
    name = "typescript"
    runner = "bun:test"
    extensions = [".ts", ".tsx", ".js", ".jsx"]

    def test_file_patterns(self) -> list[str]:
        return list(TEST_FILE_PATTERNS)

    def code_fence(self) -> str:
        return "ts"

    def default_exclude_dirs(self) -> list[str]:
        return ["node_modules", ".next", ".turbo"]

    def supports_test_runner(self, test_runner: str | None) -> bool:
        return test_runner in TEST_RUNNERS

    def effective_test_command_template(
        self,
        report: Any,
        configured_command: str | None = None,
    ) -> str:
        fallback = _configured_test_command(configured_command)
        test_runner = _report_value(report, "test_runner")
        if not test_runner or test_runner == "custom":
            return fallback

        if test_runner == "bun:test":
            return "bun test"
        if test_runner == "node:test":
            return "node --test"
        if test_runner == "vitest":
            return _package_runner_command(
                _report_value(report, "package_manager"),
                "vitest",
                ["run"],
            )
        if test_runner == "jest":
            return _package_runner_command(
                _report_value(report, "package_manager"),
                "jest",
                _jest_runner_args(report),
            )
        return fallback

    def test_command_template(self, config: dict | None = None) -> str:
        runner_config = ((config or {}).get("runner", {}) or {})
        configured = _configured_test_command(runner_config.get("command"))
        detected = self.effective_test_command_template(
            runner_config.get("bootstrap"),
            configured,
        )
        return detected or configured or "bun test"

    def runner_version_command(self, report: Any) -> list[str] | None:
        test_runner = _report_value(report, "test_runner")
        if not test_runner or test_runner == "custom":
            return None

        if test_runner == "bun:test":
            return ["bun", "--version"]
        if test_runner == "node:test":
            return ["node", "--version"]
        if test_runner == "vitest":
            return _package_runner_argv(
                _report_value(report, "package_manager"),
                "vitest",
                ["--version"],
            )
        if test_runner == "jest":
            return _package_runner_argv(
                _report_value(report, "package_manager"),
                "jest",
                ["--version"],
            )
        return None

    def is_test_run_command(self, command: str, parts: list[str]) -> bool:
        if not parts:
            return False
        executable = Path(parts[0]).name
        if executable == "bun" and len(parts) > 1 and parts[1] == "test":
            return True
        if _is_package_manager_test_command(executable, parts):
            return True
        return any(re.search(r"\.test\.[tj]sx?$", part) for part in parts[1:])

    def is_dependency_contract_lookup(self, command: str, parts: list[str]) -> bool:
        if "node_modules" not in command:
            return False
        if not parts:
            return False
        lookup_tools = {"rg", "grep", "find", "cat", "head", "tail", "sed", "awk", "ls"}
        return parts[0] in lookup_tools or "node_modules/@types" in command

    def test_api_import(self, test_runner: str) -> str | None:
        if test_runner == "bun:test":
            return BUN_TEST_API_IMPORT
        return None

    def test_api_facts(self, test_runner: str) -> list[str]:
        if test_runner != "bun:test":
            return []
        return [
            "Bun mock functions reset call history with `mockFn.mockClear()`; do not use `.mock.reset()`.",
            "When a module has import-time side effects, make `mock.module(...)` registrations happen before the target module is evaluated.",
        ]

    def typecheck_command(self, root: Path, config: dict, test_runner: str) -> str | None:
        environment = config.get("environment", {}) if isinstance(config, dict) else {}
        if environment.get("run_typecheck") is False:
            return None
        scripts = _package_scripts(root)
        if "typecheck" not in scripts:
            return None
        if test_runner == "bun:test" or _uses_bun(root):
            return "bun run typecheck"
        if (root / "pnpm-lock.yaml").exists():
            return "pnpm typecheck"
        if (root / "yarn.lock").exists():
            return "yarn typecheck"
        return "npm run typecheck"

    def detect_quality_tools(self, workdir: str) -> list[dict]:
        pkg_path = Path(workdir) / "package.json"
        if not pkg_path.is_file():
            return []
        try:
            pkg = json.loads(pkg_path.read_text())
        except (OSError, ValueError):
            return []

        scripts = pkg.get("scripts", {})
        dev_deps = pkg.get("devDependencies", {})
        deps = pkg.get("dependencies", {})
        all_deps = {**deps, **dev_deps}
        package_manager = detect_package_manager(workdir, pkg)
        exec_prefix = package_exec_prefix(package_manager)
        checks = []

        if "typecheck" in scripts:
            checks.append({"name": "typecheck", "command": f"{package_manager} run typecheck"})
        elif "typescript" in all_deps:
            checks.append({"name": "typecheck", "command": f"{exec_prefix} tsc --noEmit"})

        if any(k.startswith("@biomejs/biome") for k in all_deps):
            checks.append({
                "name": "lint",
                "command": f"{exec_prefix} biome check {{changed_files}}",
                "fix": f"{exec_prefix} biome check {{changed_files}} --fix",
            })
        elif "eslint" in all_deps:
            checks.append({
                "name": "lint",
                "command": f"{exec_prefix} eslint {{changed_files}}",
                "fix": f"{exec_prefix} eslint {{changed_files}} --fix",
            })

        has_biome = any(k.startswith("@biomejs/biome") for k in all_deps)
        if not has_biome and "prettier" in all_deps:
            checks.append({
                "name": "format",
                "command": f"{exec_prefix} prettier --check {{changed_files}}",
                "fix": f"{exec_prefix} prettier --write {{changed_files}}",
            })
        return checks

    def referenced_type_shapes(
        self,
        *,
        workdir: str | None,
        contract_facts: list[str],
    ) -> list[dict[str, Any]]:
        if not workdir:
            return []

        shapes: list[dict[str, Any]] = []
        for module_name, type_names in _referenced_framework_types(contract_facts):
            type_text = _read_module_type_declarations(Path(workdir), module_name)
            if not type_text:
                continue
            for type_name in type_names:
                shape = _extract_type_shape(type_text, type_name)
                if shape:
                    shape["module"] = module_name
                    shapes.append(shape)
        return shapes

    def test_setup_dependency_paths(
        self,
        *,
        workdir: str | None,
        test_file: Any,
    ) -> list[str]:
        if not workdir or not test_file:
            return []

        test_path = Path(workdir) / str(test_file)
        try:
            test_text = test_path.read_text()
        except OSError:
            return []

        test_dir = posixpath.dirname(_normalize_path(str(test_file)))
        paths: set[str] = set()
        for spec in _extract_bun_mock_module_specs(test_text):
            if not spec.startswith(("./", "../")):
                continue
            candidate_base = _normalize_path(posixpath.join(test_dir, spec))
            if not candidate_base or candidate_base.startswith("../") or "/../" in candidate_base:
                continue
            resolved = _resolve_repo_module_path(Path(workdir), candidate_base)
            if resolved:
                paths.add(resolved)
        return sorted(paths)

    def extract_mock_modules(self, cookbook_text: str) -> list[str]:
        modules: list[str] = []
        for line in cookbook_text.splitlines():
            stripped = line.strip()
            if stripped.startswith("mock.module("):
                modules.append(stripped)
        return modules

    def regression_test_skeleton(self, context: dict) -> str:
        facts = context.get("contract_facts") or []
        mock_block = str(context.get("module_mock_block") or "").strip()
        signature = str(context.get("source_signature") or "").strip()
        target_symbol = str(context.get("target_symbol") or "").strip()
        callback_params = _callback_params_from_contract_facts(facts)
        if not (facts and mock_block and signature and target_symbol):
            return ""

        import_path = context.get("source_import_path") or "./client"
        spy_names = _extract_spy_names(mock_block)
        type_imports = _extract_type_imports_from_contract_facts(facts)
        type_shapes = context.get("referenced_type_shapes") or []
        fixture_lines = _build_callback_fixture_lines(
            params=callback_params,
            facts=facts,
            type_shapes=type_shapes,
        )
        lines = [
            BUN_TEST_API_IMPORT,
        ]
        for module_name, names in type_imports:
            lines.append(f'import type {{ {", ".join(names)} }} from "{module_name}";')
        lines.append("")
        lines.extend(mock_block.splitlines())
        lines.append("")

        lines.extend([
            f'type ClientModule = typeof import("{import_path}");',
            f'type TargetHandler = ClientModule["{target_symbol}"];',
        ])
        if callback_params:
            lines.extend([
                "type CallbackContract = (",
                *[
                    f"  {param_name}: {type_name},"
                    for param_name, type_name in callback_params
                ],
                ") => void;",
            ])
        else:
            lines.append("type CallbackContract = TargetHandler;")
        lines.extend([
            "",
            "let targetHandler: TargetHandler;",
            "",
            "beforeEach(async () => {",
        ])
        if spy_names:
            lines.append(f"  for (const spy of [{', '.join(spy_names)}]) spy.mockClear();")
        else:
            lines.append("  // mockClear every *_spy from the cookbook here.")
        lines.extend([
            f'  const clientModule = await import("{import_path}");',
            f"  targetHandler = clientModule.{target_symbol};",
            "});",
            "",
            'test("covers the reported callback behavior", () => {',
            "  // Arrange issue-grounded doubles; do not add production test-only setters.",
        ])
        if fixture_lines:
            lines.extend(f"  {line}" if line else "" for line in fixture_lines)
        else:
            lines.append("  // Build typed callback arguments from the Callback Contract Evidence.")
        lines.extend([
            "  const callbackHandler: CallbackContract = targetHandler;",
            "  callbackHandler(" + ", ".join(_callback_argument_names(callback_params)) + ");",
            "  // Assert through the named *_spy variables and issue acceptance criteria.",
            '  throw new Error("replace skeleton comments with the focused failing regression");',
            "});",
        ])
        return "\n".join(lines)

    def intent_router_observe_tool_result(
        self,
        name: str,
        args: dict,
        result: str,
        runner_facts: Any,
    ) -> dict[str, Any]:
        observations: dict[str, Any] = {}
        if _report_value(runner_facts, "test_api_import"):
            test_file, missing = _parse_missing_bun_test_globals(result)
            if test_file and missing:
                observations["missing_globals"] = {
                    "test_file": test_file,
                    "names": sorted(missing),
                }

        if _report_value(runner_facts, "test_runner") == "bun:test":
            mock_api_file = _parse_bun_mock_reset_file(result)
            if mock_api_file:
                observations["mock_api_file"] = mock_api_file

            import_side_effect_file = _parse_bun_import_time_side_effect_file(
                name,
                args,
                result,
                runner_facts,
            )
            if import_side_effect_file:
                observations["import_side_effect_file"] = import_side_effect_file
        return observations

    def intent_router_review_tool_call(
        self,
        state: dict[str, Any],
        name: str,
        args: dict,
        runner_facts: Any,
    ) -> dict[str, Any] | None:
        import_side_effect_file = state.get("pending_import_side_effect_file")
        if import_side_effect_file:
            if self.intent_router_is_edit_to_path(name, args, import_side_effect_file):
                return None
            if (
                self.intent_router_is_framework_lookup_or_premature_run(name, args)
                or _is_source_or_test_lookup(name, args)
            ):
                source_file = _report_value(runner_facts, "source_file")
                import_path = (
                    self.import_path(import_side_effect_file, source_file)
                    if source_file
                    else "./target"
                )
                required_next = (
                    f"Edit `{import_side_effect_file}` so test mocks are registered "
                    "before the target module is evaluated. Follow an existing sibling-test "
                    "pattern if one already solves this; otherwise delay the target import."
                )
                return {
                    "event": "intent_router_answered",
                    "message": (
                        "IMPORT-TIME SIDE EFFECT ANSWER\n"
                        "intent: fix_import_time_side_effect\n"
                        "reason: the focused test failed while Bun was evaluating the target module, "
                        "before the test body could run. This is an import-order problem in the test, "
                        "not a reason to inspect provider/env/singleton modules.\n"
                        f"answer: make `mock.module(...)` registrations happen before `{import_path}` "
                        "is evaluated. Prefer the repo's existing mock/import pattern. If a static "
                        "target import would be hoisted before mocks, delay that import, for example "
                        f"with `await import('{import_path}')`.\n"
                        "guardrail: Do not inspect provider/env/singleton modules; keep the fix "
                        "in the test harness.\n"
                        f"required_next: {required_next}"
                    ),
                    "data": {
                        "intent": "fix_import_time_side_effect",
                        "pending_test_file": import_side_effect_file,
                        "source_file": source_file,
                        "import_path": import_path,
                        "required_next": required_next,
                        "tool": name,
                        "args": args,
                    },
                }

        mock_api_file = state.get("pending_mock_api_file")
        if mock_api_file:
            if self.intent_router_is_edit_to_path(name, args, mock_api_file):
                return None
            if (
                self.intent_router_is_framework_lookup_or_premature_run(name, args)
                or _is_source_or_test_lookup(name, args)
            ):
                required_next = (
                    f"Edit `{mock_api_file}` and replace `.mock.reset()` "
                    "with `.mockClear()`, then rerun the focused test."
                )
                return {
                    "event": "intent_router_answered",
                    "message": (
                        "RUNNER FACT ANSWER\n"
                        "intent: fix_bun_mock_api\n"
                        "reason: Bun mock call history is reset on the mock function, not through `.mock.reset()`.\n"
                        "answer: use `mockFn.mockClear()`.\n"
                        f"required_next: {required_next}"
                    ),
                    "data": {
                        "intent": "fix_bun_mock_api",
                        "pending_test_file": mock_api_file,
                        "required_next": required_next,
                        "tool": name,
                        "args": args,
                    },
                }

        pending_test_file = state.get("pending_test_file")
        missing_globals = set(state.get("pending_missing_globals") or [])
        test_api_import = _report_value(runner_facts, "test_api_import")
        if not (pending_test_file and missing_globals and test_api_import):
            return None
        if self.intent_router_is_edit_to_path(name, args, pending_test_file):
            return None
        if not self.intent_router_is_framework_lookup_or_premature_run(name, args):
            return None
        missing = ", ".join(sorted(missing_globals))
        required_next = f"Add `{test_api_import}` to `{pending_test_file}`, then rerun the focused test."
        return {
            "event": "intent_router_answered",
            "message": (
                "RUNNER FACT ANSWER\n"
                "intent: inspect_test_framework\n"
                f"reason: `{missing}` are test API globals provided by "
                f"{_report_value(runner_facts, 'test_runner')}; this is a missing import, not a project-config gap.\n"
                f"answer: {test_api_import}\n"
                "note: if that import is already present, keep the next fix in the same test file "
                "and correct the local import/type issue before rerunning.\n"
                f"required_next: {required_next}"
            ),
            "data": {
                "intent": "inspect_test_framework",
                "pending_test_file": pending_test_file,
                "missing_globals": sorted(missing_globals),
                "required_next": required_next,
                "tool": name,
                "args": args,
            },
        }

    def intent_router_is_edit_to_path(
        self,
        name: str,
        args: dict,
        target_path: str | None,
    ) -> bool:
        if not target_path:
            return False
        if name in {"create_file", "str_replace_editor"}:
            return _normalize_path(args.get("path")) == _normalize_path(target_path)
        if name == "apply_patch":
            return _normalize_path(target_path) in _apply_patch_paths(args)
        return False

    def intent_router_is_framework_lookup_or_premature_run(self, name: str, args: dict) -> bool:
        if name == "read_file":
            path = _normalize_path(args.get("path"))
            if not path:
                return False
            filename = PurePosixPath(path).name
            return (
                "node_modules" in PurePosixPath(path).parts
                or filename in {"package.json", "bunfig.toml"}
                or filename.startswith("tsconfig")
                or filename.startswith(("vitest.config", "jest.config"))
            )

        if name == "rg":
            paths = args.get("path") or "."
            if isinstance(paths, str):
                paths = [paths]
            return any("node_modules" in PurePosixPath(str(path)).parts for path in paths)

        if name != "run_command":
            return False
        command = args.get("command", "")
        if not isinstance(command, str):
            return False
        try:
            parts = shlex.split(command)
        except ValueError:
            parts = command.split()
        if not parts:
            return False
        executable = PurePosixPath(parts[0]).name
        if executable in PACKAGE_MANAGERS:
            return True
        if any("node_modules" in PurePosixPath(part).parts for part in parts[1:]):
            return True
        if any(
            PurePosixPath(part).name in {"package.json", "bunfig.toml"}
            for part in parts[1:]
        ):
            return True
        if any(PurePosixPath(part).name.startswith("tsconfig") for part in parts[1:]):
            return True
        return False

    def parse_imports(self, source_text: str) -> dict:
        imports = {}
        for match in _TS_NAMED_IMPORT_RE.finditer(source_text):
            module_name = match.group(2)
            for piece in match.group(1).split(","):
                item = piece.strip()
                if not item:
                    continue
                if " as " in item:
                    export_name, local_name = [p.strip() for p in item.split(" as ", 1)]
                else:
                    export_name = local_name = item
                imports[local_name] = {
                    "source_module": module_name,
                    "export_name": export_name,
                    "import_kind": "named",
                }
        for match in _TS_DEFAULT_IMPORT_RE.finditer(source_text):
            imports[match.group(1)] = {
                "source_module": match.group(2),
                "export_name": "default",
                "import_kind": "default",
            }
        for match in _TS_NAMESPACE_IMPORT_RE.finditer(source_text):
            imports[match.group(1)] = {
                "source_module": match.group(2),
                "export_name": match.group(1),
                "import_kind": "namespace",
            }
        return imports

    def parse_assignments(self, source_text: str) -> dict:
        assignments = {}
        for line in source_text.splitlines():
            if line.startswith((" ", "\t")):
                continue
            m = _TOP_LEVEL_TS_ASSIGN_RE.match(line)
            if m:
                rhs = (m.group(4) or "").strip()
                called_symbol = None
                call_match = re.match(r"([A-Za-z_]\w*)\s*\(", rhs)
                if call_match:
                    called_symbol = call_match.group(1)
                assignments[m.group(2)] = {
                    "kind": m.group(1),
                    "type_annotation": (m.group(3) or "").strip() or None,
                    "rhs": rhs,
                    "called_symbol": called_symbol,
                    "line": line,
                }
        return assignments

    def parse_signature_params(self, signature: str) -> list[str]:
        return parse_signature_params(signature, strip_optional_marker=True)

    def test_path(self, source_path: str, symbol: str) -> str:
        source = PurePosixPath(source_path)
        return (source.parent / f"{symbol}.test{source.suffix}").as_posix()

    def setter_name(self, binding: str) -> str:
        return f"__set{binding[:1].upper()}{binding[1:]}ForTests"

    def is_exported(self, source_text: str, symbol: str) -> bool:
        patterns = [
            rf"^\s*export\s+(?:async\s+)?function\s+{re.escape(symbol)}\b",
            rf"^\s*export\s+(?:const|let|var)\s+{re.escape(symbol)}\b",
            rf"export\s*{{[^}}]*\b{re.escape(symbol)}\b[^}}]*}}",
        ]
        return any(re.search(p, source_text, re.MULTILINE) for p in patterns)

    def render_seam_setter(self, binding: str, assignment: dict) -> str:
        name = self.setter_name(binding)
        type_hint = assignment.get("type_annotation") or f"typeof {binding}"
        observed_members = assignment.get("observed_members") or []
        value_type = type_hint
        if assignment.get("type_annotation") and observed_members:
            members = " | ".join(f"'{member}'" for member in observed_members)
            value_type = f"Pick<{type_hint}, {members}>"
        return (
            f"export function {name}(value: {value_type}): void {{\n"
            f"  {binding} = value as {type_hint};\n"
            f"}}"
        )

    def import_path(self, test_path: str, source_path: str) -> str:
        test_dir = PurePosixPath(test_path).parent
        source_no_ext = PurePosixPath(source_path).with_suffix("")
        target_parts = source_no_ext.parts
        start_parts = test_dir.parts
        common = 0
        for left, right in zip(target_parts, start_parts):
            if left != right:
                break
            common += 1
        up = [".."] * (len(start_parts) - common)
        down = list(target_parts[common:])
        parts = up + down
        rel = "/".join(parts) if parts else "."
        if not rel.startswith("."):
            rel = f"./{rel}"
        return rel

    def prepend_export(self, line: str) -> str:
        stripped = line.lstrip()
        indent = line[: len(line) - len(stripped)]
        return f"{indent}export {stripped}"


def _configured_test_command(command: str | None) -> str:
    if isinstance(command, str) and command.strip():
        return command.strip()
    return ""


def _report_value(report: Any, key: str) -> str | None:
    if report is None:
        return None
    value = report.get(key) if isinstance(report, dict) else getattr(report, key, None)
    return value if isinstance(value, str) and value else None


def _package_runner_command(
    package_manager: str | None,
    runner: str,
    runner_args: list[str],
) -> str:
    return shlex.join(_package_runner_argv(package_manager, runner, runner_args))


def _jest_runner_args(report: Any) -> list[str]:
    args = ["--runInBand", "--watchman=false", "--coverage=false"]
    test_config_path = _report_value(report, "test_config_path")
    if test_config_path:
        args.extend(["--config", test_config_path])
    return args


def _package_runner_argv(
    package_manager: str | None,
    runner: str,
    runner_args: list[str],
) -> list[str]:
    if package_manager == "pnpm":
        return ["pnpm", "exec", runner, *runner_args]
    if package_manager == "yarn":
        return ["yarn", runner, *runner_args]
    if package_manager == "bun":
        return ["bun", "x", runner, *runner_args]
    return ["npm", "exec", "--", runner, *runner_args]


def _package_scripts(root: Path) -> dict[str, Any]:
    package_json = root / "package.json"
    try:
        data = json.loads(package_json.read_text())
    except (OSError, json.JSONDecodeError):
        return {}
    scripts = data.get("scripts")
    return scripts if isinstance(scripts, dict) else {}


def _uses_bun(root: Path) -> bool:
    package_json = root / "package.json"
    try:
        data = json.loads(package_json.read_text())
    except (OSError, json.JSONDecodeError):
        data = {}
    package_manager = data.get("packageManager") if isinstance(data, dict) else None
    return (
        (isinstance(package_manager, str) and package_manager.startswith("bun@"))
        or (root / "bun.lock").exists()
        or (root / "bun.lockb").exists()
    )


def detect_package_manager(workdir: str, pkg: dict | None = None) -> str:
    if pkg:
        pm_field = pkg.get("packageManager", "")
        if pm_field:
            name = pm_field.split("@")[0]
            if name in PACKAGE_MANAGERS:
                return name
    lockfiles = {
        "pnpm-lock.yaml": "pnpm",
        "yarn.lock": "yarn",
        "bun.lock": "bun",
    }
    for filename, package_manager in lockfiles.items():
        if (Path(workdir) / filename).is_file():
            return package_manager
    return "npm"


def package_exec_prefix(package_manager: str) -> str:
    return {
        "npm": "npx",
        "pnpm": "pnpm exec",
        "yarn": "yarn",
        "bun": "bunx",
    }.get(package_manager, "npx")


def _is_package_manager_test_command(executable: str, parts: list[str]) -> bool:
    if executable not in {"npm", "pnpm", "yarn"}:
        return False
    return (len(parts) >= 2 and parts[1] == "test") or (
        len(parts) >= 3 and parts[1] == "run" and parts[2] == "test"
    )


def _normalize_path(path: Any) -> str:
    raw = str(path or "").strip()
    if not raw:
        return ""
    normalized = posixpath.normpath(PurePosixPath(raw).as_posix())
    if normalized == ".":
        return ""
    return normalized.removeprefix("./")


def _extract_bun_mock_module_specs(test_text: str) -> list[str]:
    return [
        match.group("spec")
        for match in re.finditer(
            r"mock\.module\(\s*['\"](?P<spec>\.{1,2}/[^'\"]+)['\"]",
            test_text,
        )
    ]


def _resolve_repo_module_path(workdir: Path, candidate_base: str) -> str:
    candidates = [
        candidate_base,
        *[f"{candidate_base}{suffix}" for suffix in (".ts", ".tsx", ".js", ".jsx", ".mts", ".cts")],
        *[
            posixpath.join(candidate_base, f"index{suffix}")
            for suffix in (".ts", ".tsx", ".js", ".jsx", ".mts", ".cts")
        ],
    ]
    for candidate in candidates:
        if (workdir / candidate).is_file():
            return candidate
    return ""


def _referenced_framework_types(contract_facts: list[str]) -> list[tuple[str, list[str]]]:
    primitive_types = {"string", "number", "boolean", "void", "unknown", "object"}
    refs: dict[str, set[str]] = {}
    for fact in contract_facts:
        match = re.search(
            r"(?P<module>[\w@./-]+) type declarations expose `[^`]+\((?P<params>[^`]*)\)`",
            fact,
        )
        if not match:
            continue
        names = refs.setdefault(match.group("module"), set())
        for _param_name, type_name in _signature_param_types(match.group("params")):
            if type_name not in primitive_types:
                names.add(type_name)
    return [
        (module_name, sorted(type_names))
        for module_name, type_names in sorted(refs.items())
        if type_names
    ]


def _read_module_type_declarations(workdir: Path, module_name: str) -> str:
    candidates = [
        workdir / "node_modules" / "@types" / module_name / "index.d.ts",
        workdir / "node_modules" / module_name / "index.d.ts",
        workdir / "node_modules" / module_name / "types.d.ts",
    ]
    for candidate in candidates:
        if candidate.is_file():
            return candidate.read_text(errors="ignore")
    return ""


def _extract_type_shape(type_text: str, type_name: str) -> dict[str, Any]:
    interface_match = re.search(
        rf"\binterface\s+{re.escape(type_name)}\b[^\{{]*{{(?P<body>.*?)\n\s*}}",
        type_text,
        re.DOTALL,
    )
    if interface_match:
        fields = _extract_type_fields(interface_match.group("body"))
        return {"name": type_name, "kind": "interface", "fields": fields}

    alias_match = re.search(
        rf"\btype\s+{re.escape(type_name)}\s*=\s*(?P<body>[^;]+);",
        type_text,
        re.DOTALL,
    )
    if alias_match:
        return {
            "name": type_name,
            "kind": "type",
            "alias": re.sub(r"\s+", " ", alias_match.group("body")).strip(),
            "fields": [],
        }
    return {}


def _extract_type_fields(body: str) -> list[dict[str, str | bool]]:
    fields: list[dict[str, str | bool]] = []
    pattern = re.compile(
        r"^\s*(?P<name>['\"][^'\"]+['\"]|[A-Za-z_$][\w$]*)"
        r"(?P<optional>\?)?\s*:\s*(?P<type>[^;\n]+);?",
        re.MULTILINE,
    )
    for match in pattern.finditer(body):
        raw_name = match.group("name")
        name = raw_name[1:-1] if raw_name.startswith(("'", '"')) else raw_name
        fields.append({
            "name": name,
            "optional": bool(match.group("optional")),
            "type": re.sub(r"\s+", " ", match.group("type")).strip(),
        })
    return fields


def _extract_spy_names(mock_block: str) -> list[str]:
    names: list[str] = []
    for line in mock_block.splitlines():
        stripped = line.strip()
        if not stripped.startswith("const ") or "_spy" not in stripped:
            continue
        name = stripped.removeprefix("const ").split("=", 1)[0].strip()
        if name.endswith("_spy"):
            names.append(name)
    return names


def _extract_type_imports_from_contract_facts(facts: list[str]) -> list[tuple[str, list[str]]]:
    return _referenced_framework_types(facts)


def _callback_params_from_contract_facts(facts: list[str]) -> list[tuple[str, str]]:
    for fact in facts:
        match = re.search(
            r"[\w@./-]+ type declarations expose `[^`]+\((?P<params>[^`]*)\)`",
            fact,
        )
        if match:
            return _signature_param_types(match.group("params"))
    return []


def _signature_param_types(signature: str) -> list[tuple[str, str]]:
    pairs: list[tuple[str, str]] = []
    for param in signature.split(","):
        match = re.search(
            r"\b(?P<name>[A-Za-z_$][\w$]*)\s*:\s*(?P<type>[A-Za-z_$][\w$]*)\b",
            param,
        )
        if match:
            pairs.append((match.group("name"), match.group("type")))
    return pairs


def _callback_argument_names(params: list[tuple[str, str]]) -> list[str]:
    return [name for name, _type_name in params]


def _build_callback_fixture_lines(
    *,
    params: list[tuple[str, str]],
    facts: list[str],
    type_shapes: list[dict[str, Any]],
) -> list[str]:
    if not params:
        return []

    event_name = _event_name_from_contract_facts(facts)
    runtime_names = _runtime_arg_names_from_contract_facts(facts)
    shapes_by_name = {shape.get("name"): shape for shape in type_shapes}
    lines: list[str] = []
    for index, (param_name, type_name) in enumerate(params):
        shape = shapes_by_name.get(type_name)
        if shape and shape.get("fields"):
            lines.extend(_fixture_object_lines(param_name, type_name, shape, event_name=event_name, facts=facts))
            continue
        runtime_name = runtime_names[index] if index < len(runtime_names) else param_name
        lines.append(f"const {param_name}: {type_name} = {_sample_primitive_value(param_name, runtime_name, type_name, facts)};")
    return lines


def _fixture_object_lines(
    param_name: str,
    type_name: str,
    shape: dict[str, Any],
    *,
    event_name: str,
    facts: list[str],
) -> list[str]:
    fields = shape.get("fields") or []
    selected = _select_fixture_fields(fields, facts)
    if not selected:
        selected = [field for field in fields if not field.get("optional")][:3]
    if not selected:
        selected = fields[:3]
    lines = [f"const {param_name}: {type_name} = {{"]
    for field in selected:
        key = field["name"]
        value = _sample_field_value(str(key), str(field.get("type") or ""), event_name=event_name)
        rendered_key = key if re.match(r"^[A-Za-z_$][\w$]*$", str(key)) else f'"{key}"'
        lines.append(f"  {rendered_key}: {value},")
    lines.append("};")
    return lines


def _select_fixture_fields(fields: list[dict[str, Any]], facts: list[str]) -> list[dict[str, Any]]:
    selected: list[dict[str, Any]] = []
    fact_text = "\n".join(facts)
    for field in fields:
        name = str(field.get("name") or "")
        if name and (
            name in fact_text
            or name == "message-type"
        ):
            selected.append(field)
    if selected:
        return selected
    return [
        field for field in fields
        if _is_simple_fixture_field(str(field.get("type") or ""))
    ][:3]


def _is_simple_fixture_field(type_text: str) -> bool:
    return any(token in type_text for token in ["string", "boolean", "number"]) or re.match(r"^[A-Za-z_$][\w$]*Plan\b", type_text)


def _sample_primitive_value(param_name: str, runtime_name: str, type_name: str, facts: list[str]) -> str:
    name = f"{param_name} {runtime_name}".lower()
    if type_name == "string":
        if "channel" in name:
            return '"#channel"'
        if "user" in name:
            return '"username"'
        if "message" in name or "msg" in name:
            return '""'
        return '""'
    if type_name == "number":
        return "1"
    if type_name == "boolean":
        return "false"
    return "{}"


def _sample_field_value(field_name: str, type_text: str, *, event_name: str) -> str:
    if field_name == "message-type" and event_name:
        return f'"{event_name}"'
    if field_name == "planName":
        return '"Tier 1"'
    if field_name == "plan" or type_text.endswith("Plan"):
        return '"1000"'
    if "string" in type_text:
        return '""'
    if field_name == "prime" or "boolean" in type_text:
        return "false"
    if "number" in type_text:
        return "1"
    return '""'


def _event_name_from_contract_facts(facts: list[str]) -> str:
    for fact in facts:
        match = re.search(r"\.on\(['\"](?P<event>[^'\"]+)['\"]", fact)
        if match:
            return match.group("event")
        match = re.search(r"source emits `(?P<event>[^`(]+)\(", fact)
        if match:
            return match.group("event")
    return ""


def _runtime_arg_names_from_contract_facts(facts: list[str]) -> list[str]:
    for fact in facts:
        match = re.search(r"source emits `[^`(]+\((?P<params>[^`]*)\)`", fact)
        if match:
            return [part.strip() for part in match.group("params").split(",") if part.strip()]
    return []


def _parse_missing_bun_test_globals(result: str) -> tuple[str | None, set[str]]:
    hits: dict[str, set[str]] = {}
    for match in _MISSING_NAME_RE.finditer(result or ""):
        missing = match.group("name")
        if missing not in _BUN_TEST_GLOBALS:
            continue
        path = _normalize_path(match.group("file"))
        hits.setdefault(path, set()).add(missing)
    if not hits:
        return None, set()
    test_file = sorted(hits, key=lambda path: (-len(hits[path]), path))[0]
    return test_file, hits[test_file]


def _parse_bun_mock_reset_file(result: str) -> str | None:
    match = _BUN_MOCK_RESET_RE.search(result or "")
    return _normalize_path(match.group("file")) if match else None


def _parse_bun_import_time_side_effect_file(
    name: str,
    args: dict,
    result: str,
    runner_facts: Any,
) -> str | None:
    output = result or ""
    source_file = _report_value(runner_facts, "source_file")
    if not source_file or source_file not in output:
        return None
    if "loadAndEvaluateModule" not in output:
        return None
    if "Unhandled error between tests" not in output and "[Reactive test]" not in output:
        return None
    return (
        _test_file_from_tool_result(name, args, output)
        or _report_value(runner_facts, "recommended_test_file")
    )


def _test_file_from_tool_result(name: str, args: dict, result: str) -> str | None:
    if name == "run_command":
        command = args.get("command", "")
        if isinstance(command, str):
            try:
                parts = shlex.split(command)
            except ValueError:
                parts = command.split()
            for part in parts:
                path = _normalize_path(part)
                if _is_test_file_path(path):
                    return path
    if name in {"create_file", "str_replace_editor"}:
        path = _normalize_path(args.get("path"))
        if _is_test_file_path(path):
            return path
    if name == "apply_patch":
        for path in _apply_patch_paths(args):
            if _is_test_file_path(path):
                return path
    for match in _TEST_FILE_RE.finditer(result or ""):
        path = _normalize_path(match.group("file"))
        if _is_test_file_path(path):
            return path
    return None


def _apply_patch_paths(args: dict) -> tuple[str, ...]:
    try:
        return tuple(_normalize_path(path) for path in apply_patch_touched_paths(str(args.get("patch") or "")))
    except ApplyPatchError:
        return ()


def _is_test_file_path(path: str | None) -> bool:
    return bool(path and re.search(r"\.test\.[tj]sx?$", path))


def _is_source_or_test_lookup(name: str, args: dict) -> bool:
    if name in {"read_file", "rg"}:
        return True
    if name != "run_command":
        return False
    command = args.get("command", "")
    if not isinstance(command, str):
        return False
    try:
        parts = shlex.split(command)
    except ValueError:
        parts = command.split()
    if not parts:
        return False
    return PurePosixPath(parts[0]).name in {
        "grep",
        "rg",
        "cat",
        "head",
        "tail",
        "sed",
        "awk",
    }


_plugin = TypeScriptLanguage()
register(_plugin.extensions, _plugin)
