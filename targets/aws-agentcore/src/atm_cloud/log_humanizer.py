from __future__ import annotations

import argparse
import json
import re
import sys
from typing import Iterable


CLOUDWATCH_PREFIX_RE = re.compile(r"^\d{4}-\d{2}-\d{2}T\S+\s+\S+\s+(?P<message>.*)$")
STEP_REQUEST_RE = re.compile(r"^>>> Step (?P<step>\d+)/(?P<max>\d+) .*$")
STEP_RESULT_RE = re.compile(
    r"^--- Step (?P<step>\d+) \| (?P<elapsed>[\d.]+)s \| "
    r"(?P<prompt>\d+|\?)(?:->|→)(?P<completion>\d+|\?) tok \| "
    r"(?P<tok_s>[\d.]+) tok/s \| finish=(?P<finish>[^ ]+) ---$"
)
TOOL_RE = re.compile(r"^\[TOOL\]\s+(?P<name>[A-Za-z0-9_]+)\((?P<args>.*)\)$")
RESULT_RE = re.compile(r"^\[RESULT\]\s+\((?P<chars>\d+) chars, (?P<elapsed>[\d.]+)s\)$")


def strip_cloudwatch_prefix(line: str) -> str:
    line = line.rstrip("\n")
    match = CLOUDWATCH_PREFIX_RE.match(line)
    if not match:
        return line
    return match.group("message")


def format_message(message: str) -> list[str]:
    message = strip_cloudwatch_prefix(message).strip()
    if not message:
        return []

    agentcore = _format_agentcore_json(message)
    if agentcore:
        return [agentcore]

    request = STEP_REQUEST_RE.match(message)
    if request:
        return [f"[step {int(request.group('step')):02d}/{request.group('max')}] requesting model"]

    step = STEP_RESULT_RE.match(message)
    if step:
        return [
            "[step {step:02d}] {elapsed}s  {prompt}->{completion} tok  "
            "{tok_s} tok/s  finish={finish}".format(
                step=int(step.group("step")),
                elapsed=step.group("elapsed"),
                prompt=step.group("prompt"),
                completion=step.group("completion"),
                tok_s=step.group("tok_s"),
                finish=step.group("finish"),
            )
        ]

    tool = TOOL_RE.match(message)
    if tool:
        return [f"  tool: {_summarize_tool(tool.group('name'), tool.group('args'))}"]

    result = RESULT_RE.match(message)
    if result:
        return [f"  result: {result.group('chars')} chars in {result.group('elapsed')}s"]

    if message.startswith("PERMISSION GRANTED:"):
        return [f"  permission granted: {message.removeprefix('PERMISSION GRANTED:').strip()}"]
    if message.startswith("PERMISSION DENIED:"):
        return [f"  permission denied: {message.removeprefix('PERMISSION DENIED:').strip()}"]
    if message.startswith("PERMISSION REQUIRED:"):
        return [f"  permission required: {message.removeprefix('PERMISSION REQUIRED:').strip()}"]
    if message.startswith("TARGET CHALLENGE ACCEPTED:"):
        return [f"  target challenge accepted: {message.removeprefix('TARGET CHALLENGE ACCEPTED:').strip()}"]
    if message.startswith("TARGET CHALLENGE REQUIRED:"):
        return [f"  target challenge required: {message.removeprefix('TARGET CHALLENGE REQUIRED:').strip()}"]
    if message.startswith("TARGET CHALLENGE DENIED:"):
        return [f"  target challenge denied: {message.removeprefix('TARGET CHALLENGE DENIED:').strip()}"]

    if message.startswith("[Reactive forbidden]"):
        return [f"  guardrail: {message}"]
    if message.startswith("[SAY]"):
        return [f"  model: {message.removeprefix('[SAY]').strip()}"]
    if message.startswith("issues detected before DONE:"):
        return [f"  guardrail: {message}"]
    if message.startswith("[AUTO]"):
        return [f"  auto: {message.removeprefix('[AUTO]').strip()}"]
    if message.startswith("[VERIFY]") or message.startswith("[RE-VERIFY]"):
        return [f"  verify: {message}"]
    if message.startswith("[QUALITY]"):
        return [f"  quality: {message.removeprefix('[QUALITY]').strip()}"]
    if message.startswith("AGENT DONE"):
        return [f"[done] {message}"]
    if message.startswith("AGENT EXHAUSTED"):
        return [f"[failed] {message}"]
    if message.startswith("[PR] "):
        return [f"[pr] {message.removeprefix('[PR] ').strip()}"]
    if message.startswith("LOG: "):
        return [f"[log] {message.removeprefix('LOG: ').strip()}"]

    if _is_low_signal_json_fragment(message):
        return []
    if _looks_like_useful_detail(message):
        return [f"    {message}"]
    return []


def format_lines(lines: Iterable[str]) -> Iterable[str]:
    for line in lines:
        for formatted in format_message(line):
            yield formatted


def _format_agentcore_json(message: str) -> str | None:
    if not message.startswith("{"):
        return None
    try:
        parsed = json.loads(message)
    except json.JSONDecodeError:
        return None
    if parsed.get("logger") == "bedrock_agentcore.app" and parsed.get("message"):
        return f"[agentcore] {parsed['message']}"
    return None


def _summarize_tool(name: str, args_text: str) -> str:
    args = _parse_args(args_text)
    if name == "ask_harness":
        return f"ask_harness intent={args.get('intent', '?')}"
    if name == "run_command":
        return f"run_command $ {args.get('command', '?')}"
    if name == "read_file":
        suffix = ""
        if args.get("view_range"):
            suffix = f" lines={args['view_range']}"
        return f"read_file {args.get('path', '?')}{suffix}"
    if name == "rg":
        return f"rg pattern={args.get('pattern', '?')!r} path={args.get('path', '.')}"
    if name == "create_file":
        return f"create_file {args.get('path', '?')}"
    if name == "str_replace_editor":
        return f"edit {args.get('path', '?')}"
    if args:
        return f"{name} {json.dumps(args, ensure_ascii=False, sort_keys=True)}"
    return name


def _parse_args(args_text: str) -> dict:
    try:
        parsed = json.loads(args_text)
    except json.JSONDecodeError:
        parsed = {}
        for key in ("intent", "path", "command", "pattern"):
            match = re.search(rf'"{key}"\s*:\s*"([^"]+)"', args_text)
            if match:
                parsed[key] = match.group(1)
    return parsed if isinstance(parsed, dict) else {}


def _is_low_signal_json_fragment(message: str) -> bool:
    stripped = message.strip()
    if stripped in {"{", "}", "},", "[", "]", "],"}:
        return True
    if re.match(r'^"[^"]+"\s*:', stripped):
        return True
    if re.match(r'^"[^"]+",?$', stripped):
        return True
    return False


def _looks_like_useful_detail(message: str) -> bool:
    lowered = message.lower()
    markers = (
        "error",
        "failed",
        "pass",
        "expected",
        "received",
        "test file",
        "bun test",
        "typecheck",
        "opened pull request",
    )
    if any(marker in lowered for marker in markers):
        return True
    return bool(re.search(r"\bran \d+ tests?\b", lowered))


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Render AgentCore ATM logs as a human timeline.")
    parser.add_argument("--raw-unmatched", action="store_true", help="Print lines the humanizer would drop.")
    args = parser.parse_args(argv)
    for raw in sys.stdin:
        formatted = list(format_message(raw))
        if formatted:
            for line in formatted:
                print(line, flush=True)
        elif args.raw_unmatched and raw.strip():
            print(strip_cloudwatch_prefix(raw), flush=True)
    return 0
