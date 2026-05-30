# ATM Execution Targets

ATM has one canonical harness: `agentic_tdd_runner`. Targets describe where and
how that harness runs.

The target layer owns operational concerns such as model startup, environment
variables, logs, worktree locations, cloud infrastructure, secrets, and runtime
adapters. It must not fork, vendor, or patch the core harness.

## Target Contract

- Invoke the harness through `python -m agentic_tdd_runner.agent`.
- Keep target-specific code under `targets/<target-name>/`.
- Keep `agentic_tdd_runner/` free of target imports. The core must not import
  AWS, CDK, boto3, AgentCore, CloudWatch, Roca, or local model supervisor code.
- Keep personal values out of the repository. Use `.env.local`,
  `*.local.toml`, shell profiles, or secret managers.
- Use neutral examples: `owner/repo`, `/path/to/model.gguf`, and placeholder
  secret names.
- Store logs and generated worktrees under ignored runtime directories unless a
  caller explicitly overrides them.
- Reuse the root `config/` defaults unless the target deliberately renders a
  complete runtime config.

## Targets

| Target | Purpose | Status |
| --- | --- | --- |
| `local` | Canonical OSS local operation with `llama-server` or another OpenAI-compatible local endpoint. | scaffolded |
| `aws-agentcore` | Optional AWS AgentCore deployment adapter for running the same harness on Bedrock. | runtime + Docker packaging |

## Non-Goals

Targets are not separate products. They should not carry their own copy of the
ATM harness, their own divergent prompt contract, or target-specific behavior in
core modules.
