# How a run flows

`atm run <issue.md | number>` starts in the repository it will work on. It
sends the run to a background process, which reads `.atm.yaml` before the
issue. [Configuration](configuration.md) owns the flags, commands, proofs,
report fields and background-run details.

| Step | Implementation | Result |
|------|----------------|--------|
| Issue | `run.readIssue` | Reads a local file or GitHub issue and its `Type:` |
| Clone | `run.clone` | Resolves the base ref, makes an isolated clone under `.atm/clones/`, runs `install` |
| Contract | `run.writeBrief` | Writes `brief.md` for the agent, with the issue and configured commands |
| Agent | `agent.run` | Runs the selected coding agent in the clone and records its report and log |
| Checks | `run.checks` | Verifies the task type's proof, then runs configured `install`, `test`, `typecheck` and `lint` commands |
| Ponytail | `agent.ponytail` | Lets the slop detector cut the diff; keeps a cut only when the checks still pass |
| Delivery | `run.deliver` | Commits the work on a branch and runs the configured `delivery` command, if set |

The agent and checks steps repeat for a proven follow-up already covered by
the issue, up to three units in one run. Other accepted follow-ups go to
`follow-ups.json`. A failed step stops later steps. [Configuration](configuration.md)
describes the retained report and clone cleanup.

`atm run` shows a live screen on a terminal. Off a terminal it prints the run
label. `atm status` shows active runs, `atm attach [run]` reconnects to a run,
and `atm runs` lists retained runs. `atm axi run` waits and returns a structured
outcome for another program.
