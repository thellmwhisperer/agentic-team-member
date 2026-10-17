#!/bin/sh
# Run ATM's Go tests, then check that every changed Go line ran under an end-to-end run of the
# real atm binary (scripts/cover-diff.sh). The counters stay in GOCOVERDIR after the run.
set -eu
cd "$(dirname "$0")/.."
GOCOVERDIR=$(mktemp -d)
export GOCOVERDIR
echo "cover-diff: GOCOVERDIR=$GOCOVERDIR"
go test -timeout 30m ./...
scripts/cover-diff.sh
