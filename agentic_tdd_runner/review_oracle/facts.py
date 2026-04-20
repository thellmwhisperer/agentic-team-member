"""Deterministic fact extractors for review/oracle cookbook generation."""

from __future__ import annotations

import ast
import os
import re
import warnings
from copy import deepcopy
from pathlib import Path, PurePosixPath
from typing import Any

from agentic_tdd_runner.review_oracle.types import Fact


_REPO_SCAN_SKIP_DIRS = {
    ".git",
    ".tox",
    ".venv",
    "__pycache__",
    "build",
    "dist",
    "env",
    "node_modules",
    "site-packages",
    "venv",
}


def _read_text(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def _parse_module(path: Path) -> ast.Module:
    source = _read_text(path)
    return ast.parse(source, filename=str(path))


def _format_range(path: str, start: int | None, end: int | None) -> str:
    if start is None:
        return path
    if end is None or end == start:
        return f"{path}:{start}"
    return f"{path}:{start}-{end}"


def _compact_source(source: str) -> str:
    return re.sub(r"\s+", " ", source).strip()


def _string_key(node: ast.AST | None) -> str | None:
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return node.value
    return None


def _dict_string_keys(node: ast.AST | None) -> list[str]:
    if not isinstance(node, ast.Dict):
        return []
    keys: list[str] = []
    for key in node.keys:
        string_key = _string_key(key)
        if string_key is not None:
            keys.append(string_key)
    return sorted(dict.fromkeys(keys))


def _dict_value(node: ast.AST | None, key_name: str) -> ast.AST | None:
    if not isinstance(node, ast.Dict):
        return None
    for key, value in zip(node.keys, node.values):
        if _string_key(key) == key_name:
            return value
    return None


def _iter_methods(class_node: ast.ClassDef) -> list[ast.FunctionDef | ast.AsyncFunctionDef]:
    return [
        node for node in class_node.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
    ]


def _iter_module_functions(tree: ast.Module) -> list[ast.FunctionDef | ast.AsyncFunctionDef]:
    return [
        node for node in tree.body
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef))
    ]


def _find_target(
    tree: ast.Module,
    symbol: str,
) -> tuple[ast.ClassDef | None, ast.FunctionDef | ast.AsyncFunctionDef]:
    for node in tree.body:
        if isinstance(node, ast.ClassDef):
            for method in _iter_methods(node):
                if method.name == symbol:
                    return node, method
    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name == symbol:
            return None, node
    raise ValueError(f"Could not find symbol: {symbol}")


def _find_class(tree: ast.Module, name: str) -> ast.ClassDef:
    for node in tree.body:
        if isinstance(node, ast.ClassDef) and node.name == name:
            return node
    raise ValueError(f"Could not find class: {name}")


def _find_method(class_node: ast.ClassDef, name: str) -> ast.FunctionDef | ast.AsyncFunctionDef:
    for method in _iter_methods(class_node):
        if method.name == name:
            return method
    raise ValueError(f"Could not find method: {class_node.name}.{name}")


def _iter_non_nested_nodes(root: ast.AST) -> list[ast.AST]:
    """Yield a function body without descending into nested defs/classes/lambdas.

    This is intentionally narrower than the older method-level walkers below.
    We only use it for repo-scoped upstream return facts, where nested local
    definitions are not part of the top-level function's own return surface.
    """
    nodes: list[ast.AST] = []
    stack = list(reversed(list(ast.iter_child_nodes(root))))
    while stack:
        node = stack.pop()
        nodes.append(node)
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef, ast.Lambda)):
            continue
        stack.extend(reversed(list(ast.iter_child_nodes(node))))
    return nodes


def _signature_from_node(source_text: str, node: ast.FunctionDef | ast.AsyncFunctionDef) -> tuple[str, str]:
    del source_text
    args = deepcopy(node.args)
    if args.args and args.args[0].arg == "self":
        args.args = args.args[1:]
    params = ast.unparse(args)
    full_params = ast.unparse(node.args)
    with_self = f"{node.name}({full_params})"
    without_self = f"{node.name}({params})"
    return _compact_source(with_self), _compact_source(without_self)


def _returns_target_call(node: ast.AST, target_symbol: str) -> bool:
    for child in ast.walk(node):
        if not isinstance(child, ast.Return) or child.value is None:
            continue
        value = child.value
        if isinstance(value, ast.Tuple):
            elements = list(value.elts)
        else:
            elements = [value]
        for element in elements:
            if isinstance(element, ast.Call) and isinstance(element.func, ast.Attribute):
                if (
                    isinstance(element.func.value, ast.Name)
                    and element.func.value.id == "self"
                    and element.func.attr == target_symbol
                ):
                    return True
    return False


def _dispatch_key_from_if(node: ast.If) -> str | None:
    test = node.test
    if not isinstance(test, ast.Compare) or len(test.ops) != 1 or len(test.comparators) != 1:
        return None
    if not isinstance(test.ops[0], ast.Eq):
        return None

    left, right = test.left, test.comparators[0]
    if isinstance(left, ast.Name) and isinstance(right, ast.Constant) and isinstance(right.value, str):
        return right.value
    if isinstance(right, ast.Name) and isinstance(left, ast.Constant) and isinstance(left.value, str):
        return left.value
    return None


def _extract_dispatch_branch(
    class_node: ast.ClassDef,
    target_symbol: str,
    source_path: str,
) -> tuple[str | None, str | None]:
    for method in _iter_methods(class_node):
        for node in ast.walk(method):
            if not isinstance(node, ast.If):
                continue
            if not _returns_target_call(node, target_symbol):
                continue
            dispatch_key = _dispatch_key_from_if(node)
            if dispatch_key is None:
                continue
            return dispatch_key, _format_range(source_path, node.lineno, node.end_lineno)
    return None, None


def _extract_wrapper_shapes(
    method_node: ast.FunctionDef | ast.AsyncFunctionDef,
    source_text: str,
) -> tuple[str | None, str | None, list[str], list[str]]:
    success_shape = None
    error_shape = None
    success_keys: list[str] = []
    error_keys: list[str] = []
    for node in ast.walk(method_node):
        if not isinstance(node, ast.Return) or node.value is None:
            continue
        if not isinstance(node.value, ast.Dict):
            continue
        keys = _dict_string_keys(node.value)
        shape = _compact_source(ast.get_source_segment(source_text, node.value) or "")
        is_error_value = None
        for key, value in zip(node.value.keys, node.value.values):
            if _string_key(key) == "isError" and isinstance(value, ast.Constant):
                is_error_value = value.value
                break
        if is_error_value is True and error_shape is None:
            error_shape = shape
            error_keys = keys
            continue
        if "content" in keys and success_shape is None:
            success_shape = shape
            success_keys = keys
    return success_shape, error_shape, success_keys, error_keys


def _derive_unwrap_pattern(
    method_node: ast.FunctionDef | ast.AsyncFunctionDef,
) -> str | None:
    for node in ast.walk(method_node):
        if not isinstance(node, ast.Return) or not isinstance(node.value, ast.Dict):
            continue
        is_error_value = _dict_value(node.value, "isError")
        if isinstance(is_error_value, ast.Constant) and is_error_value.value is True:
            continue

        content_value = _dict_value(node.value, "content")
        if not isinstance(content_value, ast.List) or not content_value.elts:
            continue
        first_item = content_value.elts[0]
        text_value = _dict_value(first_item, "text")
        if not isinstance(text_value, ast.Call):
            return None
        if len(text_value.args) != 1 or text_value.keywords:
            return None
        if not isinstance(text_value.func, ast.Attribute):
            return None
        if not isinstance(text_value.func.value, ast.Name):
            return None
        if text_value.func.value.id != "json" or text_value.func.attr != "dumps":
            return None
        return 'body = json.loads(result["content"][0]["text"])'
    return None


def _helper_added_keys(
    helper_node: ast.FunctionDef | ast.AsyncFunctionDef,
) -> list[str]:
    param_name = None
    for arg in helper_node.args.args:
        if arg.arg != "self":
            param_name = arg.arg
            break
    if param_name is None:
        return []

    keys: list[str] = []
    for node in ast.walk(helper_node):
        if not isinstance(node, ast.Assign):
            continue
        for target in node.targets:
            if not isinstance(target, ast.Subscript):
                continue
            if not isinstance(target.value, ast.Name) or target.value.id != param_name:
                continue
            key = _string_key(target.slice)
            if key is not None:
                keys.append(key)
    return sorted(dict.fromkeys(keys))


def _collect_body_return_sets(
    class_node: ast.ClassDef | None,
    target_node: ast.FunctionDef | ast.AsyncFunctionDef,
    source_text: str,
) -> tuple[list[list[str]], dict[str, list[str]]]:
    body_return_key_sets: list[list[str]] = []
    helper_key_map: dict[str, list[str]] = {}
    local_dict_assignments: dict[str, list[str]] = {}

    for node in ast.walk(target_node):
        if not isinstance(node, ast.Assign) or not isinstance(node.value, ast.Dict):
            continue
        keys = _dict_string_keys(node.value)
        if not keys:
            continue
        for target in node.targets:
            if isinstance(target, ast.Name):
                local_dict_assignments[target.id] = keys

    for node in ast.walk(target_node):
        if not isinstance(node, ast.Return) or node.value is None:
            continue
        if isinstance(node.value, ast.Dict):
            keys = _dict_string_keys(node.value)
            if keys:
                body_return_key_sets.append(keys)
            continue
        if not isinstance(node.value, ast.Call):
            continue
        if not isinstance(node.value.func, ast.Attribute):
            continue
        if not isinstance(node.value.func.value, ast.Name) or node.value.func.value.id != "self":
            continue
        helper_name = node.value.func.attr
        if not node.value.args:
            continue
        wrapped_arg = node.value.args[0]
        wrapped_keys = _dict_string_keys(wrapped_arg)
        if not wrapped_keys and isinstance(wrapped_arg, ast.Name):
            wrapped_keys = local_dict_assignments.get(wrapped_arg.id, [])
        if wrapped_keys:
            body_return_key_sets.append(wrapped_keys)
        if class_node is None:
            continue
        try:
            helper_node = _find_method(class_node, helper_name)
        except ValueError:
            continue
        helper_key_map[helper_name] = _helper_added_keys(helper_node)

    unique_key_sets: list[list[str]] = []
    seen = set()
    for keys in body_return_key_sets:
        key_tuple = tuple(keys)
        if key_tuple in seen:
            continue
        seen.add(key_tuple)
        unique_key_sets.append(keys)
    unique_key_sets.sort(key=lambda keys: tuple(keys))
    return unique_key_sets, helper_key_map


def _collect_module_fixtures(tree: ast.Module, source_path: str) -> list[dict[str, Any]]:
    fixtures: list[dict[str, Any]] = []
    for node in tree.body:
        if not isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            continue
        is_fixture = False
        for decorator in node.decorator_list:
            if isinstance(decorator, ast.Attribute) and isinstance(decorator.value, ast.Name):
                if decorator.value.id == "pytest" and decorator.attr == "fixture":
                    is_fixture = True
                    break
            if isinstance(decorator, ast.Call):
                func = decorator.func
                if isinstance(func, ast.Attribute) and isinstance(func.value, ast.Name):
                    if func.value.id == "pytest" and func.attr == "fixture":
                        is_fixture = True
                        break
        if is_fixture:
            fixtures.append({
                "name": node.name,
                "range": _format_range(source_path, node.lineno, node.end_lineno),
            })
    return fixtures


def _collect_helper_classes(tree: ast.Module, source_path: str) -> list[dict[str, Any]]:
    helpers: list[dict[str, Any]] = []
    for node in tree.body:
        if isinstance(node, ast.ClassDef) and node.name.startswith("Mock"):
            helpers.append({
                "name": node.name,
                "range": _format_range(source_path, node.lineno, node.end_lineno),
            })
    return helpers


def _collect_helper_methods(tree: ast.Module, source_path: str) -> list[dict[str, Any]]:
    helpers: list[dict[str, Any]] = []
    for node in tree.body:
        if not isinstance(node, ast.ClassDef) or not node.name.startswith("Test"):
            continue
        for method in _iter_methods(node):
            if method.name.startswith("_"):
                helpers.append({
                    "class": node.name,
                    "name": method.name,
                    "range": _format_range(source_path, method.lineno, method.end_lineno),
                })
    return helpers


def _collect_dict_return_key_sets(
    node: ast.FunctionDef | ast.AsyncFunctionDef,
) -> list[list[str]]:
    # Upstream return facts model the top-level function body only; nested
    # defs/classes/lambdas are intentionally excluded from this surface.
    key_sets: list[list[str]] = []
    local_dict_assignments: dict[str, list[str]] = {}
    for child in _iter_non_nested_nodes(node):
        if not isinstance(child, ast.Assign) or not isinstance(child.value, ast.Dict):
            continue
        keys = _dict_string_keys(child.value)
        if not keys:
            continue
        for target in child.targets:
            if isinstance(target, ast.Name):
                local_dict_assignments[target.id] = keys

    for child in _iter_non_nested_nodes(node):
        if not isinstance(child, ast.Return) or child.value is None:
            continue
        keys: list[str] = []
        if isinstance(child.value, ast.Dict):
            keys = _dict_string_keys(child.value)
        elif isinstance(child.value, ast.Name):
            keys = local_dict_assignments.get(child.value.id, [])
        if keys and keys not in key_sets:
            key_sets.append(keys)
    key_sets.sort(key=lambda keys: tuple(keys))
    return key_sets


def _collect_patch_imports(tree: ast.Module) -> tuple[set[str], set[str]]:
    direct_patch_names: set[str] = set()
    patch_module_aliases: set[str] = set()
    for node in tree.body:
        if isinstance(node, ast.ImportFrom):
            module = node.module or ""
            for alias in node.names:
                local_name = alias.asname or alias.name
                if module in {"unittest.mock", "mock"} and alias.name == "patch":
                    direct_patch_names.add(local_name)
                if module == "unittest" and alias.name == "mock":
                    patch_module_aliases.add(local_name)
        if isinstance(node, ast.Import):
            for alias in node.names:
                local_name = alias.asname or alias.name.split(".")[-1]
                if alias.name in {"unittest.mock", "mock"}:
                    patch_module_aliases.add(local_name)
                if alias.name == "unittest":
                    patch_module_aliases.add(local_name)
    return direct_patch_names, patch_module_aliases


def _attribute_chain(node: ast.AST) -> list[str]:
    parts: list[str] = []
    current = node
    while isinstance(current, ast.Attribute):
        parts.append(current.attr)
        current = current.value
    if isinstance(current, ast.Name):
        parts.append(current.id)
    return list(reversed(parts))


def _patch_target_for_call(
    call: ast.Call,
    direct_patch_names: set[str],
    patch_module_aliases: set[str],
) -> tuple[str, str] | None:
    func = call.func
    alias = None
    if isinstance(func, ast.Name) and func.id in direct_patch_names:
        alias = func.id
    elif isinstance(func, ast.Attribute):
        chain = _attribute_chain(func)
        if len(chain) >= 2 and chain[-1] == "patch":
            if chain[0] in patch_module_aliases:
                alias = ".".join(chain)
            if chain[:3] == ["unittest", "mock", "patch"]:
                alias = ".".join(chain)
    if alias is None or not call.args:
        return None
    first_arg = call.args[0]
    if not isinstance(first_arg, ast.Constant) or not isinstance(first_arg.value, str):
        return None
    return alias, first_arg.value


def _iter_repo_python_files(root: Path) -> list[Path]:
    python_files: list[Path] = []
    for current_root, dirnames, filenames in os.walk(root):
        dirnames[:] = [
            dirname for dirname in dirnames
            if dirname not in _REPO_SCAN_SKIP_DIRS
        ]
        current_path = Path(current_root)
        for filename in filenames:
            if filename.endswith(".py"):
                python_files.append(current_path / filename)
    return sorted(python_files)


def _try_parse_module(path: Path, relative_path: str) -> ast.Module | None:
    try:
        return _parse_module(path)
    except (OSError, SyntaxError, UnicodeDecodeError) as exc:
        warnings.warn(
            f"Skipping unreadable python file during review_oracle scan: {relative_path} ({exc})",
            stacklevel=2,
        )
        return None


def _collect_patch_sites(
    tree: ast.Module,
    source_path: str,
) -> tuple[list[dict[str, Any]], set[str]]:
    direct_patch_names, patch_module_aliases = _collect_patch_imports(tree)
    patch_sites: list[dict[str, Any]] = []
    observed_aliases: set[str] = set()
    for node in ast.walk(tree):
        if not isinstance(node, ast.Call):
            continue
        patch_target = _patch_target_for_call(node, direct_patch_names, patch_module_aliases)
        if patch_target is None:
            continue
        alias, target = patch_target
        observed_aliases.add(alias)
        patch_sites.append({
            "target": target,
            "alias": alias,
            "range": _format_range(source_path, node.lineno, node.end_lineno),
        })
    patch_sites.sort(key=lambda site: (site["range"], site["target"], site["alias"]))
    return patch_sites, observed_aliases


def _table_columns(body: str) -> list[str]:
    columns: list[str] = []
    for raw_line in body.splitlines():
        line = raw_line.strip().rstrip(",")
        if not line:
            continue
        upper = line.upper()
        if upper.startswith(("PRIMARY KEY", "FOREIGN KEY", "UNIQUE", "CONSTRAINT", "CHECK")):
            continue
        match = re.match(r'"?(?P<name>[A-Za-z_]\w*)"?', line)
        if match:
            columns.append(match.group("name"))
    return columns


def extract_target_identity(
    project_root: str,
    source_path: str,
    symbol: str,
) -> Fact:
    path = Path(project_root) / source_path
    source_text = _read_text(path)
    tree = _parse_module(path)
    owner_class, target_node = _find_target(tree, symbol)
    signature, _public_signature = _signature_from_node(source_text, target_node)
    visibility_prefix = "private" if symbol.startswith("_") else "public"
    kind = "method" if owner_class else "function"

    inputs_used = [_format_range(source_path, target_node.lineno, target_node.end_lineno)]
    if owner_class is not None:
        inputs_used.insert(0, _format_range(source_path, owner_class.lineno, owner_class.lineno))

    return Fact(
        name="target_identity",
        value={
            "owner_class": owner_class.name if owner_class else None,
            "symbol": symbol,
            "signature": signature,
            "source_range": _format_range(source_path, target_node.lineno, target_node.end_lineno),
            "visibility": f"{visibility_prefix} {kind}",
        },
        derivation_rule=(
            f"parse {source_path}, find the enclosing class if present, "
            f"then read the exact line span and signature for {symbol}"
        ),
        inputs_used=tuple(inputs_used),
        confidence_class="ast_exact",
    )


def extract_invocation_surface(
    project_root: str,
    source_path: str,
    owner_class_name: str,
    target_symbol: str,
) -> Fact:
    path = Path(project_root) / source_path
    source_text = _read_text(path)
    tree = _parse_module(path)
    class_node = _find_class(tree, owner_class_name)
    try:
        call_node = _find_method(class_node, "call")
    except ValueError:
        call_node = None

    if call_node is not None:
        _call_signature, public_signature = _signature_from_node(source_text, call_node)
        public_entrypoint = f"{owner_class_name}.{public_signature}"
        success_shape, error_shape, _, _ = _extract_wrapper_shapes(call_node, source_text)
        unwrap_pattern = _derive_unwrap_pattern(call_node)
        inputs_used = [_format_range(source_path, call_node.lineno, call_node.end_lineno)]
    else:
        public_entrypoint = None
        success_shape = None
        error_shape = None
        unwrap_pattern = None
        inputs_used = []

    dispatch_key, dispatch_branch = _extract_dispatch_branch(class_node, target_symbol, source_path)
    if dispatch_branch:
        inputs_used.append(dispatch_branch)

    return Fact(
        name="invocation_surface",
        value={
            "public_entrypoint": public_entrypoint,
            "dispatch_key": dispatch_key,
            "dispatch_branch": dispatch_branch,
            "wrapper_success_shape": success_shape,
            "wrapper_error_shape": error_shape,
            "unwrap_pattern": unwrap_pattern,
        },
        derivation_rule=(
            f"locate {owner_class_name}.call plus the dispatch branch that returns "
            f"{target_symbol}(...) and read the literal wrapper return dicts"
        ),
        inputs_used=tuple(inputs_used),
        confidence_class="ast_exact",
    )


def extract_result_surface(
    project_root: str,
    source_path: str,
    symbol: str,
) -> Fact:
    path = Path(project_root) / source_path
    source_text = _read_text(path)
    tree = _parse_module(path)
    owner_class, target_node = _find_target(tree, symbol)

    wrapper_success_keys: list[str] = []
    wrapper_error_keys: list[str] = []
    if owner_class is not None:
        try:
            call_node = _find_method(owner_class, "call")
        except ValueError:
            call_node = None
        if call_node is not None:
            _, _, wrapper_success_keys, wrapper_error_keys = _extract_wrapper_shapes(call_node, source_text)

    body_return_key_sets, helper_key_map = _collect_body_return_sets(owner_class, target_node, source_text)

    inputs_used = [_format_range(source_path, target_node.lineno, target_node.end_lineno)]
    if owner_class is not None:
        for helper_name in helper_key_map:
            helper_node = _find_method(owner_class, helper_name)
            inputs_used.append(_format_range(source_path, helper_node.lineno, helper_node.end_lineno))
        try:
            call_node = _find_method(owner_class, "call")
        except ValueError:
            call_node = None
        if call_node is not None:
            inputs_used.append(_format_range(source_path, call_node.lineno, call_node.end_lineno))

    return Fact(
        name="result_surface",
        value={
            "wrapper_success_keys": wrapper_success_keys,
            "wrapper_error_keys": wrapper_error_keys,
            "body_keys_always_added_by_helper": helper_key_map,
            "body_return_key_sets": body_return_key_sets,
        },
        derivation_rule=(
            f"read literal dict return sites in {symbol} and inspect helper methods that "
            "mutate the returned payload before returning it"
        ),
        inputs_used=tuple(inputs_used),
        confidence_class="ast_exact",
    )


def extract_pytest_surface(
    project_root: str,
    test_path: str,
) -> Fact:
    path = Path(project_root) / test_path
    tree = _parse_module(path)

    return Fact(
        name="pytest_surface",
        value={
            "module_test_file": PurePosixPath(test_path).as_posix(),
            "module_fixtures": _collect_module_fixtures(tree, test_path),
            "helper_classes": _collect_helper_classes(tree, test_path),
            "helper_methods": _collect_helper_methods(tree, test_path),
        },
        derivation_rule="scan module-level pytest fixtures plus helper classes and helper methods in sibling tests",
        inputs_used=(test_path,),
        confidence_class="ast_exact",
    )


def extract_patch_semantics(
    project_root: str,
    test_path: str,
) -> Fact:
    root = Path(project_root)
    test_file = root / test_path
    local_tree = _try_parse_module(test_file, test_path)
    if local_tree is not None:
        local_patch_targets, observed_aliases = _collect_patch_sites(local_tree, test_path)
    else:
        local_patch_targets, observed_aliases = [], set()

    repo_counts: dict[str, int] = {}
    contributing_paths: set[str] = set()
    for path in _iter_repo_python_files(root):
        relative_path = path.relative_to(root).as_posix()
        tree = _try_parse_module(path, relative_path)
        if tree is None:
            continue
        patch_sites, _aliases = _collect_patch_sites(tree, relative_path)
        if not patch_sites:
            continue
        contributing_paths.add(relative_path)
        for site in patch_sites:
            repo_counts[site["target"]] = repo_counts.get(site["target"], 0) + 1

    total_patch_target_counts = [
        {"target": target, "count": count}
        for target, count in sorted(repo_counts.items())
    ]
    inputs_used = [site["range"] for site in local_patch_targets] or [test_path]
    for relative_path in sorted(contributing_paths):
        if relative_path != test_path:
            inputs_used.append(relative_path)

    return Fact(
        name="patch_semantics",
        value={
            "module_test_file": PurePosixPath(test_path).as_posix(),
            "observed_patch_aliases": sorted(observed_aliases),
            "local_patch_targets": local_patch_targets,
            "total_patch_target_counts": total_patch_target_counts,
        },
        derivation_rule=(
            f"scan {test_path} for literal string targets passed to patch(...), "
            "then count those same patch(target, ...) forms across successfully "
            "parsed python files under the repo root, including the local test file"
        ),
        inputs_used=tuple(inputs_used),
        confidence_class="repo_counted",
    )


def extract_schema_surface(
    project_root: str,
    schema_path: str,
) -> Fact:
    path = Path(project_root) / schema_path
    source_text = _read_text(path)
    tables: list[dict[str, Any]] = []
    for match in re.finditer(
        r"CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?P<name>[A-Za-z_]\w*)\s*\((?P<body>.*?)\)\s*;",
        source_text,
        flags=re.IGNORECASE | re.DOTALL,
    ):
        start = source_text.count("\n", 0, match.start()) + 1
        end = source_text.count("\n", 0, match.end()) + 1
        tables.append({
            "name": match.group("name"),
            "columns": _table_columns(match.group("body")),
            "range": _format_range(schema_path, start, end),
        })

    seed_inserts: list[dict[str, Any]] = []
    for match in re.finditer(
        r"INSERT\s+INTO\s+(?P<table>[A-Za-z_]\w*)\s*\((?P<columns>.*?)\)\s*.*?;",
        source_text,
        flags=re.IGNORECASE | re.DOTALL,
    ):
        start = source_text.count("\n", 0, match.start()) + 1
        end = source_text.count("\n", 0, match.end()) + 1
        columns = [part.strip().strip('"') for part in match.group("columns").split(",") if part.strip()]
        seed_inserts.append({
            "table": match.group("table"),
            "columns": columns,
            "range": _format_range(schema_path, start, end),
        })

    inputs_used = [entry["range"] for entry in tables]
    inputs_used.extend(entry["range"] for entry in seed_inserts)
    if not inputs_used:
        inputs_used = [schema_path]

    return Fact(
        name="schema_surface",
        value={
            "schema_file": PurePosixPath(schema_path).as_posix(),
            "tables": tables,
            "seed_inserts": seed_inserts,
        },
        derivation_rule="parse CREATE TABLE and INSERT INTO statements from the schema file to recover table and seed shapes",
        inputs_used=tuple(inputs_used),
        confidence_class="text_exact",
    )


def extract_upstream_return_surface(
    project_root: str,
    upstream_path: str,
) -> Fact:
    path = Path(project_root) / upstream_path
    tree = _parse_module(path)
    functions: list[dict[str, Any]] = []
    for function_node in _iter_module_functions(tree):
        return_key_sets = _collect_dict_return_key_sets(function_node)
        if not return_key_sets:
            continue
        functions.append({
            "name": function_node.name,
            "range": _format_range(upstream_path, function_node.lineno, function_node.end_lineno),
            "return_key_sets": return_key_sets,
        })

    inputs_used = [function["range"] for function in functions] or [upstream_path]
    return Fact(
        name="upstream_return_surface",
        value={
            "module_source_file": PurePosixPath(upstream_path).as_posix(),
            "functions": functions,
        },
        derivation_rule="scan top-level upstream functions and recover literal dict return shapes from their AST",
        inputs_used=tuple(inputs_used),
        confidence_class="ast_exact",
    )


def collect_exact_facts(
    project_root: str,
    source_path: str,
    symbol: str,
    *,
    test_path: str | None = None,
) -> list[Fact]:
    target_identity = extract_target_identity(project_root, source_path, symbol)
    facts = [target_identity]

    owner_class_name = target_identity.value.get("owner_class")
    if owner_class_name:
        facts.append(
            extract_invocation_surface(project_root, source_path, owner_class_name, symbol),
        )
    facts.append(extract_result_surface(project_root, source_path, symbol))

    resolved_test_path = test_path
    if resolved_test_path is None:
        source = PurePosixPath(source_path)
        resolved_test_path = source.with_name(f"test_{source.stem}.py").as_posix()
    candidate = Path(project_root) / resolved_test_path
    if candidate.is_file():
        facts.append(extract_pytest_surface(project_root, resolved_test_path))

    return facts


def collect_repo_facts(
    project_root: str,
    *,
    test_path: str | None = None,
    schema_path: str | None = None,
    upstream_path: str | None = None,
) -> list[Fact]:
    facts: list[Fact] = []

    if test_path is not None and (Path(project_root) / test_path).is_file():
        facts.append(extract_patch_semantics(project_root, test_path))
    if schema_path is not None and (Path(project_root) / schema_path).is_file():
        facts.append(extract_schema_surface(project_root, schema_path))
    if upstream_path is not None and (Path(project_root) / upstream_path).is_file():
        facts.append(extract_upstream_return_surface(project_root, upstream_path))

    return facts
