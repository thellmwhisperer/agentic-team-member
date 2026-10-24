## 1. ATM finds and paints the gate itself

- [ ] 1.1 In `internal/run`, make the gate reader `atm attach` uses find the delivery's no-mistakes run through `no-mistakes axi status` or the launch receipt beside `report.json`, instead of relying on what the project's delivery command prints.
- [ ] 1.2 In `internal/run`, refuse at `atm run` start a delivery command that runs `no-mistakes attach`: exit before the clone with a message naming the `.atm.yaml` line and the replacement.
- [ ] 1.3 Update `docs/configuration.md`: a project's delivery only launches the run; ATM paints it.

## 2. End-to-end tests

- [ ] 2.1 Write a `TestReal*` under `internal/run` with a project-style delivery that only launches a fake no-mistakes daemon and assert the right column paints its steps and lines. It fails on main and passes with the change.
- [ ] 2.2 Write a `TestReal*` whose delivery ends with `no-mistakes attach`, using la-roca's `.atm.yaml` of 24-oct-2026 as the fixture, and assert `atm run` refuses it before the clone and names the line.
- [ ] 2.3 `make check` passes: every changed line is executed by a real `atm` end-to-end test.
