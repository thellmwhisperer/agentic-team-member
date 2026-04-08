# ATM — Agentic Team Member

Point a local LLM at a bug. Get back a fix with tests and a pull request.

## What it does

Given a GitHub issue and a repository, ATM:

1. **Generates a cookbook** — deterministic static analysis of the target function: imports, dependency graph, mock strategies, test scaffolds. No LLM involved.
2. **Runs an agent loop** — a local model (Qwen 3.5 27B) reads the source, applies the cookbook, writes failing tests, fixes the bug, and refactors.
3. **Verifies red-green** — reverts the fix and runs the test (must fail), restores and runs again (must pass). If both pass, the fix is real.
4. **Opens a pull request** — the model writes the commit message and PR description. The harness executes git and GitHub operations.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                    CLI: atm fix                      │
│   repo + issue + source + symbol + model endpoint    │
└──────────────┬──────────────────────────────────────┘
               │
               ▼
┌──────────────────────────┐
│     Cookbook Generator    │  deterministic, no LLM
│  compiler/ + cookbook.py  │  parse imports, trace deps,
│                          │  generate mock blocks + scaffold
└──────────────┬───────────┘
               │ injected into system prompt
               ▼
┌──────────────────────────┐
│      Agent Loop          │  LLM (local, tool-calling)
│  agent.py                │  RED → GREEN → REFACTOR
│                          │  tools: read, edit, create, run
└──────────────┬───────────┘
               │ DONE
               ▼
┌──────────────────────────┐
│   Red-Green Verifier     │  deterministic, no LLM
│   stash → test (FAIL)    │
│   restore → test (PASS)  │
└──────────────────────────┘
```

## The cookbook

Local models (4B–27B) can fix source code but cannot resolve complex mocking graphs. A 27B will spend 15+ steps trying to figure out what to mock, often getting stuck in loops.

The cookbook solves this deterministically before the agent starts:

- **Import tracing** — parses TypeScript and Python imports
- **Dependency classification** — factory results (`const logger = getLogger()`) vs module singletons vs mutable locals (`let client`)
- **Mock strategy** — `mock.module()` for imports, setter injection for module-level mutables
- **Module-level factory detection** — includes deps that execute on import even if the target function doesn't use them directly
- **Assertion surface scoring** — ranks outbound calls (`say` > `emit` > `info` > `get`), skips calls on parameters
- **Class method detection** — identifies methods vs top-level functions, generates correct import/instantiation

## Language plugins

Languages are plugins. Adding a new language requires zero changes to existing code:

```
languages/
  typescript.py   # TS/JS: bun:test, mock.module(), export detection
  python.py       # Python: pytest, unittest.mock, class method support
  go.py           # Future: go test, interfaces
```

Each plugin provides: `parse_imports`, `parse_assignments`, `test_path`, `setter_name`, `is_exported`, `import_path`, `render_seam_setter`.

## Skills

ATM is one agent with multiple skills. Each skill is a different cookbook — the agent loop, verifier, and language plugins are shared. Teaching ATM a new skill means writing a new cookbook, not a new agent.

```bash
atm fix --issue 41              # TDD fix (current)
atm migrate --pr 123            # Dependency migration (planned)
atm refactor --symbol X         # Codemod (planned)
```

### fix (current)

Receives a bug report, analyzes the target function, generates mock recipes, writes a failing test, fixes the code, verifies red-green, opens a PR. The full TDD cycle.

### migrate (planned)

The Dependabot problem: Dependabot opens a PR bumping a dependency from v3 to v4, but doesn't fix the breaking changes. You have to read the migration guide, understand what broke, and fix it file by file.

ATM can do this. The migration cookbook reads the upgrade guide (a URL or markdown file), extracts transformation rules (class renames, API changes, config changes), and generates a deterministic artefact. The agent applies the rules file by file, runs the build and tests after each batch, and pushes to the same Dependabot PR.

Renovate can't do this either. No tool in the ecosystem closes this gap today.

### refactor (planned)

Large-scale codemods guided by rules — not regex replacement, but an agent that understands the code. Same pattern: a cookbook generates the transformation rules, the agent applies them with verification.

### The pattern

Every skill follows the same architecture:

1. **Cookbook** (deterministic) — analyze the problem, generate an artefact
2. **Agent** (LLM) — execute the plan with tools
3. **Verifier** (deterministic) — confirm the work is correct

What changes between skills is step 1. The cookbook is the semantic layer — it's what turns a generic model into a specialist.

## Benchmark

Same bug (handleResub cumulative months), same issue text, clean sandbox each run:

| Run | Model | Steps | Result | Time | Tests | Thinking |
|---|---|---|---|---|---|---|
| R1 | 27B distilled | 23 | VERIFIED | 19.5m | 3 | 9898 chars |
| R2 | 27B distilled | 18 | VERIFIED | 14.5m | 1 | 6207 chars |
| R3 | 27B base | 15 | VERIFIED | 17.3m | 3 | 6577 chars |
| R6 | 27B base (v3 cookbook) | 17 | VERIFIED | 13.1m | 2 | 4781 chars |
| R7 | 27B base (v3 + rules) | 20 | VERIFIED | 15.7m | 3 | 6505 chars |
| R4 | 4B | 44+ | FAIL | killed | 0 | 3179 chars |

Without the cookbook, the 27B ran 33+ steps (~90 min) and never finished.

### Findings from thinking block analysis

- Models that **re-read the source** during the run write more tests (3 vs 1)
- **Deep thinking** (>3000 chars on one step) correlates with desvíos — the model is reasoning about something it shouldn't need to figure out
- The 4B **doesn't reason about errors** — it tries permutations. The 27B reads the error, thinks about the cause, and edits precisely
- Prompt rules about **what to do** (assertion style, test strategy) are obeyed. Rules about **how to organize** (beforeEach) are ignored unless the scaffold demonstrates it
- Adding a **Refactor step** to the workflow (Red → Green → Refactor) produces cleaner code

## Configuration

All configuration is in `config/agent.toml` and `config/tools.json`. No hardcoded values.

```toml
[llm]
model = "qwen3.5-27b"
temperature = 0.6

[runner]
command = "bun test"
test_file_patterns = ["*.test.ts", "*.test.tsx", "test_*.py"]

[prompt]
system = """..."""
```

## Requirements

- Python 3.12+
- `requests`
- A local LLM server: [llama-server](https://github.com/ggml-org/llama.cpp) or [Ollama](https://ollama.ai)
- A GGUF model with tool-calling support (tested with Qwen 3.5 27B)
- `gh` CLI (for PR creation)

## Usage

```bash
# Start the model server
llama-server \
  --model Qwen3.5-27B.Q4_K_M.gguf \
  --host 127.0.0.1 --port 11435 \
  --ctx-size 32768 --n-gpu-layers 999 \
  --jinja --reasoning auto --no-webui

# Run the agent
PYTHONPATH=. python -m agentic_tdd_runner.agent \
  --source src/twitch/client.ts \
  --symbol handleResub \
  --workdir /path/to/repo \
  --config config/agent.toml \
  "Bug: handleResub reports '0 meses' for all resubscriptions..."
```

## Status

Early development. The TDD fix skill works end-to-end with verified red-green. PR submission and migration skill are next.

## License

MIT
