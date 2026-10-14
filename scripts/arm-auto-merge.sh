#!/bin/sh
# Arms GitHub auto-merge by rebase on PR $1: GitHub merges it once main's required checks pass.
# Low and Medium risk arm at once. High risk, or a body without a readable level under
# `## Risk Assessment`, waits for the `risk-reviewed` label (see CONTRIBUTING.md).
# No-mistakes commits label the PR review-required and wait for review-completed; one pushed after
# $2, the previous head, takes review-completed off. pr-discarded never arms.
# See skills/no-mistakes-review/SKILL.md.
set -eu
# The workflow token's merges start no workflow and close no issue, so never fall back to it.
if [ -z "${GH_TOKEN:-}" ]; then
  echo "AUTO_MERGE_TOKEN is not set" >&2
  exit 1
fi
labels=$(gh pr view "$1" --repo "$GITHUB_REPOSITORY" --json labels --jq '.labels[].name')
has() { printf '%s\n' "$labels" | grep -qx "$1"; }
# Leaves PR $1 unarmed, disabling auto-merge if it was armed.
hold() {
  if [ "$(gh pr view "$1" --repo "$GITHUB_REPOSITORY" --json autoMergeRequest --jq '.autoMergeRequest != null')" = true ]; then
    gh pr merge "$1" --disable-auto --repo "$GITHUB_REPOSITORY"
  fi
  exit 0
}
if has pr-discarded; then
  echo "pr-discarded: never arms"
  hold "$1"
fi
nm=$("$(dirname "$0")/no-mistakes-review.sh" "$1" "${2:-}") || nm="? failed"
if [ "${nm#* }" != none ]; then
  has review-required || gh pr edit "$1" --repo "$GITHUB_REPOSITORY" --add-label review-required
  if [ "${nm#* }" = new ] && has review-completed; then
    gh pr edit "$1" --repo "$GITHUB_REPOSITORY" --remove-label review-completed
    echo "new no-mistakes commits: waiting for review-completed again"
    hold "$1"
  fi
  if [ "${nm#* }" = failed ] || ! has review-completed; then
    echo "no-mistakes commits: waiting for review-completed"
    hold "$1"
  fi
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
    if ! has risk-reviewed; then
      echo "risk high: waiting for risk-reviewed"
      exit 0
    fi
    ;;
esac
gh pr merge "$1" --auto --rebase --match-head-commit "${nm% *}" --repo "$GITHUB_REPOSITORY"
