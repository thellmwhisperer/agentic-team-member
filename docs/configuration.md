# Configuration

Two sources, and they do not overlap.

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
| `--artifact-dir`, `--log-dir` | from `--label` or a timestamp | Where the report and the log go |
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
| `dir` | `"~/.atm/runs"` | One subdirectory per run (`report.json`, `worker-*.jsonl`, briefs, `delivery-output.txt`, `command.txt`). `~` is expanded; a relative path is exit 2. `--artifact-dir` overrides it for one run; `scripts/tail-run.py --config` reads it too |

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
