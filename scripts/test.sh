#!/bin/sh
# Run ATM's tests under Python 3.12 with requirements.txt, regardless of the
# first `python3` on PATH. Arguments go to pytest.
set -eu
cd "$(dirname "$0")/.."
exec uv run --no-project --python 3.12 --with-requirements requirements.txt python -m pytest -q "$@"
