#!/bin/sh
# Detects no-mistakes(...) commits on PR $1: prints "<head> found" or "<head> none".
# Exits 1 when it cannot tell, such as when GitHub lists fewer commits than the PR has
# (it stops at 250). See skills/no-mistakes-review/SKILL.md.
set -eu
head=$(gh pr view "$1" --repo "$GITHUB_REPOSITORY" --json headRefOid --jq .headRefOid)
commits=$(gh api "repos/$GITHUB_REPOSITORY/pulls/$1/commits" --paginate --jq '
  .[] | "commit \(.commit.message | split("\n")[0])"')
total=$(gh api "repos/$GITHUB_REPOSITORY/pulls/$1" --jq '.commits')
printf '%s\n' "$commits" |
  awk -v head="$head" -v total="$total" '
    { sub(/\r$/, "") }
    $1 == "commit" { listed++; if ($2 ~ /^no-mistakes\(/) nm = 1 }
    END {
      if (head == "") { print "could not read the head of the PR" > "/dev/stderr"; exit 1 }
      if (total !~ /^[0-9]+$/ || listed != total + 0) {
        print "commit list is incomplete: saw " listed + 0 " of " total " commits" > "/dev/stderr"
        exit 1
      }
      print head, (nm ? "found" : "none")
    }'
