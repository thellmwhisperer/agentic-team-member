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
emits start and result JSON lines for the `issue`, `contract`, `clone`, `agent`
and `checks` steps. The `issue` result includes the title and task type on success, plus the issue
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
