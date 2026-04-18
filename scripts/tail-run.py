#!/usr/bin/env python3
"""Follow agent run logs in real time. Pretty, complete, no truncation."""
import argparse
import glob
import json
import os
import subprocess
import sys
import tempfile

SEP = "─" * 80


def _build_parser():
    parser = argparse.ArgumentParser(
        description="Follow ATM agent JSONL logs in real time."
    )
    parser.add_argument(
        "logfile",
        nargs="?",
        help=(
            "Specific JSONL log to follow. Defaults to the newest "
            "agent-*.jsonl found in AGENT_LOG_DIR, the current directory, "
            "XDG_RUNTIME_DIR, or the system temp directory."
        ),
    )
    return parser


def _default_log_dirs():
    candidates = [
        os.environ.get("AGENT_LOG_DIR"),
        os.getcwd(),
        os.environ.get("XDG_RUNTIME_DIR"),
        tempfile.gettempdir(),
    ]
    return [path for i, path in enumerate(candidates) if path and path not in candidates[:i]]


def _resolve_logfile(logfile):
    if logfile:
        return logfile
    files = []
    for directory in _default_log_dirs():
        files.extend(glob.glob(os.path.join(directory, "agent-*.jsonl")))
    if not files:
        return None
    return max(set(files), key=os.path.getmtime)


def fmt_metric(value, spec, suffix=""):
    if isinstance(value, (int, float)):
        return f"{value:{spec}}{suffix}"
    return f"?{suffix}"


def fmt_tool(name, args, elapsed, result_chars, result, applied=None):
    status = ""
    if applied is True:
        status = "  ✅ applied"
    elif applied is False:
        status = "  ❌ failed"
    lines = [f"  🔧 {name}{status}  ({result_chars} chars, {elapsed}s)"]

    if name == "str_replace_editor":
        lines.append(f"     file: {args.get('path', '?')}")
        old = args.get("old_str", "")
        new = args.get("new_str", "")
        lines.append("     ── old ──")
        for ol in old.split("\n"):
            lines.append(f"     - {ol}")
        lines.append("     ── new ──")
        for nl in new.split("\n"):
            lines.append(f"     + {nl}")

    elif name == "create_file":
        lines.append(f"     file: {args.get('path', '?')}")
        content = args.get("content", "")
        for cl in content.split("\n"):
            lines.append(f"     + {cl}")

    elif name == "read_file":
        lines.append(f"     path: {args.get('path', '?')}")

    elif name == "run_command":
        lines.append(f"     $ {args.get('command', '?')}")
        if result:
            for rl in result.split("\n")[:30]:
                lines.append(f"       {rl}")
            total = result.count("\n") + 1
            if total > 30:
                lines.append(f"       ... ({total} lines total)")

    else:
        lines.append(f"     args: {json.dumps(args, ensure_ascii=False, indent=2)}")

    return "\n".join(lines)


def main(argv=None):
    args = _build_parser().parse_args(argv)
    logfile = _resolve_logfile(args.logfile)
    if not logfile:
        print("No agent logs found")
        return 1

    print(f"Following: {logfile}\n")
    try:
        proc = subprocess.Popen(
            ["tail", "-f", "-n", "+1", logfile],
            stdout=subprocess.PIPE,
            text=True,
        )
    except OSError as exc:
        print(f"Failed to start tail: {exc}", file=sys.stderr)
        return 1

    for line in proc.stdout or ():
        line = line.strip()
        if not line:
            continue
        try:
            d = json.loads(line)
            e = d.get("event", "?")
            s = d.get("step", "")

            if e == "step":
                finish = d.get("finish_reason", "")
                content = d.get("content", "")
                thinking = d.get("thinking", "")
                timings = d.get("timings") or {}
                prompt_ms = timings.get("prompt_ms", 0)
                pred_ms = timings.get("predicted_ms", 0)
                tok_s = timings.get("predicted_per_second", 0)
                usage = d.get("usage", {})
                p_tok = usage.get("prompt_tokens", "?")
                c_tok = usage.get("completion_tokens", "?")
                tc = d.get("tool_calls", [])
                tc_names = ", ".join(t.get("name", "?") for t in tc) if tc else ""
                tok_s_str = fmt_metric(tok_s, ".1f", " tok/s")
                prompt_ms_str = fmt_metric(prompt_ms, ".0f", "ms")
                pred_ms_str = fmt_metric(pred_ms, ".0f", "ms")

                print(f"\n{SEP}")
                print(
                    f"  Step [{s}]  finish={finish}  {p_tok}→{c_tok} tok  "
                    f"{tok_s_str}  prompt={prompt_ms_str}  pred={pred_ms_str}"
                )
                if tc_names:
                    print(f"  tools: {tc_names}")
                if thinking:
                    print(f"\n  🧠 Thinking ({len(thinking)} chars):")
                    for tline in thinking.strip().split("\n"):
                        print(f"    {tline}")
                if content:
                    print("\n  💬 Response:")
                    for cline in content.strip().split("\n"):
                        print(f"    {cline}")

            elif e == "tool":
                name = d.get("name", "")
                args = d.get("args", {})
                elapsed = d.get("elapsed_s", 0)
                result_chars = d.get("result_chars", 0)
                result = d.get("result", "")
                applied = d.get("applied")
                print(fmt_tool(name, args, elapsed, result_chars, result, applied))

            elif e == "verify_result":
                verified = d.get("verified", "?")
                msg = d.get("message", "")
                tf = d.get("test_file", "")
                print(f"\n{'═' * 80}")
                print(f"  ✅ VERIFY  verified={verified}  test={tf}")
                for vline in msg.strip().split("\n"):
                    print(f"    {vline}")
                print(f"{'═' * 80}")

            elif e == "quality":
                passed = d.get("passed", "?")
                msg = d.get("message", "")
                print(f"\n{'═' * 80}")
                print(f"  🔍 QUALITY  passed={passed}")
                for qline in msg.strip().split("\n"):
                    print(f"    {qline}")
                print(f"{'═' * 80}")

            elif e == "done":
                print(f"\n{'█' * 80}")
                print(
                    f"  🏁 DONE at step {d.get('step', '?')}, "
                    f"verified={d.get('verified', '?')}"
                )
                print(f"{'█' * 80}")

            elif e == "exhausted":
                print(f"\n{'█' * 80}")
                print(f"  💀 EXHAUSTED after {d.get('steps', '?')} steps")
                print(f"{'█' * 80}")

            elif e == "quality_give_up":
                print(f"\n  ❌ QUALITY GIVE UP at step {d.get('step', '?')}")

            elif e == "init":
                print(
                    f"  🚀 init: model={d.get('model', '?')}, "
                    f"max_steps={d.get('max_steps', '?')}"
                )
                print(f"     workdir: {d.get('workdir', '?')}")
                print(f"     log: {d.get('log', '?')}")

            elif e == "start":
                print("  📋 issue:")
                for iline in d.get("issue", "").strip().split("\n"):
                    print(f"    {iline}")

            elif e == "cookbook":
                print(
                    "  📖 cookbook: "
                    f"{d.get('source', '')} → {d.get('symbol', '')} "
                    f"({d.get('prompt_len', '?')} chars)"
                )

            elif e == "error":
                print(f"\n  ⚠️  ERROR at step {d.get('step', '?')}: {d.get('error', '')}")

            elif e == "llm_timeout":
                print(
                    f"\n  ⏱️  LLM TIMEOUT at step {d.get('step', '?')}: "
                    f"{d.get('error', '')}"
                )

            elif e == "context_compacted":
                print(
                    f"\n  🧹 CONTEXT COMPACTED after {d.get('reason', '?')}: "
                    f"{d.get('before_messages', '?')} → "
                    f"{d.get('after_messages', '?')} messages"
                )

            else:
                print(f"  {e}: {json.dumps(d, indent=2)}")

        except Exception as ex:
            print(f"  [parse error: {ex}]")

    return proc.wait()


if __name__ == "__main__":
    raise SystemExit(main())
