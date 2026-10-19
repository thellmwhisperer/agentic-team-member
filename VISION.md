# Vision

## What ATM is

ATM is a state machine with a model in some of its nodes. The coding agent is
one node; every edge is decided by code. One run delivers one issue. The
delivery gate, no-mistakes in this project, is a second state machine of the
same kind.

## The boundary with the gate

- The `atm` binary emits events. It knows nothing about the gate.
- `atm attach` paints ATM's events and, once they end, the gate's events until
  the end of the run. It reads as one pipeline; inside, it is two programs.
- ATM does not change the gate's code and does not copy it.

## The screen

- One tab, one panel, one pipeline: ATM's steps first, the gate's steps below.
- The right column always shows what is happening now, whether an agent is
  working or a check is running.
- While the gate runs, the right column shows what the gate emits. The footer
  offers approve, fix, skip, abort and one line of text. ATM does not rebuild
  the gate's own interface.

## A clean core

- The binary does what a run needs and nothing more. Conveniences for one
  terminal or one machine live in scripts outside the binary.
- Every decision ATM takes is written to the run on disk, not only shown on
  screen.

## What counts as proof

- An end-to-end test that drives the real binary is the final test. Unit and
  integration tests can pass while the product is broken.
- A changed line that no real `atm` run executes fails the run: it is either
  code to delete or a missing end-to-end test.
- A run that ends with no final report and no change is red.
- Each new binary from `main` is used at once. Using it is the QA.

## The contract

- Agents follow the contract, and ATM rejects what does not. When a report is
  malformed or a required proof is missing, ATM returns it to the same agent
  session with a fixed message, so the work is not thrown away.
- Review always reads the brief.
- No work is lost.

## Workers

- A worker starts with a curated kit of skills and hooks, chosen for the task.
- A worker has a time limit, as the gate's steps do.

## Before a run

- `main` must be green, checked with one call to the latest CI run of `main`.
  With `main` red, no run starts.

## Open

These are proposals, not decisions:

- Keep the work when an agent dies mid-run, not only when it ends well.
- A preflight of about six checks before any token is spent.
- Which skills and hooks make up the worker kit, and how they reach the clone.
