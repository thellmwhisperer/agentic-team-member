---
name: task-slicing
description: Improve ticket quality, issue drafting, dispatch readiness, and reviewable delivery by defining one correctable contract before implementation; use for planning, slicing, launch, review, gating, exceptions, or postmortems where scope boundaries or PR size matter.
---

# Task Slicing Protocol

Use this skill to turn work into one correctable contract that can be implemented, reviewed, verified, rolled back, and learned from.
Contract density is the primary scope signal.
Line count is secondary evidence about review burden.

## Ticket contract (primary)

The unit of work is one correctable contract, not a line-count allowance.
A coherent 4,000-line change can be one valid contract while a 300-line change that mixes unrelated domains can require multiple tickets.

Do not dispatch an implementation ticket unless it contains all four dispatch gates:

- explicit Non-goals;
- explicit Invariants;
- executable Acceptance;
- explicit Split triggers.

If those gates cannot yet be written, classify the work as an umbrella, planning, research, or scout ticket.
Do not disguise discovery as implementation.

Reviewers block the current change only when it violates the ticket's acceptance criteria or invariants.
Architecture ideas, adjacent cleanup, and improvements outside that contract become follow-up tickets unless they expose a violated invariant.
Missing or non-executable acceptance and missing invariants mean the ticket was not dispatch-ready and must return to planning.

Prefer refactoring the touched design over additive slop, compatibility piles, duplicated paths, or another exception layer.
Scope discipline does not justify preserving a broken local architecture.

Apply a root-cause circuit breaker.
If the same architectural root cause survives two corrective rounds, stop the patch loop and rewrite the affected boundary or escalate the decision.
Do not continue with R3, R4, or R14 variants of the same failed correction.

### Normative ticket skeleton

Every ticket records these fields in this order:

1. **Kind:** `implement`, `fix`, `umbrella`, `planning`, `scout`, or `acceptance-gate`.
2. **Goal:** one observable outcome and who benefits.
3. **Non-goals:** nearby outcomes deliberately excluded.
4. **Invariants:** properties that must remain true before, during, and after the change.
5. **Behavioral contract / States:** inputs, outputs, transitions, failure behavior, and terminal states.
6. **Scope surface:** owned files, modules, runtimes, data, interfaces, and operational boundaries likely to change.
7. **Acceptance:** executable commands or deterministic scenarios with expected results.
8. **Split triggers:** facts discovered during implementation that require a child ticket or separate PR.
9. **Dependencies / Parent:** prerequisites, umbrella, ordering, ownership, and blocked-by relationships.
10. **Evidence (fix / ops):** reproduction, logs, traces, incident links, or before/after operational proof when applicable.

Acceptance must be executable by someone other than the author.
Behavioral prose may supplement acceptance but cannot replace it.
Invariants must name protected behavior, data, compatibility, safety, or ownership boundaries rather than vague quality aspirations.

## Size budget (secondary guardrail)

Measure gross changed lines as additions plus deletions.
Exclude generated artifacts, vendored code, lock files, and snapshots only when the ticket explains why they are mechanically verifiable and low risk.

Use these ranges as soft planning guidance:

- Ideal: often under 500 gross changed lines when the contract has a natural boundary there.
- Acceptable: often under 1,000 gross changed lines when the contract remains coherent and independently verifiable.
- Default maximum guardrail: around 1,200 gross changed lines should trigger an explicit review-burden check and a recorded reason not to split.
- Multi-thousand-line changes require a surface-by-surface review plan, strong automated verification, and a rollback story.

Never treat 500, 999, 1,000, or 1,200 lines as the definition of done.
Do not pad a ticket up to a ceiling or cut a single behavioral contract at an arbitrary line boundary.
Do not use a small diff to excuse mixed domains, unclear acceptance, or weak invariants.
Do not use a large diff alone to reject a coherent, verifiable contract.

## Role responsibilities

- Product owner or planner writes the ticket contract and decides product intent.
- Scout gathers the evidence needed to make an unknown contract dispatch-ready.
- Coder confirms the contract, expected surfaces, split triggers, validation, and rollback before implementation.
- Reviewer tests acceptance and invariants against the actual change and routes adjacent concerns to follow-up work.
- Gatekeeper or no-mistakes verifies the recorded contract, actual diff surface, checks, and exception evidence.
- Director verifies that role-owned signals exist, routes stale or missing signals to the owner, and escalates unresolved intent.

One branch has one owner.
Each implementation slice has one ticket contract, one PR, focused validation, and a rollback story.

## Split before launch

Split by contract boundary before dispatch when any of these are true:

- the work contains more than one independently correctable user or operator outcome;
- the work crosses unrelated ownership domains that have different invariants;
- one part can land, verify, and provide value without the rest;
- the work mixes policy, tool implementation, runtime sync, and product rollout without one shared acceptance contract;
- different parts need different reviewers, rollback paths, or adversarial test surfaces;
- a declared split trigger is already true.

Do not split merely because a coherent mechanical change crosses many files.
Do not keep work together merely because the estimated diff is small.

## During implementation

Stop and create or activate a child ticket when a split trigger fires.
Update the parent relationship and dependency order before continuing.
Do not silently widen acceptance, weaken an invariant, or add a second contract to use remaining line budget.

When implementation reveals that the ticket's intended behavior is undecidable, return it to planning or `needs_human`.
When implementation reveals only an execution detail, keep moving within the frozen contract.

## Exception process

An oversized coherent contract may proceed when it records:

- why splitting would break atomicity, migration safety, compatibility, or verification;
- estimated gross changed lines and affected surfaces;
- exception class: emergency, mechanical migration, compatibility bridge, or explicit human waiver;
- surface-by-surface review plan;
- automated verification proportionate to blast radius;
- rollback or roll-forward plan;
- human decision owner when product or operational risk is not mechanical.

Size exceptions do not waive ticket-contract gates.
A small PR does not receive an exception from ticket-contract gates.

## Reviewer expectations

Review the highest-risk acceptance path and invariant first.
Use the ticket's executable acceptance as the merge contract.
Record contract violations as blockers.
Record valid but out-of-contract improvements as follow-up work with a clear proposed contract.
Treat line count, file count, and surface count as risk signals that influence review depth, not as verdicts.
If an accepted exception exists, review by surface or risk boundary rather than demanding an after-the-fact cosmetic split.

## Fleet and dispatch signals

Agent runtimes receive skills as a fixed synced set.
Promote policy changes in this repository and sync them to each configured runtime instead of injecting ad hoc copies per task.

Dispatch and orchestration should collect:

- ticket kind and parent;
- goal, non-goals, invariants, behavioral states, and executable acceptance;
- scope surface and split triggers;
- expected and actual gross changed lines as secondary risk evidence;
- validation and rollback plan;
- exception class and review plan when applicable;
- current owner, phase, and exact head SHA;
- follow-up tickets created from non-blocking review findings.

## Postmortem metadata

After merge, closure, or rollback, record enough evidence to improve future contracts:

- ticket kind, parent, and contract boundary;
- actual gross changed lines, file count, and changed surfaces;
- split triggers that fired or should have fired;
- number of review and corrective rounds;
- acceptance and invariant failures by source;
- no-mistakes, CI, and configured reviewer outcomes;
- follow-up tickets created;
- root cause and whether the two-round circuit breaker fired;
- defects, reverts, or operational surprises after delivery.
