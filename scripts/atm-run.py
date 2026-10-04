#!/usr/bin/env python3
"""The launcher for harness-worker runs. One label, one output directory, one verdict.

Everything the worker needs that is not a per-run choice lives in the ``[launch]`` table of
the ``--config`` TOML (gitignored, one per target repo)::

    [launch]
    repo = "/path/to/target-repo"
    base_ref = "origin/integration"
    github_repo = "owner/repo"
    issue_number = 300
    run_root = ".worktrees"
    scope = ["internal/distribution/cli/hooks_session.go"]

Commands:

    atm-run.py run  --config C --label opus-5 --harness claude --model claude-opus-5-5
    atm-run.py run  ... --pane w3:pB      # launch inside a herdr pane and verify it started
    atm-run.py show --label opus-5         # the verdict of a finished run
    atm-run.py list                        # every run under .tmp/harness-worker with its verdict
    atm-run.py clean --config C [--yes]    # remove the target's atm-run-* clones and worktrees

Why this exists (4-oct-2026): runs were launched by pasting a 400-character command into a
pane. The pane was inside a ``tail -f`` of the previous run, so the text was swallowed and
nothing ran; the next run reused the previous label, so nobody knew which output was which;
old clones piled up under the target repo. Each of those is now a refusal or a check here.
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import time
import tomllib
from pathlib import Path

HERE = Path(__file__).resolve().parent.parent
RUNS = HERE / ".tmp" / "harness-worker"
LAUNCH_KEYS = ("repo", "base_ref", "github_repo", "issue_number", "run_root", "scope")


def load_launch(config: str) -> dict:
    path = Path(config)
    if not path.is_absolute():
        path = HERE / path
    with path.open("rb") as fh:
        data = tomllib.load(fh)
    launch = data.get("launch")
    if not launch:
        sys.exit(f"{path} has no [launch] table; it needs: {', '.join(LAUNCH_KEYS)}")
    missing = [k for k in ("repo", "issue_number") if k not in launch]
    if missing:
        sys.exit(f"[launch] in {path} is missing {', '.join(missing)}")
    return launch


def worker_command(args, launch: dict, out: Path) -> list[str]:
    cmd = [sys.executable, "-m", "agentic_tdd_runner.harness_worker",
           "--repo", str(launch["repo"]),
           "--base-ref", str(launch.get("base_ref", "main")),
           "--issue-number", str(launch["issue_number"]),
           "--harness", args.harness,
           "--config", args.config,
           "--log-dir", str(out), "--artifact-dir", str(out)]
    if launch.get("github_repo"):
        cmd += ["--github-repo", str(launch["github_repo"])]
    if launch.get("run_root"):
        cmd += ["--run-root", str(launch["run_root"])]
    for glob in launch.get("scope", []) or []:
        cmd += ["--scope", str(glob)]
    if args.model:
        cmd += ["--model", args.model]
    if args.effort:
        cmd += ["--effort", args.effort]
    for kv in args.env:
        cmd += ["--env", kv]
    if args.timeout:
        cmd += ["--timeout", str(args.timeout)]
    return cmd


def cmd_run(args) -> int:
    launch = load_launch(args.config)
    out = RUNS / args.label
    if out.exists():
        sys.exit(f"label {args.label!r} already used: {out}\nPick a new label; a run never overwrites another.")
    if args.pane:
        return launch_in_pane(args)
    out.mkdir(parents=True)
    cmd = worker_command(args, launch, out)
    (out / "command.txt").write_text(" ".join(cmd) + "\n")
    print(f"[LAUNCH] label={args.label} harness={args.harness} model={args.model or 'default'} effort={args.effort or 'default'}")
    print(f"[LAUNCH] out={out}")
    print(f"[LAUNCH] {' '.join(cmd)}", flush=True)
    with (out / "stdout.txt").open("w", buffering=1) as sink:  # line-buffered: the file is readable while the run lives
        proc = subprocess.Popen(cmd, cwd=HERE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        assert proc.stdout is not None
        for line in proc.stdout:
            sys.stdout.write(line)
            sys.stdout.flush()
            sink.write(line)
        code = proc.wait()
    save_patch(out)
    (out / "exit.txt").write_text(f"{code}\n")
    print(f"\n[LAUNCH] worker exit={code}")
    print(verdict(args.label))
    return code


def save_patch(out: Path) -> None:
    """Copy the run's work out of its clone: tracked edits and new files, as one patch.

    The clone is disposable once this exists. Written after the worker ends, so the patch is
    the final state the verifier judged, follow-up tests included."""
    report = out / "report.json"
    if not report.exists():
        return
    r = json.loads(report.read_text())
    clone = Path(r.get("worktree") or "")
    if not (clone / ".git").exists():
        return
    # Units after the first are committed in the clone, so the patch is base..working tree, not HEAD..working tree.
    base = r.get("base_sha") or "HEAD"
    subprocess.run(["git", "add", "-A", "-N", "--", ".", ":!.atm", ":!.tmp"], cwd=clone, check=False, capture_output=True)
    diff = subprocess.run(["git", "diff", "--binary", base, "--", ".", ":!.atm", ":!.tmp"], cwd=clone, capture_output=True, text=True)
    (out / "patch.diff").write_text(diff.stdout)
    atm = clone / ".atm"
    if atm.is_dir():
        shutil.copytree(atm, out / "clone-atm", dirs_exist_ok=True)
    print(f"[LAUNCH] patch saved: {out / 'patch.diff'} ({len(diff.stdout.splitlines())} lines)")


def clones_in_use() -> dict[Path, str]:
    """Clone path -> label, for every run under .tmp/harness-worker that has not finished.

    A run is finished when exit.txt exists (launched here) or when report.json exists and its
    clone already has a patch.diff beside it. Anything else is live or unaccounted for."""
    live: dict[Path, str] = {}
    if not RUNS.exists():
        return live
    for d in RUNS.iterdir():
        if not d.is_dir() or d.name.startswith("_"):
            continue
        finished = (d / "exit.txt").exists() or ((d / "report.json").exists() and (d / "patch.diff").exists())
        if finished:
            continue
        clone = None
        if (d / "report.json").exists():
            clone = json.loads((d / "report.json").read_text()).get("worktree")
        else:
            for log in d.glob("worker-*.jsonl"):
                for line in log.open():
                    if '"atm.prepared"' in line:
                        clone = json.loads(line)["event"].get("worktree")
                        break
                if clone:
                    break
        if clone:
            live[Path(clone).resolve()] = d.name
    return live


def launch_in_pane(args) -> int:
    """Send the same run (without --pane) into a herdr pane and confirm the worker started."""
    pane = args.pane
    inner = [sys.executable, str(Path(__file__).resolve()), "run", "--config", args.config,
             "--label", args.label, "--harness", args.harness]
    if args.model:
        inner += ["--model", args.model]
    if args.effort:
        inner += ["--effort", args.effort]
    for kv in args.env:
        inner += ["--env", kv]
    if args.timeout:
        inner += ["--timeout", str(args.timeout)]
    # A pane stuck in a tail/pager swallows typed text: interrupt whatever is in the foreground first.
    subprocess.run(["herdr", "pane", "send-keys", pane, "C-c"], check=False)
    time.sleep(0.5)
    subprocess.run(["herdr", "pane", "run", pane, f"cd {HERE} && clear && {' '.join(inner)}"], check=True)
    out = RUNS / args.label
    deadline = time.time() + args.wait
    while time.time() < deadline:
        if (out / "stdout.txt").exists() and "[PREPARE]" in (out / "stdout.txt").read_text():
            print(f"[LAUNCH] started in pane {pane}: {out}")
            return 0
        time.sleep(1)
    print(f"[LAUNCH] FAILED: no [PREPARE] in {out / 'stdout.txt'} after {args.wait}s. Read the pane: herdr pane read {pane}")
    return 1


def verdict(label: str) -> str:
    out = RUNS / label
    report = out / "report.json"
    if not report.exists():
        state = "running" if not (out / "exit.txt").exists() else f"exit {(out / 'exit.txt').read_text().strip()} without report"
        return f"{label}: {state} ({out})"
    r = json.loads(report.read_text())
    ok = r.get("verified") or {}
    accepted = [f for f in r.get("follow_ups", []) if f.get("accepted") and not f.get("chained_as_unit")]
    rejected = [f for f in r.get("follow_ups", []) if not f.get("accepted")]
    passed = (out / "exit.txt").read_text().strip() == "0" if (out / "exit.txt").exists() else None
    units = r.get("units") or []
    lines = [
        f"{label}: {'PASS' if passed else 'FAIL' if passed is False else 'report present'} "
        f"harness={r.get('harness')} model={r.get('model') or 'default'} effort={r.get('effort') or 'default'} {r.get('duration_seconds')}s "
        f"units={len(units) or 1}/{r.get('max_units', '-')}",
        f"  bug fixed:   {'yes' if ok.get('ok') else 'no'} ({ok.get('message', '')})",
        f"  changed:     {', '.join(r.get('changed_files') or []) or 'nothing'}",
        f"  clone:       {r.get('worktree')}{'  (deleted, patch.diff kept)' if not Path(r.get('worktree') or '/nonexistent').exists() else ''}",
        f"  follow-ups:  {len(accepted)} accepted and left, {len(rejected)} rejected, "
        f"{sum(1 for f in r.get('follow_ups', []) if f.get('chained_as_unit'))} chained",
    ]
    for u in units:
        lines.append(f"  unit {u['unit']}: {'PASS' if u.get('passed') else 'FAIL'} test={u.get('test_file')} "
                     f"changed={', '.join(u.get('changed_files') or []) or 'nothing'}")
    for f in r.get("follow_ups", []):
        if f.get("chained_as_unit"):
            lines.append(f"    > unit {f['chained_as_unit']} <- {f.get('title', '')[:100]}")
    for f in accepted:
        lines.append(f"    + {f.get('title', '')[:110]}")
        lines.append(f"      paths={', '.join(f.get('paths') or [])} red_test={f.get('red_test')}")
        lines.append(f"      not chained: {f.get('chain_reason', 'chaining not evaluated')}")
    for f in rejected:
        lines.append(f"    - {f.get('title', '')[:110]} ({f.get('reason')})")
    return "\n".join(lines)


def cmd_show(args) -> int:
    print(verdict(args.label))
    return 0


def cmd_list(args) -> int:
    if not RUNS.exists():
        print(f"no runs under {RUNS}")
        return 0
    for d in sorted(p for p in RUNS.iterdir() if p.is_dir() and not p.name.startswith("_")):
        print(verdict(d.name).splitlines()[0])
    return 0


def cmd_clean(args) -> int:
    launch = load_launch(args.config)
    repo = Path(launch["repo"]).resolve()
    run_root = Path(launch.get("run_root") or ".worktree")
    root = run_root if run_root.is_absolute() else repo / run_root
    listed = subprocess.run(["git", "worktree", "list", "--porcelain"], cwd=repo, capture_output=True, text=True).stdout
    worktrees = {Path(l.split(" ", 1)[1]).resolve() for l in listed.splitlines() if l.startswith("worktree ")}
    targets = sorted(root.glob("atm-run-*")) if root.exists() else []
    if not targets:
        print(f"nothing to clean under {root}")
        return 0
    live = clones_in_use()
    for t in targets:
        kind = "worktree" if t.resolve() in worktrees else "clone"
        if t.resolve() in live:
            print(f"keeping {kind} (run {live[t.resolve()]} has not finished): {t}")
            continue
        print(f"{'would remove' if not args.yes else 'removing'} {kind}: {t}")
        if not args.yes:
            continue
        if kind == "worktree":
            subprocess.run(["git", "worktree", "remove", "--force", str(t)], cwd=repo, check=False)
        shutil.rmtree(t, ignore_errors=True)
    if args.yes:
        subprocess.run(["git", "worktree", "prune"], cwd=repo, check=False)
    else:
        print("re-run with --yes to remove them")
    return 0


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("run", help="launch one run under a fresh label")
    r.add_argument("--config", required=True)
    r.add_argument("--label", required=True, help="output directory name under .tmp/harness-worker; must be new")
    r.add_argument("--harness", choices=["claude", "codex", "opencode", "pi"], required=True)
    r.add_argument("--env", action="append", default=[], metavar="KEY=VALUE", help="passed to the worker as --env")
    r.add_argument("--model")
    r.add_argument("--effort", choices=["low", "medium", "high", "xhigh", "max"])
    r.add_argument("--timeout", type=int)
    r.add_argument("--pane", help="herdr pane id; launch there and verify [PREPARE] appears")
    r.add_argument("--wait", type=int, default=30, help="seconds to wait for [PREPARE] with --pane")
    r.set_defaults(fn=cmd_run)
    s = sub.add_parser("show", help="verdict of one run")
    s.add_argument("--label", required=True)
    s.set_defaults(fn=cmd_show)
    l = sub.add_parser("list", help="one verdict line per run")
    l.set_defaults(fn=cmd_list)
    c = sub.add_parser("clean", help="remove the target repo's atm-run-* clones and worktrees")
    c.add_argument("--config", required=True)
    c.add_argument("--yes", action="store_true")
    c.set_defaults(fn=cmd_clean)
    args = p.parse_args(argv)
    os.chdir(HERE)
    return args.fn(args)


if __name__ == "__main__":
    sys.exit(main())
