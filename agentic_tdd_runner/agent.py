#!/usr/bin/env python3
"""
Minimal agentic coding loop using llama-server native tool calling.
Sends a GitHub issue to Qwen3.5-27B and lets it explore, test, and fix.

v2.1: anti-stub prompt, mock example, red-green verification in harness.
"""

import argparse
import json
import os
import subprocess
import sys
import time
from datetime import datetime, timezone

import requests


# --- Force unbuffered stdout ---
sys.stdout.reconfigure(line_buffering=True)
sys.stderr.reconfigure(line_buffering=True)

# --- Config ---
LLAMA_URL = "http://127.0.0.1:11435/v1/chat/completions"
MODEL = "qwen3.5-27b"
WORKDIR = "/Volumes/CrucialX9/tmp/manolito-agent-test"
MAX_STEPS = 50
MAX_TOOL_OUTPUT = 8000  # chars, not tokens
LOG_DIR = "/Volumes/CrucialX9/tmp"

# --- Logging ---
_log_file = None

def init_log():
    global _log_file
    ts = datetime.now().strftime("%Y%m%d-%H%M%S")
    path = os.path.join(LOG_DIR, f"agent-{ts}.jsonl")
    _log_file = open(path, "w")
    log("init", {"workdir": WORKDIR, "max_steps": MAX_STEPS, "model": MODEL, "log": path})
    emit(f"LOG: {path}")
    return path

def log(event: str, data: dict):
    entry = {
        "ts": datetime.now(timezone.utc).isoformat(),
        "event": event,
        **data,
    }
    _log_file.write(json.dumps(entry, ensure_ascii=False) + "\n")
    _log_file.flush()

def emit(msg: str):
    print(msg, flush=True)

# --- Tools ---
TOOLS = [
    {
        "type": "function",
        "function": {
            "name": "read_file",
            "description": "Read the contents of a file. If path is a directory, lists its contents.",
            "parameters": {
                "type": "object",
                "required": ["path"],
                "properties": {
                    "path": {"type": "string", "description": "Path relative to the repo root"}
                }
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "run_command",
            "description": "Run a shell command in the repo root and return stdout+stderr. Use for: grep, find, running tests (bun test <file>), git diff, etc.",
            "parameters": {
                "type": "object",
                "required": ["command"],
                "properties": {
                    "command": {"type": "string", "description": "Shell command to execute"}
                }
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "str_replace_editor",
            "description": "Replace an exact string in a file with new content. old_str must match exactly (including whitespace). Use for editing source code.",
            "parameters": {
                "type": "object",
                "required": ["path", "old_str", "new_str"],
                "properties": {
                    "path": {"type": "string", "description": "Path relative to repo root"},
                    "old_str": {"type": "string", "description": "Exact string to find (must match file content exactly)"},
                    "new_str": {"type": "string", "description": "Replacement string"}
                }
            }
        }
    },
    {
        "type": "function",
        "function": {
            "name": "create_file",
            "description": "Create a new file with the given content. Fails if the file already exists.",
            "parameters": {
                "type": "object",
                "required": ["path", "content"],
                "properties": {
                    "path": {"type": "string", "description": "Path relative to repo root"},
                    "content": {"type": "string", "description": "Full file content"}
                }
            }
        }
    }
]

SYSTEM_PROMPT = """You are a senior software engineer fixing a bug in a TypeScript project.

You have tools to read files, run commands, edit files, and create files. All paths are relative to the repo root.

## Workflow
1. Read the relevant source files to understand the bug
2. Read existing test files to understand testing patterns
3. Export the function if needed so it can be imported in tests
4. Write a test that imports the REAL function and fails (reproduces the bug)
5. Fix the source code
6. Run the test — it must pass
7. Say DONE

## Testing rules (CRITICAL)
- The test runner is `bun test <file>`.
- Your test MUST import the real function from the source file.
- Tests that define local stub/simulation functions are NOT valid. The harness will reject them.
- If the import fails because of module initialization errors, fix the mocks — don't give up and write stubs.
- Use `mock.module()` from bun:test to mock dependencies BEFORE the import.

## Mock pattern for this project
This is how tests in this project mock dependencies (from token.test.ts):

```typescript
import { beforeEach, describe, expect, mock, test } from 'bun:test';

// Mocks MUST come before importing the module under test
mock.module('../logger', () => ({
  log: { info: () => {}, error: () => {}, warn: () => {}, verbose: () => true, flush: () => {} },
  getLogger: () => ({ event: () => {}, response: () => {} }),
}));

// After all mock.module() calls, import the real function:
import { myFunction } from './my-module';
```

If client.ts has many dependencies that initialize on import, you need to mock ALL of them before importing. Read client.ts imports to find what needs mocking.

## Other rules
- Read before editing — you need the exact text for str_replace_editor.
- Make minimal changes. Don't refactor unrelated code.
- If a test fails, read the error carefully and fix it. Don't give up.
- When all tests pass, say DONE.
"""


def execute_tool(name: str, args: dict) -> str:
    try:
        if name == "read_file":
            full_path = os.path.join(WORKDIR, args["path"])
            if os.path.isdir(full_path):
                entries = os.listdir(full_path)
                return "\n".join(sorted(entries))
            with open(full_path, "r") as f:
                return f.read()

        elif name == "run_command":
            result = subprocess.run(
                args["command"],
                shell=True,
                cwd=WORKDIR,
                capture_output=True,
                text=True,
                timeout=60,
            )
            output = result.stdout + result.stderr
            return output if output.strip() else "(no output)"

        elif name == "str_replace_editor":
            full_path = os.path.join(WORKDIR, args["path"])
            with open(full_path, "r") as f:
                content = f.read()
            old_str = args["old_str"]
            if old_str not in content:
                return f"ERROR: old_str not found in {args['path']}. Read the file first to get the exact text."
            if content.count(old_str) > 1:
                return f"ERROR: old_str appears {content.count(old_str)} times. Make it more specific."
            new_content = content.replace(old_str, args["new_str"], 1)
            with open(full_path, "w") as f:
                f.write(new_content)
            return f"OK: replaced in {args['path']}"

        elif name == "create_file":
            full_path = os.path.join(WORKDIR, args["path"])
            if os.path.exists(full_path):
                return f"ERROR: {args['path']} already exists. Use str_replace_editor to modify it."
            os.makedirs(os.path.dirname(full_path), exist_ok=True)
            with open(full_path, "w") as f:
                f.write(args["content"])
            return f"OK: created {args['path']}"

        else:
            return f"ERROR: unknown tool {name}"

    except Exception as e:
        return f"ERROR: {type(e).__name__}: {e}"


def truncate(text: str, max_chars: int = MAX_TOOL_OUTPUT) -> str:
    if len(text) <= max_chars:
        return text
    half = max_chars // 2
    return text[:half] + f"\n\n... ({len(text) - max_chars} chars truncated) ...\n\n" + text[-half:]


def chat(messages: list) -> dict:
    payload = {
        "model": MODEL,
        "messages": messages,
        "tools": TOOLS,
        "temperature": 0.6,
        "top_p": 0.95,
        "top_k": 20,
    }
    resp = requests.post(LLAMA_URL, json=payload, timeout=600)
    resp.raise_for_status()
    return resp.json()


def find_test_file() -> str | None:
    """Find the test file the agent created."""
    for root, _dirs, files in os.walk(WORKDIR):
        for f in files:
            if f.endswith('.test.ts') and 'node_modules' not in root:
                full = os.path.join(root, f)
                rel = os.path.relpath(full, WORKDIR)
                # Skip pre-existing test files (check git)
                result = subprocess.run(
                    f"git ls-files '{rel}'",
                    shell=True, cwd=WORKDIR, capture_output=True, text=True
                )
                if not result.stdout.strip():
                    return rel
    return None


def verify_red_green(test_file: str) -> tuple[bool, str]:
    """Verify that the test actually tests the fix by doing red-green check.

    1. Revert source changes (git checkout -- src/)
    2. Run test — should FAIL (red)
    3. Re-apply fix (git checkout stash)
    4. Run test — should PASS (green)

    Returns (passed, message).
    """
    emit("\n=== RED-GREEN VERIFICATION ===")

    # Stash current state (with fix applied)
    subprocess.run("git stash", shell=True, cwd=WORKDIR, capture_output=True)

    # Run test without fix — should FAIL
    emit("  [RED] Running test WITHOUT fix...")
    red_result = subprocess.run(
        f"bun test {test_file}",
        shell=True, cwd=WORKDIR, capture_output=True, text=True, timeout=30,
    )
    red_passed = red_result.returncode == 0
    red_output = (red_result.stdout + red_result.stderr)[:500]
    emit(f"  [RED] exit={red_result.returncode} {'PASS (BAD!)' if red_passed else 'FAIL (good)'}")
    for line in red_output.split("\n")[:10]:
        emit(f"    {line}")

    # Restore fix
    subprocess.run("git stash pop", shell=True, cwd=WORKDIR, capture_output=True)

    # Run test with fix — should PASS
    emit("  [GREEN] Running test WITH fix...")
    green_result = subprocess.run(
        f"bun test {test_file}",
        shell=True, cwd=WORKDIR, capture_output=True, text=True, timeout=30,
    )
    green_passed = green_result.returncode == 0
    green_output = (green_result.stdout + green_result.stderr)[:500]
    emit(f"  [GREEN] exit={green_result.returncode} {'PASS (good)' if green_passed else 'FAIL (BAD!)'}")
    for line in green_output.split("\n")[:10]:
        emit(f"    {line}")

    log("verify", {
        "test_file": test_file,
        "red_passed": red_passed,
        "green_passed": green_passed,
        "red_output": red_output,
        "green_output": green_output,
    })

    if red_passed:
        return False, (
            f"REJECTED: Your test ({test_file}) passes even WITHOUT your source fix. "
            f"This means it doesn't test the real code — it probably uses local stub functions "
            f"instead of importing from the source. Rewrite the test to import the real "
            f"handleResub function from ./client using mock.module() for all dependencies."
        )

    if not green_passed:
        return False, (
            f"REJECTED: Your test ({test_file}) fails even WITH your source fix. "
            f"The test has errors. Fix them. Error: {green_output[:300]}"
        )

    return True, "VERIFIED: Test fails without fix, passes with fix. Real red-green."


def parse_args():
    parser = argparse.ArgumentParser(description="TDD agent with llama-server")
    parser.add_argument("issue", nargs="?", default=None, help="Issue text or path to issue file")
    parser.add_argument("--source", type=str, help="Source file path relative to workdir (e.g. src/twitch/client.ts)")
    parser.add_argument("--symbol", type=str, help="Target function/method name (e.g. handleResub)")
    parser.add_argument("--workdir", type=str, default=WORKDIR, help="Project root directory")
    parser.add_argument("--url", type=str, default=LLAMA_URL, help="LLM API URL")
    parser.add_argument("--model", type=str, default=MODEL, help="Model name")
    return parser.parse_args()


DEFAULT_ISSUE = """Bug: handleResub reports '0 meses' for all resubscriptions

When a user resubscribes, Manolito always says "lleva 0 meses" regardless of how many months they've been subscribed.

Where: src/twitch/client.ts → handleResub()

Root cause: The function receives streakMonths (3rd param from tmi.js) but treats it as cumulative months. The tmi.js resub event signature is: resub(channel, username, months, message, userstate, methods). The 3rd param is streak months (often 0). Cumulative months are in userstate['msg-param-cumulative-months'].

Expected: A user subscribed for 6 months should produce "¡username lleva 6 meses!"

Test approach: Export handleResub, import it in a test, mock dependencies, call with streakMonths=0 and userstate containing msg-param-cumulative-months='6', verify response contains "6 meses"."""


def main():
    global WORKDIR, LLAMA_URL, MODEL
    args = parse_args()
    if args.workdir:
        WORKDIR = args.workdir
    LLAMA_URL = args.url
    MODEL = args.model

    log_path = init_log()

    issue_text = args.issue or DEFAULT_ISSUE

    # Build system prompt — inject cookbook if source/symbol provided
    system_prompt = SYSTEM_PROMPT
    if args.source and args.symbol:
        from agentic_tdd_runner.cookbook import build_system_prompt
        system_prompt = build_system_prompt(
            base_prompt=SYSTEM_PROMPT,
            issue_text=issue_text,
            source_path=args.source,
            symbol=args.symbol,
            project_root=WORKDIR,
        )
        emit(f"[COOKBOOK] Injected mock cookbook for {args.symbol} in {args.source}")
        log("cookbook", {"source": args.source, "symbol": args.symbol, "prompt_len": len(system_prompt)})

    messages = [
        {"role": "system", "content": system_prompt},
        {"role": "user", "content": f"Fix this bug:\n\n{issue_text}"},
    ]

    emit(f"{'='*60}")
    emit(f"AGENT START — max {MAX_STEPS} steps")
    emit(f"Workdir: {WORKDIR}")
    emit(f"Log: {log_path}")
    emit(f"{'='*60}")

    log("start", {"issue": issue_text[:200]})

    done_rejected = 0  # how many times we rejected DONE

    for step in range(MAX_STEPS):
        emit(f"\n>>> Step {step} — requesting LLM...")
        t0 = time.time()

        try:
            response = chat(messages)
        except Exception as e:
            emit(f"  ERROR: {e}")
            log("error", {"step": step, "error": str(e)})
            break

        elapsed = time.time() - t0

        choice = response["choices"][0]
        msg = choice["message"]
        finish = choice["finish_reason"]
        usage = response.get("usage", {})
        timings = response.get("timings", {})
        thinking = msg.get("reasoning_content", "")

        log("step", {
            "step": step,
            "elapsed_s": round(elapsed, 1),
            "finish_reason": finish,
            "thinking": thinking,
            "content": msg.get("content", ""),
            "tool_calls": [
                {"name": tc["function"]["name"], "args": tc["function"]["arguments"]}
                for tc in msg.get("tool_calls", [])
            ],
            "usage": usage,
            "timings": {
                "prompt_ms": timings.get("prompt_ms"),
                "predicted_ms": timings.get("predicted_ms"),
                "prompt_per_second": timings.get("prompt_per_second"),
                "predicted_per_second": timings.get("predicted_per_second"),
            },
        })

        prompt_tok = usage.get("prompt_tokens", "?")
        comp_tok = usage.get("completion_tokens", "?")
        tok_s = timings.get("predicted_per_second", 0)
        emit(f"--- Step {step} | {elapsed:.1f}s | {prompt_tok}→{comp_tok} tok | {tok_s:.1f} tok/s | finish={finish} ---")

        if thinking:
            emit(f"  [THINK] ({len(thinking)} chars)")
            for line in thinking.strip().split("\n"):
                emit(f"    {line}")

        if msg.get("content"):
            emit(f"  [SAY] {msg['content']}")
            if "DONE" in msg["content"].upper():
                # --- RED-GREEN VERIFICATION ---
                test_file = find_test_file()
                if test_file:
                    verified, verify_msg = verify_red_green(test_file)
                    emit(f"\n  [VERIFY] {verify_msg}")
                    log("verify_result", {"verified": verified, "message": verify_msg, "test_file": test_file})

                    if verified:
                        emit(f"\n{'='*60}")
                        emit(f"AGENT DONE at step {step} — VERIFIED")
                        emit(f"{'='*60}")
                        diff = subprocess.run("git diff", shell=True, cwd=WORKDIR, capture_output=True, text=True)
                        emit(f"\n--- GIT DIFF ---\n{diff.stdout}")
                        untracked = subprocess.run("git ls-files --others --exclude-standard", shell=True, cwd=WORKDIR, capture_output=True, text=True)
                        if untracked.stdout.strip():
                            emit(f"\n--- NEW FILES ---")
                            for f in untracked.stdout.strip().split("\n"):
                                emit(f"  {f}")
                                content_result = subprocess.run(f"cat '{f}'", shell=True, cwd=WORKDIR, capture_output=True, text=True)
                                emit(content_result.stdout)
                        log("done", {"step": step, "verified": True})
                        return 0
                    else:
                        # Reject and nudge
                        done_rejected += 1
                        if done_rejected >= 3:
                            emit(f"\n  [GIVE UP] Rejected DONE {done_rejected} times. Stopping.")
                            log("give_up", {"step": step, "done_rejected": done_rejected})
                            return 1
                        messages.append(msg)
                        messages.append({
                            "role": "user",
                            "content": verify_msg,
                        })
                        continue
                else:
                    emit("  [WARN] No test file found — cannot verify")
                    messages.append(msg)
                    messages.append({
                        "role": "user",
                        "content": "You said DONE but I can't find a test file. Create a test that imports the real function.",
                    })
                    continue

        # Append assistant message to history
        messages.append(msg)

        if finish == "tool_calls" and msg.get("tool_calls"):
            for tc in msg["tool_calls"]:
                fn = tc["function"]
                name = fn["name"]
                try:
                    args = json.loads(fn["arguments"])
                except json.JSONDecodeError:
                    args = {}
                    emit(f"  WARNING: failed to parse args: {fn['arguments'][:200]}")

                args_preview = json.dumps(args, ensure_ascii=False)
                if len(args_preview) > 300:
                    args_preview = args_preview[:300] + "..."
                emit(f"  [TOOL] {name}({args_preview})")

                t1 = time.time()
                result = execute_tool(name, args)
                tool_elapsed = time.time() - t1
                result_truncated = truncate(result)

                log("tool", {
                    "step": step,
                    "name": name,
                    "args": args,
                    "result_chars": len(result),
                    "result_truncated": len(result) != len(result_truncated),
                    "elapsed_s": round(tool_elapsed, 3),
                    "result": result_truncated,
                })

                display = result_truncated
                if len(display) > 500:
                    display = display[:250] + f"\n  ... ({len(result)} chars total) ...\n" + display[-250:]
                emit(f"  [RESULT] ({len(result)} chars, {tool_elapsed:.2f}s)")
                for line in display.split("\n")[:20]:
                    emit(f"    {line}")
                if display.count("\n") > 20:
                    emit(f"    ... ({display.count(chr(10))} lines total)")

                messages.append({
                    "role": "tool",
                    "tool_call_id": tc["id"],
                    "content": result_truncated,
                })

        elif finish == "stop":
            if step > 3:
                messages.append({
                    "role": "user",
                    "content": "Continue. If all tests pass, say DONE."
                })
                emit("  [NUDGE] Continue prompt injected")
        else:
            emit(f"  [FINISH] {finish}")

    emit(f"\n{'='*60}")
    emit(f"AGENT EXHAUSTED — {MAX_STEPS} steps without DONE")
    emit(f"{'='*60}")
    log("exhausted", {"steps": MAX_STEPS})
    return 1


if __name__ == "__main__":
    sys.exit(main())
