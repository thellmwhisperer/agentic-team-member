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
rights of the user who started the worker, and Claude Code loads that user's
own settings and hooks. Codex runs in its `workspace-write` sandbox. Run ATM
only on issues and repositories you would let that agent work on unattended.

Reports in this area are in scope:

- ATM's own path handling: `resolve_repo_path` refuses report paths outside
  the clone, follow-up tests are confined to `.atm/follow-ups/`, and the gate
  fails a unit whose HEAD moved. A way around any of those is a vulnerability.
- Command construction: test and typecheck commands are built from the
  target's `package.json`, lockfile and config, then run with `shlex.split`
  and no shell. The quality step runs detected tools with a shell. Injection
  through a crafted target repository is in scope.
- Delivery: ATM commits on a branch and runs `[delivery].command` from your
  config through the shell; it never pushes or opens a PR itself. Anything
  that makes ATM do so, or runs a command the config did not name, is in
  scope. The same holds for `[monitor].command`.
- Network: `judge.py` posts issue and follow-up text to TypeSafe when
  `[follow_ups.judge]` is enabled; the duplicated-setup judge posts test lines
  to the configured Ollama URL when enabled; `scripts/slopslint.sh` downloads a
  pinned release and verifies its SHA256. Anything else that sends data off the
  machine is a bug.

Out of scope:

- Issues that require an already-compromised local environment.
- Findings against third-party dependencies, the coding agent CLIs, or
  no-mistakes; please report those upstream.
- What a model writes inside the clone. That is what the gates are for; a gate
  that misses something is a bug report, not a vulnerability.
