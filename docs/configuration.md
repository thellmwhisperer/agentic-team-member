# Configuration

This page covers the two YAML files, the flags and issue for `atm run`, and
the JSON plan for `atm axi plan`. What a run does with its configuration is
in [How a run flows](how-a-run-flows.md).

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/config-layers-dark.svg">
  <img alt="Configuration layers, each overriding shared keys from the one before: built-in defaults (harness claude), then ~/.config/atm/config.yaml (see machine settings below), then .atm.yaml at the repository root (every command and pattern, and optionally harness, model, effort), then the flags of one atm run (--harness, --model, --effort)." src="assets/config-layers-light.svg" width="900">
</picture>

## Skill

`atm skill` prints the embedded [skills/atm/SKILL.md](../skills/atm/SKILL.md) verbatim. This project
skill is the only ATM driving skill ATM maintains; machine-specific overlays
are maintained separately.

`atm skill install` writes it to `~/.claude/skills/atm/SKILL.md`.
Use `--dir <path>` to choose another skills directory. Directory symlinks are
followed. An identical installation is left alone; different content is
refused with exit 2 unless `--force` is given. Neither skill command requires
a repository.

`atm doctor` reports `skill: ok`, `skill: missing`, or
`skill: outdated (run atm skill install --force)` for the default location.
This advisory line does not change the exit status of the prerequisite checks.

## Layers

Each layer overrides shared keys from the one before. A key not supported
in its file fails the run, except in `~/.config/atm/config.yaml`: there an
unknown key is ignored and named once on stderr (`config: ignored key <key>`);
a `.atm.yaml` key there still fails.

| Layer | Where | Sets |
|-------|-------|------|
| Built-in defaults | the binary | `harness: claude` |
| Your machine | `~/.config/atm/config.yaml` | `harness`, `model`, `effort` |
| The repository | `.atm.yaml` at its root | keys in [`.atm.yaml`](#atmyaml) below |
| One run | `atm run` flags | [`--follow`](how-a-run-flows.md#following-one-run), `--base-ref`, `--harness`, `--model`, `--effort`, `--harness-arg`, `--env`, `--resume` |

## `~/.config/atm/config.yaml`

Optional. It selects your agent.

| Key | Meaning |
|-----|---------|
| `harness` | The coding agent CLI: `claude`, `codex`, `opencode`, `pi`, `cursor` or `hermes` (local models go through `pi`). Any other fails the run |
| `model` | Its model; empty is the CLI's default |
| `effort` | Its effort; empty is the CLI's default |

```yaml
harness: claude
model: claude-opus-5-5
effort: high
```

ATM opens no terminal tab or split and reports to no terminal multiplexer: a
tab per run, a split for the delivery and agent reporting are the launcher's
job, outside ATM. The screen quits when its run ends, so a pane whose root
process is `exec atm attach <run>` exits with the run.

### Cursor

`atm doctor` checks `cursor-agent` on PATH, not the editor's `cursor`
command. See [Agent](how-a-run-flows.md#agent) for the command ATM runs and
how it checks skill use.

Cursor has no effort flag. Leave `effort` unset in every configuration layer
and put it in a parameterized model, for example `model: 'grok[effort=high]'`.
A configured effort fails before cloning with
`cursor has no effort flag: put it in the model, as '<model>[effort=<effort>]'`.

### Hermes

Hermes has no native headless mode ATM uses: ATM speaks ACP (Agent Client
Protocol, JSON-RPC over the agent's stdin and stdout) with it itself, without
acpx. It starts `hermes --ignore-rules acp` in the clone, with pipes and no
terminal, then sends `initialize` (protocol version 1, no `fs`, no
`terminal`), `session/new` in the clone with no MCP servers, and the brief as
one `session/prompt`. The step ends at the prompt's reply: `end_turn` is a
finished agent, any other `stopReason`, or a JSON-RPC error, fails the step
with it. Exiting without a prompt reply also fails, even with exit code zero.
Any other request from the agent gets `-32601 method not found`.
`--ignore-rules` prevents Hermes from automatically loading workspace rules
and skills outside the brief.

Permission requests get the first `allow_once` option when every location
and raw-input path value is inside the clone and no command reaches outside.
Path values are taken whole; relative values resolve against the clone.
Commands use the [post-step reach checker](how-a-run-flows.md#agent), with
all absolute paths outside the clone rejected for permission requests.
A read-only review's permission requests also allow its scratch directory,
the same single outside-the-clone allowance the post-step checker makes.
Otherwise ATM selects the first `reject_once`, never an `*_always` option.
If the required option is absent, ATM returns `cancelled`. Each answer is a
line in the agent's log. Tool calls remain subject to the post-step check,
including calls made without asking permission.

The ponytail skill goes in the brief's text; the agent's report proves it
with `"skill": "ponytail"`. The slop detector receives `ponytail-review` the
same way and must report `"skill": "ponytail-review"`.
Hermes takes its model and effort from its own config: a non-empty effective
`model` or `effort` after merging the layers fails before cloning with
`hermes takes model and effort from its own config; unset them in .atm.yaml`.
The next ACP agent is one entry in `config.ACP`: its command line and that
error.

## `.atm.yaml`

The repository declares every command a run needs. ATM detects nothing and
defaults nothing per language. Every key in the table below except `e2e` is required: an
empty value is a deliberate "none", a missing key fails with
`config incomplete`. `harness`, `model` and `effort` may go here too, over
your machine's file.

| Key | Meaning |
|-----|---------|
| `install` | Installs dependencies in a fresh clone |
| `test` | Runs the whole test suite. In this repository it is `scripts/test.sh`, which runs `go test -timeout 30m ./...` |
| `e2e` | Optional command after `test` and before `typecheck`; absent or empty means no row. In this repository it is `scripts/cover-diff.sh`. A changed line inside a coverage block that no end-to-end run of the real `atm` binary executed fails; the check names those lines. Delete them, or write the end-to-end test that reaches them from a real `atm` command. Lines that no coverage block spans, `_test.go` files, files under `testdata/` and deletions do not fail it. It reads the diff and the coverage blocks, not the text of a line |
| `typecheck` | Typechecks |
| `lint` | Lints |
| `test_file` | Runs one test: `{file}` is its path, `{dir}` its directory as `./dir`. When not empty, it must contain `{file}` or `{dir}`. For Go, `{tests}` expands to `^(TestA|TestB)$` for the Go tests defined in the reported file, for `go test -run`. A file with no Go tests fails; if the command reports `no tests to run`, the proof fails and names the reported file. |
| `test_patterns` | Globs of the test files |
| `docs_patterns` | Globs of the documentation files |
| `delivery` | Shell line that takes the run's branch from its clone, for example to a PR. Empty skips delivery |

Every command runs with `sh -c`. For a Go repository:

```yaml
install: go mod download
test: go test ./...
typecheck: go vet ./...
lint: golangci-lint run
test_file: go test -run {tests} {dir}
test_patterns: ["**/*_test.go"]
docs_patterns: ["**/*.md", "docs/**"]
delivery: 'no-mistakes axi run --intent "$ATM_INTENT"'
```

The `delivery` line hands the branch to
[no-mistakes](https://github.com/kunchenguid/no-mistakes). Without `--yes` it
only reports: whoever launched the run answers each finding.

The `Go` workflow of this repository keeps, for every push to `main`, the
end-to-end coverage profile of that commit as the artifact `cover-main-<sha>`
of the run (`cover-main.profile`, `cover-main.func.txt`). To answer "which
real-atm test reaches this function?" without running anything:

    gh run download --name cover-main-<sha> -R thellmwhisperer/agentic-team-member
    grep 'internal/run/<file>.go' cover-main.func.txt

A function at 0.0 % has no end-to-end run reaching it: write the test that
does. Any other percentage names a file whose tests already reach it.

When `e2e` is non-empty, ATM supplies one fresh `GOCOVERDIR` directory per
Checks and exports it to `test` and `e2e` only. `scripts/test.sh` does
not create it. With no counters, `scripts/cover-diff.sh` fails in the `e2e`
row with its no-coverage-data diagnostic. Counters remain in the run directory.

Optional: `check_timeout`, a positive Go duration such as `20m`, limits each checks command (install, test, e2e, typecheck, lint and red/green); absent means `10m`, and empty, null, invalid or non-positive values fail configuration.

Also optional: `agent_timeout`, the limit for each agent call, including
the slop detector, as a positive Go duration such as `45m` or `1h`. Without it,
the limit is `30m`. An empty, null, unitless, invalid, zero, negative or
overflowed value fails before the agent starts. Past the limit, the agent's
group is killed and its step fails with `timed out after <duration>`, where the
duration is the effective limit.

## Commands

Run repository commands from inside the repository they work on.

| Command | Does |
|---------|------|
| `atm init` | Writes a commented `.atm.yaml` with every required key empty and no `e2e` key, unless `.atm.yaml` is already there, and adds `/.atm/` to `.gitignore` unless it is there. Run twice, it changes nothing |
| `atm doctor [--agent]` | Checks git, the configured agent CLI on `PATH`, `gh` when `origin` is on GitHub, `.atm.yaml`. One `ok` or `fail` line per prerequisite; every check runs, and any prerequisite failure exits non-zero. It also reports the advisory [skill status](#skill). Its last line says `run atm doctor --agent to say hello to <harness>`: it makes no model call. With `--agent` it says hello instead, see [saying hello to the agent](#saying-hello-to-the-agent) |
| `atm skill`, `atm skill install [--dir <path>] [--force]` | See [Skill](#skill) |
| `atm run [flags] <issue>` | Starts a run. On a terminal it shows the run's screen until the run ends or q or Ctrl+C, then exits with the run's code; off one it prints the run's label and returns. See [resuming a failed run](#resuming-a-failed-run) |
| `atm status [--json]` | Each run going; text shows label, step, duration, agent and issue, or `nothing runs` |
| `atm attach [run]` | The run's screen, the last one going by default; it quits when the run ends, or on q or Ctrl+C |
| `atm runs [--json]` | Retained runs; text shows label, `running`, `passed` or `failed at <step>`, duration, agent, issue, report when present, and `serve` last |
| `atm axi run [--json] [flags] <issue>` | Waits for the run to end, prints its outcome and exits with its code |
| `atm axi status [--json]`, `atm axi runs [--json]` | The runs going, or retained runs, as a table |
| `atm axi plan [--json] [--follow] <plan.json>` | Which tickets of a plan can launch now; see [following a plan](#following-a-plan) |

Leaving a screen with `q` or Ctrl-C, or closing the terminal, leaves the run
going. `atm axi` is for programs: it prints TOON, or JSON with `--json`;
`axi plan --follow` always prints JSON lines. The `axi run` outcome carries
`outcome`, `run`, `failed_node`, `reason`, `report` (empty when the run failed
before its first step) and `next_step`. The `axi status` and `axi runs` tables
have `run`, `issue`, `step`, `duration` and `agent` for `status`, and `run`,
`issue`, `outcome`, `failed_node`, `duration`, `report`, `agent` and `serve` for `runs`.
`atm status --json` and `atm runs --json` use the same JSON format as their
`axi` forms: a `runs` array of objects, empty when no runs match. The
[report reference](how-a-run-flows.md#the-report) defines the `agent` label,
how requested settings differ from the observed model, and the `serve` value.

The exit codes are in the [README](../README.md#run-it).

### Saying hello to the agent

`atm doctor --agent` starts the configured agent (`harness`, `model`,
`effort`) with the command line a run builds, in a temporary directory it then
removes, with pipes and no terminal, and gives it one brief: `Reply with the
single word OK and nothing else.` It passes when the agent's stream ends within
2 minutes with a final message containing `OK`, read as a run reads it. For
Cursor, a result with `is_error: true` fails even after an earlier `OK` and a
zero process exit. Its last line is
`ok agent: <harness> <model> answered in <duration>`, or
`FAIL agent: <harness>: <cause>: <its last 5 lines>`, the cause an exit code,
a signal, `silent`, `timed out after 2 min 00 s`, `no OK in its final message`
or, for Cursor, the error result text.

### Following a plan

`atm axi plan <plan.json>` reads a CoS's plan, never writes it, and makes no
model call. The plan is JSON:

```json
{"version": 1, "max_active": 4, "excluded": [12],
 "tickets": [{"issue": 10, "depends_on": [9], "conflicts_with": [11],
              "hold": "waits for the PR", "hold_owner": "javi"}]}
```

A ticket counts while its issue is open and not in `excluded`. It is going
when its issue is open and its last run in `.atm/runs.jsonl` is `running` or
`passed` (delivery completed, but the issue is still open). A closed issue never blocks a
conflicting ticket, even if its last run still says `running`.
It is ready when it has no `hold`,
every dependency is closed (`depends_on` and each `#N` under the issue's
`## Depends on` heading), it is not going, and it does not conflict with a
ticket going, named in either one's `conflicts_with`. A `hold` waits on the
CoS, or on Javi with `hold_owner: "javi"`. It prints `open`, `ready`,
`cos_holds`, `javi_holds`, `working` and `free`: `max_active` minus the runs
`running` in their `agent` step.

With `--follow` it reads again every minute and prints a JSON line when that
changes, and the same line every 30 minutes while a ready ticket has a free
slot or the CoS holds one. When no ticket is open it prints
`{"event":"plan_closed"}` and exits 0. A `gh` error goes to stderr and it tries
again. An invalid plan (no `version: 1`, no `max_active` above 0, a ticket
without `issue`) exits 2 naming the field.

## `atm run` flags

Flags go before or after the issue. A second issue is an error.

| Flag | Default | Meaning |
|------|---------|---------|
| `--base-ref <ref>` | `main` | The ref the run starts from, resolved in the repository |
| `--harness <cli>` | from the layers | The `harness` value described [above](#configatmconfigyaml) |
| `--model <model>` | from the layers | The agent's model |
| `--effort <effort>` | from the layers | The agent's effort |
| `--harness-arg <arg>` | none | One argument for the agent CLI, before the brief. Repeatable |
| `--env KEY=VALUE` | none | One variable in the agent's environment. Repeatable |
| `--resume <run>` | none | Runs `<run>`'s issue again from its agent's work, in place of an issue |

### Resuming a failed run

A run that fails at the checks, the slop detector or the delivery keeps its
agent's work in `.atm/runs/<label>/resume/`: `resume.json` (the issue argument
and original issue text, the base commit, the failed unit's number, its agent
report, the follow-up it chained with its scope review, pending follow-ups,
earlier proofs, `gross_lines` accumulated before that unit, the failed row,
its `e2e` output, and the agent's session, settings and skill proof) and
`work.bundle` (the commits of the units before it, then the failed unit's
uncommitted work, new and binary files included, ignored files left
out). Its `report.json` says `"resumable": true` and names the unit in
`resumable_unit`, and its `next_step` names `atm run --resume <label>`. A run
that failed before or in the agent is not resumable.

The [follow-up rules](how-a-run-flows.md#follow-ups) describe the cumulative
budget and the dependency evidence kept when a mandatory gap blocks the run.

`atm run --resume <label>` starts a new run of the same issue under its own
label. It uses the saved original issue for briefs, scope review and final
contract review; it never re-reads the live issue. It reads `.atm.yaml` again,
so a fixed config counts, clones the saved base commit and puts the work back.
Its Agent row reads `resumed from <label>`, also as `resumed_from` on its unit
in `report.json`. If the failed row was `e2e` and the saved harness can resume
the saved session, ATM gives that agent exactly one turn in the restored
clone before Checks. The prompt is:

> Checks failed at e2e: these changed lines are not executed by any end-to-end run of the real binary. Delete them, or write the end-to-end test that reaches them from a real command. Do not touch anything else.

The saved `e2e` output follows it. The turn uses the Agent step's tools,
guards and time limits, keeps the saved report and ponytail proof, and must
not commit. A missing or invalid worker report does not fail the turn.
Its event is `agent turn after e2e`, with harness and duration; `--follow`
prints its start and its result before Checks. Checks starts again at `install`.
A second `e2e` red fails the run without another turn.

Other failures, older bundles without the session fields, missing session
ids and harnesses without session resume go straight to Checks. The screen
and `--follow` say `resume: no agent turn (<reason>)`, for example
`resume: no agent turn (failed row is test)`. No fresh session substitutes
for a saved session. The other flags apply as usual, `--base-ref`
aside: the resumed run keeps the base ref of the one it resumes. A run that
does not exist, is not resumable, or whose base commit the repository no longer
has fails before cloning, saying which. Missing or blank saved original issue
text also blocks resume before cloning with `saved original issue text is
missing`, including for older runs that did not save it.

## The issue

`atm run 300` reads issue 300 with `gh issue view` from the repository
`origin` names. `atm run plans/retry.md` reads a file: its first line is the
title (leading `#` characters dropped), the rest the body.

Either way the body declares its task type on a case-sensitive `Type:` line,
lowercase: `fix` (or `hotfix`), `feature`, `greenfield`, `refactor`, `tests`,
`docs` or `chore`. The type picks the proof the checks ask for; see
[How a run flows](how-a-run-flows.md#what-each-task-type-proves).

For [delivery](how-a-run-flows.md#delivery), the issue must also supply
Non-goals, Invariants, executable Acceptance and Split triggers. The reviewer
judges their meaning: prose is valid, but empty Markdown headings are not.
Acceptance must specify a runnable check and its expected result. Split
triggers identify discoveries that require separating work or replanning.

```markdown
# Retry the upload on a timeout

Type: fix

An upload that times out is lost. It should be retried once.

Non-goals: Do not add retries for errors other than timeouts.
Invariants: Successful uploads and other errors keep their current behavior.
Acceptance: Run `go test ./upload -run TestUploadTimeout -count=1`.
The test must pass, proving one retry on timeout and no second retry.
Split triggers: Persistent retry queues require a separate contract.
```

## What a run leaves on disk

Everything ATM writes lives under `.atm/` in the repository, which `atm init`
ignores.

| Path | Holds | Kept |
|------|-------|------|
| `.atm/runs/<label>/` | `report.json`, the briefs, the agent logs, each review's scratch directory, `follow-ups.json`, `delivery-output.txt`, and `resume/` when the run is resumable | Always |
| `.atm/clones/atm-run-<timestamp>/` | The run's isolated clone | Removed after the run when no longer needed and cleanup can verify its identity |
| `.atm/runs.jsonl` | The last state of each retained run, so `atm runs` survives the background process | Every run going and the newest 200 that ended |
| `.atm/atm.sock`, `.atm/atm.lock`, `.atm/serve.log` | The background process | While it runs; the log stays |

A run's label is `<issue name>-<n>`, `n` counting the repository's runs; it is
what `atm runs` shows. A run that delivered leaves `<clone>.delivered` with its
branch and base. At its end, a run removes its own clone unless it kept
resumable work or delivered a branch that is not merged and whose PR is
still open or unknown. ATM does not sweep other runs' clones: abandoned
clones and retained delivered clones remain for the owner to clean up.
The cleanup lock is held only during removal; agents and delivery commands
inherit no ownership of the clone. On Unix, cleanup keeps a clone if its
original directory name now refers to a different directory. Cleanup
rejects symlinks in the clone's parent path, including an inherited `TMPDIR`
alias; on macOS, the system `/var` to `/private/var` alias is accepted when
its ownership and permissions can be verified.

An eligible clone is removed even when it contains owner-controlled read-only
directories; symlinks inside it are removed as links. If content removal
fails, the remaining clone and its existing `.lock`, `.delivered` and `.pid`
sidecars stay, and release returns the error. If a partly removed delivered
clone has no `.git`, ATM does not consult the enclosing repository: `gh` can
still confirm that its PR is closed or merged; without that answer, ATM keeps
the clone.
