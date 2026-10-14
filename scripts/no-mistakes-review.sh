#!/bin/sh
# Detects no-mistakes(...) commits on PR $1. Prints "<head> none" without them or
# "<head> nm <latest committer time>" otherwise. Exits 1 when it cannot read the head
# or every commit. See skills/no-mistakes-review/SKILL.md.
set -eu
head=$(gh pr view "$1" --repo "$GITHUB_REPOSITORY" --json headRefOid --jq .headRefOid)
commits=$(gh api "repos/$GITHUB_REPOSITORY/pulls/$1/commits" --paginate --jq '
  .[] | "commit \(.commit.message | split("\n")[0]) \(.commit.committer.date | try fromdateiso8601 catch "unknown")"')
total=$(gh api "repos/$GITHUB_REPOSITORY/pulls/$1" --jq '.commits')
printf '%s\n' "$commits" |
  awk -v head="$head" -v total="$total" '
    { sub(/\r$/, "") }
    $1 == "commit" {
      listed++
      if ($2 ~ /^no-mistakes\(/) {
        found = 1
        if ($NF !~ /^[0-9]+$/) missing = 1
        else if ($NF + 0 > latest) latest = $NF + 0
      }
    }
    END {
      if (head == "") { print "could not read the head of the PR" > "/dev/stderr"; exit 1 }
      if (total !~ /^[0-9]+$/ || listed != total + 0) {
        print "commit list is incomplete: saw " listed " of " total " commits" > "/dev/stderr"
        exit 1
      }
      if (!found) print head, "none"
      else if (missing) print head, "nm unknown"
      else printf "%s nm %.0f\n", head, latest
    }'
