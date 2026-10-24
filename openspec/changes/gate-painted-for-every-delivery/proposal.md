## Why

Issue #509 (closed 21-oct-2026) paints the delivery gate in `atm attach`: ATM's own
`.atm.yaml` launches `no-mistakes axi run` in the background and prints
`no-mistakes axi status` while it waits, and the screen paints those lines in its
right column. That works for a project whose delivery command is written that way. A
project whose delivery command opens the no-mistakes screen itself, with
`no-mistakes attach`, gets a different run: the delivery step (`internal/run/run.go`,
`r.step("delivery", …)`, which runs the command through `tee` on the attached
terminal) ends `failed` with `signal: terminated`, the screen keeps `Delivery pending`
while the gate is in fact running in CI, and the gate's text lands in
`delivery-output.txt` instead of the screen. ATM's screen says nothing of the gate.

Who asked: Javi, 24-oct-2026 ~17:45 BST, decision 8161, literal: "atm dentro de su
tui deberia leer los eventos del binario de no mistakes e integrarlo en su tui, no
coartar la tui de atm y sacar la de nm". Decisions 7609 and 7685 say the same: the
gate belongs in ATM's screen, read from the no-mistakes binary, for every project.

Proof: run 550-5 of la-roca, 24-oct-2026, `report.json`: delivery `failed`,
`signal: terminated`, while PR 564 was in CI and later merged; the no-mistakes screen
text was inside the run's `delivery-output.txt`. la-roca's `.atm.yaml` of that day
ends its delivery with `{ no-mistakes attach || true; }`. Reproduce: a project whose
`.atm.yaml` delivery ends with `no-mistakes attach`, then `atm run <issue>` and
`atm attach`.

## What Changes

- ATM finds the no-mistakes run of its delivery by itself, through
  `no-mistakes axi status` or the launch receipt the run writes beside `report.json`,
  and paints its steps, log and findings in the right column until the run ends. A
  project's delivery command only needs to launch the run.
- A delivery command that runs `no-mistakes attach` is refused at `atm run` start
  with a message naming the line and the replacement, so no run ends `signal:
  terminated` for that reason.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `delivery-gate`: how the gate reaches ATM's screen, for every project's delivery
  command.

## Impact

- Code: `internal/run` (delivery step and the gate reader `atm attach` uses),
  `internal/tui` (right column), `docs/configuration.md` (the `.atm.yaml` delivery
  reference).
- End-to-end tests to write, run by `make check`: a `TestReal*` under `internal/run`
  with a project-style delivery that only launches a fake no-mistakes run, asserting
  the right column paints the fake daemon's steps and lines; and one whose delivery
  ends with `no-mistakes attach`, asserting `atm run` refuses it before the clone and
  names the line. la-roca's `.atm.yaml` of 24-oct-2026 is the fixture.
- Not touched: no-mistakes itself; the keys of #482; the report JSON on the screen and
  the ponytail rows, which have their own changes.
