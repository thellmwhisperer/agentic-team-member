# Configuration

ATM configuration has three layers:

1. Versioned harness config in `config/*.toml`.
2. Target-local overrides in ignored files such as `.env.local` and
   `*.local.toml`.
3. Runtime secrets from shell environment variables or a secret manager.

The core rule is simple: commit defaults that are useful to everyone; keep
machine paths, private repos, account ids, tokens, and deployed URLs out of the
repository.

## Versioned TOML

Use versioned TOML for behavior that should be reviewed with the code:

- model URL and model alias for public examples
- step budgets and tool output caps
- timeout defaults
- runner command defaults
- environment prep behavior
- discovery, verification, quality, and PR behavior
- prompt templates
- permission-driven mode defaults
- phase-aware thinking budgets
- state reviewer defaults

The full reference lives in [`../config/agent.toml`](../config/agent.toml).

## Local Overrides

Use ignored local files for values that are true only on one machine:

- `targets/local/.env.local`
- `config/*.local.toml`
- shell profile exports

Good local-only values include:

- `ATM_LLAMA_SERVER_BIN`
- model file paths
- local ports when they differ from the default
- `ATM_TARGET_REPO`
- `ATM_GITHUB_REPO`
- `ATM_ISSUE_NUMBER`
- `ATM_CONFIG_PATH`
- `ATM_LOG_DIR`
- `ATM_RUN_ROOT`

The local example file is [`../targets/local/.env.example`](../targets/local/.env.example).

## AWS Values

Use CDK context, environment variables, or AWS Secrets Manager for AWS target
values:

- `AWS_PROFILE`
- `CDK_DEFAULT_REGION`, `AWS_REGION`, or `AWS_DEFAULT_REGION`
- `ATM_AGENTCORE_STACK_NAME`
- `ATM_AGENTCORE_RUNTIME_NAME`
- `ATM_AGENTCORE_GATEWAY_NAME`
- `ATM_BEDROCK_MODEL_ID`
- `GITHUB_TOKEN_SECRET_NAME`
- `ROCA_TOKEN_SECRET_NAME`
- `ROCA_CLOUD_MCP_URL`
- `GITHUB_REPO_ALLOWLIST`
- `ATM_BRANCH_PREFIX`
- `ATM_PERMISSION_DRIVEN`

Secrets should be referenced by name or ARN and populated outside the repo.
Committed examples should use placeholders such as `owner/repo` and
`REPLACE_WITH_BEDROCK_MODEL_ID`.

## Secret Routing

Local ATM can use developer tools such as `gh` or environment variables.

AWS AgentCore should use Secrets Manager references for GitHub and optional
Roca tokens. The runtime can also read token values from environment variables
for local smoke tests, but committed docs and config should prefer secret names.

## Generated Files

Do not commit generated runtime state:

- `.atm/`
- `cdk.out/`
- `.env.local`
- `.env.*.local`
- `*.local.toml`
- JSONL logs
- Python caches
- Docker/CDK asset output

If a target needs an example, commit an `.example` file with placeholders.
