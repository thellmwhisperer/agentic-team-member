#!/bin/sh
# Arms GitHub auto-merge by rebase on PR $1: GitHub merges it once main's required checks pass.
# Low and Medium risk arm at once. High risk, or a body without a readable level under
# `## Risk Assessment`, waits for the `risk-reviewed` label (see CONTRIBUTING.md).
set -eu
# The workflow token's merges start no workflow and close no issue, so never fall back to it.
if [ -z "${GH_TOKEN:-}" ]; then
  echo "AUTO_MERGE_TOKEN is not set" >&2
  exit 1
fi
body=$(gh pr view "$1" --repo "$GITHUB_REPOSITORY" --json body --jq .body)
level=$(printf '%s\n' "$body" | awk '
  { sub(/\r$/, "") }
  !found && /^## Risk Assessment[[:space:]]*$/ { found = 1; next }
  found && /^#+[[:space:]]/ { exit }
  found && NF { if (match($0, /[A-Za-z]+/)) print substr($0, RSTART, RLENGTH); exit }')
case "$level" in
  Low|Medium) ;;
  *)
    labels=$(gh pr view "$1" --repo "$GITHUB_REPOSITORY" --json labels --jq '.labels[].name')
    if ! printf '%s\n' "$labels" | grep -qx risk-reviewed; then
      echo "risk high: waiting for risk-reviewed"
      exit 0
    fi
    ;;
esac
gh pr merge "$1" --auto --rebase --repo "$GITHUB_REPOSITORY"
