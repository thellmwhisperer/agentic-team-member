# AGENTS.md

Read [VISION.md](VISION.md) first. When a rule here conflicts with a newer
decision, the newer one wins and the old rule is removed.

## Non-negotiable

- The ATM run writes no gate events or report fields; only the attach screen
  reads the gate. Conveniences for one setup live in scripts outside the binary.
- Every decision ATM takes is written to the run, not only to the screen.
- A changed line that no real `atm` end-to-end test executes fails. There are
  two ways out, in this order: delete the line, or write the end-to-end test
  that reaches it from a real command.
- Do not change no-mistakes, and do not copy its code into this repository.

## Issues

- This project follows OpenSpec. Every change starts as an OpenSpec change in
  `openspec/changes/`, written with the OpenSpec skills and valid under
  `openspec validate --strict`. `openspec/specs/` says how ATM behaves today;
  `openspec/config.yaml` holds the rules for each artifact.
- The issue comes from the change. Its body opens with `Type:` and
  `Where: binario|lanzador`, then `## Why` ending in Proof, `## Scenarios`
  copied from the spec's WHEN/THEN, `## Done when` naming the `TestReal*`
  end-to-end tests under `internal/run` that execute them, and `## Not touched`
  from Impact. After the merge, the change is archived into `openspec/specs/`.
- An issue answers three questions: what breaks, with proof; which test says
  it is done; what is not touched. An issue never grows.
- Acceptance runs inside the clone. Checks that only the operator can do after
  the PR go in their own section, never in Acceptance.
- An issue with no Go change is not an ATM run.
- The issue names the end-to-end test that reaches the code it changes, or the
  one to write.
- Several small fixes in one area go in one batch issue, with one acceptance
  line per fix.
- Every point of a brief quotes the requester or is marked as a proposal.

## Delivery

- no-mistakes is the gate, and its review is never skipped.
- no-mistakes commits are recognised by the title prefix `no-mistakes(`.
- When no-mistakes adds code, `review-required` blocks the merge. After that
  code is reviewed, `review-completed` lets it merge. If it cannot merge,
  `pr-discarded`, a comment on the PR saying why, and the issue updated.
  Labels and auto-merge are handled as described in
  [CONTRIBUTING.md](CONTRIBUTING.md).

## After each merge

- Rebuild and install the binary before the next run.
- Before a run, check that `main` is green as defined in
  [Before the run](docs/how-a-run-flows.md#before-the-run).
