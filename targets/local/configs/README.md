# Local Configs

ATM currently loads complete TOML config files. The canonical complete config is
the root `config/agent.toml`, which references the root `config/tools.json`.

For local personal settings:

1. Copy `config/agent.toml` to a git-ignored file such as
   `config/agent.local.toml`.
2. Edit local-only values like `[llm].url`, `[llm].model`, step budgets, or
   prompt experiments.
3. Set `ATM_CONFIG_PATH=config/agent.local.toml` in `targets/local/.env.local`.

For the canonical local MTP profile in `targets/local/.env.example`, set:

```toml
[llm]
url = "http://127.0.0.1:11435/v1/chat/completions"
model = "qwen3.6-27b-mtp"
```

Do not commit `*.local.toml` files.
