# AWS AgentCore Target

This directory contains the optional AWS AgentCore execution target for ATM.
ATM still has one canonical harness: the monorepo-root `agentic_tdd_runner`
package. The AWS target only adapts job intake, memory, Bedrock model access,
and AgentCore runtime entrypoints around that harness.

This target must not vendor or fork `agentic_tdd_runner`.

## Current Slice

This PR brings over the runtime-facing pieces from the former standalone
`atm-cloud` repo:

```text
targets/aws-agentcore/
  runtime/             AgentCore entrypoint and Bedrock OpenAI-compatible proxy
  src/atm_cloud/       job contract, optional memory client, runner adapters
  tests/               local adapter tests; no AWS deployment required
  requirements.txt     runtime dependencies for the AWS target
```

Docker, CDK, and deploy scripts are intentionally left for later PRs so the
runtime contract can settle first.

## Runtime Contract

The AgentCore entrypoint is:

```bash
python -m runtime.atm_agent
```

The runtime receives a JSON payload:

```json
{
  "repo": "owner/repo",
  "issue_number": 42,
  "mode": "pull_request",
  "base_branch": "main",
  "run_id": "optional-safe-id",
  "roca_project": "optional-memory-project"
}
```

`mode` may be `pull_request` or `dry_run`.

`SubprocessAtmHarness` invokes the canonical harness as:

```bash
python -m agentic_tdd_runner.agent
```

It passes `--github-repo`, `--issue-number`, `--repo`, `--run-root`,
`--log-dir`, and an optional rendered `--config`.

## Bedrock Proxy

Set `ATM_ENABLE_BEDROCK_PROXY=true` to render a target-local config and start
an OpenAI-compatible proxy backed by Bedrock Converse.

Required when the proxy is enabled:

- `ATM_BEDROCK_MODEL_ID`
- `AWS_REGION` or `AWS_DEFAULT_REGION`

Optional:

- `ATM_PROXY_HOST` default `127.0.0.1`
- `ATM_PROXY_PORT` default `11435`
- `ATM_BASE_CONFIG` default `/app/config/agent.toml`
- `ATM_CONFIG` default `/tmp/atm-agentcore/agentcore-agent.toml`
- `ATM_PERMISSION_DRIVEN`
- `ATM_BASE_BRANCH`
- `ATM_BRANCH_PREFIX`

## Optional Memory

Roca Cloud is optional. If `ROCA_CLOUD_MCP_URL` is unset, the runtime uses a
no-op memory adapter and still runs the harness.

To enable Roca Cloud:

- `ROCA_CLOUD_MCP_URL`: HTTPS MCP endpoint
- one of `ROCA_CLOUD_API_TOKEN` or `ROCA_CLOUD_API_TOKEN_SECRET_ARN`

The default source agent written to memory is `atm-aws-agentcore`.

## GitHub And Secrets

For GitHub issue intake and PR creation, provide either:

- `GITHUB_TOKEN`
- or `GITHUB_TOKEN_SECRET_ARN`

Required for AgentCore Gateway tools:

- `GITHUB_REPO_ALLOWLIST`: comma-separated `owner/repo` allowlist. Gateway
  calls fail closed when this is unset or empty.

Optional controls:

- `ATM_BRANCH_PREFIX`: default `atm-agentcore/`
- `ATM_GIT_USER_NAME`: default `ATM AgentCore`
- `ATM_GIT_USER_EMAIL`: default `atm-agentcore@example.invalid`
- `ATM_ENABLE_GIT_HTTPS_AUTH`: default enabled when a GitHub token exists
- `ATM_ENABLE_GITHUB_PACKAGES_AUTH`: default disabled
- `ATM_NPM_SCOPE`: required only when GitHub Packages auth is enabled
- `ATM_NPM_REGISTRY`: default `https://npm.pkg.github.com`

## Local Tests

From the monorepo root:

```bash
PYTHONPATH=. pytest -p no:cacheprovider targets/aws-agentcore/tests
```

These tests validate job parsing, subprocess command construction, optional
memory behavior, secret loading, log humanization, and Bedrock proxy request
translation without deploying AWS resources.
