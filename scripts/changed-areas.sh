#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
base=${1:?base commit is required}
python=false
go=false

while IFS= read -r path; do
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
done <<EOF
$(git diff --name-only "$base" HEAD)
EOF

if [ ! -f go.mod ]; then
  go=false
fi

printf 'python=%s\ngo=%s\n' "$python" "$go"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  printf 'python=%s\ngo=%s\n' "$python" "$go" >> "$GITHUB_OUTPUT"
fi
