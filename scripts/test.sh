#!/bin/sh
# The one way to run ATM's tests: pytest under Python 3.12 with requirements.txt, whatever
# `python3` comes first on PATH (macOS ships 3.9, ATM needs 3.11+ for tomllib). Arguments go to pytest.
set -eu
cd "$(dirname "$0")/.."
exec uv run --no-project --python 3.12 --with-requirements requirements.txt python -m pytest -q "$@"
