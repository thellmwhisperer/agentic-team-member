---
name: task-slicing
description: Review task readiness and divide independent contracts into local child drafts before delivery.
---

# Task slicing

Read the literal parent issue and the complete diff before deciding readiness,
scope or completion. Preserve the parent's mandatory requirements. Review only:
do not repair code, edit the parent, create GitHub issues or execute children.
The contract review also uses ponytail-review; neither approval replaces the other.

## Readiness

Explicitly assess all four required fields: Non-goals, Invariants, executable
Acceptance and Split triggers. For each, give a status and evidence or a reason
for absence. Prose can supply a field; a Markdown heading alone cannot.
Do not invent requirements to fill gaps. Acceptance must name a runnable check
with an observable result, not merely an aspiration.

Use `present`, `missing` or `not-executable` for each field. Any missing or
non-executable field makes the parent `unready` and blocks delivery. Explain
which field is inadequate and what needs clarification before another attempt.

## Scope and completion

Map the diff to the parent's goal, mandatory criteria, invariants and non-goals.
Decide whether the diff is one coherent contract or mixes independent outcomes
that can be accepted separately. Respect the parent's explicit split triggers.
Do not use artificial line limits or budgets to split the original contract.

Report `split-required` when the diff mixes independent contracts. Name the
independent outcomes and why separation is needed. Supply a local child draft
for the work to separate. This blocks delivery; it is not a request for an
automatic code repair. If both readiness and splitting fail, report `unready`
and include the split drafts and cause as well.

List every unmet mandatory requirement in `mandatory_pending`. A pending child,
local draft or existing follow-up does not satisfy a requirement of the parent.
A required draft always blocks delivery until the mandatory requirement is met.
Keep findings that block the original contract review in its existing findings.

An optional improvement outside the goal, absent from the product diff, does
not by itself invalidate a fulfilled parent. Keep it as an `optional` draft;
do not modify product to implement it. An unrelated change already in the diff
is not excused by calling it optional: the scope review still blocks delivery.

Use `ready` only for a prepared issue with a single contract in the diff. That
readiness alone does not approve mandatory work still pending or override any
block from the original contract review.

## Local child contracts

Every draft has a purpose (`optional`, `required` or `split`), a concrete cause
and a contract with all ten fields below. Keep dependencies explicit; identify
the parent and whether the child depends on it or must be completed before the
parent can be delivered. Never turn a mandatory parent requirement into an
optional proposal. If the parent has no issue number, identify it by its title.

1. **Kind** (`kind`): implement, investigate, fix or ops, as appropriate.
2. **Goal** (`goal`): one observable operator or user outcome.
3. **Non-goals** (`non_goals`): explicit exclusions.
4. **Invariants** (`invariants`): behavior that must remain true.
5. **Behavioral contract / States** (`behavioral_contract`): inputs, states,
   transitions and observable outputs, including failure behavior.
6. **Scope surface** (`scope_surface`): files or components the child may change.
7. **Acceptance** (`acceptance`): runnable verification and expected result.
8. **Split triggers** (`split_triggers`): discoveries that require replanning.
9. **Dependencies / Parent** (`dependencies_parent`): parent identity, dependency
   direction and required ordering.
10. **Evidence** (`evidence`): observations supporting the draft and its cause.

Do not publish, schedule or execute drafts. Return them inside the report;
ATM preserves them with the cause, literal parent issue and reviewed commits in
the run's `contract-review.json`, outside the product branch. Do not create a
second contract repository or use the worker's follow-up format.

## Report and stopping

Add `task_slicing` to the original review report, using the schema in the brief.
Always include disposition, reason, all four field results, mandatory_pending
and drafts. Use empty arrays when nothing is pending or proposed. Every field
result needs a status and reason; every draft needs all ten nonempty fields.
Keep the original review's disposition, findings, base and head independent.

Stop on unready or split-required; do not start a repair round. If the same
cause occurs in two rounds, stop and reformulate the contract with the operator.
Remote publication, planning engines, child execution and budget policies need
separate issues. Parsing Markdown or discovering acceptance heuristically in
code requires returning the ticket to planning.
