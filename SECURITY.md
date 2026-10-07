# Security Policy

## Reporting a vulnerability

Please **do not** open a public GitHub issue for security reports.

Use GitHub's private vulnerability reporting:

1. Go to the repository's **Security** tab.
2. Click **Report a vulnerability**.
3. Describe the issue, how to reproduce it, and the impact.

You can expect:

- An acknowledgement within 7 days.
- A status update within 30 days.
- Coordinated disclosure once a fix is ready, with credit to the reporter
  unless you prefer to remain anonymous.

## Supported versions

This project is in alpha. Only the `main` branch receives fixes. There are no
tagged releases yet; once releases exist, the support policy will be updated
here.

## What ATM runs on your machine

ATM is a harness: it starts a coding agent CLI as a subprocess inside a clone
of your repository, then runs your repository's own install, test, lint and
typecheck commands.

The agent is not sandboxed by ATM. Claude Code, OpenCode and pi run with the
rights of the user who started ATM. Claude Code loads the target repository's
project settings and hooks. Codex runs in its `workspace-write` sandbox. Run ATM
only on issues and repositories you would let that agent work on unattended.

Reports in this area are in scope:

- ATM's own path handling: the reported `test_file` must be a local path;
  follow-up red tests must be new paths outside `.atm/`, and a unit fails if
  its agent moved HEAD. A way around these checks is a vulnerability.
- Command execution: ATM runs the target repository's `install`, `test`,
  `typecheck`, `lint`, `test_file` and `delivery` lines from `.atm.yaml` with
  `sh -c`. Running an unconfigured command or allowing a crafted report to
  escape the clone is in scope.
- Delivery: ATM commits on a branch and runs the configured `delivery`
  command. It never pushes or opens a PR itself. Anything that makes ATM do
  either without that command is in scope.
- Network: `gh` reads GitHub issues, the selected agent CLI handles the issue
  and code, and the configured commands may use the network.
  `scripts/slopslint.sh` downloads a pinned release and verifies its SHA256.
  An unexpected transfer by ATM itself is in scope.

Out of scope:

- Issues that require an already-compromised local environment.
- Findings against third-party dependencies, the coding agent CLIs, or
  no-mistakes; please report those upstream.
- What a model writes inside the clone. That is what the gates are for; a gate
  that misses something is a bug report, not a vulnerability.
