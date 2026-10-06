#!/bin/sh
# The always-present summary check: green when every filtered job it is given
# succeeded or was skipped because its paths did not change, red otherwise.
# Arguments are job results (success, skipped, failure, cancelled).
for result in "$@"; do
  case "$result" in
    success | skipped) ;;
    *) echo "a filtered job ended $result" >&2; exit 1 ;;
  esac
done
