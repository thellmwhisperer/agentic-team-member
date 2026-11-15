---
name: atm
description: Launch ATM runs, read their JSON events, and inspect their reports and delivery output.
---

# Drive ATM

Before the first run, run `atm doctor` in the project. Check that `.atm.yaml`
declares the project's checks and delivery command. Resolve failing
prerequisites before starting a run.

Launch each ready issue with your harness's background-process facility:

```sh
atm run <issue> --follow
```

Consume stdout as JSON lines until the process ends. Each line is a run
event with `step` and `ts`. A step starts with `state: started` and ends with
`state: passed` or `state: failed`; failures include `error`. Live events
inside a step carry check results, agent activity or command `output`.
The clone step's result includes `clone` and `sha`; delivery includes `branch`.
Keep the run's report and delivery output for review.

ATM executes the configured delivery command without parsing its output or
tracking its reviews, pull requests or merges. The launcher owns decisions
and monitoring beyond the run, using the delivery tool's own interface.
A successful run means that its checks and configured delivery completed;
it does not imply that a pull request merged.

Inspect stderr and the process exit as well as events. Exit 0 means success,
1 means an agent or checks failure, 4 means delivery failed, and 2 means
another failure, including launch. Interruption exits 1 and stops following
without stopping the run. Use `atm attach <run>` to see the run again.
