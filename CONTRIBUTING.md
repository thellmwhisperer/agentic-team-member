# Contributing to ATM

Thanks for considering a contribution. ATM is a small project and the
highest-leverage contributions right now are concrete: a failing
real-world issue that exercises a weak row of the [coverage matrix](docs/code-shape-coverage.md),
or a fix that moves a row from `weak` to `partial` to `strong`.

## Filing an issue

- **Bug**: include the smallest reproducer you can — the issue text you
  fed to ATM, the relevant lines of the JSONL log, and what you
  expected vs. what you observed. Mention your model (Qwen 3.5 27B,
  4B, etc.), runner (`llama-server`, Ollama), and your local OS.
- **Feature request / coverage gap**: describe the code shape, point
  at a public example if possible, and explain what the runner does
  wrong today.

## Development setup

```bash
git clone https://github.com/<your-fork>/agentic-team-member.git
cd agentic-team-member
pip install -e '.[dev]' || pip install 'requests>=2.31,<3'
python -m pip install uv
```

You also need a local LLM server running a tool-calling GGUF model.
See [README → Requirements](README.md#requirements).

## Running the test suite

```bash
scripts/test.sh
```

`scripts/test.sh` is the one way to run the tests: it uses
[`uv`](https://docs.astral.sh/uv/) to run pytest under Python 3.12 (see
`.python-version`) with `requirements.txt`, whatever `python3` comes first
on your PATH. Its arguments go to pytest (`scripts/test.sh tests/test_config.py`).

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

### High-risk PRs

For non-draft PRs from this repository targeting `main`, auto-merge
is armed when the PR opens, reopens, or becomes ready for review if
the first risk word on the first nonblank line under the exact
`## Risk Assessment` heading is Low or Medium. High or unreadable risk
levels wait for the `risk-reviewed` label.

Before adding the label, whoever launched the run writes one PR
comment that starts with `Risk reviewed:` and has one line per review
finding:

```
Risk reviewed:
<finding id>: fixed | accepted | dismissed - <one-line reason>
```

The label is the switch; the comment is the record. Adding the label
triggers the workflow to arm auto-merge for eligible PRs.

The workflow arms auto-merge with the `AUTO_MERGE_TOKEN` secret: a
fine-grained personal access token of the owner, limited to this
repository, with Contents and Pull requests read and write. It does not
use the workflow token, because merges made with it start no workflow
(so no `CD` run) and close no issue. If the secret is missing, the
check fails with `AUTO_MERGE_TOKEN is not set`.

## What ATM does and does not accept

- **Yes**: bug fixes, new language plugins, new cookbook shapes,
  coverage-lifting changes, doc improvements, test improvements.
- **Probably yes, but discuss first**: new top-level skills
  (`migrate`, `refactor`), changes to the verify contract,
  changes to discovery scoring.
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
