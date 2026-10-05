# Contributing to ATM

Thanks for considering a contribution. ATM is a small project and the
highest-leverage contributions right now are concrete: a real-world issue
where a gate passed a bad fix or rejected a good one, with the run that
shows it.

## Filing an issue

- **Bug**: include the smallest reproducer you can — the issue text you
  fed to ATM, the relevant lines of the JSONL log, and what you
  expected vs. what you observed. Mention the agent (`--harness`),
  its version and model, and your local OS.
- **Feature request / coverage gap**: describe the code shape, point
  at a public example if possible, and explain what the runner does
  wrong today.

## Development setup

```bash
git clone https://github.com/<your-fork>/agentic-team-member.git
cd agentic-team-member
python3 -m pip install 'pytest>=9.0,<10' 'requests>=2.31,<3'
```

A real run also needs an agent CLI (`claude`, `codex`, `opencode` or `pi`).
See [README → Requirements](README.md#requirements).

## Running the test suite

```bash
python3.12 -m pytest -q
```

The full suite runs in under a minute on a modern laptop. New code
should come with tests that follow the project's TDD ordering:
write the failing test first, then the fix.

## Pull request process

1. Fork the repo, branch off `main`.
2. Keep PRs small and focused. One change per PR.
3. Run the test suite locally before pushing.
4. The PR title should be a conventional-commit subject (`fix:`,
   `feat:`, `docs:`, `chore:`, `refactor:`, `test:`).
5. The PR body should explain the *why*, link the issue (if any),
   and call out anything reviewers should look at first.
6. CI must be green before review.

## What ATM does and does not accept

- **Yes**: bug fixes, support for another agent CLI or test runner,
  doc improvements, test improvements.
- **Probably yes, but discuss first**: changes to the gates, the
  follow-up contract or the report format.
- **No (without prior discussion)**: large refactors, vendoring,
  switching test framework, adding heavy dependencies.

## Licensing of contributions

By submitting a pull request, you agree that your contribution may
be released under the project's [Apache 2.0 license](LICENSE).
No CLA is required.

## Code of conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).
By participating you agree to abide by its terms.

## Security

Please do **not** open public issues for security vulnerabilities.
See [SECURITY.md](SECURITY.md) for the disclosure process.
