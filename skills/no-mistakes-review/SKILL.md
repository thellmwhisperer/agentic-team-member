---
name: no-mistakes-review
description: >
  Closing the loop when no-mistakes writes code. Use when an ATM PR carries
  no-mistakes(...) commits or has the review-required label. The CoS makes
  sure the code is reviewed, then labels the PR review-completed to merge it
  or pr-discarded to stop it. The CoS launches and verifies; it does not
  write code.
---

# Closing the loop when no-mistakes writes code

no-mistakes commits (`no-mistakes(review|ci|lint|document|test): ...`) do not
go through ponytail or another review in the delivery gate. A PR that has one
merges only through three labels (Decision 7524). There is no unblocking
comment, no check made red by this review and no particular user: the labels
are the circuit. [Go checks](../../CONTRIBUTING.md#ci) reports tests and lint.

## What the labels do

- **`review-required` blocks the merge.** `scripts/arm-auto-merge.sh` reads
  the PR's commits. With any `no-mistakes(...)` commit awaiting review it adds
  `review-required` and leaves auto-merge disabled. If GitHub
  lists fewer commits than the PR has, it fails closed the same way.
- **`review-completed` allows auto-merge.** Adding the label triggers the
  `labeled` event and arms the merge bound to the head detection read, subject
  to [the risk rules and required checks](../../CONTRIBUTING.md#ci).
  When `review-completed` is accepted, the script removes `review-required`
  if present.
- **Any push invalidates the review.** On `synchronize`, a PR with any
  `no-mistakes(...)` commit loses `review-completed` and is left unarmed,
  even when the new commit is not from no-mistakes. Review covers one head.
- **`pr-discarded` never merges.** Auto-merge never arms it, and disables
  auto-merge if it was armed.
- A PR with no no-mistakes commits merges as before.

## The circuit

1. **Make sure the code is reviewed.** Look at the size of the no-mistakes
   commits, leaving out files marked `linguist-generated` in `.gitattributes`
   (currently `go.sum`):
   `git diff --stat <first-nm>^ <head> -- . ':(exclude,attr:linguist-generated)'`.
   A little code the CoS reviews. A lot of code goes to one agent with the
   issue text and the no-mistakes commits
   (`git log --oneline origin/main..<head> --grep '^no-mistakes('`, then
   `git show` of each), which checks every hunk against the issue's
   acceptance criteria.
2. **Ponytail pass.** Another agent passes the branch with ponytail, removing
   bloat: features nobody asked for, new dependencies, speculative
   validation, weakened tests. Real work the issue does not ask for becomes a
   follow-up issue.
3. **Fine: label it.** Once corrected, verify the head is the one reviewed,
   add `review-completed`, and the PR merges. If anything is pushed after
   that, the label goes away: review the new head and add it again.
4. **Not mergeable: discard it.** When the review breaks what the issue asks,
   or code has to be undone:
   - add `pr-discarded`;
   - comment on the PR what happened;
   - update the issue;
   - create what is needed: a relaunch, or a split into smaller tickets.

A push and a label racing each other are settled here, not in code: label
only the head that was reviewed, and check it is still the head afterwards.
