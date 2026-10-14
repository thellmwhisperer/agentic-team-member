#!/bin/sh
# Detects no-mistakes(...) commits on PR $1. Prints "<head> none" without them, "<head> new" when one
# came after $2, the previous head (or $2 is no longer on the PR, a force push), and "<head> nm"
# otherwise. Exits 1 when it cannot read the head or every commit. See skills/no-mistakes-review/SKILL.md.
set -eu
head=$(gh pr view "$1" --repo "$GITHUB_REPOSITORY" --json headRefOid --jq .headRefOid)
commits=$(gh api "repos/$GITHUB_REPOSITORY/pulls/$1/commits" --paginate --jq '
  .[] | "commit \(.commit.message | split("\n")[0]) \(.sha)"')
total=$(gh api "repos/$GITHUB_REPOSITORY/pulls/$1" --jq '.commits')
printf '%s\n' "$commits" |
  awk -v head="$head" -v before="${2:-}" -v total="$total" '
    { sub(/\r$/, "") }
    $1 == "commit" { listed++; if ($NF == before) at = listed; if ($2 ~ /^no-mistakes\(/) last = listed }
    END {
      if (head == "") { print "could not read the head of the PR" > "/dev/stderr"; exit 1 }
      if (total !~ /^[0-9]+$/ || listed != total + 0) {
        print "commit list is incomplete: saw " listed " of " total " commits" > "/dev/stderr"
        exit 1
      }
      if (!last) print head, "none"
      else if (before != "" && last > at) print head, "new"
      else print head, "nm"
    }'
