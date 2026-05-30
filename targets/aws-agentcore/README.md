# AWS AgentCore Target

This directory contains the optional AWS AgentCore execution target for ATM.
ATM still has one canonical harness: the monorepo-root `agentic_tdd_runner`
package. The AWS target only adapts job intake, memory, Bedrock model access,
and AgentCore runtime entrypoints around that harness.

This target must not vendor or fork `agentic_tdd_runner`.

## Current Slice

This target currently contains the runtime-facing pieces from the former
standalone `atm-cloud` repo, Docker packaging that builds from the monorepo
root, and a minimal CDK stack for AWS AgentCore:

```text
targets/aws-agentcore/
  Dockerfile           AgentCore runtime image; build with monorepo root context
  Makefile             local test, smoke, and docker build helpers
  cdk.json             example CDK context with placeholders
  infrastructure/      CDK app, AgentCore runtime, Gateway, IAM, Lambda wiring
  runtime/             AgentCore entrypoint and Bedrock OpenAI-compatible proxy
  scripts/             local packaging smoke checks; no deploy
  src/atm_cloud/       job contract, optional memory client, runner adapters
  tests/               local adapter tests; no AWS deployment required
  requirements.txt     runtime dependencies for the AWS target
  requirements-cdk.txt CDK dependencies for synth/deploy tooling
```

Deploy/operator scripts are intentionally left for later PRs. This slice stops
at a buildable runtime image, local smoke checks, and CDK synthesis wiring.

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

## Build And Smoke

Build from the monorepo root context so the image copies the canonical harness
directly from `agentic_tdd_runner/`:

```bash
make -C targets/aws-agentcore docker-build
```

The equivalent raw Docker command is:

```bash
docker build \
  -f targets/aws-agentcore/Dockerfile \
  -t atm-aws-agentcore:local \
  .
```

Run a no-AWS smoke check locally:

```bash
make -C targets/aws-agentcore smoke
```

Set `PYTHON=/path/to/python3.12+` if your default `python3` is older than the
project's Python requirement. Local `test` and `smoke` targets expect the root
development dependencies such as `requests` and `pytest` to be installed for
that interpreter; the Docker image installs its runtime dependencies itself.

Run the same smoke check inside the image:

```bash
make -C targets/aws-agentcore docker-smoke
```

The image uses Python 3.14 on Debian trixie to match the local target's
preferred interpreter and keep Debian's `gh` package available from the base
repository. It runs as the non-root `atm` user.

The smoke path imports the root harness and AWS adapter modules, then invokes
the AgentCore entrypoint in `dry_run` mode. It does not call Bedrock, Secrets
Manager, Roca Cloud, GitHub, CDK, or AgentCore deploy APIs.

## CDK Infrastructure

The CDK app is intentionally parameterized. It should synthesize without any
personal AWS account IDs, ARNs, profiles, deployed URLs, or repository names in
the repo.

Install CDK dependencies in your preferred environment:

```bash
python -m pip install -r targets/aws-agentcore/requirements-cdk.txt
```

Then synthesize from this directory:

```bash
make -C targets/aws-agentcore cdk-synth
```

`cdk-synth` does not deploy anything. Deployment remains an explicit operator
action once you have supplied real context values and reviewed the synthesized
template. The Makefile writes synthesized output under `.atm/cdk.out`, which is
ignored by git and by the Docker image asset.

Required context/env values for a real deployment:

- AWS profile: set via your normal `AWS_PROFILE`; do not commit it.
- AWS region: `CDK_DEFAULT_REGION`, `AWS_REGION`, or `AWS_DEFAULT_REGION`.
- stack name: `-c atmStackName=<stack-name>` or `ATM_AGENTCORE_STACK_NAME`.
- Bedrock model id: `-c atmRunnerModelId=<model-id>` or `ATM_BEDROCK_MODEL_ID`.
- GitHub token secret name: `-c githubTokenSecretName=<secret-name>` or
  `GITHUB_TOKEN_SECRET_NAME`.
- repo allowlist: `-c githubRepoAllowlist=owner/repo,owner/other` or
  `GITHUB_REPO_ALLOWLIST`.

Optional context/env values:

- Roca Cloud URL: `-c rocaMcpUrl=https://...` or `ROCA_CLOUD_MCP_URL`.
- Roca token secret name: `-c rocaTokenSecretName=<secret-name>` or
  `ROCA_TOKEN_SECRET_NAME`.
- branch prefix: `-c atmBranchPrefix=atm-agentcore/` or `ATM_BRANCH_PREFIX`.
- runtime name: `-c atmRuntimeName=<runtime-name>` or
  `ATM_AGENTCORE_RUNTIME_NAME`.
- Gateway name: `-c atmGatewayName=<gateway-name>` or
  `ATM_AGENTCORE_GATEWAY_NAME`.
- GitHub Packages npm scope: `-c npmScope=@owner` or `ATM_NPM_SCOPE`.
  When omitted, package auth is disabled.
- permission-driven flow: `-c atmPermissionDriven=true` or
  `ATM_PERMISSION_DRIVEN=true`.

The stack references the GitHub and optional Roca secrets by name; it does not
create personal placeholder secrets. Create and populate those secrets in your
AWS account before deploying. `GITHUB_REPO_ALLOWLIST` fails closed in the
Gateway Lambda if omitted or empty.

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
