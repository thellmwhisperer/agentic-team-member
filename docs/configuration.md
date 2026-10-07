# Configuration

`atm run <issue.md | number>` runs from inside the repository it works on. Its
configuration is YAML in layers, each overriding the one before: built-in
defaults (`harness: claude`), `~/.config/atm/config.yaml` (`harness`, `model`,
`effort` only), `.atm.yaml` at the repository root, then `--harness`, `--model`
and `--effort`. Unknown keys fail the run.

`.atm.yaml` declares every command the run needs. Every key is required; an
empty value is a deliberate "none"; a missing key fails with "config incomplete".
Nothing is detected or defaulted per language.

| Key | Meaning |
|-----|---------|
| `install`, `test`, `typecheck`, `lint` | Shell commands |
| `test_file` | Command for one test; when nonempty, must contain `{file}` or `{dir}` |
| `test_patterns`, `docs_patterns` | Lists of globs for test and doc files |
| `delivery` | The delivery command |

`atm init` writes a commented `.atm.yaml` with every key empty, unless the
repository has one, and adds `/.atm/` to `.gitignore` unless it is there; run
twice, it changes nothing. `atm doctor` checks git, the configured agent CLI on
`PATH`, `gh` when `origin` is on GitHub, and that `.atm.yaml` loads with every
key, one `ok` or `fail` line each; every check runs, and any failure exits
non-zero.

For an issue file, the first line is its title (all leading `#` characters are
dropped) and the rest its body. The body must declare a case-sensitive `Type:`
line with a lowercase type: `fix` (or `hotfix`), `feature`,
`greenfield`, `refactor`, `tests`, `docs` or `chore`. A number is read with
`gh issue view` from the repository `origin` names.

ATM validates configuration before reading the issue, then emits start and
result events for the `issue`, `clone`, `contract`, `agent`,
`checks`, `ponytail` and `delivery` steps. The `issue` result includes the title
and task type on success, plus the issue number for GitHub issues. Each run
keeps its report, briefs, agent logs, follow-ups and delivery output in
`.atm/runs/<label>/`, where `<label>` is its label in `atm runs`; the directory
remains after the run. Clones and background process state stay elsewhere under
`.atm/`. The `contract` step follows the clone, writes `brief.md` in the run's
directory and reports its path on success. The brief starts by directing the
agent to read `AGENTS.md` if present, then gives the issue, task-type acceptance
criteria, verification commands from `.atm.yaml`, style and
forbidden actions, and the required JSON report.

The `clone` step follows the issue. `--base-ref` (default `main`) is resolved to a SHA in
the repository; the run claims `.atm/clones/atm-run-<timestamp>` (`-2`, `-3`
for runs started in the same second), clones the repository there detached at
that SHA, excludes `/.atm/` in the clone, and runs `install` in it with `sh -c`
(empty: nothing to install). Any failure, or a command running past 10 minutes,
fails the step and the run. Its end line carries `clone` and `sha`.

The clone is removed when the run ends, unless the run delivered and its PR is
open. On Unix, each run's commands inherit a shared lock in `<clone>.lock`;
cleanup removes the clone only after it can take an exclusive lock. On Windows,
cleanup also renames the clone before removing it, leaving it in place if the
rename fails because a live process is using it. `<clone>.delivered` holds
`<branch> <base>` after the delivery command ends, including when it exits
non-zero. Each run first removes the clones of runs that are gone, except a
delivered clone whose branch is not merged into its base (`git branch --merged`
after a fetch from the repository) and whose PR
`gh` does not report closed or merged, when origin is on GitHub. On Unix, a
descendant that closes its inherited descriptors (for example a Python
subprocess with `close_fds`) does not hold the clone.

The `agent` step follows the contract. The agent is `--harness` (`claude`,
`codex`, `opencode` or `pi`; any other fails the config) with the run's model
and effort, in its most minimal mode: `claude -p` with `--setting-sources project`,
`--strict-mcp-config` and only `Read,Edit,Write,Bash,Glob,Grep,Skill`; `pi -p`
with `--no-extensions --no-skills --no-prompt-templates --no-context-files
--no-session`; `codex exec` and `opencode run --pure`, which still load the
user's skills. Local models run through `pi`. `claude` reads the brief on
stdin, the others as their last argument. `--harness-arg` (repeatable) adds an
argument before the brief, `--env KEY=VALUE` (repeatable) a variable to the
agent's environment; `CLAUDE_CODE_CHILD_SESSION` is removed from it.

The agent always loads the bundled `ponytail` skill. ATM writes it into the
clone, excluded from git: `.claude/skills/` for `claude`, `.agents/skills/` for
`codex`, `.opencode/skills/` for `opencode`, and `.atm/skills/` passed with
`--skill` for `pi`. The agent's log must prove it was used (a `Skill` tool call
for `claude`, a read of its `SKILL.md` for `pi` and `codex`, a `skill` event for
`opencode`), or the run dies.

The agent runs in the clone. Past 30 minutes, or when ATM gets SIGINT or
SIGTERM, cancellation kills its process tree on Unix or Windows, including
descendants that start a session or process group of their own. On Unix, ATM
marks the agent's environment with `ATM_RUN_MARK` and also kills every process
of the user that bears the mark, so a descendant already orphaned to init dies
too, unless it cleared its environment.
Every line it writes goes to `worker.jsonl` in the run's directory:
JSON lines as they are, any other line as a JSON string. The
report is the last JSON object of its final message (`codex`: its `-o` file,
`worker.final.md`) and must carry `test_file`: ATM never
guesses it. A non-zero exit, a timeout, an interruption or no readable report
fails the step and the run. Its end line carries `log` and `report`.

The `checks` step follows the agent, in the clone. The agent must not have
committed: HEAD still the base SHA. Then the task type's proof, then the
`.atm.yaml` commands `install` (again: the agent may have changed
dependencies), `test`, `typecheck` and `lint`, each with `sh -c`, empty
skipped. The first failure fails the step and the run, with that command's last
60 lines. After the commands, HEAD must still be the unit's base SHA: a command
that commits fails `checks` and prevents delivery. Each command runs in its own
process group, killed past 10 minutes.

`fix`, `feature` and `greenfield` prove red/green: the agent's changes are set
aside except the report's `test_file`, which runs alone through `test_file`
(`{file}` is its path, `{dir}` its directory as `./dir`) and must fail; the
changes come back and it must pass. A `fix` sets aside only the files the base
has, so the files the fix added stay and the red fails on behaviour: a test
that only exercises a new file passes without the fix and is rejected (so is a
fix made only of new files). A `fix` then runs its test twice more, the new
files kept and each old file it changes rebuilt from its diff: the base with
every added line in place (a changed line goes before the base line it
replaces), then only the added lines. A test that passes on both depends on
nothing the base had, such as one that only calls a function the fix adds to an
old file, and is rejected. Known limit: where a file's added lines cannot run
alone, as in a compiled language that needs the base's declarations, the
second trial fails and such a test passes. `feature` and `greenfield` set aside everything,
so the red may fail on a missing symbol or module. `refactor` runs `test` on
the base, which must pass, and may not change a file matching `test_patterns`.
`tests` runs the new test on the base and after, both must pass, and may change
only test and doc files. `docs` may change only files matching
`docs_patterns`. `chore` has no proof beyond the commands. After its proof,
every type fails, naming the file, when a file matching `test_patterns` that the
base has is deleted or ends with fewer bytes. A hung test fails
the run, and the clone must be the same after verification as before, or the
verdict is void.

Once the checks pass, they sort the report's `follow_ups`, each `{"title",
"red_test", "criterion"}`: a behaviour gap the agent found and did not fix.
Follow-up `n`'s test must be on disk at `.atm/follow-ups/<n>/<red_test>` in the
clone; `red_test` must be a new path outside `.atm/`. It runs alone at
`red_test` through `test_file` on the unit's work and
must fail, then the clone comes back as it was. One without its test, or whose
test passes or hangs, is rejected. No model judges a follow-up and nothing
checks what code its test calls. An accepted follow-up whose `criterion` is a
whole sentence of the issue, verbatim but for line breaks, or a whole issue
line after a list marker and final punctuation are removed, runs as the next
unit; any other goes to `follow-ups.json` in the run's directory, with its
test's text, to become a new issue. The checks' end line carries `follow_ups`,
where each one went.

The next unit runs in the same clone: the unit before is committed on HEAD as
`atm unit <n>: <title>` under the repository's git identity, its
`.atm/follow-ups` removed, and the first chained follow-up's red test put in
place. Its brief, `brief-unit-<n>.md`, asks to make that test pass without
editing it; its log is `worker-unit<n>.jsonl`. The `agent` and
`checks` steps run again, their end lines with `unit`; the checks prove it
red/green with that test, as a `feature`, and fail when it changed. A run makes
at most 3 units: the accepted follow-ups not chained, the third unit's
included, go to `follow-ups.json`, rewritten at the end of every run.

The `ponytail` step, the slop detector, runs only after the checks passed. It
writes `brief-ponytail.md` (the issue and the run's diff against the base)
and runs the same agent with the bundled `ponytail-review` skill, placed and
proven in its log the same way, logging to `ponytail.jsonl`.
The agent may only cut: delete, shrink, or replace with the standard library or
an existing helper, in the diff's files, adding none, every test kept. Its
report is `{"findings": [{"file", "family", "finding"}], "summary"}`, each
`file` in the diff, each `family` a slopslint family, each `finding` one line.
A timeout, a non-zero exit, no proof of the skill, an invalid report or a commit
by the ponytail agent fails the step and the run. A commit by a re-check command
also fails the step and the run, even when the cut would otherwise be discarded.

The cut is discarded, and the clone comes back exactly as before, when it does
not lower the run's net added lines, reports no finding, adds a file, deletes a
file, touches a file matching `test_patterns` or one outside the diff, changes
a unit's test file since that unit was proven, fails an earlier unit's red test
on its own base or its green test on the final tree with the cut, or fails the
last unit's `checks` step run again without a command committing. A kept cut leaves two
commits on the units before it: `atm unit <n>: <title>` with the last unit's
work before the cut, then
`ponytail: <n> cuts` with the cut, one `- <file>: <finding> (<family>)` line per
finding, and one slopslint tombstone per finding under `.slop/tombstones/`.
Both use the repository's git identity (`git var` at its root, never one of
ATM's own); none, or an email ending in `@localhost`, fails the run before the
commit. Its end line carries `log`, `report`, `net_lines` (before and after the
cut) and `kept`, then `reason` when discarded or `commits` and `tombstones` when
kept.

The `delivery` step follows the ponytail step when `delivery` is set, so only
after every unit and the slop detector passed. The clone goes on a new branch
`atm/<slug>-<timestamp>` (the title in lowercase letters, digits and dashes, at
most 40; the clone's timestamp), anything left uncommitted becomes
`atm unit <n>: <title>` (the next unit number) under the repository's identity, as
for the ponytail commits, and
the clone's `origin` is set to the repository's: no `origin` fails the step.
Before running the delivery command, ATM checks the author and committer of
every commit after the base SHA through HEAD against the repository's git
identity. A mismatch fails delivery; with commits to check, a missing identity
also fails delivery. If there are no such commits and nothing to commit,
delivery does not require a git identity.

`report.json` is rewritten with `head_sha`, the branch's final SHA. Then the
command runs with `sh -c` in the clone without a time limit. SIGINT or SIGTERM
to the ATM process cancels it, killing its process group on Unix or process
tree on Windows. Its environment contains `ATM_TITLE`, `ATM_ISSUE` (the issue
number, empty for a file), `ATM_BRANCH`,
`ATM_CLONE`, `ATM_REPORT` and `ATM_PONYTAIL` (the kept cut's findings, one a
line, or empty). Its output goes to the run's screen and to
`delivery-output.txt` in the run's directory. ATM never pushes or opens a pull request: that is
the command's business. Its end line carries `branch`. Empty, the step is
skipped and the run ends there.

The documented example hands the branch to no-mistakes:

```yaml
delivery: 'no-mistakes axi run --intent "$ATM_TITLE"'
```

Without `--yes`, no-mistakes only reports: whoever launched the run answers
each finding.

Every step's end line carries `duration_ms`. `report.json` in the run's
directory is rewritten at every step's start and end, so it is on disk
before delivery: `failed_node` and `reason` first, then `nodes` (each step's
`result`: `passed`, `failed`, `running`, `skipped` or `not run`, and
`duration_ms`, the last unit's for `agent` and `checks`), the task `type`, and `commands`: the configured `install`, `test`,
`typecheck`, `lint` and `delivery` commands that ran during checks or delivery,
each with its result and last 60 lines. After a delivery got its branch, `head_sha`
is that branch's final SHA. The summary goes to
the run's screen: each step, how it ended and its duration (`0.9 s`, `4 min 28 s`,
`1 h 02 min`, the one format ATM prints a time in), then the result and the
report's path. A run ends with exit code 0 when every executed step passed, 1 when `agent` or
`checks` failed, 4 when `delivery` failed, and 2 for anything else: bad
flags or config, an unreadable issue, a failed contract or clone. There is no
`--label` yet.

### Runs in the background

`atm run` checks its command line, hands the run to the repository's
background process and returns. Off a terminal it prints the run's label
(`<issue name>-<n>`, `n` counting the repository's runs); on a terminal it
shows the run's screen and exits with the run's code once it ends. Closing the
terminal, or leaving with `q` or Ctrl-C, leaves the run going.

On a terminal, ATM's screen uses ANSI colours 1 to 8 and rounded boxes titled
in their top border. The ATM box has the issue and the run's state, then a row
per node:
Issue, Clone, Contract, Agent · unit n and Checks (again for each chained unit,
the checks with a row per proof and command), Slop detector, Delivery. Each row
has its state's icon (`○` pending, a spinner running, `⏸` waiting, `✓` passed,
`✗` failed, `–` skipped), its duration and, right-aligned, its note and its
state's word. Beside it from 100 columns (a third of the width, 38 to 48), below
it under that: the Agent box while an agent runs, each tool call with its
result and its thinking; the Findings box once a node failed, its reason, or
once the slop detector kept a cut, each cut with its slop family; the Log box
with the running node's output. Connectors between nodes need 30 rows. While
the delivery command runs, the terminal is the command's, on a pseudo-terminal
of its size with its keys (Unix only): a command that attaches no-mistakes
shows no-mistakes' own screen there, and ATM's screen comes back when it exits.

Off a terminal, or with `NO_COLOR` set or `TERM=dumb`, the screen is plain
lines: one per step event (`clone     passed 0.9 s`, the error's first line
after a failure), then the delivery's output and summary.

The background process is `atm serve`, one per repository, started on demand
by the first command that needs it, and listening on `.atm/atm.sock` (mode
`0600`; a Unix socket on Windows too). It holds `.atm/atm.lock` while serving,
so commands started together use the same process; the operating system releases
the lock when that process exits. It runs every run in itself and logs to
`.atm/serve.log`. It ends after 10 minutes without a run, or once its socket is
removed while no run goes. The latest state of each retained run is saved to
`.atm/runs.jsonl`, so a new background process still lists them; a run left
running there is shown failed at its last saved step, with its duration measured
through the last save. On each new run and when a run ends, it drops the oldest
ended runs while the list exceeds 200. It never drops a running run. Clone
cleanup uses the locks described above,
even when the background process that started
the run has exited.

| Command | Prints |
|---------|--------|
| `atm status` | Each run going: label, step, duration, issue; or `nothing runs` |
| `atm attach [run]` | The run's screen (the last one going by default) until it ends; leaving leaves it going |
| `atm runs` | Retained runs: label, `running`, `passed` or `failed at <node>`, duration, issue and the report path when set |
| `atm axi run [--json] <atm run's arguments>` | Waits for the end, then the outcome |
| `atm axi status [--json]`, `atm axi runs [--json]` | The runs going, or retained runs, as a table |

`atm axi` prints TOON by default and JSON with `--json`. The outcome always
carries every field, empty or not: `outcome`, `run`, `failed_node`, `reason`,
`report` (empty when the run failed before its first step) and `next_step`.
`atm axi run` exits with the run's code. The tables are `runs` with `run`,
`issue`, `step` and `duration` for `status`, and `run`, `issue`, `outcome`,
`failed_node`, `duration` and `report` for `runs`.
