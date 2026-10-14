#!/bin/sh
# Judges PR $1 at its current head, or fails if $2 is given and the head is no longer $2.
# Prints "<head> none" without no-mistakes(...) commits, "<head> cleared" when the CoS commented
# a line `No-mistakes reviewed: <head>` for that exact head. Anything else exits 1: any later
# commit changes the head, so an older clearance never counts. See skills/no-mistakes-review/SKILL.md.
set -eu
cos=teseo # the one login whose comment clears; repository association is not authority
details=$(gh pr view "$1" --repo "$GITHUB_REPOSITORY" --json headRefOid,comments --jq '
  "head \(.headRefOid)",
  (.comments[] | .author.login as $a | .body | split("\n")[] | "comment \($a) \(.)")')
commits=$(gh api "repos/$GITHUB_REPOSITORY/pulls/$1/commits" --paginate --jq '
  .[] | "commit \(.commit.message | split("\n")[0])"')
printf '%s\n%s\n' "$details" "$commits" |
  awk -v login="$cos" -v want="${2:-}" '
    { sub(/\r$/, "") }
    $1 == "head" { head = $2 }
    $1 == "commit" && $2 ~ /^no-mistakes\(/ { nm = 1 }
    $1 == "comment" && $2 == login && $3 == "No-mistakes" && $4 == "reviewed:" && NF == 5 { ok[$5] = 1 }
    END {
      if (head == "") { print "could not read the head of the PR" > "/dev/stderr"; exit 1 }
      if (want != "" && want != head) { print "the PR head moved to " head ", past " want > "/dev/stderr"; exit 1 }
      if (!nm) { print head, "none"; exit }
      if (head in ok) { print head, "cleared"; exit }
      print "no-mistakes commits: waiting for `No-mistakes reviewed: " head "` from " login > "/dev/stderr"
      exit 1
    }'
