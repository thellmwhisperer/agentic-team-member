#!/bin/sh
# Run ATM's Go tests, then scripts/cover-diff.sh. Counters stay in GOCOVERDIR after the run.
set -eu
cd "$(dirname "$0")/.."
GOCOVERDIR=$(mktemp -d)
export GOCOVERDIR
echo "cover-diff: GOCOVERDIR=$GOCOVERDIR"
go test -timeout 30m ./...
scripts/cover-diff.sh
