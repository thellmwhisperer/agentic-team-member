#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
base=${1:?base commit is required}
classify_path() {
  path=$1
  case "$path" in
    *.py|pyproject.toml|requirements*.txt|ruff.toml|.python-version|scripts/test.sh|scripts/changed-areas.sh|config/*|.github/workflows/pr.yml|.github/workflows/cd.yml)
      python=true
      ;;
  esac
  case "$path" in
    *.go|go.mod|go.sum|.golangci.yml|Makefile|scripts/changed-areas.sh|.github/workflows/go.yml)
      go=true
      ;;
  esac
}

git diff --name-status -M "$base" HEAD | {
  python=false
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

  printf 'python=%s\ngo=%s\n' "$python" "$go"
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    printf 'python=%s\ngo=%s\n' "$python" "$go" >> "$GITHUB_OUTPUT"
  fi
}
