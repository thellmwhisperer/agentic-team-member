# ATM, Agentic Team Member

> The coding agent is one node. Every edge is decided by code.

![Status: alpha](https://img.shields.io/badge/status-alpha-orange)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/pipeline-dark.svg">
  <img alt="ATM pipeline: issue, isolated clone, contract, coding agent, checks. FAIL stops the run. PASS goes to the slop detector, a code quality gate where a model may only cut, then to the delivery and code review gate. The documented example of that gate is no-mistakes, a graph of its own where review and document are models, test and PR are code with a model part, and rebase, lint and CI call a model only after a fix answer. A passed unit with a proven follow-up goes back to the agent, at most three units." src="docs/assets/pipeline-light.svg" width="1000">
</picture>

Give ATM an issue, from GitHub or a text file. It hands the issue to a coding
agent you already pay for (Claude Code, Codex, OpenCode or pi) inside an
isolated clone, with a written contract. Then code, not the agent, decides:
does the new test fail without the fix and pass with it, do the configured
checks still pass, and does the task type's proof hold. A green run gets one
more agent call, the slop detector, that may only cut. Then the delivery and
code review gate takes the branch.

## A real run

An earlier Python-worker run on issue 130 of this repo, 5 October 2026,
used Claude Opus 5.5 at high effort. It reported steps live and required a
monitor. 14 min 01 s,
55 agent turns, five red tests first, red and green verified, 3 cuts kept by
the slop detector, [PR 136](https://github.com/thellmwhisperer/agentic-team-member/pull/136)
merged, issue closed.

```text
PREPARE   worktree .worktrees/atm-run-20261005-225238-2   base 670a60faa0   tests python3 -m pytest

AGENT  unit 1 claude
  ✎ Reading the worker, the renderer and the tests before writing the red test.
  ✎ Writing the red tests first.
  ▶ #13 Edit  tests/test_harness_worker.py                                    ✓ ok
  ▶ #17 Bash  cat >> tests/test_tail_run.py <<'EOF' def test_tail_renders_step_starts_ends_…
  ▶ #18 Bash  python3 -m pytest -q tests/test_harness_worker.py -k "full_suite_says"   ✗ AssertionError: '▶ full suite' not in …
  ✎ All five red. Now the worker: a `RunLog` that replaces `make_logger`.
  ▶ #19 … #31 Edit  agentic_tdd_runner/harness_worker.py                         ✓ ok
  ✎ Now wiring the steps into `main`.
  ✎ Now the ponytail pass.
  ✎ Now the renderer side in `scripts/tail-run.py`.
  ▶ #38 Bash  python3 -m pytest -q tests/test_tail_run.py tests/test_harness_worker.py  ✓ ok
  ▶ #43 Edit  config/agent.toml                                                 ✓ ok
  ✎ Aviso: I edited the README of the main checkout by mistake, not the worktree one. Reverting it there and applying it in the worktree.
  ✎ No other launcher in the repo. Running the full lint and the full suite.
  ✓ agent finished  turns=55

RED / GREEN VERIFICATION
  ✓ without the fix   test fails    4 failed, 64 passed in 1 min 34 s
  ✓ with the fix      test passes
  ✓ quality  ✓ gate  ✓ full suite  ✓ scope  ✓ follow-up validation

AGENT  ponytail claude
  ✎ harness_worker.py: `elif run_log.steps and …` is now a plain `else`; verify_red_green always prints the half (speculative_hardening)
  ✎ harness_worker.py: removed the `self.steps and` guard in `RunLog.tick`; both callers check under the lock (speculative_hardening)
  ✎ harness_worker.py: inlined the `NO_MONITOR` constant into its one `print` (speculative_feature)
  ✓ agent finished  turns=6
  ✓ ponytail re-checks   red/green, quality, gate, full suite, scope: all green again

SUMMARY
  harness   claude model=claude-opus-5-5 effort=high exit=0
  duration  14 min 01 s   units=1/3
  changed   harness_worker.py, tail-run.py, agent.toml, README.md, test_harness_worker.py, test_tail_run.py   +369 −56
  checks    ✓ red/green   ✓ quality   ✓ gate   ✓ scope   ✓ full suite   – typecheck n/a
  ponytail  kept, 1 net line saved, 3 tombstones written
✓ RESULT  PASS
```

The agent's own red (`#18`) is a courtesy. The verdict is the RED / GREEN
block: ATM parks the fix, runs the test, brings the fix back, runs it again.
The `Aviso` line is the agent catching itself editing outside the clone; the
scope check would have caught it too. This was the Python worker's original
live-step implementation. The Go worker has its own report and screen.

## Who decides what

| Step | Who | Says no to |
|------|-----|-----------|
| Clone and contract | code | a failed clone or install, an incomplete configuration |
| Write the fix | **model** | nothing: it decides nothing |
| Red / green | code | a test that passes without the fix or fails with it |
| The project's configured `install`, `test`, `typecheck` and `lint` commands | code | any failure |
| Follow-ups | code | a gap without a red test |
| Slop detector (code quality gate, not code review) | **model**, may only cut | a cut that breaks any check or does not shrink the diff: the clone goes back |
| Delivery and code review gate | your command | ATM branches, commits and runs it. The documented example is no-mistakes, a graph of its own: models review, judge the test evidence, update the docs and write the PR; code rebases, runs your test and lint commands, pushes and watches CI |

Step by step: [How a run flows](docs/how-a-run-flows.md).

## Run it

Download the archive for your platform from
[Releases](https://github.com/thellmwhisperer/agentic-team-member/releases) and
put `atm` in your PATH, or build it with the Go version in [go.mod](go.mod):

```bash
go install github.com/thellmwhisperer/agentic-team-member/cmd/atm@latest

cd /path/to/your/repo
atm init
# Fill in the required commands and file patterns in .atm.yaml.
atm run 300
# Or: atm run plans/retry-on-timeout.md
```

Run it from the target repository. A local issue file needs a `Type:` line; see
[Configuration](docs/configuration.md).
`atm run` shows the run on a terminal and returns its label off a terminal;
`atm status`, `atm attach` and `atm runs` let you follow it. `atm axi run` waits
for the result when another program launches ATM.

| Exit | Meaning |
|------|---------|
| `0` | Every unit passed, and delivery, if configured, exited 0 |
| `1` | A unit failed; nothing is delivered |
| `2` | Configuration, issue, clone or contract failed |
| `4` | Every unit passed, but the delivery command exited non-zero |

The target repository retains run reports; [Configuration](docs/configuration.md)
describes their location and cleanup. Delivery runs the `delivery` command
in `.atm.yaml`; its configuration example hands the branch to
[no-mistakes](https://github.com/kunchenguid/no-mistakes).
Leave it empty to skip delivery.

## Read more

- [How a run flows](docs/how-a-run-flows.md): step order and implementation.
- [Configuration](docs/configuration.md): YAML keys, run flags and detailed behavior.
- [ATM maintains itself](docs/self-maintenance.md): this repo's own fixes go through ATM; the hooks that hold the loop.
- [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md) · [Code of conduct](CODE_OF_CONDUCT.md)

## Status

Alpha. Each target repository declares its own install, test, typecheck and
lint commands in `.atm.yaml`; ATM does not detect a language's tooling. The
contract, the report and the flags can still change. Open work is in the
[issues](https://github.com/thellmwhisperer/agentic-team-member/issues).

Apache 2.0. [LICENSE](LICENSE) · [NOTICE](NOTICE)
