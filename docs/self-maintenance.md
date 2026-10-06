# ATM maintains itself

A fix to this repository goes through the same pipeline as a fix to any
target: an issue, an ATM run, a delivery gate, a merge that needs no hand.
This page is that loop and what holds it.

## The loop

```mermaid
flowchart LR
    I[Issue with a red test] --> R[ATM run]
    R -->|PASS| G[no-mistakes gate]
    R -->|FAIL| I
    G -->|findings| H[whoever launched the run answers]
    H --> G
    G --> P[PR]
    P -->|Low or Medium risk| M[auto-merge]
    P -->|High risk| L[risk-reviewed label] --> M
    M --> C[CD on main, issue closed]
```

1. **An issue with a contract.** Title, evidence, acceptance criteria a test
   can check, and a `## Red test` section naming the test that fails today.
   The agent gets exactly this text; a vague issue gives a vague run.
2. **An ATM run.** A local config (`config/agent.*.local.toml`, ignored by git)
   with `timeouts.test_run` raised to 300, because
   `tests/test_harness_worker.py` runs real git and fake agents. `--scope` names
   the files the issue allows.
3. **The verdict.** `report.json`, or the summary at the end of the run. A FAIL
   ends here: the report says which check failed and why, and the issue gets a
   better contract or the code a better check.
4. **The gate.** The green branch goes to `no-mistakes axi run` without `--yes`.
   Review, test, lint and document run as fenced model steps. The reviewer
   only reports (`auto_fix.review: 0`); whoever launched the run answers each
   finding, fixing real defects and dismissing guesses.
5. **The merge.** `auto-merge.yml` arms `gh pr merge --auto --rebase` when the
   PR's `## Risk Assessment` says Low or Medium. High risk waits for a
   `Risk reviewed:` comment and the `risk-reviewed` label. GitHub merges once
   the PR checks pass, closes the issue and deletes the branch.

## What holds the line

| Mechanism | Where | What it does |
|-----------|-------|--------------|
| `pr.yml`, `cd.yml`, `go.yml` | GitHub Actions | Change-filtered checks; see [CI](../CONTRIBUTING.md#ci) |
| `auto-merge.yml` + `scripts/arm-auto-merge.sh` | GitHub Actions | Arms auto-merge with `AUTO_MERGE_TOKEN`, a fine-grained token for this repository only. The workflow token is never used: merges made with it start no workflow and close no issue |
| `scripts/test.sh`, `scripts/lint.sh` | local and the gate | Contribution checks; see [Contributing](../CONTRIBUTING.md) |
| `tests/test_slop_gate.py` | pytest | `scripts/slopslint.sh check --classify --enforce`, so every test run enforces it |
| `.githooks/pre-commit` | local, after `git config core.hooksPath .githooks` | The same slopslint check |
| `.no-mistakes.yaml` | no-mistakes | Runs the contribution checks, sets review instructions per path, and allows one re-run of a CI check GitHub cancelled without a verdict |
| `.slop/config.yml`, `.slop/ceilings.yml` | slopslint | Two duplication scopes, production and tests, under ceilings that may go down and never up |
| `.slop/tombstones/` | slopslint | One record per slop incident, with its family and evidence |
| `ruff.toml` | ruff | 120 columns, `E9` and `F` only |

`scripts/slopslint.sh` downloads the pinned slopslint release into
`.tmp/slopslint/`, checks its SHA256 and runs it. The first run needs the
network.

## The review instructions

`.no-mistakes.yaml` tells the review model what this repository has already
decided. Each line exists because a model got it wrong once:

- `agentic_tdd_runner/**`: branch names carry a timestamp to the second and
  every run has its own clone, so they are unique by construction: no uuid,
  nonce or random suffix, no tests for concurrency the design rules out. The
  delivery gate has a fixed `--wait` ceiling: no polling loops, never a loop
  without an upper bound. A speculative hardening is a note, not a change.
- `tests/**`: never delete or weaken a test the issue's acceptance criteria or
  red test ask for.
- `**`: run tests only through `scripts/test.sh`.

## The tombstones

`.slop/tombstones/` holds one YAML record per incident: the pattern, what went
wrong, the root cause, the rule it set, and the evidence as a slopslint family
with an example and the file. Records named `T-PONYTAIL-*` are written by the
ponytail pass, one per cut it kept. A tombstone is the shape the gate looks for
next time.

## Before a PR merges

Auto-merge is armed, so this is the last look, and it is short:

1. `git log --oneline origin/main..<branch>`: the `atm unit` commit, a
   `ponytail:` commit if a cut was kept, then the `no-mistakes(...)` commits
   the gate added.
2. `git show --stat` of each gate commit. Review and test are models: a uuid,
   a new loop, a deleted or mock-heavy test, a reformat to another width gets
   reverted on the branch, a tombstone, and another gate run.
3. The run's `report.json`: every check green, follow-ups with their reasons.

## Cleaning up

Each run leaves a full clone under the run root (`--run-root .worktrees` in this
repository's runs). ATM removes merged clones as described in
[How a run flows](how-a-run-flows.md#what-a-run-leaves-on-disk). Run
directories under [`[runs].dir`](configuration.md#runs) are the record of what happened.
