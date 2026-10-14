---
name: no-mistakes-review
description: >
  Closing the loop when no-mistakes writes code. Use when an ATM PR carries
  no-mistakes(...) commits, has the review-required label, or its Go checks
  wait for `No-mistakes reviewed:`. One trim pass and one fixer-free review,
  then the CoS clears the exact head. The CoS launches and verifies; it does
  not write code.
---

# Closing the loop when no-mistakes writes code

no-mistakes commits (`no-mistakes(review|ci|lint|document|test): ...`) are
never reviewed again and never go through ponytail. So a PR that has one
cannot merge until this circuit runs once. There is no loop: at most one trim
pass and one read-only review.

## What blocks the merge

- `scripts/no-mistakes-review.sh` reads the PR's commits. With any
  `no-mistakes(...)` commit, **Go checks** stays red and `auto-merge.yml` adds
  the `review-required` label instead of arming auto-merge.
- It clears only on a comment by the login set in that script with the line
  `No-mistakes reviewed: <full head sha>` naming the current head. Any later
  commit changes the head, so an older comment no longer counts. Repository
  role is not authority.
- A PR with no no-mistakes commits merges as before. High risk still needs
  its `Risk reviewed:` comment and the `risk-reviewed` label.

## The circuit

1. **Look at the size.** A signal, not a gate. Generated files are marked
   `linguist-generated` in `.gitattributes` and left out:
   `git diff --stat <first-nm>^ <head> -- . ':(exclude,attr:linguist-generated)'`.
2. **Trim.** Launch one agent with the issue text and only the no-mistakes
   commits (`git log --oneline origin/main..<head> --grep '^no-mistakes('`,
   then `git show` of each). It classifies every hunk against the issue's
   acceptance criteria:
   - **legit**: meets a criterion or fixes a real bug. Keep.
   - **bloat**: features nobody asked for, new dependencies, speculative
     validation, weakened tests. Remove.
   - **out of scope**: real work the issue does not ask for. Remove and open a
     follow-up issue for it.

   It removes bloat and out-of-scope hunks in **one** commit and lists each
   hunk with its class.
3. **Review, read only.** One review of the branch with no fixer: it can only
   report and never adds commits.
   - Clean: go to 4.
   - A finding inside the issue's scope that does not break it: a follow-up
     issue; the PR goes on.
   - A finding that breaks what the issue asks: the PR goes to draft and back
     to the worker. No merge, and this circuit ends.
4. **Clear the head.** Verify the trim commit and the review, then comment on
   the PR, naming the head as it is now:

   ```
   No-mistakes reviewed: <full head sha>
   <commit>: kept | trimmed | follow-up #<n> - <one-line reason>
   ```

   Re-run the Go checks and Auto-merge runs of that head. Go checks turns
   green, Auto-merge removes `review-required` and arms the merge bound to
   that head. Low or Medium risk merges by itself; High waits for
   `risk-reviewed` as always.

A new no-mistakes commit after step 4 blocks the PR again. Return it to the
worker and run this circuit again for the new no-mistakes commits before
clearing the new head.
