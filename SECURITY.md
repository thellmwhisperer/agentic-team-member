# Security Policy

## Reporting a vulnerability

Please **do not** open a public GitHub issue for security reports.

Instead, use GitHub's private vulnerability reporting:

1. Go to the repository's **Security** tab.
2. Click **Report a vulnerability**.
3. Describe the issue, how to reproduce it, and the impact.

You can expect:

- An acknowledgement within 7 days.
- A status update within 30 days.
- Coordinated disclosure once a fix is ready, with credit to the
  reporter unless you prefer to remain anonymous.

## Supported versions

This project is in alpha. Only the `main` branch receives fixes.
There are no tagged releases yet; once releases exist, the support
policy will be updated here.

## Scope

In scope:

- The runner code in `agentic_tdd_runner/`.
- The default configs in `config/`.
- Documentation that could mislead users into an unsafe setup.

Out of scope:

- Issues that require an already-compromised local environment.
- Findings against third-party dependencies — please report those
  upstream.
- Behavior of the local LLM itself; that is governed by the model
  provider.
