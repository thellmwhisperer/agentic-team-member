# ATM, Agentic Team Member

> The coding agent is one node. Every edge is decided by code.

![Status: alpha](https://img.shields.io/badge/status-alpha-orange)
[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

Give ATM an issue, from GitHub or a text file. It hands the issue to a coding
agent you already pay for (Claude Code, Codex, OpenCode or pi) inside an
isolated clone, with a written contract. Then code, not the agent, decides:
does the new test fail without the fix and pass with it, do the configured
checks still pass, and does the task type's proof hold. A green run gets one
more agent call, the slop detector, that may only cut. Then the delivery and
code review gate takes the branch.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/pipeline-dark.svg">
  <img alt="ATM pipeline: issue, isolated clone, contract, coding agent, checks. FAIL stops the run. PASS goes to the slop detector, a code quality gate where a model may only cut, then to the delivery and code review gate. The documented example of that gate is no-mistakes, a graph of its own where review and document are models, test and PR are code with a model part, and rebase, lint and CI call a model only after a fix answer. A passed unit with a proven follow-up goes back to the agent, at most three units." src="docs/assets/pipeline-light.svg" width="1000">
</picture>

## A real run

Issue 218 of this repository, run 218-32 of the binary built from main at
`c0a1d53`, 8 October 2026, Claude Opus 5.5 at high effort from
`~/.config/atm/config.yaml`. 54 min 15 s in all: agent 11 min 16 s, 30 tool
calls; red and green verified; the slop detector kept 1 cut and re-proved it;
delivery through no-mistakes opened
[PR 244](https://github.com/thellmwhisperer/agentic-team-member/pull/244),
CI green on ubuntu, macOS and Windows, merged, issue closed. It was the second
run of the issue: the first, 218-29, failed at checks because the agent
shortened an existing test file.

The same run outside a terminal, `atm attach 218-32` piped. Its output:

```text
issue     started
issue     passed 0.3 s
clone     started
clone     passed 1.0 s
contract  started
contract  passed 0.0 s
agent     started
agent     passed 11 min 16 s
checks    started
checks    passed 8 min 20 s
ponytail  started
ponytail  passed 11 min 35 s
delivery  started
delivery  passed 22 min 59 s
issue     ✓ 0.3 s
clone     ✓ 1.0 s
contract  ✓ 0.0 s
agent     ✓ 11 min 16 s
checks    ✓ 8 min 20 s
ponytail  ✓ 11 min 35 s
delivery  ✓ 22 min 59 s
RESULT  PASS
report  .atm/runs/218-32/report.json
```

The red/green proof confirmed the fix. The slop detector kept one cut, and
every check ran again before it was kept.

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

## Install

Download the archive for your platform from
[Releases](https://github.com/thellmwhisperer/agentic-team-member/releases)
(macOS and Linux on amd64 and arm64, Windows on amd64) and put `atm` on your
`PATH`, or build it with the Go version in [go.mod](go.mod):

```bash
go install github.com/thellmwhisperer/agentic-team-member/cmd/atm@latest
```

You also need one coding agent CLI (`claude`, `codex`, `opencode` or `pi`),
and `gh` for GitHub issues.

## Run it

```bash
cd /path/to/your/repo
atm init     # writes .atm.yaml: fill in its commands and file patterns
atm doctor   # checks git, the agent CLI, gh and .atm.yaml
atm run 300  # or: atm run plans/retry-on-timeout.md
```

`.atm.yaml` declares the repository's `install`, `test`, `typecheck` and
`lint` commands, how to run one test, which files are tests and docs, and the
`delivery` command; `~/.config/atm/config.yaml` picks your agent, model and
effort. Both are in [Configuration](docs/configuration.md).

| Command | Does |
|---------|------|
| `atm init` | Writes a commented `.atm.yaml` and ignores `.atm/` |
| `atm doctor` | Checks that a run can start |
| `atm run [flags] <issue>` | Runs an issue: a GitHub number or a file with a `Type:` line |
| `atm status` | The runs going |
| `atm attach [run]` | A run's screen until it ends |
| `atm runs` | Every retained run and its report |
| `atm axi run`, `atm axi status`, `atm axi runs` | The same for programs, in TOON or JSON |

A run goes on in the background: leaving its screen or closing the terminal
does not stop it.

| Exit | Meaning |
|------|---------|
| `0` | Every unit passed, and delivery, if configured, exited 0 |
| `1` | A unit failed; nothing is delivered |
| `2` | Configuration, issue, clone or contract failed |
| `4` | Every unit passed, but the delivery command exited non-zero |

## Read more

- [Configuration](docs/configuration.md): both YAML files, the commands and their flags, what a run leaves on disk.
- [How a run flows](docs/how-a-run-flows.md): each step, what each task type proves, the report.
- [ATM maintains itself](docs/self-maintenance.md): this repo's own fixes go through ATM; the hooks that hold the loop.
- [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md) · [Code of conduct](CODE_OF_CONDUCT.md)

## Status

Alpha. Each target repository declares its own install, test, typecheck and
lint commands in `.atm.yaml`; ATM does not detect a language's tooling. The
contract, the report and the flags can still change. Open work is in the
[issues](https://github.com/thellmwhisperer/agentic-team-member/issues).

Apache 2.0. [LICENSE](LICENSE) · [NOTICE](NOTICE)
