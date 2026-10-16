---
name: atm
description: Launch ATM runs, act on their streamed events, judge no-mistakes findings against the issue, and follow delivery through merge.
---

# Drive ATM

Before the first run, run `atm doctor` in the project. Check `.atm.yaml`
declares the project's checks and delivery command, and that `no-mistakes`
is reachable on PATH. Resolve failing prerequisites. A separate watcher is
not needed when using the followed launch below.

Launch each ready issue with your harness's background-process facility:

```sh
atm run <issue> --follow
```

Keep one background process per run and consume its stdout as JSON lines.
Keep the process alive through delivery and merge. Every line carries
`time`, `run`, `issue` and `event`; act on each line for that run:

| Event | Meaning | Action |
|-------|---------|--------|
| `step` | A step starts; `step` names it. | Track progress and keep following. |
| `delivery` | Delivery starts; `clone` gives its directory. | Keep the clone path for gate decisions. |
| `gate_waiting` | The gate waits; `step`, `clone` and `findings` identify the decision. | Read each finding and the issue; respond to no-mistakes from that clone. |
| `pr_opened` | `url` identifies the PR. | Read its diff and track its checks. |
| `gate_fixing_ci` | `commit` names a CI repair by `sha` and `subject`. | Read the full commit and judge it against the issue before merge. |
| `ci_green` | The PR's checks pass. | Verify every gate commit has been read and the issue's acceptance criteria still hold before merge. |
| `stalled` | No new event; `minutes` measures the silence. | Inspect the run and gate for a pending decision or failure. |
| `merged` | The PR merged; the process exits 0. | Record completion and launch the next ready issue. |
| `closed` | The PR closed without merging; the process exits 1. | Investigate the closure before scheduling dependent issues. |
| `run_failed` | The run or gate failed; `step` and `reason` explain it. | Read the failure, resolve its cause, and reconsider readiness. |

For a gate decision, inspect `no-mistakes axi status` from the reported clone.
Judge each finding against the issue's scope, acceptance criteria and
invariants. Accept required fixes; explain why an irrelevant finding does
not apply. Use no-mistakes' decision commands from that same clone, with the
finding's ID and your reason. Do not approve findings without reading them.
Read every gate commit's full diff before allowing merge, including repairs
made after the initial review.

Also inspect stderr and the process exit: launch failure exits 2 without an
event; interruption or delivery without a PR can exit 1 without a final
event. Interruption stops following, not the run. Reattach observation with
`atm watch --adopt <run>` before continuing supervision.
