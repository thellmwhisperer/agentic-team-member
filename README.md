# agentic-tdd-runner

Point a local LLM at a bug, get back a fix with tests and a pull request.

## What it does

Given a GitHub issue and a repository, agentic-tdd-runner:

1. **Generates a cookbook** — deterministic static analysis of the target function: imports, dependency graph, mock strategies, test seam setters, and a ready-to-use test scaffold. No LLM involved.
2. **Runs an agent loop** — a local model (Qwen 3.5 27B via llama-server or Ollama) reads the source, applies the cookbook, writes a failing test, fixes the bug, and iterates until tests pass.
3. **Verifies red-green** — the harness reverts the fix and runs the test (must fail), then restores the fix and runs again (must pass). If both checks pass, the fix is real.
4. **Opens a pull request** — the model writes the commit message, PR title, and description. The harness executes the git and GitHub operations.

## Architecture

```
┌─────────────────────────────────────────────────┐
│                   CLI entry point                │
│   repo URL + issue + model + server endpoint     │
└──────────────┬──────────────────────────────────┘
               │
               ▼
┌──────────────────────────┐
│     Cookbook Generator    │  ← deterministic, no LLM
│  tdd_p1p2_contract.py    │     parses imports, traces deps,
│  tdd_cookbook.py          │     generates mock blocks + scaffold
└──────────────┬───────────┘
               │ text injected into system prompt
               ▼
┌──────────────────────────┐
│      Agent Loop          │  ← LLM (local, tool-calling)
│  agent.py                │     read → edit → test → iterate
│                          │     tools: read_file, run_command,
│                          │     str_replace_editor, create_file
└──────────────┬───────────┘
               │ DONE
               ▼
┌──────────────────────────┐
│   Red-Green Verifier     │  ← deterministic, no LLM
│   revert → test (FAIL)   │
│   restore → test (PASS)  │
└──────────────┬───────────┘
               │ VERIFIED
               ▼
┌──────────────────────────┐
│     PR Submitter         │  ← LLM writes content,
│   submit_pr.py           │     harness executes git + gh
└──────────────────────────┘
```

## The cookbook

The key insight: local models (4B–27B) can fix source code but cannot resolve complex mocking graphs. A 27B model will spend 15+ steps trying to figure out what to mock and how, often getting stuck in loops.

The cookbook generator solves this deterministically by analyzing the source file before the agent starts:

- **Import tracing** — parses TypeScript/Python imports and maps every binding to its source module
- **Dependency classification** — distinguishes factory results (`const logger = getLogger()`) from module singletons (`import logger from`) from mutable locals (`let client`)
- **Mock strategy** — assigns `mock.module()` for imports, `set_test_seam` (setter injection) for module-level mutables
- **Shape enrichment** — resolves sibling imports from the same module to build complete mock shapes, preventing import-time crashes
- **Assertion surface scoring** — ranks outbound calls by observability (`say` > `emit` > `info` > `get`) to pick the best assertion target
- **Scaffold generation** — produces a complete test file with `TODO` slots the model fills in

The cookbook is injected into the system prompt. The model starts knowing what to mock instead of discovering it by trial and error.

## Benchmark results

Same bug (handleResub cumulative months), same issue text, same cookbook, clean sandbox each run:

| Model | Runtime | Steps | Result | Time |
|---|---|---|---|---|
| Qwen 3.5 27B (distilled) | llama-server | 23 | VERIFIED | ~20 min |
| Qwen 3.5 27B (distilled) | llama-server | 18 | VERIFIED | ~15 min |
| Qwen 3.5 27B (base) | llama-server | 15 | VERIFIED | ~15 min |
| Qwen 3.5 4B | Ollama | 33+ | FAIL (loop) | killed |

Without the cookbook, the 27B distilled ran 33 steps (~90 min) and never finished — stuck in the mocking wall.

## Requirements

- Python 3.12+
- `requests`
- A local LLM server: [llama-server](https://github.com/ggml-org/llama.cpp) (recommended) or [Ollama](https://ollama.ai)
- A GGUF model with tool-calling support (tested with Qwen 3.5 27B)
- `gh` CLI (for PR creation)
- `bun` or `pytest` in the target repo (for running tests)

## Usage

```bash
# Start the model server
llama-server \
  --model Qwen3.5-27B.Q4_K_M.gguf \
  --host 127.0.0.1 --port 11435 \
  --ctx-size 32768 --n-gpu-layers 999 \
  --jinja --reasoning auto --no-webui

# Run the agent
agentic-tdd-runner \
  --repo https://github.com/org/project \
  --issue 41 \
  --source src/twitch/client.ts \
  --symbol handleResub \
  --model qwen3.5-27b \
  --server http://127.0.0.1:11435
```

## Project structure

```
agentic_tdd_runner/
  __init__.py
  cli.py                  # CLI entry point
  cookbook.py              # cookbook generator (deterministic)
  compiler.py             # P1-P2 contract compiler (pure functions)
  agent.py                # agent loop (LLM + tools)
  verifier.py             # red-green verification
  submit_pr.py            # PR creation (LLM content + git harness)

tests/
  test_cookbook.py
  test_compiler.py
  test_agent.py
  test_verifier.py
  test_submit_pr.py
```

## How it works internally

### 1. Cookbook generation (no LLM)

```python
from agentic_tdd_runner.cookbook import generate_cookbook

cookbook = generate_cookbook(
    source_path="src/twitch/client.ts",
    symbol="handleResub",
    project_root="/path/to/repo",
)
# Returns ~2KB of text: mock blocks, seam setters, test scaffold
```

### 2. Agent loop

The agent runs in a step loop with 4 tools. Each step:
- Sends the conversation to the LLM (with cookbook in system prompt)
- LLM responds with tool calls or text
- Harness executes tools and appends results
- On "DONE": triggers red-green verification
- On VERIFIED: triggers PR submission

### 3. Red-green verification

```
git stash                    # save fix
bun test handleResub.test.ts # must FAIL (red)
git stash pop                # restore fix
bun test handleResub.test.ts # must PASS (green)
```

If red-green fails, the agent gets a rejection message and keeps iterating.

## Status

Early development. The cookbook generator and agent loop work. PR submission is next.

## License

MIT
