#!/bin/sh
# Run ATM's Go tests.
set -eu
cd "$(dirname "$0")/.."
go test -timeout 30m ./...
