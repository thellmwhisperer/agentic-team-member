# Contributing to ATM

ATM is small and alpha. The contributions that help most are concrete: a
repository and an issue where a run failed, a gate that let something through,
a language whose tests ATM cannot yet run.

## Filing an issue

ATM runs on issues, including its own, so an issue here is a contract. Give
it:

- **Title**: what should be true and is not.
- **Evidence**: what happened. For a failed run, the `report.json` of the run
  and the relevant lines of `worker*.jsonl`, with the harness and model
  named. Strip paths and hostnames you do not want published.
- **Acceptance criteria**: numbered, each one checkable by a test.
- **Red test**: the test that fails today, or a sentence describing it.

An issue without a red test gets one before any code is written.

## Development setup

```bash
git clone https://github.com/<your-fork>/agentic-team-member.git
cd agentic-team-member
git config core.hooksPath .githooks
scripts/test.sh
```

The suite runs real git and fake harness subprocesses; expect about two minutes.
The pre-commit hook runs slopslint; its first run downloads the pinned binary
into `.tmp/slopslint/` and needs the network.

To run ATM on this repository you need one coding agent CLI (`claude`,
`codex`, `opencode` or `pi`) and, for delivery, `no-mistakes`. See
[docs/self-maintenance.md](docs/self-maintenance.md).

## Making a change

The preferred path is the pipeline: open the issue, run ATM on it, let
no-mistakes open the PR, read it before it merges. A hand-made PR is welcome
when the pipeline cannot do the job yet; say so in the PR body.

Either way:

1. Branch off `main`. One change per PR.
2. Write the failing test first, then the fix. Red/green is what ATM checks
   in every target and what reviewers check here.
3. Do not weaken, skip or delete a test to make a change pass. If a test is
   wrong, say so in the PR and change it in the open.
4. Keep the diff as small as the change allows. No scaffolding for later, no
   speculative hardening, no helpers nobody calls. The repository's slopslint
   ceilings only go down.
5. `scripts/test.sh` and `scripts/lint.sh` must pass locally. Both run the
   Go side too (`go test ./...`, `golangci-lint run`) when `go.mod` exists.
6. PR titles use a conventional-commit subject (`fix:`, `feat:`, `docs:`,
   `chore:`, `refactor:`, `test:`). The body says why and links the issue.

## CI

| Workflow | Runs on | Runs when these change | Does |
|---|---|---|---|
| `go.yml` | every PR to `main`, push to `main` | Go paths, `go.yml`, or `scripts/changed-areas.sh`, when `go.mod` exists | `make test` and `make lint` on ubuntu, macos, windows |
| `auto-merge.yml` | every PR to `main` | always | arms auto-merge (below) |

`go.yml` starts on every PR and skips its test jobs when its paths did not
change. It reports one summary check that is always present: **Go checks**.
The summary is green when its tests passed or were skipped, red when they
failed or were cancelled (`scripts/ci-summary.sh`). It is the required check
on `main`; a
path-filtered workflow that never starts would leave its check pending and
block the merge.

Auto-merge by rebase is armed when the PR body's `## Risk Assessment` says
Low or Medium, and GitHub merges once CI is green. High risk, or a level it
cannot read, waits for the `risk-reviewed` label. Before adding it, whoever
launched the run writes one comment:

```
Risk reviewed:
<finding id>: fixed | accepted | dismissed - <one-line reason>
```

The label is the switch; the comment is the record.

## What is accepted

- **Yes**: bug fixes with a red test, a new gate with the slop it catches
  documented, test-runner detection for a framework ATM misreads, language
  support (environment prep, test and typecheck commands, quality tools) with
  a real repository it was tried on, documentation that matches the code.
- **Discuss first**: changes to the contract the agent receives, to the
  report format, to the CLI flags, a new model call anywhere in the pipeline.
- **No without prior discussion**: heavy dependencies, a different test
  framework, large refactors, anything that lets a model decide an edge of the
  pipeline.

## Licensing of contributions

By submitting a pull request you agree that your contribution may be released
under the project's [Apache 2.0 license](LICENSE). No CLA is required.

## Code of conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).

## Security

Do not open public issues for vulnerabilities. See [SECURITY.md](SECURITY.md).
