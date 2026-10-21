# How a run flows

This page is what `atm run` does with an issue, step by step, and what makes
each step fail. Models write the fix and cut the diff; code enforces the step
results. What you
set before a run is in [Configuration](configuration.md).

## Following one run

`atm run <issue> --follow` starts the run and prints one JSON object per run
event on standard output until the run ends or awaits the launcher's answer. The flag
goes before or after the issue and works on a pipe or a terminal. It also works
with `--resume`.
The events are the run's own step starts, results and live activity, with
`step` and `ts`, plus fields specific to each event. Output from a clone or
Checks command has `check` naming its row, such as `install`, `test`,
`typecheck` or `lint`. Delivery output has `check: "delivery"`. Both
appear as `output` events without interpreting the command's text.

The command exits with the run's code: 0 for success, 1 for agent or checks
failure, 4 for delivery failure, and 2 for other failures. Launch errors go
to standard error. Interrupting the stream exits 1 and leaves the run going;
`atm attach <run>` shows it again. No monitoring process or heartbeat is required.

## Before the run

`atm run`, `--resume` too, checks what the run stands on before it hands the
run to the background process. The first check that fails stops the launch
with exit 2, `atm: preflight: <check>: <why>` on standard error, no clone, no
run directory and no agent. `atm run --help` lists them. ATM fetches the
requested branch from `origin` itself, stops if fetching fails, and resolves
`origin/<base-ref>` once. Configuration, CI and a fresh clone use that commit.
A resumed clone still starts at its saved base commit.

| Check | Passes when |
|-------|-------------|
| `config` | `.atm.yaml` as the base ref has it is complete. The run uses that file, never the working tree's; a working tree's `.atm.yaml` that differs gets one warning line |
| `base green` | The latest completed CI run of each workflow on the base ref's head passed or was skipped, as one `gh run list --commit` call says. No completed run, or a `gh` that does not answer, fails. A repository whose origin is not on GitHub skips it with one line saying so |
| `worker files` | `--settings` and `--skill` accept either a separate value or `=<value>`. Absolute paths are readable on disk; relative paths exist in the resolved base commit, or the run's recorded base commit on resume, relative to the clone root. Only `--settings` values starting with `{` are inline JSON |
| `delivery` | The first word of `delivery` is a command `sh` finds, as `command -v` says: on `PATH`, or one of `sh`'s own, as `if` or `cd`. An empty `delivery` passes |

## The steps

A failed step stops the run before later steps. An Agent exit with an error,
a write outside its clone and scratch directory, or a Checks, slop detector or
Delivery failure waits for the launcher's answer; see [Checks](#checks).

| Step | Does | Failure condition |
|------|------|--------------------|
| Issue | Reads the issue file or GitHub issue | The issue cannot be read or has no title or body |
| Clone | Clones the repository at the commit checked by preflight, or the saved base commit on resume into `.atm/clones/`, runs `install` | The ref, the clone or `install` fails |
| Contract | Writes `brief.md`, the agent's contract | The brief cannot be written |
| Agent | Runs the coding agent in the clone on the brief | It fails to complete successfully, times out or writes outside its clone and scratch directory. A missing or invalid report does not fail it: see [Rejected reports](#rejected-reports) |
| Checks | Prepares the clone, then install, test, optional e2e, typecheck and lint; see [Checks](#checks) | Cleanup fails, the reported test file is invalid, a command fails, or HEAD moves |
| Slop detector | Lets a model cut the diff; keeps the cut only if every check still passes | Its agent fails, fails [report validation](#rejected-reports), or anything commits |
| Delivery | Commits the work on a branch and runs the `delivery` command | The branch or the command fails. Skipped when `delivery` is empty |

A run is one unit: the issue's work. A gap the agent sees and does not fix is a
note in its report's `summary`; ATM takes no action on it. For the configured
delivery command, see [Delivery](#delivery).

## Issue, clone and contract

`.atm.yaml` is read before the issue, so a bad configuration fails before
anything else. The clone is detached at the base commit, has `/.atm/` excluded,
and gets its `install` run once. Before `install`, ATM copies the repository's
`.tmp/slopslint/` into the clone at the same path, preserving file bytes and
modes so a cached executable can run without a download. No other `.tmp/`
contents are copied. If the cache is absent, cloning proceeds without creating
`.tmp/`; a copy failure stops the clone step.

The contract, `brief.md` in the run's
directory, tells the agent to read `AGENTS.md` if there is one, then gives the
issue and VERIFY: the tests of each changed package, then the configured
`typecheck` and `lint`. The configured `test` suite runs later in Checks.
The contract also gives the style, what is forbidden and the JSON report it
must end with. It limits reading, searching and running to the clone, with
access to the worker's scratch directory for files set aside. It forbids
network access except package-manager dependency installation, and forbids
access to the surrounding repository, the rest of `.atm/runs/` and other clones.

## Agent

The native agents run in the clone in their most minimal mode.
For Hermes's ACP invocation and completion rules, see
[Hermes](configuration.md#hermes).

| Harness | Command | Reads the brief |
|---------|---------|-----------------|
| `claude` | `claude -p --setting-sources project --strict-mcp-config --thinking-display summarized`, tools `Read,Edit,Write,Bash,Glob,Grep,Skill` only; `--harness-arg`s follow this default | on stdin |
| `codex` | `codex exec`, which still loads your skills | as its last argument |
| `opencode` | `opencode run --pure`, which still loads your skills | as its last argument |
| `pi` | `pi -p --no-extensions --no-skills --no-prompt-templates --no-context-files` | as its last argument |
| `cursor` | `cursor-agent -p --output-format stream-json --trust --force --workspace <clone>`, then `--model <model>` when set and any `--harness-arg`s | as its last argument |

For `claude`, ATM sets `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1` for every
agent call, including the slop detector. It overrides
an inherited value or one passed with `--env`, so their turns wait for running
commands to finish.

For native agents, ATM writes the bundled `ponytail` skill into the clone, outside git; for Cursor at
`.cursor/skills/<skill>/SKILL.md`. Hermes's skill delivery is described in
[Hermes](configuration.md#hermes). Every line the agent writes
goes to `worker.jsonl`, and its stderr to `worker.stderr.txt` too, as it is
written. Its report is the last JSON
object of its final message and must include `test_file`, which may be empty.
When named, ATM checks that the file is inside the clone and records its
contents to protect it during a slop detector cut. The configured suite runs
the tests. ATM checks each logged tool call for every
harness. A call fails the step only when it writes outside the clone and its
scratch directory with an absolute path, such as `/tmp`, the repository around
the clone or `$HOME`: a write or edit tool's destination, or a shell command's output
redirection or the paths `tee`, `cp`, `mv`, `sed -i` and the like write. A
worker call gets an empty scratch directory under the run's directory, outside
the clone, named in its brief and set as its `TMPDIR`. Logs, outputs and files
the worker sets aside go there, including on an agent turn after an e2e failure
in a resumed run. The system temp directory remains outside the allowance.
Reading outside the clone, writing inside it by any means, and URLs pass. The error
names the first offending call. Missing or invalid reports follow
[Rejected reports](#rejected-reports).
For native agents, any non-zero exit
also fails; Hermes uses the completion rules linked above.
For Cursor, a stream result with `is_error: true` fails the step with its
result text even if the process exits zero.

For pi, an assistant `message_end` with `stopReason: "error"` and a nonempty
`errorMessage` fails the step with `<exit>; pi: <errorMessage>; log: <path>`,
preserving the provider's message verbatim, even with exit code zero. This
applies to every pi agent step, including the worker and slop detector,
before report validation. A forbidden tool
call still takes precedence. A later assistant `message_end` with
`stopReason: "stop"`, `"length"` or `"toolUse"` clears the model error; the
usual report checks then apply. ATM does not retry a model
error or return it as a rejected report.

### Rejected reports

When final-report parsing or validation fails, ATM asks once more, with the
exact validation error:

```text
Your final message was rejected: <the exact validation error>. Reply with only the JSON object the brief asks for, nothing else.
```

For `claude`, `codex`, `pi` and `cursor` with a session ID, it goes to the
same session. Otherwise ATM runs the agent again on its last message followed
by that text. If the second report validates, the step uses it.

If the worker's second report still fails, the Agent step passes and records
`"report": "missing"`. The unit goes to Checks with no test file. It must
have changed something. All configured commands still run. The slop detector fails with the second error instead.

The agent's `.jsonl` log records a `report_rejected` event with the exact
error in `error`. Both
attempts' output stays in that log, and both attempts' stderr stays in its
companion `.stderr.txt` file. Process failures, hangs and forbidden tool
calls fail without returning the report.

### Hung agents

An agent call is hung when a periodic sample finds at least 10 minutes with no
output and no observed child activity. ATM samples descendants for process
starts and exits, cumulative subsecond CPU time, and I/O byte transfers on
Linux and macOS.
An incomplete observation cannot establish inactivity: readable counters and
output are retained, and the inactivity interval starts again.
ATM kills it with every process it started,
and its step fails with `hung: no output and no child process working for 10m`.
An agent that is silent while a child works, such as a test pass, is never
stopped. A hung worker asks its launcher as a failed check does (see
[Checks](#checks)): `approve` runs Checks on the work it left, `fix` gives it
one turn with the finding. Any other hung agent call fails its step.

## Checks

When the Agent writes outside its clone and scratch directory, its process
exits with an error, or Checks, the slop detector or Delivery fails, the
background run asks its launcher:

```sh
atm answer <run> approve
atm answer <run> fix
atm answer <run> stop
```

The `decision` event carries the finding, these three choices and whether the
run is `resumable`. The clone and its work stay in place while the launcher
decides. `approve` records the finding and approval. After an Agent failure it
runs Checks on the current work; after a later step fails it continues to the
next step. `fix` gives the worker one turn with the finding, reusing its session
when supported, then runs Checks and the slop detector before Delivery. A new
failure in any of these steps asks again. `stop` ends the run and keeps its work.
See [Resuming a failed run](configuration.md#resuming-a-failed-run) for recovery
after `stop`. Answers are recorded before the run continues; a second answer
to the same question is rejected.
`atm run --follow` and an attached screen return with the failed step's exit
code at the question: 1 for Agent or Checks, 2 for the slop detector and 4 for
Delivery. The background run remains waiting. `atm attach <run>` shows the
question again or follows the continuation after an answer.

The Agent and a worker fixing a failure must not commit. A Delivery command
may commit before failing; Checks use the commit left by Delivery as the
starting point for the fix turn. ATM records the clone's paths before the
agent starts, including contents of ignored nested repositories. Before
`install`, Checks removes newly made ignored files and directories,
including read-only cache trees and new contents within retained directories.
It then restores changed or deleted pre-existing ignored files from the
pre-agent snapshot, byte for byte, so package-manager state cannot claim
that removed dependencies are still installed. This includes tracked and
untracked files inside pre-existing ignored nested repositories, even when
their HEAD has not changed; restoring those files leaves their `.git` unchanged.
A repository nested inside one of those repositories is still recorded only
as a Git reference, so its file contents are not restored. Existing directory
modes remain; symlinks are removed as links, without changing their targets.
A cleanup or restoration error stops Checks before
`install`. Delivery leaves the removed paths out, so a test that
needs one fails. ATM moves `.atm/` out of the clone while Checks runs,
including `install` and the other commands, then restores it even
if a check fails. This keeps `.atm/` artefacts out of every check command. Checks
then run `install` again (the agent may have changed dependencies), and run
`test`, `e2e`, `typecheck` and `lint`, empty ones skipped. In this repository
`e2e` runs `scripts/cover-diff.sh` on the counters `test`
wrote in ATM's fresh `GOCOVERDIR`. Which lines fail,
and the ways out, are under [`e2e`](configuration.md#atmyaml). The first
failed command asks the launcher with its last 60 lines as the finding. A
command that commits fails the checks too.

Resuming an `e2e` red gives the agent one turn in its saved session before
Checks starts again at `install`. The turn receives the failed output and
the two ways out above, under the Agent step's hang detection. A second
red asks the launcher without an automatic agent turn. Other failures and
sessions that cannot resume go straight to Checks with
`resume: no agent turn (<reason>)`; see
[Resuming a failed run](configuration.md#resuming-a-failed-run).

The [issue contract](configuration.md#the-issue) does not select checks.
Checks run the configured suite on the completed work, followed by `e2e`,
`typecheck` and `lint` when configured.


## Unit commit

A unit commit has the single-line message `chore: <title>`. A Delivery repair
can produce another unit commit. ATM does not infer a release type from issue
metadata.

## Slop detector

Once the unit passed, the same agent gets `brief-ponytail.md`, the issue and
the run's diff, with the bundled `ponytail-review` skill. It may only cut: delete, shrink, or swap in the standard library or
an existing helper, in the diff's files. The required
report format is in the [ponytail brief](../internal/run/brief-ponytail.md.tmpl).

ATM snapshots the whole clone first, then judges the cut per file. It rejects
a file when a finding naming it has no `issue_sentence` key, or the cut adds it,
deletes a non-test file, or changes it outside the run's diff. The sentence a
finding quotes is recorded and decides nothing.
Only rejected files return to their contents before the cut;
newly added rejected files are removed. Other files' cuts stay for validation.
There is no splitting within a file. If no file survives, the whole cut is
discarded as before.

The remaining cut must report a finding and lower the run's net added lines.
ATM then checks for new files, including ignored files, preserves the reported
test, and runs the configured checks on the completed cut. When those fail,
ATM puts back every cut
file whose full repo path the failure names, as compiler and test output name a file,
and checks again, while the failure names one. A failure that names none, with at least two files left:
ATM puts back each remaining file alone, once, and keeps the first remainder
that passes. The kept remainder must still report a finding and lower the net
added lines. At most twice the cut files, plus one, repeated checks. If any of these
requirements fails, ATM restores the whole clone byte for byte and discards
the entire cut.

A kept cut is its own commit, `ponytail: <n> cuts`, one line per surviving finding,
after a unit commit, with one slopslint tombstone per surviving finding under
`.slop/tombstones/`. For a deleted test, the tombstone embeds its path and
pre-cut content and references that record as the evidence artifact.
ATM's commits use the repository's git identity; none, or
an email ending in `@localhost`, fails the run.

## Delivery

When `delivery` is set, the clone goes on a branch
`atm/<title slug>-<timestamp>`, whatever is left uncommitted is committed as
the next unit commit, and the clone's `origin` becomes the repository's. A
repair retries Delivery on the same branch after Checks and the slop detector.
Every commit after the base must carry the repository's git identity.

The command runs once per attempt with `sh -c` in the clone, without
a time limit, with these variables:

| Variable | Value |
|----------|-------|
| `ATM_TITLE` | The issue's title |
| `ATM_ISSUE` | The issue number; empty for a file |
| `ATM_ISSUE_TEXT` | The whole issue, title and body |
| `ATM_INTENT` | `ATM_ISSUE_TEXT`, then, when the slop detector committed tombstones, a `## Files ATM adds` section naming all retained tombstones as ATM's own record, not the issue's scope |
| `ATM_BRANCH` | The branch |
| `ATM_CLONE` | The clone's path |
| `ATM_REPORT` | The path of `report.json` |
| `ATM_PONYTAIL` | The findings of all retained cuts, one a line, or empty |

The command's output goes to the screen and to `delivery-output.txt`.
ATM executes the configured command without recognizing its name or parsing
its output. Exit zero passes Delivery; a nonzero exit asks the launcher before
the run can continue or stop. Publishing, review decisions and PR tracking
belong to that command or its launcher. The attached screen observes the gate
after Delivery passes and the run ends, as described below.

## Time limits

ATM runs on Linux and macOS, and on Windows through WSL.

| What | Limit |
|------|-------|
| Reading a GitHub issue | 60 s |
| Each checks command (`install`, `test`, `e2e`, `typecheck`, `lint`) | none |
| Each agent call, the slop detector's included | none; stopped when [hung](#hung-agents) |
| The delivery command | none |

Past its limit, or when the background process executing the run gets
Ctrl-C or SIGTERM, a process is killed with
every process it started, including those that started a session or process
group of their own and those already orphaned.
For cancellation of `atm run --follow`, see [Following one run](#following-one-run).

## The report

`.atm/runs/<label>/report.json` is rewritten at every step's start and end, so
it is on disk before delivery reads it.

| Field | Holds |
|-------|-------|
| `failed_node`, `reason` | The step that failed, if any, and the reason for failure |
| `agent` | The effective requested `harness`, `model`, `effort` and ordered `harness_args`, after global config, `.atm.yaml` and flags. Unset model and effort stay `""`; no extra arguments is `[]` |
| `nodes` | Each step's `result` (`passed`, `failed`, `running`, `skipped` or `not run`), `started_at` (UTC, RFC 3339 with milliseconds) when its start is known, and `duration_ms` when it ends; the agent node also has `exit` and `tail` when the agent ran but its step failed, and `"report": "missing"` when the worker's report never came ([Rejected reports](#rejected-reports)). Checks includes an `e2e` row in `rows` when that command ran, passed or failed, with `name`, `result`, `started_at` and `duration_ms`. Delivery has `rows`, each with `name`, `result` (`running`, `passed` or `failed`), `started_at` and `duration_ms`, for each commit or command attempt; earlier attempts keep their result and duration. |
| `units` | The unit's number (`1`), base commit, reported test file, result, `started_at` and `duration_ms` |
| `reproofs` | Each rechecked unit's number, base commit, reported test file, result, `started_at` and `duration_ms` after a cut; configured commands are in `commands` |
| `commands` | Every configured command that ran, the clone's `install` included, with its result, last 60 lines, `started_at` and `duration_ms` |
| `decisions` | Each Agent exit error, forbidden write, Checks, slop detector or Delivery finding and the launcher's `approve`, `fix` or `stop` answer; the answer is empty while the question is pending |
| `head_sha` | The latest commit prepared for Delivery; a repair may update it |
| `serve` | The version of the `atm serve` process that executed the run, from `buildinfo.String()`, for example `vA (aaa1111) 2026-10-15` |

The Agent and `ponytail` nodes also have `agent_model`: the first model named
by that invocation's stream, or `""` when none is named. Claude and Cursor
supply the `model` in a `system` / `init` event. Pi's assistant `message_end`
supplies `message.provider` and `message.model`, recorded as
`<provider>/<model>` or just `<model>` when the provider is absent. For Codex
and OpenCode, ATM reads only an event's top-level `model` field. This value never
falls back to the requested model.
The `units` entry keeps its own `agent_model`, so the slop detector cannot
replace the unit's attribution. A resumed unit that
does not invoke an agent has an empty observed model.

`atm runs` and `atm status`, including their `axi` forms and `--json` output,
include an `agent` column: `<harness> <model>`, or `<harness> default` when
the requested model is empty. `.atm/runs.jsonl` retains the requested
`agent` object for finished runs and after the server restarts. Old history
without attribution displays `-`; ATM does not infer it from current config.

`atm runs` and `atm axi runs`, including `--json`, show the recorded `serve`
value. `.atm/runs.jsonl` retains it after the background process exits.
Runs recorded before this field existed display `-` in these listings.

The `ponytail` node also records `kept` (`true` or `false`) once ATM decides
whether to keep the cut, and `reason`: the exact rejection reason from the
cut policy, new-file check or repeated checks, or `""` when kept.
`net_lines` is `[before, after]`, the run's net added lines before and after
the judged cut, measured against the run's base. When some files survive and
others are restored, `after` measures the remaining cut. `findings` preserves
the agent's findings as reported, including findings for rejected files.
Discarding the whole cut does not remove these fields or replace the judged
cut's line count with the restored count. If the step stops early, fields
whose values are not yet available are omitted.

`rejected_files` lists each file ATM put back, once, with its `path` and the
exact rejection `reason`. It is `[]` when nothing was put back, including a
cut kept in full. A partly kept cut still has `kept: true` and `reason: ""`;
its rejected files carry their individual policy or check reasons that
put them back. If the remaining
cut also fails, files already rejected retain their reasons, and the other
files put back carry the whole-cut rejection reason. See
[Slop detector](#slop-detector) for the rejection rules.

## The screen

`atm run` hands the run to the repository's background process, one per
repository, started by the first command that needs it and ended after
10 min without a run. Closing the terminal does not stop the run.

Before sending a run, the launcher checks the connected `atm serve` process's
full build identity against its own. It reads the last PID/version pair for
that process in `.atm/serve.log`, so the check also works with older serves
and binaries installed through a symlink or under a different filename.
Matching builds accept the run as usual. A different or unverifiable build
rejects the launch with `serve <A> is older than atm <B>`; a missing version
is shown as `unknown`. This is an exact match check, not version ordering.
The rejected run is not queued, and runs already executing remain untouched.

When an idle serve detects that its executable file was replaced and closes
the connection to hand off, the launcher reconnects, starting its own serve
if needed, and checks again before sending the run. A version mismatch alone
does not stop the old serve or start a second serve alongside it. After a
rejected launch, retry once the old serve has exited and a matching serve can
take the run.

Without `--follow`, `atm run` attaches the screen on a terminal; see
[Following one run](#following-one-run) for that flag's output. The screen is
the ATM box, a row per step with its icon (`○`
pending, a spinner running, `⏸` waiting, `✓` passed, `✗` failed, `–` skipped), duration and
note, and beside it, from 100 columns, the Agent box while an agent works,
the Findings box with a failure or the slop detector's cuts, and the Log box.
The ATM box's Agent row reads `Agent`; Log headers and Findings references for
that agent retain `Agent · unit 1`.
Above the rows, the issue's title wraps: up to three lines show whole; a
longer one shows three lines and `… i unfold title`, and `i` shows or folds
every line. The title stays above the rows while it leaves them a line; when
the ATM box is too short for that, past its first line the title scrolls
with the rows, and the ATM box's keys reach it.

While a step runs, the right column shows it. The Agent box shows an agent's
events, with the Log box below it when the agent emits output. For other steps,
the Log box shows the step's name and elapsed time until output replaces them.
Output appears under a header for each command, or under the step when it has
no command. The header shows its name and state.
By default, it opens while the command runs and folds when it ends; click
the header to show or hide its output. Folded headers preview the first line.
The preview and unfolded output render Markdown as the Agent box does. The Agent
box's title names the effective harness, model and, when set, effort, as
`Agent · pi zai/glm-5.3 · max`.

Like Checks, Delivery shows a sub-row per part that runs: `commit`, and
`command` only when the command starts. Each has its own live state and
elapsed time. Completed runs restore these rows from `report.json` when
attached. The
command row carries state and time only and does not repeat the command's
output. The Delivery row becomes passed or failed when the command exits.
An empty delivery command
shows `Delivery skipped` without sub-rows.

Once a run ends with Delivery passed, the screen goes on with the gate: it
asks `no-mistakes axi status`, in the run's clone, for the run of the
delivered branch, and `no-mistakes axi logs --step <step> --full` for each step it started,
every second. The gate's steps get rows below Delivery in the ATM box, and
the Gate box in the right column has a header row per started step, its log
rendered as the Agent box renders Markdown, folded to its first line like
thinking, and one line per finding: ID, severity, file and description. Findings
from status also show for the current gate step when its log has not shown them;
an ID already in the log is shown only once.
The public findings table does not expose a source line number; the screen
shows the supplied file without inventing a line. Tab switches between the
ATM and Gate boxes; arrows, PageUp, PageDown, Home and End scroll the focused box.
The run writes nothing of the gate.

While a gate step waits, the footer offers `a` to approve or `s` to skip it.
For a fix, press `f`, Enter, type one line, then Enter. Enter alone also opens
the line. The line becomes fix instructions. To fix selected findings, start it
with their displayed IDs, as in `id1,id2: instructions`; the other findings
shown for that step are ignored. Without IDs, the fix covers every finding
shown for the waiting step. While a reviewer question shows, Enter opens the
line for its answer instead; Enter submits it. Questions are answered in order
during the current attachment.
Answered questions are remembered only until the screen closes: if you detach
between questions while the gate still lists both, reattaching can target the
first question again. Press `x`, then `y`, to abort the gate; any other key
cancels the confirmation. These actions use no-mistakes' commands in the run's
clone. `q` or Ctrl+C still detaches without aborting.

The Agent box renders the agent's Markdown prose within the panel width as
`✎` lines; non-empty thinking appears as `∴` lines. By default, consecutive
successful or running tool calls are folded into one dim line below them,
with tool names and repeat counts, and each thinking message folds to one
row with its first line rendered as Markdown and cut to the panel width,
until it is unfolded to its full text. A running group has a spinner and
its active tool's detail; a failed tool keeps its own row, including the first
line of its error when available. Every row a click folds or unfolds starts
with `▸` folded and `▾` unfolded, the arrow alone underlined like a link.
The JSON report a message ends with is never shown raw: it becomes an
`Informe` row, unfolded, with its summary's Markdown, tables in aligned
columns, and four folded rows, `Ficheros cambiados (n)`,
`Comandos ejecutados (n)`, `Test <file>` and `Hallazgos (n)`; unfolded,
each item is a row, a finding with its severity first. The box omits empty
thinking, user messages, skill text and full tool output. Its `N tools`
note counts tool calls only.

In both `atm run` and `atm attach`, press `t` to unfold all tool groups and
press it again to fold them, and `h` to do the same for every thinking
message. Each press clears individual choices. Click a group's `N tools`
header, or a thinking message's row, to toggle just that one; clicking an
individual call does nothing. The header remains visible when unfolded.
Each unfolded call occupies one row: a state icon and word (`running`, `ok`
or `failed`), the tool name, the first line of its detail, and, after ` · `,
the first line of its result. Rows are cut to the panel width, with terminal
controls filtered out. Result summaries are read for pi, Claude, Codex,
OpenCode and Cursor. If the result is absent or its first line is blank,
the separator and summary are omitted. The footer shows `t show tool use`
or `t hide tool use`, and `h show thinking` or `h hide thinking`, beside
`q detach` while the Agent box is visible. Fold choices are not saved across
attaches.

The ATM, Agent and Gate boxes each keep every line. The Log box keeps up to
200 output events from the current step. All boxes scroll on their own.
The mouse wheel scrolls the box under the pointer. The keys scroll the box
with the `*` marker in its title. `Tab` cycles through the visible boxes:
ATM, Agent and Log while the agent runs, or ATM and Gate after Delivery.
`↑` and `↓` move by a line, `PgUp` and `PgDn` by a page, and `Home` and `End`
go to the first and the last lines. A box scrolled above its last line stays
where it is as new lines come while those lines remain available; back at its
last line it follows them again. Folding or unfolding preserves the reading
position; a call hidden by folding anchors to its group header. Stacked under
100 columns, the ATM box takes at most two thirds of the space left after the
footer and panel gap.
On Linux and macOS, and on Windows through WSL, ATM keeps its screen live
while the delivery command is silent. From the command's first output until
it ends, ATM gives it the attached terminal
and its keys. The delivery command can draw its own screen and receive
input there. ATM's screen comes back when the command ends, showing the
Delivery result and duration.
On a terminal, the screen quits when the run ends unless Delivery passed and
no-mistakes finds a gate run in the clone; then it quits when the gate ends.
If no gate run is found or the query fails, it quits at once. It leaves its
last view on the terminal; `q` or Ctrl+C closes it before. Off-terminal output
returns when the ATM run ends.

Off a terminal, or with `NO_COLOR` or `TERM=dumb`, the screen is plain lines:
one per step start and end, then the delivery's output and the summary. Every
duration ATM prints reads like `0.9 s`, `4 min 28 s` or `1 h 02 min`.
