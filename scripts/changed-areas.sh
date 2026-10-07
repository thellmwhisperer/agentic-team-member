#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
base=${1:?base commit is required}
classify_path() {
  case "$1" in
    *.go|go.mod|go.sum|.golangci.yml|Makefile|scripts/changed-areas.sh|scripts/test.sh|scripts/lint.sh|.github/workflows/go.yml)
      go=true
      ;;
  esac
}

git diff --name-status -M "$base" HEAD | {
  go=false
  while IFS="$(printf '\t')" read -r status first second; do
    case "$status" in
      R*|C*) classify_path "$first"; classify_path "$second" ;;
      *) classify_path "$first" ;;
    esac
  done

  if [ ! -f go.mod ]; then
    go=false
  fi

  printf 'go=%s\n' "$go"
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    printf 'go=%s\n' "$go" >> "$GITHUB_OUTPUT"
  fi
}
