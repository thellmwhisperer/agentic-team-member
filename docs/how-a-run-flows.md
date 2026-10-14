# How a run flows

This page is what `atm run` does with an issue, step by step, and what makes
each step fail. Models write the fix, cut the diff and review the commit
against the issue before delivery; code enforces the step results. What you
set before a run is in [Configuration](configuration.md).

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/red-green-dark.svg">
  <img alt="For fix and hotfix tasks, ATM sets aside changes to files already in the base while keeping the test and new files: the test must fail. ATM restores the changes and runs the test again: it must pass. A test that passes without the fix or fails with it fails the run." src="assets/red-green-light.svg" width="900">
</picture>

## The watcher

No watcher, no run. The session that launches a run starts `atm watch` first,
under its harness's streaming monitor (any tool that wakes the session on each
output line), and passes the id from its first line, `watching as <id>`, to
`atm run --watcher <id>`. It replaces the CoS skill's `vigia.sh`: it never
exits on an event, so there is nothing to re-arm.

`atm run` without `--watcher`, or naming a watcher that is not alive, fails
before cloning with exit code 2:
`no watcher: start atm watch in the session that launches this run and pass --watcher <id>`.
A watcher is alive while its lease, `.atm/watch/<id>.json`, names a process
that runs and a heartbeat it rewrites every 10 s is under 30 s old.
`report.json` and `atm runs` record the run's watcher, and `atm doctor` says
whether one is alive.

The watcher prints one line per event of each run bound to it, the time and
the run first, until it is killed:

| Line | When |
|------|------|
| `started`, then each step's name | The run, then each step, starts; `delivery` is the last |
| `gate waiting: <step>` | The no-mistakes gate waits on a decision, once per new wait |
| `still waiting 20m: <step>` | Every 10 minutes the same wait goes on |
| `gate fixing CI`, `pr <url>`, `ci green`, `merged` | The gate fixes CI, opens the PR, CI passes, the PR merges |
| `gate failed: <step>` | A gate step fails |
| `finished: passed`, `finished: failed at <step>` | The run ends |

From delivery on it reads the gate with `no-mistakes axi status` in the run's
clone every 10 s, after the run ends too, until the PR merges or the gate
fails. If Delivery fails before its command runs and no gate was seen, it stops
checking gate status and prints `finished: failed at Delivery`. If the command
ran, it keeps retrying transient status errors because a gate may have started.

When a watcher dies, each running run bound to it shows
`WATCHER GONE at <time>: no one is watching this run` once, on its screen and
in `.atm/serve.log`. `atm watch --adopt <run>` binds the run to a new watcher,
which prints its current state first, as `adopted: running at <step>`.

## The steps

A failed step stops the run; the steps after it are not run.

| Step | Does | Fails the run when |
|------|------|--------------------|
| Issue | Reads the issue file or GitHub issue and its `Type:` | The issue cannot be read, or has no valid type |
| Clone | Resolves `--base-ref` to a commit, clones the repository there into `.atm/clones/`, runs `install` | The ref, the clone or `install` fails |
| Contract | Writes `brief.md`, the agent's contract | The brief cannot be written |
| Agent | Runs the coding agent in the clone on the brief | It exits non-zero, times out, says nothing for 10 min, reaches outside its clone, does not load the `ponytail` skill, or leaves no readable report |
| Checks | Removes new ignored paths, then runs `install`, proof, `test`, `typecheck` and `lint`; see [Checks](#checks) | Cleanup, proof or a command fails, anything committed, or a [follow-up limit or mandatory gap](#follow-ups) blocks the run |
| Slop detector | Lets a model cut the diff; keeps the cut only if every check still passes | Its agent fails, reports badly, or anything commits |
| Delivery | Commits the work on a branch, has the agent review the commit against the issue, and runs the `delivery` command | The branch, the review or the command fails. Skipped when `delivery` is empty |

Agent and Checks run again for each chained follow-up, at most three units in
one run; see [Follow-ups](#follow-ups).

## Issue, clone and contract

`.atm.yaml` is read before the issue, so a bad configuration fails before
anything else. The clone is detached at the base commit, has `/.atm/` excluded,
and gets its `install` run once. The contract, `brief.md` in the run's
directory, tells the agent to read `AGENTS.md` if there is one, then gives the
issue, the acceptance criteria of its task type, the commands from
`.atm.yaml`, the style, what is forbidden and the JSON report it must end
with. It limits reading, searching and running to the clone, forbids network
access except package-manager dependency installation, and forbids access to
the surrounding repository, `.atm/runs/` and other clones.

## Agent

The agent runs in the clone in its most minimal mode:

| Harness | Command | Reads the brief |
|---------|---------|-----------------|
| `claude` | `claude -p --setting-sources project --strict-mcp-config --thinking-display summarized`, tools `Read,Edit,Write,Bash,Glob,Grep,Skill` only; `--harness-arg`s follow this default | on stdin |
| `codex` | `codex exec`, which still loads your skills | as its last argument |
| `opencode` | `opencode run --pure`, which still loads your skills | as its last argument |
| `pi` | `pi -p --no-extensions --no-skills --no-prompt-templates --no-context-files --no-session` | as its last argument |
| `cursor` | `cursor-agent -p --output-format stream-json --trust --force --workspace <clone>`, then `--model <model>` when set and any `--harness-arg`s | as its last argument |

For `claude`, ATM sets `CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1` for every
agent call, including the slop detector and contract reviewer. It overrides
an inherited value or one passed with `--env`, so their turns wait for running
commands to finish.

ATM writes the bundled `ponytail` skill into the clone, outside git, and the
agent's log must show it loaded under that exact name and succeeded. For Cursor,
ATM installs it at `.cursor/skills/<skill>/SKILL.md` outside git; a successful
`read` or direct `cat` of that file proves use. Every line the agent writes
goes to `worker.jsonl`, and its stderr to `worker.stderr.txt` too, as it is
written. Its report is the last JSON
object of its final message and must name `test_file`: ATM never guesses
which test proves the fix. ATM checks each logged tool call for every
harness. For write and edit tools (`write`, `edit`, `write_file` and
`edit_file`), it ignores written content (`content`, `fileText` and old/new
text fields), while checking the remaining arguments, including the destination
path. It checks all arguments for other tools. A call fails the step if it
names an absolute path
under the repository root but outside the clone, or if its shell command runs
`curl` or `wget` or contains an HTTP(S) URL whose host is not `localhost`,
`127.0.0.1` or `::1`. One narrow exception applies to a leading plain `cat`
that writes a quoted heredoc of valid Go test source to a literal clone-relative
`_test.go` file, followed only by a literal `go test` invocation. The heredoc
must contain a recognized test function using the standard `testing.T` type.
The exception covers URL text in that fixture only; URLs or fetch commands
elsewhere in the shell command still fail. The error names the first offending
call. A zero exit without a valid report fails; so does any non-zero exit.
For Cursor, a stream result with `is_error: true` fails the step with its
result text even if the process exits zero.

## Checks

The agent must not have committed. ATM records the clone's paths before the
agent starts, including contents of ignored nested repositories. Before
`install` or proof, Checks removes newly made ignored files and directories,
including read-only cache trees and new contents within retained directories.
Pre-existing contents and directory modes remain; symlinks are removed as
links, without changing their targets. A cleanup error stops Checks before
`install` or proof. Delivery leaves the removed paths out, so a test that
needs one fails. ATM moves `.atm/` out of the clone while Checks runs,
including `install`, the proof and the other commands, then restores it even
if a check fails. This keeps follow-up tests and other `.atm/` artefacts for
ATM to process after Checks without exposing them to a check command. Checks
then run `install` again (the agent may have changed dependencies), prove the
task type, and run `test`, `typecheck` and `lint`, empty ones skipped. The
first failure stops the run with that command's last 60 lines. A command that
commits fails the checks too.

### What each task type proves

| Type | Proof |
|------|-------|
| `fix`, `hotfix` | Red/green on the files the base already has: the files the fix added stay, so the test must fail on behaviour. Then twice more, on the base with only the fix's added lines in place: a test that passes there depends on nothing the base had and is rejected |
| `feature`, `greenfield` | Red/green with everything set aside; the red may fail on a missing symbol at runtime, but not on a parse or compile error of the test file itself |
| `refactor` | `test` passes on the base, and no test file changed |
| `tests` | The new test passes on the base and after; only test and doc files changed |
| `docs` | Only files matching `docs_patterns` changed |
| `chore` | Nothing beyond the commands |

For `fix`, `feature` and `greenfield`, ATM classifies the red at the shared red/green boundary, on its whole output, before it judges the green. Recognized runner diagnostics fail with "red did not run: parse/compile error", never with "fails with the fix". For Go test files parsed before execution, a parser error gets the same classification. A red that fails on a missing symbol at runtime or on an assertion is a valid red. ATM uses each runner's output structure to distinguish diagnostics from test failures. It recognizes Go's `FAIL <package> [build failed]` or `[setup failed]` line, which a test file without a valid package clause also gets; pytest's `ERROR collecting` header and `E   SyntaxError` / `E   ImportError` lines; Jest's `● Test suite failed to run` under a `FAIL <file>` header when its `Tests` summary reports zero tests; Vitest's `Error: Failed to load url` diagnostic when its final summary reports a failed test file and no tests; node --test's `SyntaxError:` report without an assertion failure record, or its plain spec reporter error; and tsc's `<file>(<line>,<column>): error TS<n>` or `<file>:<line>:<column> - error TS<n>` diagnostic. A Jest or Vitest final summary with failed tests or a TAP test failure marked `ERR_ASSERTION` shows that an assertion ran, even if its text contains one of those markers. So a Go assertion whose own text holds `[build failed]` remains a valid red.

Red/green runs the report's `test_file` alone through `test_file` in
`.atm.yaml`, once with the fix set aside and once with it back. After each
trial ATM restores ignored files to their state before the first, including
files the trial changed or deleted, so the green cannot pass on the red's
changes. A changed tracked file voids the verdict, and a test that hangs fails
it. Known limit of the `fix` proof: where added lines cannot run alone, as in
a compiled language that needs the base's declarations, that trial fails and
the test passes it.

Every type then fails, naming the old path, when a test file the base has is
deleted or has fewer lines. A shorter file with the same line count is allowed.
If Git detects a rename, ATM checks the staged file at its new path, so moving
an unchanged or longer test is allowed.

## Follow-ups

A follow-up is a behaviour gap the agent found and did not fix. Its report
lists each as `{"title", "red_test", "criterion"}`, with the failing test
written at `.atm/follow-ups/<n>/<red_test>`. After a unit passes Checks, ATM
runs each follow-up test at `red_test` on the unit's work: it must fail. One
without its test, or whose test passes or hangs, is rejected. No model judges
a follow-up.

The first accepted follow-up whose `criterion` quotes the issue becomes the
next unit, while a unit remains, in the same clone: the unit before is
committed as a unit commit, the red test is put in place, and the agent gets
`brief-unit-<n>.md` to make it pass without editing it. The checks prove that
unit red/green as a `feature`. The quote matches a whole issue line or sentence;
whitespace, list markers and a line's final punctuation are normalized.
Other follow-ups go to `follow-ups.json` in the run's directory with their
tests. Each accepted follow-up there has `"verified": true`.

The run allows at most three units. Units after the first share a fixed
200-line gross diff budget: additions plus deletions measured for each unit
against its starting commit, then accumulated. Deleting a line added by an
earlier unit counts again; net growth does not determine the budget. The
primary unit is exempt. The red test ATM placed for the additional unit is
excluded, and binary files contribute no lines. Resuming retains the earlier
units' total and measures the resumed unit again. This budget does not apply
to the slop detector's cuts.

Exceeding the budget fails Checks and saves the chained follow-up as a
blocking child draft in `follow-ups.json`: its title, criterion and red test
form the contract, `diff` holds the actual unit diff, `cause` names the total
and limit, and `dependency` names the unit's starting commit.

A quoted criterion with a verified failing test is mandatory. When no next
unit chains, including after unit three, any such pending follow-up blocks
the run with an error naming the criterion. Neither the slop detector nor
delivery runs. Every mandatory pending item retains its test and criterion,
plus the stopping unit's actual `diff`, a `cause`, and the committed stopping
unit as its `dependency`. The dependency commit is preserved in the resume
bundle. Follow-ups without a matching quote remain out of goal, however
small: they are persisted separately and do not block delivery. The CoS
creates and dispatches child issues from these drafts; workers do not.

A unit that fails before follow-up verification still hands over every
follow-up its report lists: none is run or chained, and each goes to
`follow-ups.json` with the test the
agent left, if any, and `"verified": false`. The run ends at that failure.
When `follow_ups` is invalid, the Checks failure remains the reason, with the
follow-ups problem on the next line. `atm run --resume` reads that unit's
report again, so its follow-ups appear once: unverified while Checks fail,
then verified and sorted as above if Checks pass and their red tests fail.

A unit commit is a conventional commit, so release-please sees it: its
subject is `<type>: <title>`, its body `atm unit <n>`. The type comes from the
issue's `Type:`: `fix` and `hotfix` give `fix`, `feature` and `greenfield`
`feat`, `refactor` `refactor`, `tests` `test`, `docs` `docs`, `chore` `chore`.
A chained unit, a `feature`, is a `feat`.

## Slop detector

Once every unit passed, the same agent gets `brief-ponytail.md`, the issue and
the run's diff, with the bundled `ponytail-review` skill, proven loaded the
same way. It may only cut: delete, shrink, or swap in the standard library or
an existing helper, in the diff's files, keeping every test. Its report is
`{"findings": [{"file", "family", "finding"}], "summary"}`, each `family` a
slopslint family.

ATM snapshots the whole clone first and restores it byte for byte, discarding
the cut, if the cut:

| Discarded when the cut |
|------------------------|
| adds or deletes a file |
| reports no finding, or does not lower the run's net added lines |
| touches a test file, a file outside the diff, or a unit's proven test |
| fails any unit's red/green proof again, or the last unit's checks |

A kept cut is its own commit, `ponytail: <n> cuts`, one line per finding,
after the unit commit, with one slopslint tombstone per finding under
`.slop/tombstones/`. Both commits use the repository's git identity; none, or
an email ending in `@localhost`, fails the run.

## Delivery

When `delivery` is set, the clone goes on a new branch
`atm/<title slug>-<timestamp>`, whatever is left uncommitted is committed as
the next unit commit, and the clone's `origin` becomes the repository's. Every
commit after the base must carry the repository's git identity.

Then the agent, with the `ponytail-review` skill, reviews read only the issue
and the committed diff from the original base to that HEAD. Its final message
must be a single JSON object reporting `approved` or `blocked`, with no
surrounding text; approval requires no findings, and each block quotes the
issue's criterion and its evidence in the diff.
`contract-review.json` in the run's directory keeps the issue, base, HEAD,
report and reason. A block, a report that is not valid or names another base
or HEAD, a failed reviewer, a reviewer that changes the clone or HEAD, or
uncommitted changes after review fails Delivery before its command runs;
nothing retries it. With no `delivery` command, neither the review nor the
command runs.

Once approved, the command runs with `sh -c` in the clone, without a time limit, with these
variables:

| Variable | Value |
|----------|-------|
| `ATM_TITLE` | The issue's title |
| `ATM_ISSUE` | The issue number; empty for a file |
| `ATM_ISSUE_TEXT` | The whole issue, title and body |
| `ATM_INTENT` | `ATM_ISSUE_TEXT`, then, when the slop detector committed tombstones, a `## Files ATM adds` section naming them as ATM's own record, not the issue's scope |
| `ATM_BRANCH` | The branch |
| `ATM_CLONE` | The clone's path |
| `ATM_REPORT` | The path of `report.json` |
| `ATM_PONYTAIL` | The kept cut's findings, one a line, or empty |

With no-mistakes, the line is `no-mistakes axi run --intent "$ATM_INTENT"`.
Its output goes to the screen and to `delivery-output.txt`. ATM never pushes
or opens a pull request: that is the command's business.

## Time limits

| What | Limit |
|------|-------|
| Reading a GitHub issue | 60 s |
| Each command (`install`, `test`, `typecheck`, `lint`, a proof) | 10 min |
| Each agent call, the slop detector's included | See [`agent_timeout`](configuration.md) |
| An agent that writes nothing on stdout or stderr | 10 min |
| The delivery command | none |

Past its limit, or when ATM gets Ctrl-C or SIGTERM, a process is killed with
every process it started, on Unix and Windows, including those that started a
session or process group of their own and, on Unix, those already orphaned.

## The report

`.atm/runs/<label>/report.json` is rewritten at every step's start and end, so
it is on disk before delivery reads it.

| Field | Holds |
|-------|-------|
| `failed_node`, `reason` | The step that failed and why |
| `nodes` | Each step's `result` (`passed`, `failed`, `running`, `skipped` or `not run`), `started_at` (UTC, RFC 3339 with milliseconds) when it starts, and `duration_ms` when it ends; the agent node also has `exit` and `tail` when the agent ran but its step failed |
| `type` | The task type |
| `units` | Each unit's number, base commit, test file, type, result, red/green proof row span, and red and green trials: command, exit code, last 60 lines, `started_at` and `duration_ms` |
| `reproofs` | The same proof rows and spans, run again after a kept cut |
| `commands` | Every configured command that ran, the clone's `install` included, with its result, last 60 lines, `started_at` and `duration_ms` |
| `head_sha` | The delivered branch's final commit |

## The screen

`atm run` hands the run to the repository's background process, one per
repository, started by the first command that needs it and ended after
10 min without a run. Closing the terminal does not stop the run.

On a terminal the screen is the ATM box, a row per step with its icon (`○`
pending, a spinner running, `⏸` waiting, `✓` passed, `✗` failed, `–` skipped), duration and
note, and beside it, from 100 columns, the Agent box while an agent works,
the Findings box with a failure or the slop detector's cuts, and the Log box.
The Agent box renders the agent's Markdown prose within the panel width as
`✎` lines; non-empty thinking appears as `∴` lines. Consecutive successful or running tool calls appear as one dim line
below them, with tool names and repeat counts. A running batch has a spinner
and its active tool's detail; a failed tool keeps its own row. It omits empty thinking, user messages, skill text,
tool-result text and the JSON report at the end of the agent's final message.
Its `N tools` note counts tool calls only.
The ATM box and the Agent box each keep every line and scroll on their own.
The mouse wheel scrolls the box under the pointer. The keys scroll the box
with the `*` marker in its title: the Agent box while it shows, the ATM box
after `Tab`. `↑` and `↓` move by a line, `PgUp` and `PgDn` by a page, and
`Home` and `End` go to the first and the last lines. A box scrolled above its
last line stays where it is as new lines come; back at its last line it follows
them again. Stacked under 100 columns, the ATM box takes at most two thirds of
the space left after the footer and panel gap.
On Unix, ATM keeps its screen live while the delivery command is silent. From
the command's first output until it ends, ATM gives it the attached terminal
and its keys, so no-mistakes shows its own screen there; ATM's screen comes
back when the command ends.
On Windows, the delivery command runs without ATM's attached terminal, so its
output appears as lines.
After a terminal run completes, the ATM screen stays open until `q` or
Ctrl+C; off-terminal output still returns when the run ends.

In a herdr pane (`HERDR_ENV=1` and
`HERDR_PANE_ID` set), the screen also shows the run in herdr's Agents section
as `atm`, with the run's label as its session: `working` with the issue, then
with each step it reaches (`#7 Agent · unit 2`, `#7 Checks · red/green`). Until
the issue title is available, `working` uses the run label. On completion, it reports
`idle` with `#7 passed` or `#7 failed at <step>` when the run ends. An issue
file goes by the run's label instead of `#7`. Leaving the screen, by the run's
end or by `q`, takes `atm` out of the section. ATM runs `$HERDR_BIN_PATH` or
`herdr`, a second at most per call; a failing or missing `herdr` changes
nothing. The coding agent keeps its own herdr reporting, if it has any.

Off a terminal, or with `NO_COLOR` or `TERM=dumb`, the screen is plain lines:
one per step start and end, then the delivery's output and the summary. Every
duration ATM prints reads like `0.9 s`, `4 min 28 s` or `1 h 02 min`.
