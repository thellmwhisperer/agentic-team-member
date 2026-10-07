# Configuration

The Python worker uses two sources, and they do not overlap. The Go port reads
[its own YAML layers](#go-port-configuration).

- **The command line** chooses what varies per run.
- **The TOML file** (`--config`, default `config/agent.toml`) holds what is
  true for a target repository across runs. Copy it to a `*.local.toml`, which
  git ignores, and pass it with `--config`.

`tests/test_config.py` asserts that every key in `config/agent.toml` is read by
some module in the package. A key nobody reads fails the suite.

## Command line

| Flag | Default | Meaning |
|------|---------|---------|
| `--repo` | required | The git repository to fix |
| `--issue-number` + `--github-repo` | | A GitHub issue, read with `gh` |
| `--issue-file` | | A text file instead: first line the title |
| `--base-ref` | `main` | Resolved in `--repo`, so `origin/main` works |
| `--harness` | `claude` | `claude`, `codex`, `opencode` or `pi` |
| `--model`, `--effort` | the CLI's own | `--effort` is `low` to `max`; Codex has no `xhigh` or `max` |
| `--harness-arg`, `--env`, `--harness-bin` | | Extra CLI arguments, environment, or another executable for the agent |
| `--timeout` | `1800` | Seconds for one agent call |
| `--scope GLOB` | from the issue | Paths the diff may touch, repeatable |
| `--max-units` | `[harness_worker].max_units` | First unit plus chained follow-ups |
| `--label` | | Run directory `<[runs].dir>/<label>`; exit 2 if it exists |
| `--artifact-dir` | `[runs].dir/<label or timestamp>` | Override the run directory for one run |
| `--log-dir` | the run directory | Override where `worker-*.jsonl` is written |
| `--run-root` | `<repo>/.worktree` | Where clones go; relative to `--repo` |
| `--config` | `config/agent.toml` | The TOML file |
| `--dry-run` | | Prepare, print the contract, stop |

## `[timeouts]`

| Key | Default | Read at |
|-----|---------|---------|
| `test_run` | `30` | One red or green run of one test file, and each follow-up's red test. A slow test times out and fails the unit; raise it |
| `tool_execution` | `60` | Each lint, format or typecheck tool in the quality step |

## `[runner]`

Consulted for JavaScript and TypeScript test files and for languages ATM does
not know. Python test files always run with `python3 -m pytest`.

| Key | Default | Meaning |
|-----|---------|---------|
| `command` | `""` | Command for one test file; `{test_file}` and `{test_dir}` are substituted, otherwise the file is appended. Go needs `go test {test_dir}` |
| `framework` | `""` | `bun`, `vitest`, `jest`, `node`: forces the template when `override_detected` is on |
| `override_detected` | `false` | `true` makes `command` win over what the bootstrap detected |
| `test_file_patterns` | `[]` | fnmatch patterns on the file name; empty means the name has `test` next to `_`, `.` or `-` |

## `[environment]`

| Key | Default | Meaning |
|-----|---------|---------|
| `install` | `"auto"` | `auto`, `always` or `never` for the JavaScript install |
| `require_clean` | `true` | Refuse a dirty clone |
| `run_typecheck` | `true` | Run the JavaScript typecheck as a preflight |
| `timeout` | `300` | Seconds for install, preflight, the full suite and typecheck |
| `preflight_test_command` | unset | One more JavaScript preflight command |

## `[runs]`

| Key | Default | Meaning |
|-----|---------|---------|
| `dir` | `"~/.atm/runs"` in the shipped config | Base directory for runs, independent of the caller's working directory. `~` is expanded; a relative path makes the worker exit 2. `scripts/tail-run.py` reads the same key from its `--config` file (default `config/agent.toml`) when finding a run by label or choosing the newest run |

## `[monitor]`

| Key | Default | Meaning |
|-----|---------|---------|
| `command` | `""` | Shell line run at every step start and end, with `ATM_LABEL`, `ATM_STEP`, `ATM_STATE`, `ATM_DURATION`, `ATM_REPORT`. Output discarded, exit code ignored, cut after 5 s. Empty means stdout must be a terminal, or the worker exits 2 |

## `[delivery]`

| Key | Default | Meaning |
|-----|---------|---------|
| `command` | the no-mistakes example | Shell line run in the clone after a green run and the ponytail pass, on branch `atm/<slug>-<timestamp>`, with `ATM_TITLE`, `ATM_ISSUE`, `ATM_BRANCH`, `ATM_CLONE`, `ATM_REPORT`, `ATM_PONYTAIL`. Non-zero exit is worker exit 4. Empty skips delivery |
| `lint_command` | `""` | The target's lint; a ponytail cut must pass it to be kept |

## `[harness_worker]`

| Key | Default | Meaning |
|-----|---------|---------|
| `max_units` | `3` | Units per run: the first one plus chained follow-ups |

## `[quality]`

| Key | Default | Meaning |
|-----|---------|---------|
| `enabled` | `true` | Turn the whole quality step off |
| `typescript.forbidden` | `as any`, `as unknown as`, `as never`, `{} as`, `: any`, `eslint-disable`, `@ts-ignore`, `@ts-expect-error` | Rejected on added lines |
| `python.forbidden` | `type: ignore`, `noqa` | Same |
| `go.forbidden` | unset | Same, applied by `scan_forbidden` |
| `<language>.checks` | unset | Tools to run when auto-detection finds none: `name`, `command`, optional `fix` |

### `[quality.duplicated_setup_judge]`

Consulted only when a test repeats a line and the heuristics cannot tell
setup from slop. It can reject; it cannot approve. If it does not answer, the
lines pass.

| Key | Default |
|-----|---------|
| `enabled` | `true` |
| `url` | `http://127.0.0.1:11434/api/chat` (Ollama's native chat endpoint) |
| `model` | `gemma4:e2b` |
| `temperature`, `num_ctx`, `think`, `timeout` | `0`, `4096`, `false`, `10` |

## `[prompt]`

| Key | Meaning |
|-----|---------|
| `quality_failed` | Wraps the quality findings in the unit record; `{details}` is substituted |

## `[follow_ups.judge]`

Optional. Off unless the table is present with `enabled = true`.

| Key | Default | Meaning |
|-----|---------|---------|
| `enabled` | `false` | Ask TypeSafe's Jev whether a follow-up serves the issue |
| `model` | `jev-latest` | |
| `threshold` | `0.6` | Below it, the follow-up is not chained |
| `api_key_file` | `~/.config/typesafe/api_key` | `TYPESAFE_API_KEY` in the environment wins |
| `timeout` | `30` | Seconds |

Enabled with no key, the judge fails closed: the follow-up stays in the report
and is not chained.

## `[tooling]`

| Key | Meaning |
|-----|---------|
| `path_dirs` | Directories appended to `PATH` for prep, suite, typecheck and delivery |

## Environment variables

| Variable | Effect |
|----------|--------|
| `TYPESAFE_API_KEY` | Key for the follow-up judge |
| `CI=1` | Set on clone, install, preflight, full suite and typecheck |
| `PYTHONDONTWRITEBYTECODE=1` | Set by the worker, so stale `.pyc` files cannot survive the red/green stash |
| `CLAUDE_CODE_CHILD_SESSION` | Removed from the agent's environment, so a nested Claude Code session keeps its transcripts |
| `ATM_*` | Set for `[monitor].command` and `[delivery].command`, as listed above |

## Go port configuration

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

For an issue file, the first line is its title (all leading `#` characters are
dropped) and the rest its body. The body must declare a case-sensitive `Type:`
line with a lowercase type: `fix` (or `hotfix`), `feature`,
`greenfield`, `refactor`, `tests`, `docs` or `chore`. A number is read with
`gh issue view` from the repository `origin` names.

The current Go port validates configuration before reading the issue, then
emits start and result JSON lines for the `issue`, `contract`, `clone`, `agent`,
`checks`, `ponytail` and `delivery` steps. The `issue` result includes the title and task type on success, plus the issue
number for GitHub issues. The `contract` step writes `.atm/brief.md` at the
repository root and reports its path on success. The brief starts by directing
the agent to read `AGENTS.md` if present, then gives the issue, task-type
acceptance criteria, verification commands from `.atm.yaml`, style and
forbidden actions, and the required JSON report.

The `clone` step follows the contract. `--base-ref` (default `main`) is resolved to a SHA in
the repository; the run claims `.atm/clones/atm-run-<timestamp>` (`-2`, `-3`
for runs started in the same second), clones the repository there detached at
that SHA, excludes `/.atm/` in the clone, and runs `install` in it with `sh -c`
(empty: nothing to install). Any failure, or a command running past 10 minutes,
fails the step and the run. Its end line carries `clone` and `sha`.

The clone is removed when the run ends, unless the run delivered and its PR is
open. Next to each clone, `<clone>.pid` holds its run's pid and
`<clone>.delivered` holds `<branch> <base>` once it delivered. Each run first
removes the clones of runs that are gone, except a delivered clone whose branch
is not merged into its base (`git branch --merged` after a fetch from the
repository) and whose PR `gh` does not report closed or merged, when origin is
on GitHub.

The `agent` step follows the clone. The agent is `--harness` (`claude`, `codex`,
`opencode` or `pi`; any other fails the config) with the run's model and effort,
in its most minimal mode: `claude -p` with `--setting-sources project`,
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
SIGTERM, cancellation kills its process group on Unix or its process tree on
Windows. Every line it writes goes to `.atm/worker-<timestamp>.jsonl`, named
after the clone: JSON lines as they are, any other line as a JSON string. The
report is the last JSON object of its final message (`codex`: its `-o` file,
`.atm/worker-<timestamp>.final.md`) and must carry `test_file`: ATM never
guesses it. A non-zero exit, a timeout, an interruption or no readable report
fails the step and the run. Its end line carries `log` and `report`.

The `checks` step follows the agent, in the clone. The agent must not have
committed: HEAD still the base SHA. Then the task type's proof, then the
`.atm.yaml` commands `install` (again: the agent may have changed
dependencies), `test`, `typecheck` and `lint`, each with `sh -c`, empty
skipped. The first failure fails the step and the run, with that command's last
60 lines. Each command runs in its own process group, killed past 10 minutes.

`fix`, `feature` and `greenfield` prove red/green: the agent's changes are set
aside except the report's `test_file`, which runs alone through `test_file`
(`{file}` is its path, `{dir}` its directory as `./dir`) and must fail; the
changes come back and it must pass. A `fix` sets aside only the files the base
has, so the files the fix added stay and the red fails on behaviour: a test
that only exercises a new file passes without the fix and is rejected (so is a
fix made only of new files). `feature` and `greenfield` set aside everything,
so the red may fail on a missing symbol or module. `refactor` runs `test` on
the base, which must pass, and may not change a file matching `test_patterns`.
`tests` runs the new test on the base and after, both must pass, and may change
only test and doc files. `docs` may change only files matching
`docs_patterns`. `chore` has no proof beyond the commands. A hung test fails
the run, and the clone must be the same after verification as before, or the
verdict is void.

Once the checks pass, they sort the report's `follow_ups`, each `{"title",
"red_test", "criterion"}`: a behaviour gap the agent found and did not fix.
Follow-up `n`'s test must be on disk at `.atm/follow-ups/<n>/<red_test>` in the
clone; it runs alone at `red_test` through `test_file` on the unit's work and
must fail, then the clone comes back as it was. One without its test, or whose
test passes or hangs, is rejected. No model judges a follow-up and nothing
checks what code its test calls. An accepted follow-up whose `criterion` is a
whole sentence of the issue, verbatim but for line breaks, runs as the next
unit; any other goes to `.atm/follow-ups.json` at the repository root, with its
test's text, to become a new issue. The checks' end line carries `follow_ups`,
where each one went.

The next unit runs in the same clone: the unit before is committed on HEAD as
`atm unit <n>: <title>` under the repository's git identity, its
`.atm/follow-ups` removed, and the first chained follow-up's red test put in
place. Its brief, `.atm/brief-unit-<n>.md`, asks to make that test pass without
editing it; its log is `.atm/worker-unit<n>-<timestamp>.jsonl`. The `agent` and
`checks` steps run again, their end lines with `unit`; the checks prove it
red/green with that test, as a `feature`, and fail when it changed. A run makes
at most 3 units: the accepted follow-ups not chained, the third unit's
included, go to `follow-ups.json`, rewritten at the end of every run.

The `ponytail` step, the slop detector, runs only after the checks passed. It
writes `.atm/brief-ponytail.md` (the issue and the run's diff against the base)
and runs the same agent with the bundled `ponytail-review` skill, placed and
proven in its log the same way, logging to `.atm/ponytail-<timestamp>.jsonl`.
The agent may only cut: delete, shrink, or replace with the standard library or
an existing helper, in the diff's files, adding none, every test kept. Its
report is `{"findings": [{"file", "family", "finding"}], "summary"}`, each
`file` in the diff, each `family` a slopslint family, each `finding` one line.
A timeout, a non-zero exit, no proof of the skill, an invalid report or a commit
fails the step and the run.

The cut is discarded, and the clone comes back exactly as before, when it does
not lower the run's net added lines, reports no finding, adds a file, deletes a
file, touches a file matching `test_patterns` or one outside the diff, or fails
the `checks` step run again. A kept cut leaves two commits on the units
before it: `atm unit <n>: <title>` with the last unit's work before the cut, then
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
most 40; the clone's timestamp), anything left uncommitted becomes `atm unit 1:
<title>` under the repository's git identity, as for the ponytail commits, and
the clone's `origin` is set to the repository's: no `origin` fails the step.
`report.json` is rewritten with `head_sha`, the branch's final SHA. Then the
command runs with `sh -c` in the clone, killed past 10 minutes, with
`ATM_TITLE`, `ATM_ISSUE` (the issue number, empty for a file), `ATM_BRANCH`,
`ATM_CLONE`, `ATM_REPORT` and `ATM_PONYTAIL` (the kept cut's findings, one a
line, or empty) in its environment. Its output goes to stderr and to
`.atm/delivery-output.txt`. ATM never pushes or opens a pull request: that is
the command's business. Its end line carries `branch`. Empty, the step is
skipped and the run ends there.

The documented example hands the branch to no-mistakes:

```yaml
delivery: 'no-mistakes axi run --intent "$ATM_TITLE"'
```

Without `--yes`, no-mistakes only reports: whoever launched the run answers
each finding.

Every step's end line carries `duration_ms`. `.atm/report.json` at the
repository root is rewritten at every step's start and end, so it is on disk
before delivery: `failed_node` and `reason` first, then `nodes` (each step's
`result`: `passed`, `failed`, `running`, `skipped` or `not run`, and
`duration_ms`, the last unit's for `agent` and `checks`), the task `type`, and `commands`: the configured `install`, `test`,
`typecheck`, `lint` and `delivery` commands that ran during checks or delivery,
each with its result and last 60 lines. After a delivery got its branch, `head_sha`
is that branch's final SHA. The summary goes to
stderr: each step, how it ended and its duration (`0.9 s`, `4 min 28 s`,
`1 h 02 min`, the one format ATM prints a time in), then the result and the
report's path. `atm run` exits 0 when every executed step passed, 1 when `agent` or
`checks` failed, 4 when `delivery` failed, and 2 for anything else: bad
flags or config, an unreadable issue, a failed contract or clone. There is no
`--label` yet.
