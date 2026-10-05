# ATM: Agentic Team Member

> An issue goes in. A verified patch comes out.

[![python](https://img.shields.io/badge/python-3.12%2B-blue)](#requirements)
[![license](https://img.shields.io/badge/license-Apache%202.0-blue)](LICENSE)
[![status](https://img.shields.io/badge/status-alpha-orange)](#status)

ATM hands one bug to a coding agent you already run (Claude Code, Codex, OpenCode or
pi) inside a throwaway clone of your repository. The agent writes a failing test and the
fix. ATM then checks the result itself and records a verdict:

```
issue ──▶ clone ──▶ agent: red test, then fix ──▶ ATM gates ──▶ report.json (+ patch.diff)
                                                     │
              accepted follow-up with a red test ◀───┘  (same clone, next unit)
```

ATM does not call a model API. The agent runs as a subprocess with your own login.
ATM never pushes or opens a PR. It commits only inside the throwaway clone, between
chained units.

## The gates

A unit passes only if every gate passes:

| Gate | What ATM checks |
|---|---|
| red / green | ATM runs the agent's test with the source change parked: it must fail. With the change back, it must pass. |
| behavioral red | The red failure is not a missing module or import. |
| untouched verdict | The worktree is identical before and after verification. |
| pre-existing source | HEAD did not move, and the diff changes at least one source file that existed. |
| scope | Every changed non-test file matches a `--scope` glob. Without `--scope`: the files the issue names, or no limit if it names none. |
| full suite, typecheck | The repo's own commands, when detected: `go test ./...` and `go vet ./...`, `python3 -m pytest`, or the package.json test and typecheck scripts. |
| quality | Python and TypeScript: detected linters and formatters, plus `[quality.<lang>].forbidden` patterns. Other languages: the forbidden patterns only. |

## Follow-ups

An agent that finds a gap outside the scope must not fix it. It declares a follow-up:
a red test on disk under `.atm/follow-ups/<n>/` plus one sentence from the issue that
the gap violates. ATM accepts the follow-up only if the red test fails today on
behavior. It chains the follow-up as the next unit, in the same clone, only if:

1. the test calls code that exists in the base commit;
2. the cited sentence is in the issue;
3. the optional judge (`[follow_ups.judge]`) says the gap serves this issue.

`--max-units` (default 3) caps the chain. Accepted follow-ups that are not chained are
written to `follow-ups.json`.

## Quick start

```bash
git clone https://github.com/thellmwhisperer/agentic-team-member
cd agentic-team-member
python3 -m pip install 'requests>=2.31,<3'

# The issue is a markdown file: the first line is the title.
python3 -m agentic_tdd_runner.harness_worker \
  --repo ~/code/your-repo --base-ref origin/main \
  --issue-file issue.md --harness claude --model claude-opus-5-5 --effort high \
  --scope 'src/billing/*.py'

# Or a GitHub issue, read with gh.
python3 -m agentic_tdd_runner.harness_worker \
  --repo ~/code/your-repo --issue-number 300 --github-repo you/your-repo --harness codex

# Prepare the clone and print the brief, without running an agent.
python3 -m agentic_tdd_runner.harness_worker --repo ~/code/your-repo --issue-file issue.md --dry-run
```

`python3 -m agentic_tdd_runner.harness_worker --help` lists every flag. The main ones:

| Flag | Meaning |
|---|---|
| `--repo`, `--base-ref` | Repository to fix and the ref to clone (default `main`) |
| `--issue-file` or `--issue-number` + `--github-repo` | Where the issue comes from |
| `--harness` | `claude`, `codex`, `opencode` or `pi` (default `claude`) |
| `--model`, `--effort` | Passed to the agent; `--effort` is one of `low`, `medium`, `high`, `xhigh`, `max` |
| `--scope GLOB` | Paths the diff may touch, repeatable; test files are always allowed |
| `--max-units N` | Longest follow-up chain |
| `--env KEY=VALUE`, `--harness-arg ARG` | Extra environment and arguments for the agent process |
| `--timeout` | Seconds per unit before the agent is killed (default 1800) |
| `--config` | Worker TOML (default `config/agent.toml`) |
| `--artifact-dir`, `--log-dir` | Output directory (default `.tmp/harness-worker/<timestamp>`) |
| `--run-root` | Where clones go (default `REPO/.worktree`) |

Exit code: 0 when every unit passed, 1 when a unit failed, 2 when the clone or the
environment could not be prepared.

The artifact directory holds `brief.md`, `report.json` and the full event stream as
`worker-<timestamp>.jsonl`. The clone stays at `REPO/.worktree/atm-run-<timestamp>`
until you remove it.

### scripts/atm-run.py

A launcher for repeated runs against one target. It gives each run a label and an output
directory under `.tmp/harness-worker/<label>`, refuses to reuse a label, saves
`patch.diff` from the clone when the run ends, and prints a short verdict.

```bash
python3 scripts/atm-run.py run --config config/agent.myrepo.local.toml \
  --label opus-1 --harness claude --model claude-opus-5-5 --effort high
python3 scripts/atm-run.py show --label opus-1
python3 scripts/atm-run.py list
python3 scripts/atm-run.py clean --config config/agent.myrepo.local.toml --yes
```

`--pane <id>` launches the run inside a herdr terminal pane and waits until the worker has started. The `--config` file is the worker config
plus a `[launch]` table, so start from a copy of `config/agent.toml`:

```toml
[launch]
repo = "/path/to/target-repo"     # required
issue_number = 300                # required
github_repo = "owner/repo"
base_ref = "origin/main"
run_root = ".worktrees"
scope = ["src/billing/*.py"]
```

Files named `*.local.toml` are ignored by git.

## Watching a run

```bash
python3 scripts/tail-run.py                              # newest run under .tmp/harness-worker
python3 scripts/tail-run.py .tmp/harness-worker/opus-1   # one run directory, or a worker-*.jsonl
python3 scripts/tail-run.py --no-follow                  # print what is there and exit
```

It prints the agent's thinking, text and tool calls, and ATM's own events: scope, unit
start and end, red/green result, rejected follow-ups and the final result. It stops when
the run writes its report. The worker prints the same agent stream on stdout, and
`atm-run.py` saves it as `stdout.txt`.

## Configuration

`--config` points to a TOML file. `config/agent.toml` is the default and documents every
key. The worker reads these tables:

| Table | Keys |
|---|---|
| `[timeouts]` | `test_run` (one test file, required), `tool_execution` (each quality tool) |
| `[runner]` | `command`, `framework`, `override_detected`, `test_file_patterns`: override the detected test runner |
| `[environment]` | `install` (`auto`, `always`, `never`), `require_clean`, `run_typecheck`, `timeout` |
| `[quality]` | `enabled`; `[quality.python]`, `[quality.typescript]`, `[quality.go]`: `forbidden` patterns |
| `[quality.duplicated_setup_judge]` | Local Ollama model that flags duplicated test setup; errors do not fail the run |
| `[prompt]` | `quality_failed`: template for the quality failure message |
| `[tools]` | `path_dirs`: extra directories put on PATH for test and quality commands |
| `[harness_worker]` | `max_units`: default for `--max-units` |
| `[follow_ups.judge]` | `enabled`, `model`, `threshold`, `api_key_file`, `timeout`: the chaining judge |

The judge is TypeSafe's Jev model. It is off by default. With
`enabled = true`, it reads the key from `TYPESAFE_API_KEY` or `api_key_file`. Without a
key, it fails closed: the follow-up is kept but not chained.

## Requirements

- Python 3.12+ and `requests`
- git
- At least one agent CLI on PATH and logged in: `claude`, `codex`, `opencode` or `pi`
- `gh`, only for `--issue-number`
- The target repository's own toolchain (go, node and its package manager, pytest)

## Development

```bash
python3 -m pytest -q
```

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Status

Alpha. Flags, config keys and the report format can change without notice. Real runs so
far are on one Go repository, with Claude Code and Codex. The Python and TypeScript paths
are covered by unit tests with a fake agent, not by real runs. Use it on code you can
throw away, and review every patch.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE). Contributions are
accepted under the same terms; see [CONTRIBUTING.md](CONTRIBUTING.md) and
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). Security reports: [SECURITY.md](SECURITY.md).
