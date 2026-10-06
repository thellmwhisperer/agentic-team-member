#!/bin/sh
# Run ATM's tests under Python 3.12 with requirements.txt, regardless of the
# first `python3` on PATH. Arguments go to pytest. Without arguments, the Go
# tests run after pytest when go.mod exists.
set -eu
cd "$(dirname "$0")/.."
uv run --no-project --python 3.12 --with-requirements requirements.txt python -m pytest -q "$@"
if [ "$#" -eq 0 ] && [ -f go.mod ]; then
  go test ./...
fi
