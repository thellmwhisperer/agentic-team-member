# ATM maintains itself

This page is for whoever maintains ATM itself. A fix to this repository goes
through the same pipeline as a fix to any target: an issue, an ATM run, a
delivery gate, a merge that needs no hand. Here is that loop and what holds
it.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/self-maintenance-dark.svg">
  <img alt="The loop. An issue with acceptance tests goes to an ATM run. A failed check first asks the launcher, and stop keeps the work for a later run. PASS goes to the no-mistakes gate; its findings go to whoever launched the run, who answers them, then the gate opens a PR. Required risk or no-mistakes review precedes auto-merge. Merged on main, the issue closes." src="assets/self-maintenance-light.svg" width="900">
</picture>

## The loop

1. **An issue with a contract.** Follow
   [the issue requirements](configuration.md#the-issue), with evidence and a
   `## Acceptance` section naming the test that demonstrates the result.
   The agent gets exactly this text; a vague issue gives a vague run.
2. **An ATM run.** Start `atm run <number> --follow` in the launching session. Fill `.atm.yaml` once with the
   repository commands, file patterns and the no-mistakes delivery line; see
   [following one run](how-a-run-flows.md#following-one-run) and [Configuration](configuration.md).
3. **The checks.** A failed check [asks the launcher](how-a-run-flows.md#checks).
   The final [report](how-a-run-flows.md#the-report) also covers the
   [delivery gate](how-a-run-flows.md#delivery).
4. **The gate.** The green branch goes through the
   [configured delivery line](configuration.md#atmyaml).
   It saves the attempt's nonce beside `report.json` and uses the report
   directory's name as its validation generation. On recovery, the same attempt
   replays the run bound by its saved launch receipt, including after an
   interrupted launch or a replay that failed before another receipt. The
   command follows that run with `axi status --run` until a terminal result;
   a PR ready to merge or a decision is not a terminal result. It prints each
   new gate and keeps following the same run while its operator answers. It
   passes only on `completed` with outcome `passed`, `passed-with-override` or
   `passed-with-skips`, and prints that result. A failure, cancellation,
   missing outcome or query error fails delivery. An active run
   the attempt cannot prove its own is left untouched, and delivery fails
   without launching another run.
   Review, targeted test, lint and document run as fenced model steps. The reviewer
   only reports (`auto_fix.review: 0`); whoever launched the run answers each
   finding, fixing real defects and dismissing guesses.
5. **The merge.** `auto-merge.yml` arms `gh pr merge --auto --rebase` after
   any required review. GitHub merges when the checks pass, then closes the
   issue and deletes the branch. See [the merge conditions](../CONTRIBUTING.md#ci)
   and [the no-mistakes review circuit](../skills/no-mistakes-review/SKILL.md).

## What holds the line

| Mechanism | Where | What it does |
|-----------|-------|--------------|
| `go.yml` | GitHub Actions | Change-filtered tests and lint, summarised in the required **Go checks**; see [CI](../CONTRIBUTING.md#ci) |
| `auto-merge.yml` + `scripts/arm-auto-merge.sh` | GitHub Actions | Manages `review-required` and `review-completed`, honours `pr-discarded`, and arms auto-merge with `AUTO_MERGE_TOKEN`, a fine-grained token for this repository only. The workflow token is never used: merges made with it start no workflow and close no issue |
| `scripts/test.sh` | local and ATM's Checks | Contribution tests; see [Contributing](../CONTRIBUTING.md#making-a-change) |
| `scripts/lint.sh` | local, ATM's Checks and the gate | Contribution lint; see [Contributing](../CONTRIBUTING.md#making-a-change) |
| `.githooks/pre-commit` | local, after `git config core.hooksPath .githooks` | The same slopslint check |
| `.no-mistakes.yaml` | no-mistakes | Configures lint and targeted review tests without a full-suite test command, tells the reviewer that only a defect against the issue's criteria or the checks is a finding, and allows one re-run of a CI check GitHub cancelled without a verdict |
| `.slop/config.yml`, `.slop/ceilings.yml` | slopslint | Two duplication scopes, production and tests, under ceilings that may go down and never up |
| `.slop/tombstones/` | slopslint | One record per slop incident, with its family and evidence |

For slopslint setup and offline ATM runs, see
[Development setup](../CONTRIBUTING.md#development-setup).

## The tombstones

`.slop/tombstones/` holds one YAML record per incident: the pattern, what went
wrong, the root cause, the rule it set, and the evidence as a slopslint family
with an example and the file. Records named `T-PONYTAIL-*` are written by the
slop detector, one per cut it kept. A tombstone is the shape the gate looks for
next time.

## Before a PR merges

Read `git log --oneline origin/main..<branch>` and the run's `report.json` for
the unit commit and any cuts; read `worker.jsonl` for the agent's notes. If the
gate added a `no-mistakes(...)` commit, follow
[the review circuit](../skills/no-mistakes-review/SKILL.md) before merging.
