#!/bin/sh
# Run ATM's Go tests. ATM supplies GOCOVERDIR for the subsequent e2e row.
set -eu
cd "$(dirname "$0")/.."
go test -timeout 30m ./...
