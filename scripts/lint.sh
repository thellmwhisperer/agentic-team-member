#!/bin/sh
# The gate's lint: golangci-lint and slopslint.
set -eu
cd "$(dirname "$0")/.."
golangci-lint run
scripts/slopslint.sh check --classify --enforce > /dev/null
