#!/bin/sh
# The gate's lint: ruff and slopslint, plus golangci-lint when go.mod exists.
set -eu
cd "$(dirname "$0")/.."
uvx ruff check .
scripts/slopslint.sh check --classify --enforce > /dev/null
if [ -f go.mod ]; then
  golangci-lint run
fi
