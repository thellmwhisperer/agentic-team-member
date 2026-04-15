# ATM — Agentic Team Member

Point a local LLM at a bug. Get back a fix with tests, a verified red-green, and a PR.

## What it does

Given a GitHub issue and a repository, ATM:

1. **Generates a cookbook** — deterministic static analysis of the target function: imports, dependency graph, mock strategies, test scaffolds. No LLM involved.
2. **Runs a phased agent loop** — a local model (Qwen 3.5 27B) follows a two-phase TDD cycle: first write a failing test, then fix the source. Tools: read, edit, create, run.
3. **Verifies red-green** — reverts the fix and runs the test (must fail), restores and runs again (must pass). If both pass, the fix is real.
4. **Runs quality checks** — typecheck, lint, format, and forbidden pattern checks. Autofixes what it can, feeds remaining issues back to the model.
5. **Opens a PR** — commits the fix and test, pushes to a branch, and creates a pull request via `gh`.

## Architecture

```text
┌─────────────────────────────────────────────────────┐
│              python -m agentic_tdd_runner.agent       │
│   repo + issue + source + symbol + model endpoint    │
└──────────────┬──────────────────────────────────────┘
               │
               ▼
┌──────────────────────────┐
│     Cookbook Generator    │  deterministic, no LLM
│  compiler/ + cookbook.py  │  parse imports, trace deps,
│                          │  generate mock blocks + scaffold
└──────────────┬───────────┘
               │ mechanical edits applied to disk
               │ episode context injected into prompt
               ▼
┌──────────────────────────┐
│   Phase 1: Test First    │  LLM writes a failing test
│   Phase 2: Fix + Green   │  LLM fixes source, runs test
│   agent.py               │  tools: read, edit, create, run
└──────────────┬───────────┘
               │ DONE
               ▼
┌──────────────────────────┐
│   Red-Green Verifier     │  deterministic, no LLM
│   stash fix → test FAIL  │
│   restore  → test PASS   │
└──────────────┬───────────┘
               │ verified
               ▼
┌──────────────────────────┐
│   Quality Gate           │  typecheck + lint + format +
│                          │  forbidden patterns
└──────────────┬───────────┘
               │ all green
               ▼
┌──────────────────────────┐
│   PR Creation            │  git commit + push + gh pr
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

```text
languages/
  typescript.py   # TS/JS: bun:test, mock.module(), export detection
  python.py       # Python: pytest, unittest.mock, class method support
  go.py           # Future: go test, interfaces
```

Each plugin provides: `parse_imports`, `parse_assignments`, `test_path`, `setter_name`, `is_exported`, `import_path`, `render_seam_setter`.

## How the runtime works

The agent loop is a harness around a local LLM. Every design decision optimizes for fewer steps and higher KV cache hit rates.

### Phased prompting

The agent runs in two phases with distinct prompts:

1. **Phase 1 (test-first)**: the model receives the bug description, the cookbook output (mocks, seams, assertion hints), and a prompt that says "write a failing test, then fix the source." This eliminates source-first drift — the model goes straight to TDD.
2. **Phase 2 (quality fix)**: after red-green verification, quality issues (`: any`, missing types, lint) are fed back. The model fixes only what's listed.

A step-counter nudge ("Step N/M") is appended as the last user message on each turn. This placement is deliberate — appending at the tail preserves the KV cache prefix for all preceding messages.

### Context management

The system prompt is built once before the loop and never mutated. This makes it byte-identical across turns, so the LLM server (llama-server) reuses 100% of the KV cache for the system prompt on every step.

After a quality failure, the runner decides whether to compact based on actual token usage:

- **Below 85% of context window**: preserve all messages, append quality feedback. The model keeps full context and doesn't need to re-read files.
- **Above 85%**: compact to 3 messages (system prompt, issue, quality feedback) as a safety net.

In practice, most runs never compact. The handleResub benchmark uses ~9k of 32k tokens at the quality boundary — compacting there destroyed useful context and added 2-3 steps of re-reading.

### File read dedup

The runner tracks `{path: mtime}` for every `read_file` call. If the model re-reads an unchanged file, it gets a stub: "File unchanged since last read. Refer to the earlier content."

The cache is invalidated when `str_replace_editor` modifies the file (explicit `pop`) and cleared entirely on compaction (the model loses the original tool result, so the cache must reset).

### Reactive feedback

After every `str_replace_editor` or `create_file`, the runner runs the project's typecheck and feeds errors back inline in the tool result. The model sees the tsc error immediately, not on the next test run. This cuts a full round-trip per type error.

### TypeScript pill

An optional ~250-token block of standard TypeScript handbook material in the system prompt:

- How to read structured tsc errors (the target type appears in continuation lines)
- Type narrowing techniques (`typeof`, truthiness, equality, `in`, `String()`)
- Type import syntax (`import type { T }`)

This isn't model-specific instruction — it's reference material. It reduces quality-phase steps by teaching the model to read tsc output instead of searching for types.

## Skills

ATM is one agent with multiple skills. Each skill is a different cookbook — the agent loop, verifier, and language plugins are shared. Teaching ATM a new skill means writing a new cookbook, not a new agent.

```bash
atm fix --issue 41              # TDD fix (current)
atm migrate --pr 123            # Dependency migration (planned)
atm refactor --symbol X         # Codemod (planned)
```

### fix (current)

Receives a bug report, analyzes the target function, generates mock recipes, writes a failing test, fixes the code, verifies red-green, outputs the diff. The full TDD cycle.

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

Same bug (handleResub cumulative months), same issue text, clean worktree each run.

### Current results (phased runner + quality gate)

| Run | Config | Thinking | Green | Done | Notes |
|---|---|---|---|---|---|
| r32 | base | on | 4 | 15 | baseline with quality gate |
| r34 | pill, 4B | on | 5 | 21 | 4B model, TypeScript pill validated |
| r36 | pill | on | 4 | 11 | best pre-optimization |
| r38 | base | off | ~5 | 19 | thinking_budget_tokens=0 |
| r39 | pill+nothink | off | 7 | 16 | pill compensates for no thinking |
| r41 | pill+think | on | 4 | 10 | with threshold compact + read dedup |

Green = step where test passes. Done = total steps including quality fixes and PR.

### Key findings

- **27B is deterministic**: green at step 4 in every 27B run. Variance is only in quality phase.
- **TypeScript pill matters**: ~250 tokens of TS handbook reference (narrowing, reading tsc errors, type imports) reduces quality-phase steps significantly.
- **Thinking matters in quality phase**: the model uses extended reasoning to resolve type narrowing (`string | true` → `String()`). Without thinking, it takes more steps.
- **Context preservation beats compaction**: compacting at 9k/32k (28% usage) destroyed useful context and forced re-reads. Threshold-based compaction (85%) preserves context when there's headroom.
- **File read dedup**: mtime-keyed cache avoids re-reading unchanged files. Invalidated on edit/write, cleared on compaction.

### Earlier results (pre-phased runner)

| Run | Model | Steps | Result |
|---|---|---|---|
| R1 | 27B distilled | 23 | VERIFIED |
| R2 | 27B distilled | 18 | VERIFIED |
| R3 | 27B base | 15 | VERIFIED |
| R4 | 4B | 44+ | FAIL |

Without the cookbook, the 27B ran 33+ steps (~90 min) and never finished.

## Configuration

Per-run configuration in TOML. Multiple configs for different models/strategies:

```toml
[llm]
model = "qwen3.5-27b"
url = "http://127.0.0.1:11435/v1/chat/completions"
temperature = 0.6
thinking_budget_tokens = 0  # optional: disable thinking per-request

[runner]
command = "bun test"
test_file_patterns = ["*.test.ts", "*.test.tsx", "test_*.py"]

[quality]
enabled = true
compact_threshold_ratio = 0.85  # only compact when context is near limit

[quality.typescript]
forbidden = ["as any", ": any", "eslint-disable", "@ts-ignore"]

[pr]
enabled = true
base_branch = "main"
branch_prefix = "atm/fix-"
```

## Requirements

- Python 3.12+
- `requests`
- A local LLM server: [llama-server](https://github.com/ggml-org/llama.cpp) or [Ollama](https://ollama.ai)
- A GGUF model with tool-calling support (tested with Qwen 3.5 27B and 4B)
- `gh` CLI for PR creation

## Usage

```bash
# Start the model server (no --reasoning-budget for per-request control)
llama-server \
  --model Qwen3.5-27B.Q4_K_M.gguf \
  --host 127.0.0.1 --port 11435 \
  --ctx-size 32768 --n-gpu-layers 999 \
  --jinja --no-webui

# Run the agent
python3.14 -m agentic_tdd_runner.agent \
  --source src/twitch/client.ts \
  --symbol handleResub \
  --workdir /path/to/repo \
  --config config/agent-r35-27b-pill.toml \
  --log-dir /tmp \
  /path/to/issue.md
```

## Status

The TDD fix skill works end-to-end: cookbook → phased agent loop → verified red-green → quality gate → PR. Migration and refactor skills are planned.

## License

MIT
