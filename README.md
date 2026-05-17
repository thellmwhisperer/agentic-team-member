# ATM - Agentic Team Member

Turn a real issue into a verified pull request using local coding agents.

ATM is a skill-driven harness for local models. It prepares the repository,
builds deterministic context, lets the model edit code with tools, verifies the
result, and opens a PR.

## What ATM Does

Given a repository and an issue, ATM:

1. Prepares an isolated run worktree under the target repo.
2. Installs and validates the project before the first model call.
3. Reads the issue and discovers the target surface when possible.
4. Builds a deterministic cookbook for mocks, seams, tests, and assertions.
5. Lets a local coding model write a regression test and fix the source.
6. Verifies red-green deterministically.
7. Runs quality and safety gates.
8. Commits, pushes, and opens a pull request when PR automation is enabled.

## Product Flow

```text
Repo + Issue
    |
    v
Isolated Worktree
    |
    v
Environment Prep
    |
    v
Reconnaissance
    |
    v
Cookbook
    |
    v
Local Coding Agent
    |
    v
Deterministic Gates
    |
    v
Pull Request
```

The model is only one part of the system. ATM keeps deterministic work outside
the model wherever possible: repo setup, target analysis, test scaffolding,
red-green verification, quality checks, and PR creation.

## Cookbook

The cookbook is the semantic layer between a generic coding model and a real
project. It turns repository facts into concrete instructions the agent can
execute:

- Import tracing
- Dependency classification
- Mock strategy
- Test file placement
- Mechanical seams and exports
- Assertion surfaces
- Framework and language conventions

The goal is to make the agent spend its context on the actual code change, not
on rediscovering the same project mechanics every run.

## Skills

ATM is one runtime with multiple skills. Each skill has its own cookbook; the
worktree setup, tools, verifier, quality gates, language plugins, and PR
workflow are shared.

### fix

`fix` is the first supported skill. It receives a bug report, prepares the repo,
generates a test strategy, writes a failing regression test, fixes the code,
verifies red-green, and opens a PR.

### migrate

`migrate` is planned. It will read a dependency migration guide, extract
transformation rules, apply the changes file by file, run project checks, and
push back to the dependency-update PR.

### refactor

`refactor` is planned. It will apply verified code transformations across a
repo using the same cookbook, agent, and verifier pattern.

## Language Plugins

Languages are plugins. Adding a language means teaching ATM how to parse, test,
mock, and patch that language without changing the rest of the flow:

```text
languages/
  typescript.py   # TS/JS: test runners, module mocks, export detection
  python.py       # Python: pytest, unittest.mock, class method support
  go.py           # Planned: go test, interfaces
```

Each plugin provides the language-specific pieces used by the cookbook:
imports, assignments, test paths, export detection, import rendering, and seam
generation.

## Current Status

The `fix` flow works end to end:

- repo worktree preparation
- issue loading from a file or GitHub issue
- deterministic environment prep
- deterministic cookbook generation
- tool-based local model loop
- red-green verification
- quality gates
- PR automation

TypeScript and JavaScript are the most exercised path today. Python support is
available through the plugin interface and pytest-oriented generation, and needs
more real-project coverage before it should be considered equally mature.

## Road to OSS

The public roadmap is organized around making the product contract portable
across real projects:

1. **Runner bootstrap**: build exact test commands for Bun, Jest, Vitest,
   `node:test`, and pytest from repo facts, including minimal config shims when
   isolated regression tests need them.
2. **Reconnaissance**: turn issue and repo inspection into a deterministic
   per-episode cookbook, so normal bug reports do not require `--source` and
   `--symbol`.
3. **Installable CLI**: publish package metadata, dependencies, and an `atm`
   command with a documented local-first install path.
4. **Smoke matrix**: maintain small fresh-repo checks for Bun, Jest/Vitest, and
   pytest through the same `--repo` workflow.
5. **Project polish**: add public contribution docs, issue templates, license
   file, and a concise first-run guide.

## Requirements

- Python 3.12+
- `requests`
- A local OpenAI-compatible model server, such as
  [llama-server](https://github.com/ggml-org/llama.cpp)
- A tool-calling local model
- `gh` CLI for GitHub issue loading and PR creation

## Usage

Start a local model server:

```bash
llama-server \
  --model /path/to/model.gguf \
  --host 127.0.0.1 --port 11435 \
  --ctx-size 32768 --n-gpu-layers 999 \
  --jinja --no-webui
```

Run from an issue file:

```bash
python3 -m agentic_tdd_runner.agent \
  --repo /path/to/repo \
  /path/to/issue.md
```

Or load the issue directly from GitHub:

```bash
python3 -m agentic_tdd_runner.agent \
  --repo /path/to/repo \
  --github-repo owner/repo \
  --issue-number 41 \
  --base-ref main
```

When `--repo` is supplied, ATM creates a detached run worktree under
`REPO/.worktree/` by default, then runs environment prep there. That keeps run
filesystem access inside the target project. You can still pass `--run-root` as
an explicit override.

You can pass `--source` and `--symbol` to pin the target, or omit them when
discovery is enabled.

Runtime defaults can come from `.env` in the current directory:

```bash
AGENT_CONFIG=config/agent.toml
AGENT_LOG_DIR=/tmp
```

## Configuration

Runtime configuration lives in TOML:

```toml
[llm]
model = "local-coder"
url = "http://127.0.0.1:11435/v1/chat/completions"
temperature = 0.6

[timeouts]
tool_execution = 60
llm_request = 600
test_run = 30
pr_create = 120

[runner]
command = "bun test"
test_file_patterns = ["*.test.ts", "*.test.tsx", "*.test.js", "*.test.jsx", "test_*.py"]

[environment]
enabled = true
install = "auto"
require_clean = true
run_typecheck = true
timeout = 300

[discovery]
enabled = true

[quality]
enabled = true

[quality.typescript]
forbidden = ["as any", ": any", "eslint-disable", "@ts-ignore"]

[pr]
enabled = true
base_branch = "main"
branch_prefix = "atm/fix-"
```

## License

MIT
