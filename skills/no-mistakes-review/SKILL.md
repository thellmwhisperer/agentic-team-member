---
name: no-mistakes-review
description: >
  Closing the loop when no-mistakes writes code. Use when an ATM PR carries
  no-mistakes(...) commits or has the review-required label. The CoS makes
  sure the code is reviewed and passed through ponytail, then labels the PR
  review-completed to merge it, or pr-discarded and closes it when it cannot
  merge. The CoS launches and verifies; it does not write code.
---

# Closing the loop when no-mistakes writes code

no-mistakes commits (`no-mistakes(review|ci|lint|document|test): ...`) do not
go through ponytail or another review in the delivery gate. A PR that has one
merges only through three labels (Decision 7524). No comment, check or login
unblocks it.

## 1. `review-required` blocks the merge

`auto-merge.yml` runs `scripts/arm-auto-merge.sh`, which reads every commit
of the PR through `scripts/no-mistakes-review.sh`. With any
`no-mistakes(...)` commit it adds `review-required` and does not arm
auto-merge; if auto-merge was armed, it disables it. If GitHub returns fewer
commits than the PR reports, it does the same: an incomplete list never
merges.

A PR with no no-mistakes commits merges as before. High risk still needs its
`Risk reviewed:` comment and the `risk-reviewed` label.

## 2. The CoS makes sure the code is reviewed

1. **Look at the size.** Files marked `linguist-generated` in
   `.gitattributes` (currently `go.sum`) are left out:
   `git diff --stat <first-nm>^ <head> -- . ':(exclude,attr:linguist-generated)'`.
   A little code, the CoS reads it. A lot, it delegates the review to an
   agent with the issue text and the no-mistakes commits
   (`git log --oneline origin/main..<head> --grep '^no-mistakes('`, then
   `git show` of each).
2. **Ponytail.** Another agent then passes over the same commits with
   ponytail: features nobody asked for, new dependencies, speculative
   validation and weakened tests go; real work the issue does not ask for
   goes to a follow-up issue.

## 3. Fine: `review-completed`

Once corrected, the CoS adds the `review-completed` label. The `labeled`
event runs `auto-merge.yml`, which arms auto-merge under the usual risk
rules: Low or Medium merges by itself, High waits for `risk-reviewed`.
Removing `review-required` does nothing.

A push that adds a new no-mistakes commit after `review-completed` takes the
label off and disables auto-merge. Review the new commits and label again.

## 4. Cannot merge: `pr-discarded`

When the review breaks what the issue asks, or code has to be undone:

1. Add `pr-discarded` to the PR, then close it with `gh pr close <number>`.
   The label disables auto-merge if it was armed; closing the PR prevents a
   racing auto-merge run from merging it.
2. Comment on the PR what happened.
3. Update the issue.
4. Create what is needed: a relaunch, or a split into smaller issues.
