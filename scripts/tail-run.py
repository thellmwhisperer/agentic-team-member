#!/usr/bin/env python3
"""Follow agent run logs in real time. Pretty, complete, no truncation."""
import json
import sys
import subprocess
import glob
import textwrap

files = glob.glob("/Volumes/CrucialX9/tmp/agent-*.jsonl")
if not files:
    print("No agent logs found for today")
    sys.exit(1)

latest = sorted(files)[-1]
print(f"Following: {latest}\n")

SEP = "─" * 80


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


proc = subprocess.Popen(["tail", "-f", "-n", "+1", latest], stdout=subprocess.PIPE, text=True)
for line in proc.stdout:
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
            timings = d.get("timings", {})
            prompt_ms = timings.get("prompt_ms", 0)
            pred_ms = timings.get("predicted_ms", 0)
            tok_s = timings.get("predicted_per_second", 0)
            usage = d.get("usage", {})
            p_tok = usage.get("prompt_tokens", "?")
            c_tok = usage.get("completion_tokens", "?")
            tc = d.get("tool_calls", [])
            tc_names = ", ".join(t.get("name", "?") for t in tc) if tc else ""

            print(f"\n{SEP}")
            print(f"  Step [{s}]  finish={finish}  {p_tok}→{c_tok} tok  {tok_s:.1f} tok/s  prompt={prompt_ms:.0f}ms  pred={pred_ms:.0f}ms")
            if tc_names:
                print(f"  tools: {tc_names}")
            if thinking:
                print(f"\n  🧠 Thinking ({len(thinking)} chars):")
                for tline in thinking.strip().split("\n"):
                    print(f"    {tline}")
            if content:
                print(f"\n  💬 Response:")
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
            print(f"  🏁 DONE at step {d.get('step', '?')}, verified={d.get('verified', '?')}")
            print(f"{'█' * 80}")

        elif e == "exhausted":
            print(f"\n{'█' * 80}")
            print(f"  💀 EXHAUSTED after {d.get('steps', '?')} steps")
            print(f"{'█' * 80}")

        elif e == "quality_give_up":
            print(f"\n  ❌ QUALITY GIVE UP at step {d.get('step', '?')}")

        elif e == "init":
            print(f"  🚀 init: model={d.get('model', '?')}, max_steps={d.get('max_steps', '?')}")
            print(f"     workdir: {d.get('workdir', '?')}")
            print(f"     log: {d.get('log', '?')}")

        elif e == "start":
            print(f"  📋 issue:")
            for iline in d.get("issue", "").strip().split("\n"):
                print(f"    {iline}")

        elif e == "cookbook":
            print(f"  📖 cookbook: {d.get('source', '')} → {d.get('symbol', '')} ({d.get('prompt_len', '?')} chars)")

        elif e == "error":
            print(f"\n  ⚠️  ERROR at step {d.get('step', '?')}: {d.get('error', '')}")

        elif e == "llm_timeout":
            print(f"\n  ⏱️  LLM TIMEOUT at step {d.get('step', '?')}: {d.get('error', '')}")

        elif e == "context_compacted":
            print(
                f"\n  🧹 CONTEXT COMPACTED after {d.get('reason', '?')}: "
                f"{d.get('before_messages', '?')} → {d.get('after_messages', '?')} messages"
            )

        else:
            print(f"  {e}: {json.dumps(d, indent=2)}")

    except Exception as ex:
        print(f"  [parse error: {ex}]")
