"""Export deterministic run artifacts without duplicating large payloads."""

from __future__ import annotations

import hashlib
import json
from dataclasses import asdict, is_dataclass
from pathlib import Path
from typing import Any


def export_harness_artifacts(
    output_dir: str | Path,
    *,
    run_context: Any,
    workdir: str,
    log_path: str | None = None,
) -> dict:
    """Write canonical and rendered run artifacts.

    Canonical JSON files own structured facts. Rendered Markdown files are
    human-facing views. The manifest records references and hashes only.
    """
    root = Path(output_dir)
    canonical = root / "canonical"
    rendered = root / "rendered"
    canonical.mkdir(parents=True, exist_ok=True)
    rendered.mkdir(parents=True, exist_ok=True)

    files: dict[str, dict] = {}

    issue_contract = getattr(run_context, "issue_contract", None)
    if issue_contract is not None:
        issue_payload = (
            issue_contract.to_log_dict()
            if hasattr(issue_contract, "to_log_dict")
            else _jsonable(issue_contract)
        )
        _write_json(canonical / "issue_contract.json", issue_payload, root, files)

    episode = getattr(run_context, "episode", None) or {}
    runner_facts = episode.get("runner_facts") if isinstance(episode, dict) else None
    if runner_facts is not None:
        runner_payload = (
            runner_facts.to_log_dict()
            if hasattr(runner_facts, "to_log_dict")
            else _jsonable(runner_facts)
        )
        _write_json(canonical / "runner_facts.json", runner_payload, root, files)

    episode_payload = _canonical_episode_payload(episode, has_runner_facts=runner_facts is not None)
    if episode_payload:
        _write_json(canonical / "episode.json", episode_payload, root, files)

    cookbook_text = _episode_text(episode, "cookbook_text")
    if cookbook_text:
        _write_text(rendered / "cookbook.md", cookbook_text, root, files)

    runner_facts_text = _episode_text(episode, "runner_facts_text")
    if runner_facts_text:
        _write_text(rendered / "runner_facts.md", runner_facts_text, root, files)

    system_prompt = _message_content(run_context, "system")
    if system_prompt:
        _write_text(rendered / "system_prompt.md", system_prompt, root, files)

    user_prompt = _message_content(run_context, "user")
    if user_prompt:
        _write_text(rendered / "user_prompt.md", user_prompt, root, files)

    manifest = {
        "schema_version": 1,
        "workdir": workdir,
        "log_path": log_path,
        "source_path": getattr(run_context, "source_path", None),
        "symbol": getattr(run_context, "symbol", None),
        "files": dict(sorted(files.items())),
    }
    _write_json(root / "manifest.json", manifest, root, files=None)
    return manifest


def _canonical_episode_payload(episode: Any, *, has_runner_facts: bool) -> dict:
    if not isinstance(episode, dict):
        return {}
    payload = {
        key: _jsonable(value)
        for key, value in episode.items()
        if key not in {
            "cookbook_text",
            "mocks_text",
            "runner_facts",
            "runner_facts_text",
        }
    }
    _dedupe_conditional_source_edits(payload)
    if "cookbook_text" in episode:
        payload["cookbook_ref"] = "rendered/cookbook.md"
    if has_runner_facts:
        payload["runner_facts_ref"] = "canonical/runner_facts.json"
    if "runner_facts_text" in episode:
        payload["runner_facts_text_ref"] = "rendered/runner_facts.md"
    return payload


def _dedupe_conditional_source_edits(payload: dict) -> None:
    pre_test = payload.get("pre_test_source_edits")
    conditional = payload.get("conditional_source_edits")
    if not isinstance(pre_test, list) or not isinstance(conditional, list):
        return
    pre_keys = {json.dumps(edit, sort_keys=True, ensure_ascii=False) for edit in pre_test}
    unique_conditional = [
        edit
        for edit in conditional
        if json.dumps(edit, sort_keys=True, ensure_ascii=False) not in pre_keys
    ]
    if len(unique_conditional) != len(conditional):
        payload["conditional_source_edits_deduped_from"] = "pre_test_source_edits"
    if unique_conditional:
        payload["conditional_source_edits"] = unique_conditional
    else:
        payload.pop("conditional_source_edits", None)


def _episode_text(episode: Any, key: str) -> str:
    if not isinstance(episode, dict):
        return ""
    value = episode.get(key)
    return value if isinstance(value, str) else ""


def _message_content(run_context: Any, role: str) -> str:
    messages = getattr(run_context, "messages", []) or []
    for message in messages:
        if isinstance(message, dict) and message.get("role") == role:
            content = message.get("content")
            return content if isinstance(content, str) else ""
    return ""


def _write_json(path: Path, payload: Any, root: Path, files: dict[str, dict] | None) -> None:
    path.write_text(json.dumps(_jsonable(payload), indent=2, ensure_ascii=False) + "\n")
    if files is not None:
        _record_file(path, root, files)


def _write_text(path: Path, content: str, root: Path, files: dict[str, dict]) -> None:
    path.write_text(content)
    _record_file(path, root, files)


def _record_file(path: Path, root: Path, files: dict[str, dict]) -> None:
    data = path.read_bytes()
    rel = path.relative_to(root).as_posix()
    files[rel] = {
        "path": rel,
        "bytes": len(data),
        "sha256": hashlib.sha256(data).hexdigest(),
    }


def _jsonable(value: Any) -> Any:
    if hasattr(value, "to_log_dict"):
        return value.to_log_dict()
    if is_dataclass(value):
        return _jsonable(asdict(value))
    if isinstance(value, dict):
        return {str(key): _jsonable(inner) for key, inner in value.items()}
    if isinstance(value, (list, tuple)):
        return [_jsonable(inner) for inner in value]
    if isinstance(value, set):
        return sorted(_jsonable(inner) for inner in value)
    return value
