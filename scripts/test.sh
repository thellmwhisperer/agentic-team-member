#!/bin/sh
# Run ATM's Go tests. ATM supplies GOCOVERDIR for the subsequent e2e row.
# internal/run holds nearly all the time and its tests call t.Setenv, so they cannot use t.Parallel: they
# are split by name over ATM_TEST_SHARDS go test processes, beside one go test command for all other
# packages. ATM_TEST_SHARDS=1 runs the whole suite in one process.
set -eu
cd "$(dirname "$0")/.."
shards=${ATM_TEST_SHARDS:-8}
if [ "$shards" -le 1 ]; then
	go test -timeout 30m ./...
	exit
fi
out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT
names=$(go test -list . ./internal/run | grep '^Test')
go test -timeout 30m $(go list ./... | grep -v '/internal/run$') >"$out/rest" 2>&1 || touch "$out/rest.failed" &
i=0
while [ "$i" -lt "$shards" ]; do
	run=$(echo "$names" | awk -v k="$shards" -v i="$i" 'NR % k == i' | paste -sd '|' -)
	go test -timeout 30m -run "^($run)\$" ./internal/run >"$out/run-$i" 2>&1 || touch "$out/run-$i.failed" &
	i=$((i + 1))
done
wait
# A failing process prints last, so the tail of the output is its failure.
status=0
for f in "$out"/*; do
	case $f in *.failed) continue ;; esac
	[ -e "$f.failed" ] || cat "$f"
done
for f in "$out"/*.failed; do
	[ -e "$f" ] || continue
	cat "${f%.failed}"
	status=1
done
exit "$status"
