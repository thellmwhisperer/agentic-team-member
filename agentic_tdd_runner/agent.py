#!/usr/bin/env python3
"""Agentic TDD runner — local LLM fixes bugs with tests."""

import argparse
import json
import os
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

import requests

# --- Load .env if present ---
_env_path = Path.cwd() / ".env"
if _env_path.exists():
    for line in _env_path.read_text().splitlines():
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            key, _, value = line.partition("=")
            os.environ.setdefault(key.strip(), value.strip())

# --- Force unbuffered stdout ---
sys.stdout.reconfigure(line_buffering=True)
sys.stderr.reconfigure(line_buffering=True)

# --- Config (loaded from TOML, overridden by env vars / CLI args) ---
_CONFIG = None  # populated by main()
WORKDIR = os.environ.get("AGENT_WORKDIR", os.getcwd())
LOG_DIR = os.environ.get("AGENT_LOG_DIR", os.getcwd())

# --- Logging ---
_log_file = None

def init_log():
    global _log_file
    ts = datetime.now().strftime("%Y%m%d-%H%M%S")
    path = os.path.join(LOG_DIR, f"agent-{ts}.jsonl")
    _log_file = open(path, "w")
    log("init", {"workdir": WORKDIR, "max_steps": _CONFIG["agent"]["max_steps"], "model": _CONFIG["llm"]["model"], "log": path})
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


# Tools and prompts loaded from config/agent.toml + config/tools.json


def _resolve_repo_path(path: str, workdir: str = None) -> Path:
    """Resolve a relative path within the workdir. Raises if it escapes."""
    repo_root = Path(workdir or WORKDIR).resolve()
    candidate = (repo_root / path).resolve()
    try:
        candidate.relative_to(repo_root)
    except ValueError as exc:
        raise ValueError(f"path escapes workdir: {path}") from exc
    return candidate


def execute_tool(name: str, args: dict) -> str:
    try:
        if name == "read_file":
            full_path = _resolve_repo_path(args["path"])
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
                timeout=_CONFIG["timeouts"]["tool_execution"],
            )
            output = result.stdout + result.stderr
            return output if output.strip() else "(no output)"

        elif name == "str_replace_editor":
            full_path = _resolve_repo_path(args["path"])
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
            full_path = _resolve_repo_path(args["path"])
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


def truncate(text: str, max_chars: int = 0) -> str:
    if max_chars == 0:
        max_chars = _CONFIG["agent"]["max_tool_output"]
    if len(text) <= max_chars:
        return text
    half = max_chars // 2
    return text[:half] + f"\n\n... ({len(text) - max_chars} chars truncated) ...\n\n" + text[-half:]


def chat(messages: list) -> dict:
    llm = _CONFIG["llm"]
    payload = {
        "model": llm["model"],
        "messages": messages,
        "tools": _CONFIG["tools"],
        "temperature": llm.get("temperature", 0.6),
        "top_p": llm.get("top_p", 0.95),
        "top_k": llm.get("top_k", 20),
    }
    resp = requests.post(llm["url"], json=payload, timeout=_CONFIG["timeouts"]["llm_request"])
    resp.raise_for_status()
    return resp.json()


def find_test_file(hint: str | None = None) -> str | None:
    """Find the test file the agent created. Uses hint from cookbook if available."""
    import fnmatch
    if hint:
        hinted = os.path.join(WORKDIR, hint)
        if os.path.isfile(hinted):
            return hint
    patterns = _CONFIG["runner"]["test_file_patterns"]
    exclude = _CONFIG["runner"].get("exclude_dirs", [])
    for root, _dirs, files in os.walk(WORKDIR):
        if any(ex in root for ex in exclude):
            continue
        for f in files:
            if any(fnmatch.fnmatch(f, p) for p in patterns):
                full = os.path.join(root, f)
                rel = os.path.relpath(full, WORKDIR)
                result = subprocess.run(
                    f"git ls-files '{rel}'",
                    shell=True, cwd=WORKDIR, capture_output=True, text=True
                )
                if not result.stdout.strip():
                    return rel
    return None


def verify_red_green(test_file: str) -> tuple[bool, str]:
    """Verify red-green: test fails without fix, passes with fix.

    1. Stash current changes (with fix)
    2. Run test — should FAIL (red)
    3. Restore fix from stash
    4. Run test — should PASS (green)
    """
    run_cmd = _CONFIG["runner"]["command"]
    test_timeout = _CONFIG["timeouts"]["test_run"]

    emit("\n=== RED-GREEN VERIFICATION ===")

    subprocess.run("git stash --include-untracked", shell=True, cwd=WORKDIR, capture_output=True)

    emit("  [RED] Running test WITHOUT fix...")
    red_result = subprocess.run(
        f"{run_cmd} {test_file}",
        shell=True, cwd=WORKDIR, capture_output=True, text=True, timeout=test_timeout,
    )
    red_passed = red_result.returncode == 0
    red_output = (red_result.stdout + red_result.stderr)[:500]
    emit(f"  [RED] exit={red_result.returncode} {'PASS (BAD!)' if red_passed else 'FAIL (good)'}")
    for line in red_output.split("\n")[:10]:
        emit(f"    {line}")

    subprocess.run("git stash pop", shell=True, cwd=WORKDIR, capture_output=True)

    emit("  [GREEN] Running test WITH fix...")
    green_result = subprocess.run(
        f"{run_cmd} {test_file}",
        shell=True, cwd=WORKDIR, capture_output=True, text=True, timeout=test_timeout,
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
            f"function and mock its dependencies properly."
        )

    if not green_passed:
        return False, (
            f"REJECTED: Your test ({test_file}) fails even WITH your source fix. "
            f"The test has errors. Fix them. Error: {green_output[:300]}"
        )

    return True, "VERIFIED: Test fails without fix, passes with fix. Real red-green."


def _default_config_path():
    """Find config/agent.toml relative to the package."""
    pkg = Path(__file__).parent.parent / "config" / "agent.toml"
    if pkg.exists():
        return str(pkg)
    return None


def parse_args():
    parser = argparse.ArgumentParser(description="Agentic TDD runner — local LLM fixes bugs with tests")
    parser.add_argument("issue", help="Issue text, or path to a file containing the issue description")
    parser.add_argument("--source", type=str, help="Source file path relative to workdir (e.g. src/twitch/client.ts)")
    parser.add_argument("--symbol", type=str, help="Target function/method name (e.g. handleResub)")
    parser.add_argument("--workdir", type=str, default=WORKDIR, help="Project root directory (default: cwd)")
    parser.add_argument("--config", type=str, default=_default_config_path(), help="Path to agent.toml config file")
    parser.add_argument("--log-dir", type=str, default=LOG_DIR, help="Directory for JSONL logs (default: cwd)")
    return parser.parse_args()


def main():
    global _CONFIG, WORKDIR, LOG_DIR
    args = parse_args()

    from agentic_tdd_runner.config import load_config
    _CONFIG = load_config(args.config)

    WORKDIR = args.workdir
    LOG_DIR = args.log_dir

    log_path = init_log()

    # Issue can be inline text or a path to a file
    issue_text = args.issue
    if os.path.isfile(issue_text):
        with open(issue_text) as f:
            issue_text = f.read()

    # Build system prompt — inject cookbook if source/symbol provided
    system_prompt = _CONFIG["prompt"]["system"].strip()
    if args.source and args.symbol:
        from agentic_tdd_runner.cookbook import build_system_prompt
        system_prompt = build_system_prompt(
            base_prompt=system_prompt,
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
    max_steps = _CONFIG["agent"]["max_steps"]
    emit(f"AGENT START — max {max_steps} steps")
    emit(f"Workdir: {WORKDIR}")
    emit(f"Log: {log_path}")
    emit(f"{'='*60}")

    log("start", {"issue": issue_text[:200]})

    done_rejected = 0  # how many times we rejected DONE

    max_rejections = _CONFIG["verification"]["max_rejections"]
    for step in range(max_steps):
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
                        if done_rejected >= max_rejections:
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
                        "content": _CONFIG["prompt"]["no_test_found"],
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
                    "content": _CONFIG["prompt"]["nudge"]
                })
                emit("  [NUDGE] Continue prompt injected")
        else:
            emit(f"  [FINISH] {finish}")

    emit(f"\n{'='*60}")
    emit(f"AGENT EXHAUSTED — {max_steps} steps without DONE")
    emit(f"{'='*60}")
    log("exhausted", {"steps": max_steps})
    return 1


if __name__ == "__main__":
    sys.exit(main())
