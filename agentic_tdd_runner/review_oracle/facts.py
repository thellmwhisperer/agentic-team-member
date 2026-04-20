"""AST-exact fact extractors for review/oracle cookbook generation."""

from __future__ import annotations

import ast
import re
from pathlib import Path, PurePosixPath
from typing import Any

from agentic_tdd_runner.review_oracle.types import Fact


def _read_text(path: Path) -> str:
    return path.read_text()


def _parse_module(path: Path) -> ast.Module:
    return ast.parse(_read_text(path))


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


def _iter_methods(class_node: ast.ClassDef) -> list[ast.FunctionDef | ast.AsyncFunctionDef]:
    return [
        node for node in class_node.body
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


def _signature_from_node(source_text: str, node: ast.FunctionDef | ast.AsyncFunctionDef) -> str:
    segment = ast.get_source_segment(source_text, node) or ""
    match = re.search(
        r"(?:async\s+def|def)\s+\w+\((?:.|\n)*?\)\s*(?:->\s*[^:]+)?",
        segment,
    )
    if match:
        header = match.group(0)
        header = re.sub(r"^(?:async\s+def|def)\s+", "", header)
        return _compact_source(header)
    return f"{node.name}(...)"


def _signature_without_self(signature: str) -> str:
    match = re.match(r"^(?P<name>\w+)\((?P<params>.*)\)$", signature)
    if not match:
        return signature
    params = [part.strip() for part in match.group("params").split(",") if part.strip()]
    if params and params[0] == "self":
        params = params[1:]
    return f"{match.group('name')}({', '.join(params)})"


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
                if element.func.attr == target_symbol:
                    return True
    return False


def _dispatch_key_from_if(node: ast.If) -> str | None:
    test = node.test
    if not isinstance(test, ast.Compare) or len(test.ops) != 1 or len(test.comparators) != 1:
        return None
    if not isinstance(test.ops[0], ast.Eq):
        return None

    left, right = test.left, test.comparators[0]
    if isinstance(left, ast.Name) and left.id == "name" and isinstance(right, ast.Constant) and isinstance(right.value, str):
        return right.value
    if isinstance(right, ast.Name) and right.id == "name" and isinstance(left, ast.Constant) and isinstance(left.value, str):
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
        if "isError" in keys and error_shape is None:
            error_shape = shape
            error_keys = keys
            continue
        if "content" in keys and success_shape is None:
            success_shape = shape
            success_keys = keys
    return success_shape, error_shape, success_keys, error_keys


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


def extract_target_identity(
    project_root: str,
    source_path: str,
    symbol: str,
) -> Fact:
    path = Path(project_root) / source_path
    source_text = _read_text(path)
    tree = _parse_module(path)
    owner_class, target_node = _find_target(tree, symbol)
    signature = _signature_from_node(source_text, target_node)
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
    call_node = _find_method(class_node, "call")
    call_signature = _signature_from_node(source_text, call_node)
    public_entrypoint = f"{owner_class_name}.{_signature_without_self(call_signature)}"
    dispatch_key, dispatch_branch = _extract_dispatch_branch(class_node, target_symbol, source_path)
    success_shape, error_shape, _, _ = _extract_wrapper_shapes(call_node, source_text)

    inputs_used = [
        _format_range(source_path, call_node.lineno, call_node.end_lineno),
    ]
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
            "unwrap_pattern": 'body = json.loads(result["content"][0]["text"])',
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
