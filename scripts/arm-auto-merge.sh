#!/bin/sh
# Arms GitHub auto-merge by rebase on PR $1 ($2 is the pull_request event action): GitHub merges
# it once main's required checks pass. Low and Medium risk arm at once. High risk, or a body
# without a readable level under `## Risk Assessment`, waits for the `risk-reviewed` label (see
# CONTRIBUTING.md). No-mistakes commits label it review-required and wait for review-completed,
# which any push removes; pr-discarded never arms (skills/no-mistakes-review/SKILL.md).
set -eu
# The workflow token's merges start no workflow and close no issue, so never fall back to it.
if [ -z "${GH_TOKEN:-}" ]; then
  echo "AUTO_MERGE_TOKEN is not set" >&2
  exit 1
fi
repo=$GITHUB_REPOSITORY
state=$(gh pr view "$1" --repo "$repo" --json autoMergeRequest,labels \
  --jq '"armed \(.autoMergeRequest != null)", "label \(.labels[].name)"')
has() { printf '%s\n' "$state" | grep -qx "$1"; }
# Leaves the PR unarmed, disabling an auto-merge armed earlier.
hold() {
  if has 'armed true'; then
    gh pr merge "$1" --disable-auto --repo "$repo"
  fi
  echo "$2"
  exit 0
}
if has 'label pr-discarded'; then
  hold "$1" "pr-discarded: never armed"
fi
block() {
  gh pr edit "$1" --repo "$repo" --add-label review-required
  if has 'label review-completed'; then
    gh pr edit "$1" --repo "$repo" --remove-label review-completed
  fi
  hold "$1" "no-mistakes commits: waiting for review-completed"
}
nm=$("$(dirname "$0")/no-mistakes-review.sh" "$1") || nm=" unknown"
head=${nm% *}
case "${nm#* }" in
  none) ;;
  # A review covers one head: any push needs a new review-completed.
  found) if [ "${2:-}" = synchronize ] || ! has 'label review-completed'; then block "$1"; fi ;;
  *) block "$1" ;;
esac
body=$(gh pr view "$1" --repo "$repo" --json body --jq .body)
level=$(printf '%s\n' "$body" | awk '
  { sub(/\r$/, "") }
  !found && /^## Risk Assessment[[:space:]]*$/ { found = 1; next }
  found && /^#+[[:space:]]/ { exit }
  found && NF { if (match($0, /[A-Za-z]+/)) print substr($0, RSTART, RLENGTH); exit }')
case "$level" in
  Low|Medium) ;;
  *)
    if ! has 'label risk-reviewed'; then
      echo "risk high: waiting for risk-reviewed"
      exit 0
    fi
    ;;
esac
gh pr merge "$1" --auto --rebase --match-head-commit "$head" --repo "$repo"
